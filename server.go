// Unix Socket HTTP 服务：目录 0700、socket 0600；仅两个只读端点。
// 残留 socket 先试连——活着说明有旧实例（报错退出），连不通才替换。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	queryBodyLimit = 8 << 10
	socketDirMode  = 0o700
	socketFileMode = 0o600
)

type Server struct {
	engine *Engine
	listen net.Listener
	http   *http.Server
}

// NewServer prepares (but does not start) the socket listener.
func NewServer(socketPath string, engine *Engine) (*Server, error) {
	dir := filepath.Dir(socketPath)
	if errMkdir := os.MkdirAll(dir, socketDirMode); errMkdir != nil {
		return nil, fmt.Errorf("create socket dir: %w", errMkdir)
	}
	if info, errStat := os.Stat(dir); errStat == nil {
		if perm := info.Mode().Perm(); perm != socketDirMode {
			return nil, fmt.Errorf("socket dir %s mode is %o, want 0700 (refusing to loosen)", dir, perm)
		}
	}
	if errProbe := probeStaleSocket(socketPath); errProbe != nil {
		return nil, errProbe
	}
	listener, errListen := net.Listen("unix", socketPath)
	if errListen != nil {
		return nil, fmt.Errorf("listen %s: %w", socketPath, errListen)
	}
	if errChmod := os.Chmod(socketPath, socketFileMode); errChmod != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("chmod socket: %w", errChmod)
	}
	srv := &Server{
		engine: engine,
		listen: listener,
		http: &http.Server{
			Handler:           engine.routes(),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       20 * time.Second,
			WriteTimeout:      20 * time.Second,
		},
	}
	return srv, nil
}

// probeStaleSocket removes a leftover socket only after confirming nothing is
// listening on it. An answerable socket means another quota-hub instance (or
// an unknown service) owns it — never delete or kill.
func probeStaleSocket(socketPath string) error {
	info, errStat := os.Stat(socketPath)
	if os.IsNotExist(errStat) {
		return nil
	}
	if errStat != nil {
		return fmt.Errorf("stat socket: %w", errStat)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s exists and is not a socket; refusing to touch it", socketPath)
	}
	probe := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: 2 * time.Second,
	}
	resp, errProbe := probe.Get("http://quota-hub.local/v1/capabilities")
	if errProbe == nil {
		_ = resp.Body.Close()
		return fmt.Errorf("socket %s is served by a running instance; refusing to take over", socketPath)
	}
	if errRemove := os.Remove(socketPath); errRemove != nil {
		return fmt.Errorf("remove stale socket: %w", errRemove)
	}
	return nil
}

func (e *Engine) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/capabilities", e.handleCapabilities)
	mux.HandleFunc("/v1/quota/query", e.handleQuery)
	return mux
}

func (e *Engine) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "只支持 GET")
		return
	}
	writeJSON(w, http.StatusOK, e.Capabilities(r.Context(), time.Now()))
}

func (e *Engine) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "只支持 POST")
		return
	}
	body, errRead := io.ReadAll(io.LimitReader(r.Body, queryBodyLimit+1))
	if errRead != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "请求体读取失败")
		return
	}
	if len(body) > queryBodyLimit {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "bad_request", "请求体过大")
		return
	}
	var req QueryRequest
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&req); errDecode != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "请求体必须是 {model, refresh}")
		return
	}
	model := strings.TrimSpace(req.Model)
	if model == "" || len(model) > 256 || strings.ContainsAny(model, " \t\r\n") {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "model 必须是完整的模型 ID")
		return
	}

	log.Printf("query model=%s refresh=%v", model, req.Refresh)
	ctx, cancel := context.WithTimeout(r.Context(), e.cfg.TotalTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, e.Query(ctx, model, req.Refresh))
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		log.Printf("marshal response: %v", errMarshal)
		status = http.StatusInternalServerError
		raw = []byte(`{"error":"internal","message":"响应序列化失败"}`)
	}
	// 显式 Content-Length：注入侧的 ZCode 主进程客户端是手写 HTTP/1.1 解析，
	// 不支持 chunked；设了长度 Go 就不会切换 chunked 编码。
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(status)
	if _, errWrite := w.Write(raw); errWrite != nil {
		log.Printf("write response: %v", errWrite)
	}
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": code, "message": message})
}

// Serve runs until ctx is canceled or the listener fails.
func (s *Server) Serve(ctx context.Context) error {
	errServe := make(chan error, 1)
	go func() {
		if err := s.http.Serve(s.listen); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errServe <- err
		}
		close(errServe)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.http.Shutdown(shutdownCtx)
	case err := <-errServe:
		return err
	}
}

// Close removes the socket file this process created.
func (s *Server) Close() {
	_ = s.listen.Close()
}
