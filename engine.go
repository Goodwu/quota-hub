// 查询编排：路由 → 适配器选择 → 并发抓取（singleflight 合并 + 全局并发上限）
// → 汇总状态。错误映射为安全文案，绝不外泄密钥、上游响应体或本地路径。
package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Engine struct {
	cfg    Config
	mgmt   *MgmtClient
	router *Router
	cache  *quotaCache
	sem    chan struct{}

	mu       sync.Mutex
	inflight map[string]*inflightCall
}

type inflightCall struct {
	done   chan struct{}
	result AccountQuota
}

func NewEngine(cfg Config, mgmt *MgmtClient, router *Router) *Engine {
	return &Engine{
		cfg:      cfg,
		mgmt:     mgmt,
		router:   router,
		cache:    newQuotaCache(cfg.QuotaCacheTTL, cfg.StaleAfter, cfg.DefaultBackoff),
		sem:      make(chan struct{}, cfg.MaxConcurrency),
		inflight: map[string]*inflightCall{},
	}
}

// Capabilities reports service health for GET /v1/capabilities.
func (e *Engine) Capabilities(ctx context.Context, now time.Time) CapabilitiesResponse {
	resp := CapabilitiesResponse{
		SchemaVersion: socketSchemaVersion,
		Service:       "quota-hub",
		Status:        "ok",
		Adapters:      []string{"plugin-quota", "codex-usage"},
		ServerTime:    rfc3339(now),
	}
	if !e.mgmt.HasKey() {
		resp.Status = "degraded"
		resp.Reason = "管理密钥未配置（~/.quota-hub/management.key）"
		return resp
	}
	if _, errList := e.router.snapshotFor(ctx, false); errList != nil {
		resp.Status = "degraded"
		if isMgmtAuthError(errList) {
			resp.Reason = "管理密钥认证失败"
		} else {
			resp.Reason = "管理服务暂时不可用"
		}
	}
	return resp
}

// Query answers one model across all serving accounts.
func (e *Engine) Query(ctx context.Context, model string, refresh bool) QueryResponse {
	now := time.Now()
	resp := QueryResponse{
		SchemaVersion: socketSchemaVersion,
		Model:         model,
		QueriedAt:     rfc3339(now),
	}
	if !e.mgmt.HasKey() {
		resp.Status = statusUnavailable
		resp.Reason = "管理密钥未配置（~/.quota-hub/management.key）"
		return resp
	}

	accounts, errRoute := e.router.Accounts(ctx, model, refresh)
	if errRoute != nil {
		resp.Status = statusUnavailable
		switch {
		case isMgmtAuthError(errRoute):
			resp.Reason = "管理密钥认证失败"
		case isMgmtRateLimited(errRoute):
			resp.Reason = "管理接口限流，稍后重试"
		default:
			resp.Reason = "管理服务暂时不可用"
		}
		return resp
	}
	if len(accounts) == 0 {
		resp.Status = statusUnmapped
		resp.Reason = "该模型未映射到任何账号"
		return resp
	}

	results := make([]AccountQuota, len(accounts))
	var wg sync.WaitGroup
	for i := range accounts {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = e.fetchAccount(ctx, accounts[idx], refresh)
		}(i)
	}
	wg.Wait()

	allOK := true
	for _, result := range results {
		if result.Status != accountOK {
			allOK = false
		}
	}
	resp.Accounts = results
	if allOK {
		resp.Status = statusOK
	} else {
		resp.Status = statusPartial
	}
	return resp
}

// adapterKey identifies the quota source inside cache keys.
func adapterKey(kind adapterKind, pluginID string) string {
	switch kind {
	case adapterPluginQuota:
		return "plugin:" + pluginID
	case adapterCodexUsage:
		return "codex-usage"
	default:
		return "none"
	}
}

func (e *Engine) fetchAccount(ctx context.Context, account Account, refresh bool) AccountQuota {
	kind, pluginID := adapterFor(account)
	key := e.cache.key(account.AccountID(), adapterKey(kind, pluginID)+"|rev="+account.Revision)
	now := time.Now()

	if kind == adapterNone {
		return AccountQuota{
			AccountID: account.AccountID(),
			Label:     accountLabel(account),
			Provider:  account.Provider,
			Status:    accountUnsupported,
			Source:    "none",
			FetchedAt: rfc3339(now),
			Error:     "该渠道暂不支持额度查询",
		}
	}

	fresh, entry, ok := e.cache.lookup(key, now)
	if ok && fresh && !refresh {
		served := entry.quota
		served.FetchedAt = rfc3339(entry.at)
		served.Stale = false
		return served
	}
	if ok && !refresh && !fresh && now.Before(entry.backoffEnd) && entry.quota.Quota != nil {
		// Backoff window: serve the kept data, clearly labelled.
		served := entry.quota
		served.FetchedAt = rfc3339(entry.at)
		served.Stale = true
		served.Status = entry.backoffFor
		return served
	}

	// Coalesce identical concurrent fetches.
	if call := e.joinInflight(key); call != nil {
		select {
		case <-call.done:
			return call.result
		case <-ctx.Done():
			return e.failureResponse(account, key, accountTempUnavail, "查询超时", now)
		}
	}

	result := e.runFetch(ctx, account, key, kind, pluginID, now)
	e.finishInflight(key, result)
	return result
}

