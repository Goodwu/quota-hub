package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// fakeMgmtOptions tunes the fake management server behavior.
type fakeMgmtOptions struct {
	status  int    // non-zero forces this HTTP status on every call
	authKey string // expected Authorization header value ("" = skip check)
}

// fakeHandler answers one request: (statusCode, payload, matched). An
// unmatched path answers 404.
type fakeHandler func(r *http.Request) (int, any, bool)

// newFakeMgmt starts a loopback httptest server standing in for the CPA
// management API.
func newFakeMgmt(t *testing.T, opts fakeMgmtOptions, handler fakeHandler) *MgmtClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if opts.status != 0 {
			w.WriteHeader(opts.status)
			return
		}
		if opts.authKey != "" {
			if got := r.Header.Get("Authorization"); got != opts.authKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		if code, payload, ok := handler(r); ok {
			if code == 0 {
				code = http.StatusOK
			}
			if payload != nil {
				w.Header().Set("Content-Type", "application/json")
			}
			w.WriteHeader(code)
			if payload != nil {
				_ = json.NewEncoder(w).Encode(payload)
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	client, errNew := NewMgmtClient(server.URL, "test-key")
	if errNew != nil {
		t.Fatalf("new fake mgmt: %v", errNew)
	}
	return client
}

// shortTempDir returns a short-lived directory safe for unix socket paths
// (macOS sun_path limit is 104 bytes; t.TempDir() is usually longer).
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, errMkdir := os.MkdirTemp("/tmp", fmt.Sprintf("qh%d-", os.Getpid()))
	if errMkdir != nil {
		t.Fatalf("short temp dir: %v", errMkdir)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	raw, errRead := io.ReadAll(r)
	if errRead != nil {
		t.Fatalf("read body: %v", errRead)
	}
	return raw
}
