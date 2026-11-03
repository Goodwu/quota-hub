package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func ptrFloat(v float64) *float64 { return &v }

func atRFC3339(s string) flexInstant { return flexInstant{text: s} }

func atUnix(seconds float64) flexInstant { return flexInstant{raw: seconds, isNum: true} }

func floatApprox(got, want float64) bool {
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-9
}

func TestBuildCodexQuotaWindowClassification(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	resetAt := "2026-11-03T17:00:00Z"
	payload := codexUsagePayload{
		PlanType: "pro",
		RateLimit: &codexRateLimitInfo{
			PrimaryWindow: &codexUsageWindow{
				UsedPercent:        ptrFloat(40),
				ResetAt:            atRFC3339(resetAt),
				LimitWindowSeconds: ptrFloat(18000),
			},
			SecondaryWindow: &codexUsageWindow{
				UsedPercent:        ptrFloat(70),
				ResetAt:            atRFC3339(resetAt),
				LimitWindowSeconds: ptrFloat(604800),
			},
		},
		CodeReviewRateLimit: &codexRateLimitInfo{
			PrimaryWindow: &codexUsageWindow{
				UsedPercent:        ptrFloat(0),
				ResetAt:            atRFC3339(resetAt),
				LimitWindowSeconds: ptrFloat(18000),
			},
		},
		AdditionalRateLimits: []codexAdditional{{
			LimitName: "Voice Mode",
			RateLimit: &codexRateLimitInfo{
				PrimaryWindow: &codexUsageWindow{
					UsedPercent:        ptrFloat(10),
					LimitWindowSeconds: ptrFloat(18000),
					ResetAfterSeconds:  ptrFloat(3600),
				},
			},
		}},
	}
	quota := buildCodexQuota(payload, now)

	if quota.Subscription == nil || quota.Subscription.Plan != "pro" {
		t.Fatalf("subscription plan = %+v, want pro", quota.Subscription)
	}
	if len(quota.Groups) != 3 {
		t.Fatalf("groups = %d, want 3 (usage, code review, additional)", len(quota.Groups))
	}
	usage := quota.Groups[0]
	if len(usage.Buckets) != 2 || usage.Buckets[0].Window != "5h" || usage.Buckets[1].Window != "weekly" {
		t.Fatalf("usage buckets = %+v", usage.Buckets)
	}
	if got := *usage.Buckets[0].RemainingFraction; !floatApprox(got, 0.6) {
		t.Fatalf("5h remaining = %v, want ~0.6", got)
	}
	if got := *usage.Buckets[1].RemainingFraction; !floatApprox(got, 0.3) {
		t.Fatalf("weekly remaining = %v, want ~0.3", got)
	}
	if usage.Buckets[0].ResetTime != resetAt {
		t.Fatalf("resetTime = %s, want %s", usage.Buckets[0].ResetTime, resetAt)
	}

	review := quota.Groups[1]
	if len(review.Buckets) != 1 || review.Buckets[0].Window != "code-review-5h" {
		t.Fatalf("review buckets = %+v", review.Buckets)
	}

	additional := quota.Groups[2]
	if additional.DisplayName != "Voice Mode" || len(additional.Buckets) != 1 {
		t.Fatalf("additional group = %+v", additional)
	}
	if additional.Buckets[0].Window != "voice-mode-5h" {
		t.Fatalf("additional window id = %s", additional.Buckets[0].Window)
	}
	// reset_after_seconds → 绝对时间 = now+1h
	if additional.Buckets[0].ResetTime != now.Add(time.Hour).UTC().Format(time.RFC3339) {
		t.Fatalf("additional reset = %s", additional.Buckets[0].ResetTime)
	}
}

func TestBuildCodexQuotaMonthlyAndLegacyFallback(t *testing.T) {
	now := time.Now()
	t.Run("monthly secondary", func(t *testing.T) {
		payload := codexUsagePayload{RateLimit: &codexRateLimitInfo{
			PrimaryWindow:   &codexUsageWindow{UsedPercent: ptrFloat(20), LimitWindowSeconds: ptrFloat(18000)},
			SecondaryWindow: &codexUsageWindow{UsedPercent: ptrFloat(50), LimitWindowSeconds: ptrFloat(30 * 24 * 3600)},
		}}
		quota := buildCodexQuota(payload, now)
		if quota.Groups[0].Buckets[1].Window != "monthly" {
			t.Fatalf("window = %s, want monthly", quota.Groups[0].Buckets[1].Window)
		}
	})
	t.Run("legacy no durations order fallback", func(t *testing.T) {
		payload := codexUsagePayload{RateLimit: &codexRateLimitInfo{
			PrimaryWindow:   &codexUsageWindow{UsedPercent: ptrFloat(30)},
			SecondaryWindow: &codexUsageWindow{UsedPercent: ptrFloat(60)},
		}}
		quota := buildCodexQuota(payload, now)
		if quota.Groups[0].Buckets[0].Window != "5h" || quota.Groups[0].Buckets[1].Window != "weekly" {
			t.Fatalf("legacy windows = %+v", quota.Groups[0].Buckets)
		}
	})
}

