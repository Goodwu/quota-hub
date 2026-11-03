package main

import (
	"net/http"
	"testing"
	"time"
)

func fakeAuthFiles() []AuthFileEntry {
	return []AuthFileEntry{
		{AuthIndex: "cc-1", Name: "commandcode-go-main.json", Provider: "commandcode-go", Label: "CC 主号", Status: "active", QuotaPlugin: "commandcode-go", UpdatedAt: "2026-11-01T00:00:00Z"},
		{AuthIndex: "cx-1", Name: "codex-a.json", Provider: "codex", Label: "Codex A", Status: "active"},
		{AuthIndex: "cx-2", Name: "codex-b.json", Provider: "codex", Status: "active", Disabled: true},
		{AuthIndex: "ag-1", Name: "antigravity-x.json", Provider: "antigravity", Status: "active"},
	}
}

func fakeModelsFor(name string) []string {
	switch name {
	case "commandcode-go-main.json":
		return []string{"cc/deepseek/deepseek-v4-flash", "cc/glm/glm-5.3"}
	case "codex-a.json":
		return []string{"codex/gpt-6-astra", "gpt-6-astra"}
	case "antigravity-x.json":
		return []string{"gemini-3-pro-preview"}
	}
	return nil
}

// fakeMgmtWithFilesAndModels serves the auth-files list plus per-name models.
func fakeMgmtWithFilesAndModels(t *testing.T) *MgmtClient {
	t.Helper()
	return newFakeMgmt(t, fakeMgmtOptions{}, func(r *http.Request) (int, any, bool) {
		switch r.URL.Path {
		case mgmtPathAuthFiles:
			return http.StatusOK, authFilesEnvelope{Files: fakeAuthFiles()}, true
		case mgmtPathModels:
			name := r.URL.Query().Get("name")
			models := fakeModelsFor(name)
			converted := make([]AuthModel, 0, len(models))
			for _, m := range models {
				converted = append(converted, AuthModel{ID: m})
			}
			return http.StatusOK, authModelsEnvelope{Models: converted}, true
		}
		return 0, nil, false
	})
}

func TestRouterExactModelMatchSkipsDisabled(t *testing.T) {
	router := NewRouter(fakeMgmtWithFilesAndModels(t), nil, 30*time.Second)
	accounts, err := router.Accounts(t.Context(), "codex/gpt-6-astra", false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(accounts) != 1 || accounts[0].AuthIndex != "cx-1" {
		t.Fatalf("codex accounts = %+v (disabled cx-2 must be skipped)", accounts)
	}

	accounts, err = router.Accounts(t.Context(), "cc/deepseek/deepseek-v4-flash", false)
	if err != nil {
		t.Fatalf("route cc: %v", err)
	}
	if len(accounts) != 1 || accounts[0].QuotaPluginID != "commandcode-go" {
		t.Fatalf("cc accounts = %+v", accounts)
	}
}

func TestRouterUnmappedThenExplicitMappingBySegments(t *testing.T) {
	router := NewRouter(fakeMgmtWithFilesAndModels(t), []ModelMapping{{Prefix: "cc", Provider: "commandcode-go"}}, 30*time.Second)

	accounts, err := router.Accounts(t.Context(), "cc/unknown/new-model", false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(accounts) != 1 || accounts[0].AuthIndex != "cc-1" {
		t.Fatalf("explicit mapping failed: %+v", accounts)
	}

	// 段前缀：ccc/... 不得命中 cc 映射。
	accounts, err = router.Accounts(t.Context(), "ccc/unknown", false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("cc 前缀不得命中 ccc 模型: %+v", accounts)
	}

	// 更长前缀优先。
	routerLong := NewRouter(fakeMgmtWithFilesAndModels(t), []ModelMapping{
		{Prefix: "cc", Provider: "commandcode-go"},
		{Prefix: "cc/deepseek", Provider: "codex"},
	}, 30*time.Second)
	accounts, err = routerLong.Accounts(t.Context(), "cc/deepseek/other", false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(accounts) != 1 || accounts[0].Provider != "codex" {
		t.Fatalf("longest prefix mapping failed: %+v", accounts)
	}
}

func TestSegmentsPrefix(t *testing.T) {
	cases := []struct {
		model, prefix string
		want          bool
	}{
		{"cc/a/b", "cc", true},
		{"cc", "cc", true},
		{"ccx/a", "cc", false},
		{"cc/deepseek/m", "cc/deepseek", true},
		{"cc/deepseekx/m", "cc/deepseek", false},
	}
	for _, c := range cases {
		if got := segmentsPrefix(c.model, c.prefix); got != c.want {
			t.Fatalf("segmentsPrefix(%q,%q)=%v want %v", c.model, c.prefix, got, c.want)
		}
	}
}

func TestAccountIDStableAndOpaque(t *testing.T) {
	a := Account{Provider: "codex", AuthIndex: "idx"}
	b := Account{Provider: "codex", AuthIndex: "idx2"}
	if a.AccountID() != a.AccountID() || a.AccountID() == b.AccountID() {
		t.Fatal("account id must be stable and distinct")
	}
}

func TestRouterAuthFailureFailsSnapshot(t *testing.T) {
	mgmt := newFakeMgmt(t, fakeMgmtOptions{authKey: "Bearer other-key"}, func(r *http.Request) (int, any, bool) {
		return 0, nil, false
	})
	router := NewRouter(mgmt, nil, 30*time.Second)
	_, err := router.Accounts(t.Context(), "cc/deepseek/deepseek-v4-flash", false)
	if err == nil || !isMgmtAuthError(err) {
		t.Fatalf("want auth error, got %v", err)
	}
}
