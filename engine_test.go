package main

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func engineConfig() Config {
	cfg := defaultConfig()
	if errApply := applyDefaults(&cfg); errApply != nil {
		panic(errApply)
	}
	cfg.AccountTimeout = 2 * time.Second
	return cfg
}

// newEngineMgmt serves two accounts: commandcode-go (plugin quota) and an
// antigravity account with no quota path. quotaFail>0 makes the plugin quota
// endpoint answer that status.
func newEngineMgmt(t *testing.T, quotaFail *atomic.Int32) *MgmtClient {
	t.Helper()
	return newFakeMgmt(t, fakeMgmtOptions{}, func(r *http.Request) (int, any, bool) {
		switch r.URL.Path {
		case mgmtPathAuthFiles:
			return http.StatusOK, authFilesEnvelope{Files: []AuthFileEntry{
				{AuthIndex: "cc-1", Name: "cc.json", Provider: "commandcode-go", Status: "active", QuotaPlugin: "commandcode-go"},
				{AuthIndex: "ag-1", Name: "ag.json", Provider: "antigravity", Status: "active"},
			}}, true
		case mgmtPathModels:
			var models []AuthModel
			if r.URL.Query().Get("name") == "cc.json" {
				models = []AuthModel{{ID: "cc/deepseek/deepseek-v4-flash"}}
			} else {
				models = []AuthModel{{ID: "ag-model"}}
			}
			return http.StatusOK, authModelsEnvelope{Models: models}, true
		case "/v0/management/plugins/commandcode-go/quota":
			if quotaFail != nil && quotaFail.Load() != 0 {
				return int(quotaFail.Load()), nil, true
			}
			return http.StatusOK, CPAQuotaResponse{Groups: []CPAGroup{{DisplayName: "Usage limits"}}}, true
		}
		return 0, nil, false
	})
}

func TestEngineQueryOKAndPartial(t *testing.T) {
	mgmt := newEngineMgmt(t, nil)
	engine := NewEngine(engineConfig(), mgmt, NewRouter(mgmt, nil, 30*time.Second))

	resp := engine.Query(t.Context(), "cc/deepseek/deepseek-v4-flash", false)
	if resp.Status != statusOK || len(resp.Accounts) != 1 || resp.Accounts[0].Status != accountOK {
		t.Fatalf("cc query = %+v", resp)
	}
	if resp.Accounts[0].Quota == nil || len(resp.Accounts[0].Quota.Groups) != 1 {
		t.Fatalf("quota payload missing: %+v", resp.Accounts[0])
	}
	if resp.Accounts[0].Source != "plugin/commandcode-go" {
		t.Fatalf("source = %s", resp.Accounts[0].Source)
	}

	// antigravity 账号无插件非 codex → unsupported → partial。
	resp = engine.Query(t.Context(), "ag-model", false)
	if resp.Status != statusPartial {
		t.Fatalf("status = %s want partial", resp.Status)
	}
	if resp.Accounts[0].Status != accountUnsupported {
		t.Fatalf("account status = %s", resp.Accounts[0].Status)
	}
}

func TestEngineQueryUnmappedAndNoKey(t *testing.T) {
	mgmt := newFakeMgmt(t, fakeMgmtOptions{}, func(r *http.Request) (int, any, bool) {
		return http.StatusOK, authFilesEnvelope{}, true
	})
	engine := NewEngine(engineConfig(), mgmt, NewRouter(mgmt, nil, 30*time.Second))

	resp := engine.Query(t.Context(), "nope/model", false)
	if resp.Status != statusUnmapped {
		t.Fatalf("status = %s want unmapped", resp.Status)
	}

	mgmt.SetKey("")
	resp = engine.Query(t.Context(), "any", false)
	if resp.Status != statusUnavailable {
		t.Fatalf("no key: status = %s", resp.Status)
	}
}

func TestEngineAuthRequiredAndStalePreservation(t *testing.T) {
	var quotaFail atomic.Int32
	mgmt := newEngineMgmt(t, &quotaFail)
	engine := NewEngine(engineConfig(), mgmt, NewRouter(mgmt, nil, 30*time.Second))

	first := engine.Query(t.Context(), "cc/deepseek/deepseek-v4-flash", false)
	if first.Status != statusOK {
		t.Fatalf("first query = %+v", first)
	}

	quotaFail.Store(401)
	second := engine.Query(t.Context(), "cc/deepseek/deepseek-v4-flash", true)
	account := second.Accounts[0]
	if account.Status != accountAuthRequired {
		t.Fatalf("second status = %s want auth_required", account.Status)
	}
	if account.Quota == nil || !account.Stale {
		t.Fatalf("stale data must stay visible on auth failure: %+v", account)
	}
	if account.FetchedAt != first.Accounts[0].FetchedAt {
		t.Fatalf("fetchedAt must reflect original fetch time")
	}
}