func TestBuildCodexQuotaUnknownVsExhausted(t *testing.T) {
	now := time.Now()
	t.Run("exhausted without percent", func(t *testing.T) {
		payload := codexUsagePayload{RateLimit: &codexRateLimitInfo{
			LimitReached:    true,
			PrimaryWindow:   &codexUsageWindow{ResetAfterSeconds: ptrFloat(600), LimitWindowSeconds: ptrFloat(18000)},
			SecondaryWindow: &codexUsageWindow{UsedPercent: ptrFloat(10), LimitWindowSeconds: ptrFloat(604800)},
		}}
		quota := buildCodexQuota(payload, now)
		fiveHour := quota.Groups[0].Buckets[0]
		if fiveHour.RemainingFraction == nil || *fiveHour.RemainingFraction != 0 {
			t.Fatalf("limit reached without percent must map to 0, got %+v", fiveHour)
		}
	})
	t.Run("unknown stays nil", func(t *testing.T) {
		payload := codexUsagePayload{RateLimit: &codexRateLimitInfo{
			PrimaryWindow:   &codexUsageWindow{LimitWindowSeconds: ptrFloat(18000)},
			SecondaryWindow: &codexUsageWindow{UsedPercent: ptrFloat(10), LimitWindowSeconds: ptrFloat(604800)},
		}}
		quota := buildCodexQuota(payload, now)
		if quota.Groups[0].Buckets[0].RemainingFraction != nil {
			t.Fatalf("missing percent without limit must stay unknown (nil)")
		}
	})
}

func TestParseCodexResetAtUnixSeconds(t *testing.T) {
	raw := `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":20,"reset_at":1793693132,"limit_window_seconds":18000}}}`
	payload, errParse := parseCodexUsage([]byte(raw))
	if errParse != nil {
		t.Fatalf("parse: %v", errParse)
	}
	quota := buildCodexQuota(payload, time.Now())
	if len(quota.Groups) == 0 || len(quota.Groups[0].Buckets) == 0 {
		t.Fatal("no buckets")
	}
	reset := quota.Groups[0].Buckets[0].ResetTime
	if reset == "" {
		t.Fatal("unix-second reset_at must map to a reset time")
	}
	want := time.Unix(1793693132, 0).UTC().Format(time.RFC3339)
	if reset != want {
		t.Fatalf("reset = %s want %s", reset, want)
	}
}

func TestParseCodexUsageCamelCase(t *testing.T) {
	raw := `{"planType":"plus","rateLimit":{"primaryWindow":{"usedPercent":15,"resetAt":"2026-11-03T16:00:00Z","limitWindowSeconds":18000}}}`
	payload, err := parseCodexUsage([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if payload.RateLimitCamel == nil || payload.RateLimitCamel.primary() == nil {
		t.Fatal("camel rate limit not decoded")
	}
	if got := *payload.rateLimit().primary().usedPercent(); got != 15 {
		t.Fatalf("usedPercent = %v", got)
	}
	if _, ok := payload.rateLimit().primary().resetAt(); !ok {
		t.Fatal("resetAt not decoded")
	}
}

func TestCodexAPIRequestShape(t *testing.T) {
	// api-call 请求体只含 auth_index/method/url/header，凭证由 $TOKEN$ 占位。
	var sent map[string]any
	mgmt := newFakeMgmt(t, fakeMgmtOptions{}, func(r *http.Request) (int, any, bool) {
		if r.URL.Path != mgmtPathAPICall {
			return 0, nil, false
		}
		_ = json.Unmarshal(readAll(t, r.Body), &sent)
		return http.StatusOK, APICallResponse{StatusCode: 200, Body: `{"plan_type":"x","rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000}}}`}, true
	})
	account := Account{AuthIndex: "idx-1", Provider: "codex", ChatGPTAcctID: "acct-77"}
	if _, err := fetchCodexQuota(t.Context(), mgmt, account); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	header := sent["header"].(map[string]any)
	if header["Authorization"] != "Bearer $TOKEN$" {
		t.Fatalf("authorization = %v, must stay $TOKEN$ placeholder", header["Authorization"])
	}
	if header["Chatgpt-Account-Id"] != "acct-77" {
		t.Fatalf("chatgpt account id header missing: %+v", header)
	}
	if sent["url"] != codexUsageURL || sent["method"] != "GET" {
		t.Fatalf("unexpected call target: %+v", sent)
	}
}
