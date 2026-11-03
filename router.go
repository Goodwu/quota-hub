// 路由：完整模型 ID → 服务该模型的账号集合。
// 快照 = 管理 auth-files ⊕ 逐账号 auth-files/models（同源于宿主全局 registry）。
// 精确匹配优先；显式 prefix 映射按完整路径段最长前缀兜底；不猜测。
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Account is one credential in the route snapshot.
type Account struct {
	AuthIndex     string
	Name          string // auth file name (for auth-files/models queries)
	Label         string
	Provider      string
	Status        string
	Disabled      bool
	Unavailable   bool
	QuotaPluginID string
	Email         string
	Models        []string
	ChatGPTAcctID string // codex only (id_token claims)
	Revision      string // observable credential revision (cache isolation)
}

// AccountID is the stable opaque id exposed over the socket.
func (a Account) AccountID() string {
	sum := sha256.Sum256([]byte(a.Provider + "\x00" + a.AuthIndex))
	return fmt.Sprintf("%x", sum[:8])
}

// routeSnapshot is an immutable point-in-time view.
type routeSnapshot struct {
	at        time.Time
	accounts  []Account
	byModel   map[string][]int // exact model id -> account indexes
	providers []string         // sorted distinct providers (non-disabled)
}

func (s *routeSnapshot) accountsForModel(model string) []Account {
	idx := s.byModel[model]
	out := make([]Account, 0, len(idx))
	for _, i := range idx {
		out = append(out, s.accounts[i])
	}
	return out
}

func (s *routeSnapshot) accountsByProvider(provider string) []Account {
	var out []Account
	for i := range s.accounts {
		if equalFold(s.accounts[i].Provider, provider) {
			out = append(out, s.accounts[i])
		}
	}
	return out
}

// Router builds and caches route snapshots.
type Router struct {
	mgmt    *MgmtClient
	mapping []ModelMapping
	ttl     time.Duration

	mu       sync.Mutex
	snapshot *routeSnapshot
	refresh  *snapshotRefresh // coalesces concurrent refreshes
}

type snapshotRefresh struct {
	done chan struct{}
	snap *routeSnapshot
	err  error
}

func NewRouter(mgmt *MgmtClient, mapping []ModelMapping, ttl time.Duration) *Router {
	return &Router{mgmt: mgmt, mapping: mapping, ttl: ttl}
}

// Accounts returns the accounts serving model. forceRefresh bypasses a fresh
// enough snapshot once (used on unmapped lookups). Disabled accounts never
// appear here; exhausted/cooling ones do.
func (r *Router) Accounts(ctx context.Context, model string, forceRefresh bool) ([]Account, error) {
	snap, errSnap := r.snapshotFor(ctx, forceRefresh)
	if errSnap != nil {
		return nil, errSnap
	}
	if accounts := snap.accountsForModel(model); len(accounts) > 0 {
		return accounts, nil
	}
	if !forceRefresh {
		// Model unknown to the cached snapshot: refresh once before judging.
		if snapFresh, errFresh := r.snapshotFor(ctx, true); errFresh == nil {
			if accounts := snapFresh.accountsForModel(model); len(accounts) > 0 {
				return accounts, nil
			}
		}
	}
	if provider := matchExplicitMapping(r.mapping, model); provider != "" {
		return snap.accountsByProvider(provider), nil
	}
	return nil, nil
}

// Providers returns distinct non-disabled providers (capabilities reporting).
func (r *Router) Providers(ctx context.Context) []string {
	snap, errSnap := r.snapshotFor(ctx, false)
	if errSnap != nil {
		return nil
	}
	return snap.providers
}

func (r *Router) snapshotFor(ctx context.Context, force bool) (*routeSnapshot, error) {
	r.mu.Lock()
	current := r.snapshot
	if !force && current != nil && time.Since(current.at) < r.ttl {
		r.mu.Unlock()
		return current, nil
	}
	pending := r.refresh
	if pending == nil {
		pending = &snapshotRefresh{done: make(chan struct{})}
		r.refresh = pending
		r.mu.Unlock()
		go func() {
			snap, err := buildSnapshot(ctx, r.mgmt)
			pending.snap, pending.err = snap, err
			if snap != nil {
				r.mu.Lock()
				r.snapshot = snap
				r.mu.Unlock()
			}
			close(pending.done)
			r.mu.Lock()
			if r.refresh == pending {
				r.refresh = nil
			}
			r.mu.Unlock()
		}()
	} else {
		r.mu.Unlock()
	}
	select {
	case <-pending.done:
		if pending.err != nil {
			return nil, pending.err
		}
		return pending.snap, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("route snapshot: %w", ctx.Err())
	}
}

