package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsWriteWait  = 10 * time.Second
	wsPongWait   = 60 * time.Second
	wsPingPeriod = 30 * time.Second
	wsMaxFrame   = 4 << 10 // 我们只收心跳和 close，收不到这么大的东西
	wsBacklogMax = 200
)

func (s *Server) upgrader(token string) websocket.Upgrader {
	return websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		// 浏览器 WebSocket 不能用 Authorization 头，所以允许用子协议带令牌：
		//   new WebSocket(url, ["bearer." + token])
		// 服务端必须把这个子协议原样回一个，否则浏览器直接断。
		Subprotocols: []string{"bearer." + token},
		CheckOrigin:  s.checkOrigin,
	}
}

// handleWS 是实时订阅口：GET /api/{group}/ws（必须先过鉴权才会升级）。
//
// 只出不进：客户端通过 HTTP POST 发消息，这里只负责「秒收」。
// 这样「一条 HTTP 就是一条消息」这条信条在实时通道上也成立。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	p, err := s.requireMember(r, r.PathValue("group"))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}

	// ?since=<id> 可选：连上后先把这几条补给你，再进入实时流。
	// 先订阅、后查历史，宁可重复不可漏 —— 重复客户端按 id 去重就行。
	since := int64(0)
	if v := r.URL.Query().Get("since"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			since = n
		}
	}

	sub := s.hub.Subscribe(p.GroupID)
	defer s.hub.Unsubscribe(p.GroupID, sub)

	up := s.upgrader(tokenFromRequest(r))
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade 失败时自己已经写过响应了
		s.logf("ws upgrade 失败 group=%s: %v", p.GroupID, err)
		return
	}
	s.logf("ws 已连接 group=%s member=%s proto=%q online=%d",
		p.GroupID, p.Name, conn.Subprotocol(), s.hub.Count(p.GroupID))
	defer conn.Close()

	latest, _ := s.db.LatestID(p.GroupID)
	hello, _ := json.Marshal(map[string]any{
		"type":      "hello",
		"group_id":  p.GroupID,
		"member_id": p.MemberID,
		"member":    p.Name,
		"kind":      p.Kind,
		"role":      p.Role,
		"since":     since,
		"latest":    latest,
		"online":    s.hub.Count(p.GroupID),
	})

	conn.SetReadLimit(wsMaxFrame)
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

	// 读循环：只为了拿到 pong 和 close。收到别的一律忽略。
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	write := func(b []byte) bool {
		_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
		return conn.WriteMessage(websocket.TextMessage, b) == nil
	}
	if !write(hello) {
		return
	}

	// 补历史（subscribe 已经先发生，所以这中间新来的会既走这里也走实时流，重复可容忍）
	if since > 0 {
		if backlog, err := s.db.MessagesSince(p.GroupID, since, wsBacklogMax); err == nil {
			for i := range backlog {
				frame, err := json.Marshal(wsMessageFrame(backlog[i]))
				if err != nil {
					continue
				}
				if !write(frame) {
					return
				}
			}
		}
	}

	ping := time.NewTicker(wsPingPeriod)
	defer ping.Stop()

	for {
		select {
		case <-readDone:
			s.logf("ws 已断开 group=%s member=%s", p.GroupID, p.Name)
			return

		case <-sub.closed:
			// Hub 判定这个订阅者太慢，主动踢掉让他重连补齐
			s.logf("ws 太慢被断开 group=%s member=%s", p.GroupID, p.Name)
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "slow consumer"))
			return

		case payload := <-sub.ch:
			if !write(payload) {
				return
			}

		case <-ping.C:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// wsMessageFrame 在消息本体上加一个 type 字段，方便客户端一条 switch 分流。
func wsMessageFrame(m Message) map[string]any {
	return map[string]any{
		"type":       "message",
		"id":         m.ID,
		"group_id":   m.GroupID,
		"sender_id":  m.SenderID,
		"sender":     m.Sender,
		"kind":       m.Kind,
		"body":       m.Body,
		"created_at": m.CreatedAt,
		"ts":         m.TS,
	}
}
