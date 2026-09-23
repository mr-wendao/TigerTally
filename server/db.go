package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 纯 Go 驱动，无 cgo —— 交叉编译到 Win/macOS 不需要工具链
)

// ---------- 领域对象 ----------

type Group struct {
	ID        string
	Name      string
	CreatedAt int64 // unix 毫秒
}

type Member struct {
	ID        int64
	GroupID   string
	Name      string
	Kind      string // human / agent / device
	Role      string // admin / member
	TokenHash string // 只在按令牌查人时填，绝不外传
	CreatedAt int64
}

type Message struct {
	ID        int64  `json:"id"`
	GroupID   string `json:"group_id"`
	SenderID  int64  `json:"sender_id"`
	Sender    string `json:"sender"`
	Kind      string `json:"kind"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"` // RFC3339 UTC
	TS        int64  `json:"ts"`         // unix 毫秒
}

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrBadInput = errors.New("bad input")
)

// ---------- 存储 ----------

// DB 包一层 database/sql。
// 只开一条连接：SQLite 同一时刻只允许一个写者，串行化最省心，
// 群聊这个量级完全够用，也彻底躲开 SQLITE_BUSY。
type DB struct {
	sql  *sql.DB
	path string
}

const schema = `
CREATE TABLE IF NOT EXISTS groups (
  id         TEXT    PRIMARY KEY,
  name       TEXT    NOT NULL UNIQUE,
  created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS members (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  group_id   TEXT    NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  name       TEXT    NOT NULL,
  kind       TEXT    NOT NULL DEFAULT 'human',
  role       TEXT    NOT NULL DEFAULT 'member',
  token_hash TEXT    NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  UNIQUE (group_id, name)
) STRICT;

-- 消息只增不删。AUTOINCREMENT 保证 id 永不复用，
-- 设备用 ?since=<id> 轮询才不会漏。
CREATE TABLE IF NOT EXISTS messages (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  group_id    TEXT    NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  sender_id   INTEGER NOT NULL REFERENCES members(id),
  sender_name TEXT    NOT NULL,
  sender_kind TEXT    NOT NULL,
  body        TEXT    NOT NULL,
  created_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_messages_group ON messages(group_id, id);
CREATE INDEX IF NOT EXISTS idx_members_group  ON members(group_id);
`

// OpenDB 打开（必要时创建）数据库。
// 落盘策略：WAL + synchronous=FULL —— 进程被 kill -9 也不丢已提交的消息。
func OpenDB(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("建目录 %s: %w", dir, err)
		}
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(abs) +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(FULL)" +
		"&_pragma=foreign_keys(1)"

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetConnMaxLifetime(0)
	sqlDB.SetMaxIdleConns(1)

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("连接数据库: %w", err)
	}
	if _, err := sqlDB.Exec(schema); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("建表: %w", err)
	}

	db := &DB{sql: sqlDB, path: abs}
	// 只允许本人读写：库里虽然没有明文令牌，但哈希也不该随便给人看
	if err := os.Chmod(abs, 0o600); err != nil && !os.IsNotExist(err) {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) Close() error {
	if d == nil || d.sql == nil {
		return nil
	}
	// 收尾：把 WAL 合并回主库，方便备份/拷贝
	_, _ = d.sql.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return d.sql.Close()
}

// ---------- 群 ----------

func (d *DB) CreateGroup(name, ownerName string) (*Group, *Member, string, error) {
	name = strings.TrimSpace(name)
	ownerName = strings.TrimSpace(ownerName)
	if name == "" {
		return nil, nil, "", fmt.Errorf("%w: 群名不能为空", ErrBadInput)
	}
	if len(name) > 64 {
		return nil, nil, "", fmt.Errorf("%w: 群名最长 64 字节", ErrBadInput)
	}
	if ownerName == "" {
		ownerName = "群主"
	}
	if len(ownerName) > 64 {
		return nil, nil, "", fmt.Errorf("%w: 成员名最长 64 字节", ErrBadInput)
	}

	token, hash, err := newToken()
	if err != nil {
		return nil, nil, "", err
	}
	now := time.Now().UnixMilli()

	var lastErr error
	for attempt := 0; attempt < 5; attempt++ { // 群 ID 撞了就重摇
		gid, err := newGroupID()
		if err != nil {
			return nil, nil, "", err
		}
		tx, err := d.sql.Begin()
		if err != nil {
			return nil, nil, "", err
		}
		_, err = tx.Exec(`INSERT INTO groups (id, name, created_at) VALUES (?, ?, ?)`, gid, name, now)
		if err != nil {
			tx.Rollback()
			if isUniqueViolation(err) {
				lastErr = fmt.Errorf("%w: 群名 %q 已存在", ErrConflict, name)
				break // 名字冲突，重摇 ID 没用
			}
			lastErr = err
			continue
		}
		res, err := tx.Exec(
			`INSERT INTO members (group_id, name, kind, role, token_hash, created_at)
			 VALUES (?, ?, 'human', 'admin', ?, ?)`, gid, ownerName, hash, now)
		if err != nil {
			tx.Rollback()
			lastErr = err
			continue
		}
		mid, err := res.LastInsertId()
		if err != nil {
			tx.Rollback()
			return nil, nil, "", err
		}
		if err := tx.Commit(); err != nil {
			lastErr = err
			continue
		}
		g := &Group{ID: gid, Name: name, CreatedAt: now}
		m := &Member{ID: mid, GroupID: gid, Name: ownerName, Kind: "human", Role: "admin", CreatedAt: now}
		return g, m, token, nil
	}
	return nil, nil, "", lastErr
}