// buildSnapshot merges the auth-file list with per-file model lists and the
// host quota-provider map. A file whose models query fails is kept with an
// empty model list (exact matching will simply not hit it); a total
// auth-files failure fails the snapshot.
func buildSnapshot(ctx context.Context, mgmt *MgmtClient) (*routeSnapshot, error) {
	files, errList := mgmt.ListAuthFiles(ctx)
	if errList != nil {
		return nil, errList
	}
	// auth-files 不带 quota_plugin_id（v8 专属字段），改从宿主 quota providers
	// 注册表取 provider→pluginID；失败只降级为无插件适配，不失败整个快照。
	pluginByProvider := map[string]string{}
	if providers, errProviders := mgmt.QuotaProviders(ctx); errProviders == nil {
		for _, p := range providers {
			if p.PluginID == "" {
				continue
			}
			if p.Provider != "" {
				pluginByProvider[strings.ToLower(p.Provider)] = p.PluginID
			}
			for _, supported := range p.SupportedProviders {
				if supported != "" {
					pluginByProvider[strings.ToLower(supported)] = p.PluginID
				}
			}
		}
	}
	snap := &routeSnapshot{at: time.Now(), byModel: map[string][]int{}}
	providerSet := map[string]bool{}
	for _, f := range files {
		if f.Disabled {
			continue
		}
		provider := strings.TrimSpace(f.Provider)
		quotaPlugin := strings.TrimSpace(f.QuotaPlugin)
		if quotaPlugin == "" {
			quotaPlugin = pluginByProvider[strings.ToLower(provider)]
		}
		account := Account{
			AuthIndex:     strings.TrimSpace(f.AuthIndex),
			Name:          strings.TrimSpace(f.Name),
			Label:         strings.TrimSpace(f.Label),
			Provider:      provider,
			Status:        strings.TrimSpace(f.Status),
			Disabled:      f.Disabled,
			Unavailable:   f.Unavailable,
			QuotaPluginID: quotaPlugin,
			Email:         strings.TrimSpace(f.Email),
			ChatGPTAcctID: f.IDTokenClaims.chatGPTAccountID(),
			Revision:      revisionOf(f),
		}
		if account.AuthIndex == "" || account.Provider == "" {
			continue
		}
		if account.Name != "" {
			if models, errModels := mgmt.ListAuthModels(ctx, account.Name); errModels == nil {
				account.Models = models
			}
		}
		idx := len(snap.accounts)
		snap.accounts = append(snap.accounts, account)
		if !account.Unavailable {
			providerSet[strings.ToLower(account.Provider)] = true
		}
		for _, m := range account.Models {
			snap.byModel[m] = append(snap.byModel[m], idx)
		}
	}
	for p := range providerSet {
		snap.providers = append(snap.providers, p)
	}
	sort.Strings(snap.providers)
	return snap, nil
}

func (c *CodexIDTokenClaims) chatGPTAccountID() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.ChatGPTAccountID)
}

// revisionOf picks an observable credential revision for cache isolation.
// The account disappears from the list on deletion (cache dropped with the
// account); updated_at/modtime cover credential rotation.
func revisionOf(f AuthFileEntry) string {
	for _, candidate := range []string{f.UpdatedAt, f.ModTime} {
		if v := strings.TrimSpace(candidate); v != "" {
			return v
		}
	}
	return ""
}

// matchExplicitMapping returns the provider for the longest whole-segment
// prefix that matches model.
func matchExplicitMapping(mapping []ModelMapping, model string) string {
	bestLen := -1
	best := ""
	for _, m := range mapping {
		if m.Prefix == "" || m.Provider == "" {
			continue
		}
		if segmentsPrefix(model, m.Prefix) && len(m.Prefix) > bestLen {
			bestLen = len(m.Prefix)
			best = m.Provider
		}
	}
	return best
}

// segmentsPrefix reports whether model starts with prefix on whole path
// segments ("cc/ds" matches "cc/ds/x", not "cc/dsx").
func segmentsPrefix(model, prefix string) bool {
	if !strings.HasPrefix(model, prefix) {
		return false
	}
	rest := model[len(prefix):]
	return rest == "" || strings.HasPrefix(rest, "/")
}

func equalFold(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
