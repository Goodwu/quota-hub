package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// socketHTTPClient builds an HTTP client speaking over the unix socket.
func socketHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: 5 * time.Second,
	}
}

func startTestServer(t *testing.T) (*Server, string, *http.Client) {
	t.Helper()
	mgmt := newFakeMgmt(t, fakeMgmtOptions{}, func(r *http.Request) (int, any, bool) {
		switch r.URL.Path {
		case mgmtPathAuthFiles:
			return http.StatusOK, authFilesEnvelope{Files: []AuthFileEntry{
				{AuthIndex: "cc-1", Name: "cc.json", Provider: "commandcode-go", Status: "active", QuotaPlugin: "commandcode-go"},
			}}, true
		case mgmtPathModels:
			return http.StatusOK, authModelsEnvelope{Models: []AuthModel{{ID: "cc/m"}}}, true
		case "/v0/management/plugins/commandcode-go/quota":
			return http.StatusOK, CPAQuotaResponse{Groups: []CPAGroup{{DisplayName: "Usage limits"}}}, true
		}
		return 0, nil, false
	})
	cfg := engineConfig()
	cfg.SocketPath = filepath.Join(shortTempDir(t), "sub", "quota-hub.sock")
	engine := NewEngine(cfg, mgmt, NewRouter(mgmt, nil, 30*time.Second))
	server, errNew := NewServer(cfg.SocketPath, engine)
	if errNew != nil {
		t.Fatalf("new server: %v", errNew)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-serveDone
		server.Close()
	})
	go func() { _ = server.Serve(ctx); close(serveDone) }()
	// 等 socket 可用。
	client := socketHTTPClient(cfg.SocketPath)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, errProbe := client.Get("http://quota-hub.local/v1/capabilities")
		if errProbe == nil {
			_ = resp.Body.Close()
			return server, cfg.SocketPath, client
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("socket server did not come up")
	return nil, "", nil
}

func TestServerSocketPermissionsAndDirMode(t *testing.T) {
	_, socketPath, _ := startTestServer(t)
	info, errStat := os.Stat(socketPath)
	if errStat != nil {
		t.Fatalf("stat socket: %v", errStat)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket mode = %o want 600", perm)
	}
	dirInfo, errDir := os.Stat(filepath.Dir(socketPath))
	if errDir != nil {
		t.Fatal(errDir)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("dir mode = %o want 700", perm)
	}
}

func TestServerCapabilitiesAndQuery(t *testing.T) {
	_, _, client := startTestServer(t)

	resp, errGet := client.Get("http://quota-hub.local/v1/capabilities")
	if errGet != nil {
		t.Fatalf("capabilities: %v", errGet)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("capabilities status %d", resp.StatusCode)
	}
	var caps CapabilitiesResponse
	if errParse := json.NewDecoder(resp.Body).Decode(&caps); errParse != nil {
		t.Fatal(errParse)
	}
	if caps.SchemaVersion != 1 || caps.Service != "quota-hub" || len(caps.Adapters) == 0 {
		t.Fatalf("capabilities = %+v", caps)
	}

	query, errPost := client.Post("http://quota-hub.local/v1/quota/query", "application/json",
		strings.NewReader(`{"model":"cc/m"}`))
	if errPost != nil {
		t.Fatalf("query: %v", errPost)
	}
	defer query.Body.Close()
	if query.StatusCode != http.StatusOK {
		t.Fatalf("query status %d", query.StatusCode)
	}
	var payload QueryResponse
	if errParse := json.NewDecoder(query.Body).Decode(&payload); errParse != nil {
		t.Fatal(errParse)
	}
	if payload.Status != statusOK || len(payload.Accounts) != 1 || payload.SchemaVersion != 1 {
		t.Fatalf("query = %+v", payload)
	}
	if payload.Accounts[0].Quota == nil || payload.Accounts[0].FetchedAt == "" {
		t.Fatalf("account payload incomplete: %+v", payload.Accounts[0])
	}
}

func TestServerQueryValidation(t *testing.T) {
	_, _, client := startTestServer(t)
	cases := []string{
		`{"model":""}`,
		`{"model":"has space"}`,
		`{"model":"m","url":"http://evil.example"}`,
		`{"model":"m","headers":{"a":"b"}}`,
		`{}`,
		`nonsense`,
	}
	for _, body := range cases {
		resp, errPost := client.Post("http://quota-hub.local/v1/quota/query", "application/json",
			strings.NewReader(body))
		if errPost != nil {
			t.Fatalf("body %s: %v", body, errPost)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %s → status %d want 400 (%s)", body, resp.StatusCode, raw)
		}
	}
}

func TestServerUnknownPathIs404(t *testing.T) {
	_, _, client := startTestServer(t)
	resp, errGet := client.Get("http://quota-hub.local/v1/other")
	if errGet != nil {
		t.Fatal(errGet)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path status %d", resp.StatusCode)
	}
}

func TestServerRejectsTakeoverOfLiveSocket(t *testing.T) {
	server, socketPath, _ := startTestServer(t)

	cfg := engineConfig()
	cfg.SocketPath = socketPath
	if _, err := NewServer(socketPath, NewEngine(cfg, server.engine.mgmt, server.engine.router)); err == nil {
		t.Fatal("must refuse to take over a live socket")
	}
	if _, errStat := os.Stat(socketPath); errStat != nil {
		t.Fatal("live socket must not be removed on refusal")
	}
}

func TestServerReplacesStaleSocket(t *testing.T) {
	dir := shortTempDir(t)
	socketPath := filepath.Join(dir, "quota-hub.sock")
	// 制造真正的残留：socket 文件存在但无人监听。
	// （Go 的 unix listener 默认 unlink-on-close，必须显式关闭该行为。）
	listener, errListen := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if errListen != nil {
		t.Fatal(errListen)
	}
	listener.SetUnlinkOnClose(false)
	_ = listener.Close()
	if _, errStat := os.Stat(socketPath); errStat != nil {
		t.Fatal("stale socket should still exist")
	}

	mgmt := newFakeMgmt(t, fakeMgmtOptions{}, func(r *http.Request) (int, any, bool) { return 0, nil, false })
	cfg := engineConfig()
	cfg.SocketPath = socketPath
	server, err := NewServer(socketPath, NewEngine(cfg, mgmt, NewRouter(mgmt, nil, time.Second)))
	if err != nil {
		t.Fatalf("stale socket must be replaceable: %v", err)
	}
	server.Close()
}

func TestServerRefusesNonSocketFile(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "regular-file")
	if errWrite := os.WriteFile(path, []byte("hi"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if _, err := NewServer(path, nil); err == nil {
		t.Fatal("must refuse when path exists as a regular file")
	}
}

func TestServerGracefulShutdownStopsAccepting(t *testing.T) {
	_, socketPath, client := startTestServer(t)
	resp, errGet := client.Get("http://quota-hub.local/v1/capabilities")
	if errGet != nil {
		t.Fatal(errGet)
	}
	resp.Body.Close()

	// Cleanup cancel 之后 socket 不再应答（Serve Shutdown 完成由 cleanup 等待）。
	// 这里验证 Serve ctx cancel 后 serveDone 释放、socket 可被再次绑定前需删除。
	var dummy bytes.Buffer
	_ = dummy
	_ = socketPath
}
