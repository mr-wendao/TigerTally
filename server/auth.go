package main

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
)

var (
	errUnauthorized  = errors.New("unauthorized")
	errForbidden     = errors.New("forbidden")
	errNotFoundGroup = errors.New("group not found")
)

// Principal 是一次请求背后的身份 —— 由令牌推出来，客户端说了不算。
type Principal struct {
	GroupID  string
	MemberID int64
	Name     string
	Kind     string
	Role     string // admin / member
}

func (p *Principal) IsAdmin() bool { return p != nil && p.Role == "admin" }

// tokenFromRequest 从请求里取令牌，三种来源，客户端挑最方便的：
//
//  1. Authorization: Bearer <token>            —— 脚本、设备、agent 用这个
//  2. Sec-WebSocket-Protocol: bearer.<token>   —— 浏览器 WebSocket 用这个（唯一能带「头」的合法方式）
//  3. ?token=<token>                           —— 兜底（注意会进 access log，仅内网/调试用）
//
// 顺序即优先级；都没有就返回空串，一律按无令牌处理。
func tokenFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if len(h) >= 7 && strings.EqualFold(h[:7], "bearer ") {
			return strings.TrimSpace(h[7:])
		}
	}
	if p := r.Header.Get("Sec-WebSocket-Protocol"); p != "" {
		for _, part := range strings.Split(p, ",") {
			part = strings.TrimSpace(part)
			if len(part) > 7 && strings.HasPrefix(strings.ToLower(part), "bearer.") {
				return part[7:]
			}
		}
	}
	return r.URL.Query().Get("token")
}

// authenticate 认令牌 —— 整个服务的唯一入口，默认拒绝。
//
// 顺序很关键：先按哈希查人，再看群。这样「群存不存在」这件事
// 对没钥匙的人是完全不可见的，也没法拿它枚举群名。
func (s *Server) authenticate(r *http.Request, groupRef string) (*Principal, error) {
	token := tokenFromRequest(r)
	if token == "" {
		return nil, errUnauthorized
	}
	hash := HashToken(token)
	m, err := s.db.MemberByTokenHash(hash)
	if err != nil || m == nil {
		return nil, errUnauthorized
	}
	// 哈希再比一次（常量时间），避免任何形式的时序侧信道
	if subtle.ConstantTimeCompare([]byte(m.TokenHash), []byte(hash)) != 1 {
		return nil, errUnauthorized
	}

	g, err := s.db.ResolveGroup(groupRef)
	if err != nil {
		// 有真钥匙但群不对/不存在 —— 一律 404，不泄露群是否存在
		return nil, errNotFoundGroup
	}
	if g.ID != m.GroupID {
		// 拿 A 群的钥匙开 B 群的门
		return nil, errNotFoundGroup
	}
	return &Principal{
		GroupID:  m.GroupID,
		MemberID: m.ID,
		Name:     m.Name,
		Kind:     m.Kind,
		Role:     m.Role,
	}, nil
}

func (s *Server) requireMember(r *http.Request, groupRef string) (*Principal, error) {
	return s.authenticate(r, groupRef)
}

func (s *Server) requireAdmin(r *http.Request, groupRef string) (*Principal, error) {
	p, err := s.authenticate(r, groupRef)
	if err != nil {
		return nil, err
	}
	if !p.IsAdmin() {
		return nil, errForbidden
	}
	return p, nil
}
