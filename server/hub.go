package main

import "sync"

// Hub 是「实时通道」的内存广播台。
//
// 它不存消息 —— 消息的唯一真相在 SQLite。Hub 只负责把刚落库的消息
// 推给正在线的人。掉了线、或者推的时候缓冲区满了，客户端下次
// GET /messages?since=<id> 就能补齐，永远不会丢。
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[*subscriber]struct{}
}

type subscriber struct {
	ch     chan []byte
	closed chan struct{}
	once   sync.Once
}

func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[*subscriber]struct{})}
}

func (h *Hub) Subscribe(groupID string) *subscriber {
	s := &subscriber{
		ch:     make(chan []byte, 64),
		closed: make(chan struct{}),
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[groupID] == nil {
		h.subs[groupID] = make(map[*subscriber]struct{})
	}
	h.subs[groupID][s] = struct{}{}
	return s
}

func (h *Hub) Unsubscribe(groupID string, s *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m := h.subs[groupID]; m != nil {
		delete(m, s)
		if len(m) == 0 {
			delete(h.subs, groupID)
		}
	}
	s.Close()
}

// Publish 把一条消息推给群里所有在线订阅者。
// 非阻塞：谁接不过来就断开谁 —— 他重连后会用 since=<id> 补齐，
// 不能因为一个慢客户端把整个群卡住。
func (h *Hub) Publish(groupID string, payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.subs[groupID] {
		select {
		case s.ch <- payload:
		default:
			s.Close()
		}
	}
}

func (s *subscriber) Close() {
	s.once.Do(func() { close(s.closed) })
}

// Count 在线订阅数（用于日志和健康检查，不做鉴权）。
func (h *Hub) Count(groupID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[groupID])
}