func TestEngineSingleflightMergesConcurrentFetches(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	mgmt := newFakeMgmt(t, fakeMgmtOptions{}, func(r *http.Request) (int, any, bool) {
		switch r.URL.Path {
		case mgmtPathAuthFiles:
			return http.StatusOK, authFilesEnvelope{Files: []AuthFileEntry{
				{AuthIndex: "cc-1", Name: "cc.json", Provider: "commandcode-go", Status: "active", QuotaPlugin: "commandcode-go"},
			}}, true
		case mgmtPathModels:
			return http.StatusOK, authModelsEnvelope{Models: []AuthModel{{ID: "cc/m"}}}, true
		case "/v0/management/plugins/commandcode-go/quota":
			calls.Add(1)
			<-release // 挂起首个请求，让并发查询合并到它
			return http.StatusOK, CPAQuotaResponse{Groups: []CPAGroup{{DisplayName: "Usage limits"}}}, true
		}
		return 0, nil, false
	})
	engine := NewEngine(engineConfig(), mgmt, NewRouter(mgmt, nil, 30*time.Second))

	const concurrency = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]QueryResponse, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx] = engine.Query(t.Context(), "cc/m", true)
		}(i)
	}
	close(start)
	go func() {
		time.Sleep(300 * time.Millisecond)
		once.Do(func() { close(release) })
	}()
	wg.Wait()

	if calls.Load() < 1 || calls.Load() > int32(len(results)) {
		t.Fatalf("upstream calls = %d (want ≥1, ≤%d)", calls.Load(), len(results))
	}
	for i, resp := range results {
		if resp.Status != statusOK {
			t.Fatalf("result %d status = %s: %+v", i, resp.Status, resp)
		}
	}
}

func TestEngineRespectsMaxConcurrency(t *testing.T) {
	var inflight, maxInflight atomic.Int32
	block := make(chan struct{})
	mgmt := newFakeMgmt(t, fakeMgmtOptions{}, func(r *http.Request) (int, any, bool) {
		switch r.URL.Path {
		case mgmtPathAuthFiles:
			files := make([]AuthFileEntry, 0, 8)
			for i := 0; i < 8; i++ {
				files = append(files, AuthFileEntry{AuthIndex: string(rune('a' + i)), Name: string(rune('a'+i)) + ".json", Provider: "codex", Status: "active"})
			}
			return http.StatusOK, authFilesEnvelope{Files: files}, true
		case mgmtPathModels:
			return http.StatusOK, authModelsEnvelope{Models: []AuthModel{{ID: "codex/all"}}}, true
		case mgmtPathAPICall:
			current := inflight.Add(1)
			for {
				old := maxInflight.Load()
				if current <= old || maxInflight.CompareAndSwap(old, current) {
					break
				}
			}
			<-block
			inflight.Add(-1)
			return http.StatusOK, APICallResponse{StatusCode: 200, Body: `{"plan_type":"x","rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000}}}`}, true
		}
		return 0, nil, false
	})
	cfg := engineConfig()
	engine := NewEngine(cfg, mgmt, NewRouter(mgmt, nil, 30*time.Second))

	go func() {
		time.Sleep(400 * time.Millisecond)
		close(block)
	}()
	resp := engine.Query(t.Context(), "codex/all", true)
	if resp.Status != statusOK {
		t.Fatalf("status = %s: %+v", resp.Status, resp)
	}
	if got := maxInflight.Load(); got > int32(cfg.MaxConcurrency) {
		t.Fatalf("observed concurrency %d exceeds limit %d", got, cfg.MaxConcurrency)
	}
}

func TestEngineCapabilitiesDegradedWithoutKey(t *testing.T) {
	mgmt := newFakeMgmt(t, fakeMgmtOptions{}, func(r *http.Request) (int, any, bool) {
		return 0, nil, false
	})
	mgmt.SetKey("")
	engine := NewEngine(engineConfig(), mgmt, NewRouter(mgmt, nil, time.Second))
	caps := engine.Capabilities(t.Context(), time.Now())
	if caps.Status != "degraded" || caps.Reason == "" {
		t.Fatalf("capabilities = %+v", caps)
	}
}
