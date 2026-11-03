// 额度缓存与退避：按账号+适配器+凭证修订隔离；60s TTL；最后成功数据保留
// 15 分钟并标 stale；429 遵循 Retry-After（缺省 60s）；401/403 也进入退避
// 以避免反复用错误密钥打扰宿主。
package main

import (
	"sync"
	"time"
)

type cacheEntry struct {
	quota      AccountQuota // last successful fetch
	at         time.Time
	backoffEnd time.Time // until when the upstream must not be retried
	backoffFor string    // rate_limited | auth_required | unavailable
}

type quotaCache struct {
	mu          sync.Mutex
	items       map[string]cacheEntry
	ttl         time.Duration
	staleAfter  time.Duration
	defaultWait time.Duration
}

func newQuotaCache(ttl, staleAfter, defaultBackoff time.Duration) *quotaCache {
	return &quotaCache{
		items:       map[string]cacheEntry{},
		ttl:         ttl,
		staleAfter:  staleAfter,
		defaultWait: defaultBackoff,
	}
}

func (c *quotaCache) key(accountID, adapterKey string) string {
	return accountID + "|" + adapterKey
}

// lookup returns (fresh, entry, ok): fresh = within TTL and not in backoff.
func (c *quotaCache) lookup(key string, now time.Time) (fresh bool, entry cacheEntry, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok = c.items[key]
	if !ok {
		return false, cacheEntry{}, false
	}
	if now.Before(entry.backoffEnd) {
		// Backoff still active: serve any kept data as stale, never upstream.
		if now.Sub(entry.at) <= c.staleAfter {
			return false, entry, true
		}
		return false, cacheEntry{}, false
	}
	if now.Sub(entry.at) < c.ttl {
		return true, entry, true
	}
	if now.Sub(entry.at) <= c.staleAfter {
		return false, entry, true
	}
	delete(c.items, key)
	return false, cacheEntry{}, false
}

// staleOnly returns the kept entry regardless of TTL (fallback on failure).
func (c *quotaCache) staleOnly(key string, now time.Time) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok || now.Sub(entry.at) > c.staleAfter {
		return cacheEntry{}, false
	}
	return entry, true
}

// storeSuccess records a successful fetch and clears backoff.
func (c *quotaCache) storeSuccess(key string, quota AccountQuota, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = cacheEntry{quota: quota, at: now}
}

// storeFailure keeps the previous data (if any) and applies backoff. The
// reason decides the account status while waiting out the backoff.
func (c *quotaCache) storeFailure(key, reason string, wait time.Duration, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok {
		entry = cacheEntry{at: now}
	} else if now.Sub(entry.at) > c.staleAfter {
		// Data too old to show; drop it, keep only timing.
		entry.quota = AccountQuota{}
	}
	if wait <= 0 {
		wait = c.defaultWait
	}
	entry.backoffEnd = now.Add(wait)
	entry.backoffFor = reason
	c.items[key] = entry
}

// invalidate drops one key (account deleted / revision changed).
func (c *quotaCache) invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}
