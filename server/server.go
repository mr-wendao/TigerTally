package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config 是服务的全部可调项。零值即安全默认。
type Config struct {
	Addr           string   // 监听地址
	DBPath         string   // SQLite 文件
	BootstrapToken string   // 非空则建群必须带这个口令
	AllowOrigins   []string // 额外放行的跨域来源；含 "*" 表示不检查
}

type Server struct {
	cfg    Config
	db     *DB
	hub    *Hub
	logger *log.Logger

	allowAllOrigins bool
	origins         map[string]bool
}

func NewServer(cfg Config, db *DB, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	s := &Server{cfg: cfg, db: db, hub: NewHub(), logger: logger, origins: map[string]bool{}}
	for _, o := range cfg.AllowOrigins {
		o = strings.TrimSpace(o)
		if o == "*" {
			s.allowAllOrigins = true
			continue
		}
		if o != "" {
			s.origins[strings.ToLower(o)] = true
		}
	}
	return s
}

func (s *Server) logf(format string, args ...any) {
	s.logger.Printf(format, args...)
}

// Handler 组装路由 + 中间件。
//
// 注意 /api/ 下没有「公开」路由：唯一不要令牌的是 POST /api/groups
// （不然第一把钥匙没处领），它不读也不写任何已有数据。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("/", s.handleRoot)

	mux.HandleFunc("POST /api/groups", s.handleCreateGroup)

	mux.HandleFunc("POST /api/{group}/messages", s.handlePostMessage)
	mux.HandleFunc("GET /api/{group}/messages", s.handleGetMessages)
	mux.HandleFunc("GET /api/{group}/ws", s.handleWS)
	mux.HandleFunc("POST /api/{group}/members", s.handleAddMember)
	mux.HandleFunc("GET /api/{group}/members", s.handleListMembers)

	// 兜底：/api/ 下任何别的路径也先验令牌，认不出人就是 401。
	// 这样「无令牌一律拒绝」没有死角，连探测路由都进不来。
	mux.HandleFunc("/api/", s.handleAPIFallback)

	return s.recoverer(s.requestLogger(s.cors(mux)))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"service": "tigertally",
		"version": version,
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "虎符 tigertally %s —— 自托管群聊消息服务\n\n", version)
	fmt.Fprint(w, "接口：\n"+
		"  POST /api/groups                     建群，返回管理员令牌\n"+
		"  POST /api/{群}/messages              发消息（Bearer 令牌）\n"+
		"  GET  /api/{群}/messages?since=<id>   拉消息\n"+
		"  GET  /api/{群}/ws                    实时订阅（WebSocket）\n"+
		"  POST /api/{群}/members               加成员（管理员令牌）\n"+
		"  GET  /api/{群}/members               成员列表\n"+
		"  GET  /healthz                        存活检查\n")
}

// handleAPIFallback 保证 /api/ 下没有「不验令牌就能拿到响应」的路径。
func (s *Server) handleAPIFallback(w http.ResponseWriter, r *http.Request) {
	token := tokenFromRequest(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "缺少令牌")
		return
	}
	if _, err := s.db.MemberByTokenHash(HashToken(token)); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "令牌无效")
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "没有这个接口")
}

// ---------- 中间件 ----------

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logf("panic %s %s: %v", r.Method, r.URL.Path, rec)
				writeError(w, http.StatusInternalServerError, "internal", "服务内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Hijack 让日志中间件不挡住 WebSocket 升级
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("底层 ResponseWriter 不支持 hijack")
	}
	return h.Hijack()
}

// requestLogger 记方法和路径，绝不记查询串 —— WebSocket 的令牌可能在里面。
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		s.logf("%s %s %d %dB %s", r.Method, r.URL.Path, rec.status, rec.bytes,
			time.Since(start).Round(time.Millisecond))
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && s.originAllowed(origin, r.Host) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originAllowed 判断浏览器来源。
// 默认：同源 + file:// 的 null + 显式配置的来源。
// 鉴权靠 Bearer 令牌而非 Cookie，所以跨域本身不是漏洞，这里只是纵深防御。
func (s *Server) originAllowed(origin, host string) bool {
	if origin == "" {
		return true // 非浏览器客户端（curl / 设备）没有 Origin
	}
	if s.allowAllOrigins {
		return true
	}
	if origin == "null" {
		return true // file:// 打开的本地页面
	}
	if s.origins[strings.ToLower(origin)] {
		return true
	}
	if u, err := url.Parse(origin); err == nil && u.Host != "" && strings.EqualFold(u.Host, host) {
		return true
	}
	return false
}

func (s *Server) checkOrigin(r *http.Request) bool {
	return s.originAllowed(r.Header.Get("Origin"), r.Host)
}

// ---------- 错误映射 ----------

func (s *Server) writeDBError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrBadInput):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	default:
		s.logf("数据库错误: %v", err)
		writeError(w, http.StatusInternalServerError, "internal", "服务内部错误")
	}
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ---------- 启动 ----------

// Run 起服务，阻塞到 ctx 被取消（Ctrl-C / SIGTERM）或监听出错。
func Run(ctx context.Context, cfg Config, out io.Writer) error {
	logger := log.New(out, "", log.LstdFlags)

	db, err := OpenDB(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	s := NewServer(cfg, db, logger)

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("监听 %s: %w", cfg.Addr, err)
	}

	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// 刻意不设 WriteTimeout：WebSocket 是长连接
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	s.logf("虎符 tigertally %s 已启动", version)
	s.logf("监听   http://%s", ln.Addr())
	s.logf("数据库 %s", db.path)
	if cfg.BootstrapToken != "" {
		s.logf("建群需要 bootstrap 口令")
	} else {
		s.logf("建群当前无需口令（任何人可建新群）；要关门请设 --bootstrap-token")
	}
	s.logf("第一个群：curl -X POST http://%s/api/groups -H 'Content-Type: application/json' -d '{\"name\":\"我的群\",\"owner\":\"我\"}'", ln.Addr())

	select {
	case <-ctx.Done():
		s.logf("收到退出信号，正在收尾…")
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			s.logf("关闭时出错: %v", err)
		}
		if err := db.Close(); err != nil {
			s.logf("关数据库出错: %v", err)
		}
		s.logf("已停止")
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
