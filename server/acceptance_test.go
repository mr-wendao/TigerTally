package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// 这一组测试就是 docs/DESIGN-M0.md 里那五条验收标准的可重复版本。
// 跑法：go test -v ./...

// ---------- 测试脚手架 ----------

type testEnv struct {
	t      *testing.T
	ts     *httptest.Server
	srv    *Server
	db     *DB
	group  string
	admin  string // 管理员令牌
	member string // 普通成员令牌
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	db, err := OpenDB(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := NewServer(Config{DBPath: filepath.Join(dir, "test.db")}, db, log.New(io.Discard, "", 0))
	ts := httptest.NewServer(s.Handler())

	e := &testEnv{t: t, ts: ts, srv: s, db: db}
	t.Cleanup(func() {
		ts.Close()
		db.Close()
	})

	e.group, e.admin = e.createGroup("测试群", "群主")
	e.member = e.addMember(e.admin, "小马", "agent")
	return e
}

func (e *testEnv) do(method, path, token string, body any) (*http.Response, []byte) {
	e.t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatalf("序列化: %v", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, rdr)
	if err != nil {
		e.t.Fatalf("造请求: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

// doText 发裸文本（Content-Type: text/plain）—— 给不会 JSON 的单片机留的路。
func (e *testEnv) doText(method, path, token, text string) (*http.Response, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, strings.NewReader(text))
	if err != nil {
		e.t.Fatalf("造请求: %v", err)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func (e *testEnv) createGroup(name, owner string) (groupID, token string) {
	e.t.Helper()
	resp, raw := e.do("POST", "/api/groups", "", map[string]string{"name": name, "owner": owner})
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("建群失败: %d %s", resp.StatusCode, raw)
	}
	var out struct {
		GroupID string `json:"group_id"`
		Token   string `json:"token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("解析建群响应: %v (%s)", err, raw)
	}
	if out.GroupID == "" || out.Token == "" {
		e.t.Fatalf("建群响应缺字段: %s", raw)
	}
	return out.GroupID, out.Token
}

func (e *testEnv) addMember(adminToken, name, kind string) string {
	e.t.Helper()
	resp, raw := e.do("POST", "/api/"+e.group+"/members", adminToken,
		map[string]string{"name": name, "kind": kind})
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("加成员失败: %d %s", resp.StatusCode, raw)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("解析加成员响应: %v", err)
	}
	return out.Token
}

func (e *testEnv) wsURL() string {
	return "ws" + strings.TrimPrefix(e.ts.URL, "http")
}

func (e *testEnv) dial(t *testing.T, query string, header http.Header, subprotocols []string) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second, Subprotocols: subprotocols}
	conn, resp, err := d.Dial(e.wsURL()+"/api/"+e.group+"/ws"+query, header)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("拨号失败 (status=%d): %v", status, err)
	}
	return conn
}

func bearer(tok string) http.Header {
	return http.Header{"Authorization": {"Bearer " + tok}}
}

func readFrame(t *testing.T, conn *websocket.Conn, timeout time.Duration) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("读 WebSocket 帧: %v", err)
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("解析帧 %s: %v", raw, err)
	}
	return frame
}

// ---------- 验收 2：两个 WebSocket，一个发，另一个秒收 ----------

func TestAcceptance2_WebSocketRealtime(t *testing.T) {
	e := newTestEnv(t)

	// 连接 A：脚本/agent 走 Authorization 头
	connA := e.dial(t, "", bearer(e.admin), nil)
	defer connA.Close()

	// 连接 B：浏览器走子协议（浏览器 WebSocket 不能设 Authorization 头）
	connB := e.dial(t, "", nil, []string{"bearer." + e.member})
	defer connB.Close()

	if got := connB.Subprotocol(); got != "bearer."+e.member {
		t.Fatalf("子协议没协商上: %q", got)
	}

	helloA := readFrame(t, connA, 2*time.Second)
	helloB := readFrame(t, connB, 2*time.Second)
	if helloA["type"] != "hello" || helloB["type"] != "hello" {
		t.Fatalf("首帧应为 hello: A=%v B=%v", helloA, helloB)
	}
	t.Logf("A hello: %v", helloA)
	t.Logf("B hello: %v", helloB)

	// 一个用 HTTP 发（信条：一条 HTTP 就是一条消息）
	start := time.Now()
	resp, raw := e.do("POST", "/api/"+e.group+"/messages", e.admin,
		map[string]string{"body": "实时测试：喂——"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("发消息失败: %d %s", resp.StatusCode, raw)
	}

	// 另一个应当「秒收」
	for _, c := range []struct {
		name string
		conn *websocket.Conn
	}{{"A", connA}, {"B", connB}} {
		frame := readFrame(t, c.conn, 3*time.Second)
		elapsed := time.Since(start)
		if frame["type"] != "message" || frame["body"] != "实时测试：喂——" {
			t.Fatalf("%s 收到的帧不对: %v", c.name, frame)
		}
		if frame["sender"] != "群主" {
			t.Fatalf("%s 发信人应为「群主」: %v", c.name, frame["sender"])
		}
		if elapsed > 2*time.Second {
			t.Fatalf("%s 太慢: %v", c.name, elapsed)
		}
		t.Logf("%s 实时收到 id=%v sender=%v body=%q 用时 %v",
			c.name, frame["id"], frame["sender"], frame["body"], elapsed.Round(time.Millisecond))
	}
}

// 连上时带 ?since= 应当先补历史，再进实时流。
func TestWebSocketBacklog(t *testing.T) {
	e := newTestEnv(t)
	for i := 1; i <= 3; i++ {
		e.do("POST", "/api/"+e.group+"/messages", e.admin,
			map[string]string{"body": fmt.Sprintf("历史 %d", i)})
	}
	conn := e.dial(t, "?since=1", bearer(e.admin), nil)
	defer conn.Close()

	if f := readFrame(t, conn, 2*time.Second); f["type"] != "hello" {
		t.Fatalf("首帧应为 hello: %v", f)
	}
	for i := 2; i <= 3; i++ {
		f := readFrame(t, conn, 2*time.Second)
		if f["body"] != fmt.Sprintf("历史 %d", i) {
			t.Fatalf("补历史不对: %v", f)
		}
		t.Logf("补到历史: id=%v body=%v", f["id"], f["body"])
	}
}

// ---------- 验收 1：重启不丢消息（库层面） ----------

func TestAcceptance1_MessagesSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "restart.db")

	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	g, owner, _, err := db.CreateGroup("重启群", "群主")
	if err != nil {
		t.Fatalf("建群: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := db.InsertMessage(g.ID, owner, fmt.Sprintf("第 %d 条", i)); err != nil {
			t.Fatalf("写消息: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关库: %v", err)
	}
	t.Log("已写入 3 条并关闭数据库（模拟重启）")

	db2, err := OpenDB(path)
	if err != nil {
		t.Fatalf("重开库: %v", err)
	}
	defer db2.Close()

	msgs, err := db2.MessagesSince(g.ID, 0, 100)
	if err != nil {
		t.Fatalf("拉消息: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("重启后应有 3 条，实际 %d 条", len(msgs))
	}
	for _, m := range msgs {
		t.Logf("重启后仍在: id=%d sender=%s body=%q", m.ID, m.Sender, m.Body)
	}
}

// ---------- 验收 3：纯 HTTP 收发 ----------

func TestAcceptance3_HTTPRoundTrip(t *testing.T) {
	e := newTestEnv(t)

	resp, raw := e.do("POST", "/api/"+e.group+"/messages", e.admin, `{"body":"curl 发的"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("发消息: %d %s", resp.StatusCode, raw)
	}
	t.Logf("POST 返回 %d: %s", resp.StatusCode, raw)

	resp, raw = e.do("GET", "/api/"+e.group+"/messages?since=0", e.member, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("拉消息: %d %s", resp.StatusCode, raw)
	}
	t.Logf("GET 返回 %d: %s", resp.StatusCode, raw)

	var out struct {
		Messages []Message `json:"messages"`
		Latest   int64     `json:"latest"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if len(out.Messages) != 1 || out.Messages[0].Body != "curl 发的" {
		t.Fatalf("拉到的消息不对: %s", raw)
	}
	// 成员用成员自己的令牌发，信发人应是他自己
	resp, raw = e.do("POST", "/api/"+e.group+"/messages", e.member, `{"body":"第二条"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("成员发言: %d %s", resp.StatusCode, raw)
	}
	var m Message
	json.Unmarshal(raw, &m)
	if m.Sender != "小马" || m.Kind != "agent" {
		t.Fatalf("发信人应由令牌决定，实际 %+v", m)
	}
	t.Logf("成员令牌发言，服务端记为 sender=%s kind=%s", m.Sender, m.Kind)

	// since 游标
	resp, raw = e.do("GET", "/api/"+e.group+"/messages?since=1", e.admin, nil)
	var page struct {
		Messages []Message `json:"messages"`
	}
	json.Unmarshal(raw, &page)
	if len(page.Messages) != 1 || page.Messages[0].ID != 2 {
		t.Fatalf("since 游标不对: %s", raw)
	}
	t.Logf("since=1 只返回 id=2：%s", raw)
}

// 裸文本（不会 JSON 的单片机）也要能发。
func TestPlainTextBody(t *testing.T) {
	e := newTestEnv(t)
	resp, raw := e.doText("POST", "/api/"+e.group+"/messages", e.member, "裸文本也能发")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("裸文本: %d %s", resp.StatusCode, raw)
	}
	var m Message
	json.Unmarshal(raw, &m)
	if m.Body != "裸文本也能发" {
		t.Fatalf("裸文本内容不对: %s", raw)
	}
	t.Logf("裸文本收到: %s", raw)
}

// ---------- 验收 4：无令牌一律 401（安全底线） ----------

func TestAcceptance4_NoTokenRejected(t *testing.T) {
	e := newTestEnv(t)
	_, otherToken := e.createGroup("另一个群", "别人")

	cases := []struct {
		name   string
		method string
		path   string
		token  string
		body   any
		want   int
	}{
		{"拉消息·不带令牌", "GET", "/api/" + e.group + "/messages", "", nil, 401},
		{"拉消息·错令牌", "GET", "/api/" + e.group + "/messages", "tt_" + strings.Repeat("0", 64), nil, 401},
		{"拉消息·乱令牌", "GET", "/api/" + e.group + "/messages", "hunter2", nil, 401},
		{"发消息·不带令牌", "POST", "/api/" + e.group + "/messages", "", map[string]string{"body": "偷发"}, 401},
		{"发消息·错令牌", "POST", "/api/" + e.group + "/messages", "tt_bad", map[string]string{"body": "偷发"}, 401},
		{"成员列表·不带令牌", "GET", "/api/" + e.group + "/members", "", nil, 401},
		{"加成员·不带令牌", "POST", "/api/" + e.group + "/members", "", map[string]string{"name": "内鬼"}, 401},
		{"加成员·普通成员令牌", "POST", "/api/" + e.group + "/members", e.member, map[string]string{"name": "内鬼"}, 403},
		{"WebSocket·不带令牌", "GET", "/api/" + e.group + "/ws", "", nil, 401},
		{"别的群的令牌", "GET", "/api/" + e.group + "/messages", otherToken, nil, 404},
		{"不存在的接口·不带令牌", "GET", "/api/whatever", "", nil, 401},
		{"不存在的群·不带令牌", "GET", "/api/nosuchgroup/messages", "", nil, 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// WebSocket 走 HTTP GET 也能验证鉴权（升级前就该被拦）
			resp, raw := e.do(c.method, c.path, c.token, c.body)
			if resp.StatusCode != c.want {
				t.Fatalf("想要 %d，实际 %d：%s", c.want, resp.StatusCode, raw)
			}
			t.Logf("%-24s → %d  %s", c.name, resp.StatusCode, strings.TrimSpace(string(raw)))
		})
	}

	// 带错令牌的 WebSocket 拨号必须在握手阶段就被拒
	_, resp, err := websocket.DefaultDialer.Dial(
		e.wsURL()+"/api/"+e.group+"/ws", bearer("tt_wrong"))
	if err == nil {
		t.Fatal("错令牌的 WebSocket 竟然连上了")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("WebSocket 错令牌应返回 401，实际 %v", resp)
	}
	t.Logf("WebSocket 错令牌握手被拒: %d", resp.StatusCode)

	// 没有令牌的请求不能碰到任何数据
	resp, raw := e.do("GET", "/api/"+e.group+"/messages?since=0", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无令牌拉到了消息: %d %s", resp.StatusCode, raw)
	}
}

// ---------- 验收：数据库里没有明文令牌 ----------

func TestTokensStoredHashedOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hash.db")

	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	_, _, adminToken, err := db.CreateGroup("哈希群", "群主")
	if err != nil {
		t.Fatalf("建群: %v", err)
	}
	memberToken := ""
	if _, tok, err := db.AddMember("x", "n", "human"); err == nil {
		memberToken = tok
	}
	_ = memberToken
	db.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读库文件: %v", err)
	}
	if bytes.Contains(raw, []byte(adminToken)) {
		t.Fatal("数据库文件里出现了明文管理员令牌！")
	}
	if !bytes.Contains(raw, []byte(HashToken(adminToken))) {
		t.Fatal("数据库里找不到令牌哈希，存疑")
	}
	t.Logf("库文件 %d 字节；明文令牌不在其中；哈希 %s… 在", len(raw), HashToken(adminToken)[:16])
}

// 令牌必须随机：两把不能一样。
func TestTokensAreUniqueAndRandom(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		tok, hash, err := newToken()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(tok, tokenPrefix) {
			t.Fatalf("令牌前缀不对: %s", tok)
		}
		if len(tok) != len(tokenPrefix)+64 {
			t.Fatalf("令牌长度不对: %d", len(tok))
		}
		if seen[tok] || seen[hash] {
			t.Fatal("令牌重复了")
		}
		seen[tok], seen[hash] = true, true
	}
	t.Log("500 把令牌互不相同，长度与格式正确")
}

// ---------- 建群/加成员的基本约束 ----------

func TestGroupAndMemberValidation(t *testing.T) {
	e := newTestEnv(t)

	if resp, _ := e.do("POST", "/api/groups", "", map[string]string{"name": "测试群"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("重名建群应 409，实际 %d", resp.StatusCode)
	} else {
		t.Log("重名建群 → 409")
	}
	if resp, _ := e.do("POST", "/api/groups", "", map[string]string{"name": ""}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空名建群应 400，实际 %d", resp.StatusCode)
	} else {
		t.Log("空名建群 → 400")
	}
	if resp, _ := e.do("POST", "/api/"+e.group+"/members", e.admin,
		map[string]string{"name": "小马", "kind": "agent"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("重名成员应 409，实际 %d", resp.StatusCode)
	} else {
		t.Log("重名成员 → 409")
	}
	if resp, _ := e.do("POST", "/api/"+e.group+"/messages", e.admin,
		map[string]string{"body": "   "}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空消息应 400，实际 %d", resp.StatusCode)
	} else {
		t.Log("空消息 → 400")
	}
}

// ---------- 建群口令（bootstrap token） ----------

func TestBootstrapTokenLocksGroupCreation(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(filepath.Join(dir, "boot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewServer(Config{BootstrapToken: "secret-boot"}, db, log.New(io.Discard, "", 0))
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	post := func(token string) int {
		req, _ := http.NewRequest("POST", ts.URL+"/api/groups",
			strings.NewReader(`{"name":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := post(""); got != http.StatusUnauthorized {
		t.Fatalf("没口令建群应 401，实际 %d", got)
	}
	t.Log("没带 bootstrap 口令建群 → 401")
	if got := post("wrong"); got != http.StatusUnauthorized {
		t.Fatalf("错口令建群应 401，实际 %d", got)
	}
	t.Log("错 bootstrap 口令建群 → 401")
	if got := post("secret-boot"); got != http.StatusCreated {
		t.Fatalf("对口令建群应 201，实际 %d", got)
	}
	t.Log("对 bootstrap 口令建群 → 201")
}

// 消息只增不删：没有任何删除接口，删不掉。
func TestMessagesAreAppendOnly(t *testing.T) {
	e := newTestEnv(t)
	for i := 1; i <= 5; i++ {
		e.do("POST", "/api/"+e.group+"/messages", e.admin,
			map[string]string{"body": fmt.Sprintf("m%d", i)})
	}
	// 试着用各种方式删
	for _, c := range []struct{ method, path string }{
		{"DELETE", "/api/" + e.group + "/messages"},
		{"DELETE", "/api/" + e.group + "/messages/1"},
		{"POST", "/api/" + e.group + "/messages/1/delete"},
	} {
		resp, _ := e.do(c.method, c.path, e.admin, nil)
		t.Logf("%s %s → %d（没有删除接口）", c.method, c.path, resp.StatusCode)
	}
	n, err := e.db.CountMessages(e.group)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("应有 5 条，实际 %d", n)
	}
	t.Logf("消息仍然 %d 条，一条没少", n)
}