func (d *DB) groupBy(query string, arg any) (*Group, error) {
	var g Group
	err := d.sql.QueryRow(
		`SELECT id, name, created_at FROM groups WHERE `+query, arg).
		Scan(&g.ID, &g.Name, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// ResolveGroup 先按 ID 找，再按群名找 —— 让人可以 /api/工作群/messages 这样直白地用。
func (d *DB) ResolveGroup(ref string) (*Group, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, ErrNotFound
	}
	g, err := d.groupBy("id = ?", ref)
	if err == nil {
		return g, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return d.groupBy("name = ?", ref)
}

// ---------- 成员 ----------

func (d *DB) AddMember(groupID, name, kind string) (*Member, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", fmt.Errorf("%w: 成员名不能为空", ErrBadInput)
	}
	if len(name) > 64 {
		return nil, "", fmt.Errorf("%w: 成员名最长 64 字节", ErrBadInput)
	}
	if kind == "" {
		kind = "human"
	}
	switch kind {
	case "human", "agent", "device":
	default:
		return nil, "", fmt.Errorf("%w: kind 只能是 human/agent/device", ErrBadInput)
	}

	token, hash, err := newToken()
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UnixMilli()
	res, err := d.sql.Exec(
		`INSERT INTO members (group_id, name, kind, role, token_hash, created_at)
		 VALUES (?, ?, ?, 'member', ?, ?)`, groupID, name, kind, hash, now)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, "", fmt.Errorf("%w: 成员 %q 已在群里", ErrConflict, name)
		}
		return nil, "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, "", err
	}
	return &Member{ID: id, GroupID: groupID, Name: name, Kind: kind, Role: "member", CreatedAt: now}, token, nil
}

// MemberByTokenHash 按令牌哈希全局查人（令牌全局唯一）。
// 先认令牌再认群，群存不存在都不会泄露给没钥匙的人。
func (d *DB) MemberByTokenHash(hash string) (*Member, error) {
	var m Member
	err := d.sql.QueryRow(
		`SELECT id, group_id, name, kind, role, token_hash, created_at FROM members WHERE token_hash = ?`, hash).
		Scan(&m.ID, &m.GroupID, &m.Name, &m.Kind, &m.Role, &m.TokenHash, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (d *DB) ListMembers(groupID string) ([]Member, error) {
	rows, err := d.sql.Query(
		`SELECT id, group_id, name, kind, role, created_at FROM members
		 WHERE group_id = ? ORDER BY id ASC`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ID, &m.GroupID, &m.Name, &m.Kind, &m.Role, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---------- 消息 ----------

func (d *DB) InsertMessage(groupID string, sender *Member, body string) (*Message, error) {
	now := time.Now().UnixMilli()
	res, err := d.sql.Exec(
		`INSERT INTO messages (group_id, sender_id, sender_name, sender_kind, body, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`, groupID, sender.ID, sender.Name, sender.Kind, body, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &Message{
		ID:        id,
		GroupID:   groupID,
		SenderID:  sender.ID,
		Sender:    sender.Name,
		Kind:      sender.Kind,
		Body:      body,
		CreatedAt: time.UnixMilli(now).UTC().Format(time.RFC3339Nano),
		TS:        now,
	}, nil
}

// MessagesSince 取 id > since 的消息，升序。limit 由调用方夹紧。
func (d *DB) MessagesSince(groupID string, since int64, limit int) ([]Message, error) {
	rows, err := d.sql.Query(
		`SELECT id, group_id, sender_id, sender_name, sender_kind, body, created_at
		 FROM messages WHERE group_id = ? AND id > ? ORDER BY id ASC LIMIT ?`,
		groupID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Message{}
	for rows.Next() {
		var (
			m  Message
			ts int64
		)
		if err := rows.Scan(&m.ID, &m.GroupID, &m.SenderID, &m.Sender, &m.Kind, &m.Body, &ts); err != nil {
			return nil, err
		}
		m.TS = ts
		m.CreatedAt = time.UnixMilli(ts).UTC().Format(time.RFC3339Nano)
		out = append(out, m)
	}
	return out, rows.Err()
}

// LatestID 取群里最新一条消息的 id，没有消息则为 0。
func (d *DB) LatestID(groupID string) (int64, error) {
	var id int64
	err := d.sql.QueryRow(
		`SELECT COALESCE(MAX(id), 0) FROM messages WHERE group_id = ?`, groupID).Scan(&id)
	return id, err
}

func (d *DB) CountMessages(groupID string) (int, error) {
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM messages WHERE group_id = ?`, groupID).Scan(&n)
	return n, err
}

// ---------- 令牌 ----------

// newToken 生成一把新钥匙：32 字节随机，返回明文和哈希。
// 明文只在此刻出现一次，库里永远只有哈希。
func newToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = tokenPrefix + hex.EncodeToString(b)
	return token, HashToken(token), nil
}

const tokenPrefix = "tt_"

// HashToken 算令牌的 SHA-256。
// 令牌本身是 256 位随机数，猜不出来，所以不需要慢哈希（bcrypt 那套是防人脑弱口令的）。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newGroupID 生成 12 位十六进制群 ID（URL 安全，不用转义）。
func newGroupID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// isUniqueViolation 判断是不是唯一索引冲突。
// modernc 驱动把 SQLite 的报错原文带在 message 里。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToUpper(err.Error())
	return strings.Contains(s, "UNIQUE CONSTRAINT FAILED") || strings.Contains(s, "CONSTRAINT UNIQUE")
}
