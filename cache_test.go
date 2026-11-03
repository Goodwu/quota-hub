package main

import (
	"testing"
	"time"
)

func sampleQuota(label string) AccountQuota {
	value := 0.5
	return AccountQuota{
		AccountID: "acc",
		Label:     label,
		Provider:  "codex",
		Status:    accountOK,
		Source:    "codex/usage",
		Quota: &CPAQuotaResponse{Groups: []CPAGroup{{
			DisplayName: "Usage limits",
			Buckets:     []CPABucket{{Window: "5h", RemainingFraction: &value}},
		}}},
	}
}

func TestCacheTTLAndStale(t *testing.T) {
	cache := newQuotaCache(time.Minute, 15*time.Minute, time.Minute)
	now := time.Now()
	cache.storeSuccess("k", sampleQuota("live"), now)

	fresh, entry, ok := cache.lookup("k", now.Add(30*time.Second))
	if !ok || !fresh || entry.quota.Label != "live" {
		t.Fatalf("fresh lookup failed: %+v", entry)
	}

	fresh, entry, ok = cache.lookup("k", now.Add(2*time.Minute))
	if !ok || fresh {
		t.Fatalf("after TTL must be stale-not-fresh: %+v", entry)
	}
	if entry.quota.Quota == nil {
		t.Fatal("stale entry must keep its data")
	}

	_, _, ok = cache.lookup("k", now.Add(16*time.Minute))
	if ok {
		t.Fatal("beyond staleAfter must be dropped")
	}
}

func TestCacheBackoffServesStale(t *testing.T) {
	cache := newQuotaCache(time.Minute, 15*time.Minute, time.Minute)
	now := time.Now()
	cache.storeSuccess("k", sampleQuota("live"), now)
	cache.storeFailure("k", "rate_limited", 0, now.Add(time.Minute))

	fresh, entry, ok := cache.lookup("k", now.Add(90*time.Second))
	if ok && fresh {
		t.Fatal("backoff window must not report fresh")
	}
	if !ok {
		t.Fatal("backoff must serve kept data as stale")
	}
	if entry.quota.Quota == nil {
		t.Fatal("kept data must survive a failure")
	}
}

func TestCacheFailureDropsTooOldData(t *testing.T) {
	cache := newQuotaCache(time.Minute, 15*time.Minute, time.Minute)
	now := time.Now()
	cache.storeSuccess("k", sampleQuota("live"), now)
	// 超过 staleAfter 后失败：旧数据不再展示，只剩退避计时。
	cache.storeFailure("k", "unavailable", time.Minute, now.Add(16*time.Minute))
	if _, has := cache.staleOnly("k", now.Add(16*time.Minute)); has {
		t.Fatal("too-old data must be dropped on failure")
	}
	// 且 lookup 不再返回数据（退避期内）。
	_, entry, ok := cache.lookup("k", now.Add(16*time.Minute+time.Second))
	if ok && entry.quota.Quota != nil {
		t.Fatal("no stale data should be visible after expiry")
	}
}

func TestCacheInvalidate(t *testing.T) {
	cache := newQuotaCache(time.Minute, 15*time.Minute, time.Minute)
	now := time.Now()
	cache.storeSuccess("k", sampleQuota("live"), now)
	cache.invalidate("k")
	if _, _, ok := cache.lookup("k", now); ok {
		t.Fatal("invalidate must drop the entry")
	}
}