// runFetch performs the bounded upstream fetch for one account. The caller
// has registered the inflight entry and publishes the result afterwards.
func (e *Engine) runFetch(ctx context.Context, account Account, key string, kind adapterKind, pluginID string, now time.Time) AccountQuota {
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		return e.failureResponse(account, key, accountTempUnavail, "查询超时", now)
	}

	accountCtx, cancel := context.WithTimeout(ctx, e.cfg.AccountTimeout)
	defer cancel()

	var result AccountQuota
	var errFetch error
	switch kind {
	case adapterPluginQuota:
		result, errFetch = fetchPluginQuota(accountCtx, e.mgmt, account, pluginID)
	case adapterCodexUsage:
		result, errFetch = fetchCodexQuota(accountCtx, e.mgmt, account)
	}
	if errFetch == nil {
		result.FetchedAt = rfc3339(time.Now())
		e.cache.storeSuccess(key, result, time.Now())
		return result
	}
	return e.handleFetchFailure(account, key, errFetch, time.Now())
}

func (e *Engine) handleFetchFailure(account Account, key string, err error, now time.Time) AccountQuota {
	var me *MgmtError
	isMgmt := errors.As(err, &me)
	var status, message string
	switch {
	case isMgmt && (me.Status == 401 || me.Status == 403):
		status, message = accountAuthRequired, "凭证认证失败"
	case isMgmt && me.Status == 429:
		status, message = accountRateLimited, "限流中，稍后自动重试"
	case isMgmt && me.Code == "auth_required":
		status, message = accountAuthRequired, "凭证认证失败"
	case ctxErr(err):
		status, message = accountTempUnavail, "查询超时"
	default:
		status, message = accountTempUnavail, "额度服务暂时不可用"
	}
	wait := e.cfg.DefaultBackoff
	if isMgmt && me.Status == 429 {
		if hint := parseRetryAfter(me.Detail); hint > 0 {
			wait = hint
		}
	}
	e.cache.storeFailure(key, status, wait, now)

	// Keep last successful data (<=15min) visible while degraded.
	if entry, has := e.cache.staleOnly(key, now); has && entry.quota.Quota != nil {
		served := entry.quota
		served.FetchedAt = rfc3339(entry.at)
		served.Stale = true
		served.Status = status
		served.Error = message
		return served
	}
	out := AccountQuota{
		AccountID: account.AccountID(),
		Label:     accountLabel(account),
		Provider:  account.Provider,
		Status:    status,
		Source:    "none",
		FetchedAt: rfc3339(now),
		Error:     message,
	}
	return out
}

func (e *Engine) failureResponse(account Account, key, status, message string, now time.Time) AccountQuota {
	e.cache.storeFailure(key, status, e.cfg.DefaultBackoff, now)
	if entry, has := e.cache.staleOnly(key, now); has && entry.quota.Quota != nil {
		served := entry.quota
		served.FetchedAt = rfc3339(entry.at)
		served.Stale = true
		served.Status = status
		served.Error = message
		return served
	}
	return AccountQuota{
		AccountID: account.AccountID(),
		Label:     accountLabel(account),
		Provider:  account.Provider,
		Status:    status,
		Source:    "none",
		FetchedAt: rfc3339(now),
		Error:     message,
	}
}

func ctxErr(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

func parseRetryAfter(detail string) time.Duration {
	// detail 形如 "retry-after=30s"，由 mgmtclient 记录。
	var seconds int
	if n, errScan := fmt.Sscanf(detail, "retry-after=%ds", &seconds); n == 1 && errScan == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}

// ── singleflight ────────────────────────────────────────────────────────────

func (e *Engine) joinInflight(key string) *inflightCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	if call, ok := e.inflight[key]; ok {
		return call
	}
	call := &inflightCall{done: make(chan struct{})}
	e.inflight[key] = call
	return nil
}

func (e *Engine) finishInflight(key string, result AccountQuota) {
	e.mu.Lock()
	call := e.inflight[key]
	delete(e.inflight, key)
	e.mu.Unlock()
	if call != nil {
		call.result = result
		close(call.done)
	}
}
