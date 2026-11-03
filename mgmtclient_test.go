package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewMgmtClientRejectsNonLoopbackAndHTTPS(t *testing.T) {
	if _, err := NewMgmtClient("https://127.0.0.1:8317", "k"); err == nil {
		t.Fatal("https base must be rejected")
	}
	if _, err := NewMgmtClient("http://10.0.0.5:8317", "k"); err == nil {
		t.Fatal("non-loopback base must be rejected")
	}
	if _, err := NewMgmtClient("http://localhost:8317", "k"); err != nil {
		t.Fatalf("localhost must be accepted: %v", err)
	}
}

func TestMgmtErrorMapping(t *testing.T) {
	cases := []struct {
		status   int
		wantCode string
		authWant bool
		rateWant bool
	}{
		{401, "auth_required", true, false},
		{403, "auth_required", true, false},
		{429, "rate_limited", false, true},
		{500, "unavailable", false, false},
	}
	for _, c := range cases {
		mgmt := newFakeMgmt(t, fakeMgmtOptions{status: c.status}, func(r *http.Request) (int, any, bool) {
			return 0, nil, false
		})
		_, err := mgmt.ListAuthFiles(context.Background())
		if err == nil {
			t.Fatalf("status %d must error", c.status)
		}
		var me *MgmtError
		if !asMgmtErr(err, &me) || me.Code != c.wantCode {
			t.Fatalf("status %d code = %+v want %s", c.status, me, c.wantCode)
		}
		if isMgmtAuthError(err) != c.authWant || isMgmtRateLimited(err) != c.rateWant {
			t.Fatalf("status %d classification wrong", c.status)
		}
		// 安全文案不携带上游 body。
		if me.Message != "" && containsAny(me.Message, []string{"<", "secret"}) {
			t.Fatalf("unsafe message: %s", me.Message)
		}
	}
}

func asMgmtErr(err error, target **MgmtError) bool {
	me, ok := err.(*MgmtError)
	if ok {
		*target = me
	}
	return ok
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if len(n) > 0 && len(s) >= len(n) && indexOfStr(s, n) >= 0 {
			return true
		}
	}
	return false
}

func indexOfStr(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestMgmtClientRejectsRedirect(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "http://127.0.0.1:1/evil", http.StatusFound)
			return
		}
		// 直连 /evil 不应发生；发生即说明跟随了重定向。
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, errNew := NewMgmtClient(server.URL, "k")
	if errNew != nil {
		t.Fatal(errNew)
	}
	// 直接对非白名单路径发起请求应被客户端自身拒绝。
	if err := client.do(context.Background(), http.MethodGet, "/start", nil, nil); err == nil {
		t.Fatal("non-whitelisted path must be rejected")
	}
}

func TestIsAllowedPathWhitelist(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/v0/management/auth-files", true},
		{http.MethodGet, "/v0/management/auth-files/models?name=x.json", true},
		{http.MethodPost, "/v0/management/api-call", true},
		{http.MethodPost, "/v0/management/plugins/commandcode-go/quota", true},
		{http.MethodPost, "/v0/management/plugins/../quota", false},
		{http.MethodPost, "/v0/management/plugins/bad%20id/quota", false},
		{http.MethodGet, "/v0/management/config", false},
		{http.MethodPost, "/v0/management/api-call/../config", false},
		{http.MethodDelete, "/v0/management/auth-files", false},
	}
	for _, c := range cases {
		if got := isAllowedPath(c.method, c.path); got != c.want {
			t.Fatalf("isAllowedPath(%s,%s)=%v want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestFetchPluginQuotaRejectsUnsafePluginID(t *testing.T) {
	mgmt := newFakeMgmt(t, fakeMgmtOptions{}, func(r *http.Request) (int, any, bool) {
		return 0, nil, false
	})
	if _, err := mgmt.FetchPluginQuota(context.Background(), "../evil", "idx"); err == nil {
		t.Fatal("unsafe plugin id must be rejected before any request")
	}
}
