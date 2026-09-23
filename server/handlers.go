package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxJSONBody    = 1 << 20 // 1 MiB，够大，防的是内存被打爆
	maxMessageBody = 64 << 10
	defaultLimit   = 200
	maxLimit       = 1000
)

// ---------- 统一响应 ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="tigertally"`)
	}
	writeJSON(w, status, apiError{Error: msg, Code: code})
}

func (s *Server) writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUnauthorized):
		writeError(w, http.StatusUnauthorized, "unauthorized", "缺少令牌或令牌无效")
	case errors.Is(err, errForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "需要管理员令牌")
	default:
		writeError(w, http.StatusNotFound, "not_found", "群不存在或令牌不属于这个群")
	}
}

// decodeJSONBody 读请求体。用 MaxBytesReader 卡住上限。
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		return errors.New("请求体不是合法 JSON")
	}
	return nil
}

// ---------- 建群 ----------

type createGroupReq struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
}

// handleCreateGroup 是唯一不需要令牌的业务接口 —— 不然第一把钥匙没处领。
// 它不会读写任何已有数据，也绝不返回别人的令牌。
// 想要彻底关门，就设 --bootstrap-token。
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	if s.cfg.BootstrapToken != "" {
		got := tokenFromRequest(r)
		if got == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "建群需要口令（bootstrap token）")
			return
		}
		if !constantTimeEqual(got, s.cfg.BootstrapToken) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "建群口令不对")
			return
		}
	}

	var req createGroupReq
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	g, owner, token, err := s.db.CreateGroup(req.Name, req.Owner)
	if err != nil {
		s.writeDBError(w, err)
		return
	}
	s.logf("建群 group=%s name=%q owner=%s", g.ID, g.Name, owner.Name)
	writeJSON(w, http.StatusCreated, map[string]any{
		"group_id":    g.ID,
		"group_name":  g.Name,
		"member_id":   owner.ID,
		"member_name": owner.Name,
		"role":        owner.Role,
		"token":       token, // 明文只出现这一次
		"created_at":  time.UnixMilli(g.CreatedAt).UTC().Format(time.RFC3339Nano),
	})
}

// ---------- 成员 ----------

type addMemberReq struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// handleAddMember POST /api/{group}/members —— 需要管理员令牌。
func (s *Server) handleAddMember(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("group")
	admin, err := s.requireAdmin(r, ref)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}

	var req addMemberReq
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	m, token, err := s.db.AddMember(admin.GroupID, req.Name, req.Kind)
	if err != nil {
		s.writeDBError(w, err)
		return
	}
	s.logf("加成员 group=%s name=%q kind=%s", m.GroupID, m.Name, m.Kind)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         m.ID,
		"group_id":   m.GroupID,
		"name":       m.Name,
		"kind":       m.Kind,
		"role":       m.Role,
		"token":      token, // 明文只出现这一次
		"created_at": time.UnixMilli(m.CreatedAt).UTC().Format(time.RFC3339Nano),
	})
}

// handleListMembers GET /api/{group}/members —— 群里任何成员都能看名册。
// 永远不会返回 token 或 token_hash。
func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	p, err := s.requireMember(r, r.PathValue("group"))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	members, err := s.db.ListMembers(p.GroupID)
	if err != nil {
		s.writeDBError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(members))
	for _, m := range members {
		out = append(out, map[string]any{
			"id":         m.ID,
			"name":       m.Name,
			"kind":       m.Kind,
			"role":       m.Role,
			"created_at": time.UnixMilli(m.CreatedAt).UTC().Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"group_id": p.GroupID,
		"members":  out,
		"count":    len(out),
	})
}

// ---------- 消息 ----------

// handlePostMessage POST /api/{group}/messages —— 一条 HTTP 就是一条消息。
//
// 发信人由令牌决定，客户端说了不算 —— 想冒充别人，得先有别人的钥匙。
// 请求体既接受 {"body":"..."}，也接受裸文本（给不会 JSON 的单片机留的路）。
func (s *Server) handlePostMessage(w http.ResponseWriter, r *http.Request) {
	p, err := s.requireMember(r, r.PathValue("group"))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}

	body, err := readMessageBody(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	m, err := s.db.InsertMessage(p.GroupID, &Member{
		ID: p.MemberID, Name: p.Name, Kind: p.Kind,
	}, body)
	if err != nil {
		s.writeDBError(w, err)
		return
	}

	// 先落库（上面那步），再广播 —— 推丢了也不影响持久化
	if frame, err := json.Marshal(wsMessageFrame(*m)); err == nil {
		s.hub.Publish(p.GroupID, frame)
	}
	s.logf("消息 group=%s id=%d from=%s bytes=%d online=%d",
		p.GroupID, m.ID, p.Name, len(body), s.hub.Count(p.GroupID))

	writeJSON(w, http.StatusCreated, m)
}

func readMessageBody(w http.ResponseWriter, r *http.Request) (string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return "", errors.New("读请求体失败（可能超长）")
	}

	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "json") {
		var req struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return "", errors.New(`请求体不是合法 JSON，应为 {"body":"内容"}`)
		}
		return validateBody(req.Body)
	}

	// 不是 JSON 就当作裸文本（curl -d "话" 也能直接用）
	if len(strings.TrimSpace(string(raw))) == 0 {
		return "", errors.New(`消息为空；JSON 用 {"body":"内容"}，或直接发裸文本`)
	}
	return validateBody(string(raw))
}

func validateBody(s string) (string, error) {
	s = strings.TrimRight(s, "\n")
	if strings.TrimSpace(s) == "" {
		return "", errors.New("消息不能为空")
	}
	if len(s) > maxMessageBody {
		return "", errors.New("消息太长（上限 64 KiB）")
	}
	return s, nil
}

// handleGetMessages GET /api/{group}/messages?since=<id>&limit=<n>
//
// 设备轮询用。返回 id > since 的消息，升序。
// latest 是「下次该传的 since」：没有新消息时等于传进来的 since。
func (s *Server) handleGetMessages(w http.ResponseWriter, r *http.Request) {
	p, err := s.requireMember(r, r.PathValue("group"))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}

	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "since 必须是非负整数")
			return
		}
		since = n
	}

	limit := defaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit 必须是正整数")
			return
		}
		if n > maxLimit {
			n = maxLimit
		}
		limit = n
	}

	// 多要一条用来判断还有没有
	msgs, err := s.db.MessagesSince(p.GroupID, since, limit+1)
	if err != nil {
		s.writeDBError(w, err)
		return
	}
	hasMore := len(msgs) > limit
	if hasMore {
		msgs = msgs[:limit]
	}
	latest := since
	if len(msgs) > 0 {
		latest = msgs[len(msgs)-1].ID
	}
	if msgs == nil {
		msgs = []Message{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"group_id": p.GroupID,
		"messages": msgs,
		"count":    len(msgs),
		"latest":   latest,
		"has_more": hasMore,
	})
}
