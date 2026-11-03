// Codex 额度适配：经管理 api-call 读取 wham/usage 并把窗口移植为 CPA 归一
// 结构。主窗口、周/月窗口、Code Review 与命名额度各自独立，不混算。
// 解析规则移植自 Cli-Proxy-API-Management-Center codex/data.ts。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"
	codexUA       = "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal"

	codexFiveHourSeconds = 18000.0
	codexWeekSeconds     = 604800.0
	codexMinMonthSeconds = 28 * 24 * 60 * 60
	codexMaxMonthSeconds = 31 * 24 * 60 * 60
)

// fetchCodexQuota reads wham/usage through the management api-call. The
// credential token stays inside the host ($TOKEN$ substitution).
func fetchCodexQuota(ctx context.Context, mgmt *MgmtClient, account Account) (AccountQuota, error) {
	headers := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    codexUA,
	}
	if account.ChatGPTAcctID != "" {
		headers["Chatgpt-Account-Id"] = account.ChatGPTAcctID
	}
	call, errCall := mgmt.APICall(ctx, account.AuthIndex, http.MethodGet, codexUsageURL, headers, "")
	if errCall != nil {
		return AccountQuota{}, errCall
	}
	if call.StatusCode < 200 || call.StatusCode >= 300 {
		return AccountQuota{}, mgmtErr(call.StatusCode, upstreamErrorCode(call.StatusCode), codexUpstreamMessage(call.StatusCode), fmt.Sprintf("wham/usage body: %s", truncateForLog(call.Body)))
	}
	payload, errParse := parseCodexUsage([]byte(call.Body))
	if errParse != nil {
		return AccountQuota{}, mgmtErr(0, "unavailable", "Codex 用量响应解析失败", errParse.Error())
	}
	quota := buildCodexQuota(payload, time.Now())
	return AccountQuota{
		AccountID: account.AccountID(),
		Label:     accountLabel(account),
		Provider:  account.Provider,
		Status:    accountOK,
		Source:    "codex/usage",
		Quota:     &quota,
	}, nil
}

func upstreamErrorCode(status int) string {
	switch {
	case status == 401 || status == 403:
		return "auth_required"
	case status == 429:
		return "rate_limited"
	default:
		return "unavailable"
	}
}

func codexUpstreamMessage(status int) string {
	switch {
	case status == 401 || status == 403:
		return "Codex 凭证认证失败"
	case status == 429:
		return "Codex 用量接口限流"
	default:
		return "Codex 用量接口返回异常"
	}
}

// ── payload types (snake_case + camelCase tolerant) ─────────────────────────

type codexUsagePayload struct {
	PlanType string `json:"plan_type"`

	RateLimit            *codexRateLimitInfo `json:"rate_limit"`
	RateLimitCamel       *codexRateLimitInfo `json:"rateLimit"`
	CodeReviewRateLimit  *codexRateLimitInfo `json:"code_review_rate_limit"`
	CodeReviewCamel      *codexRateLimitInfo `json:"codeReviewRateLimit"`
	AdditionalRateLimits []codexAdditional   `json:"additional_rate_limits"`
	AdditionalCamel      []codexAdditional   `json:"additionalRateLimits"`
}

type codexAdditional struct {
	LimitName      string              `json:"limit_name"`
	LimitNameCamel string              `json:"limitName"`
	MeteredFeature string              `json:"metered_feature"`
	MeteredCamel   string              `json:"meteredFeature"`
	RateLimit      *codexRateLimitInfo `json:"rate_limit"`
	RateLimitCamel *codexRateLimitInfo `json:"rateLimit"`
}

func (a codexAdditional) name() string {
	if a.LimitName != "" {
		return a.LimitName
	}
	if a.LimitNameCamel != "" {
		return a.LimitNameCamel
	}
	if a.MeteredFeature != "" {
		return a.MeteredFeature
	}
	return a.MeteredCamel
}

func (a codexAdditional) rateLimit() *codexRateLimitInfo {
	if a.RateLimit != nil {
		return a.RateLimit
	}
	return a.RateLimitCamel
}

type codexRateLimitInfo struct {
	PrimaryWindow     *codexUsageWindow `json:"primary_window"`
	PrimaryCamel      *codexUsageWindow `json:"primaryWindow"`
	SecondaryWindow   *codexUsageWindow `json:"secondary_window"`
	SecondaryCamel    *codexUsageWindow `json:"secondaryWindow"`
	LimitReached      bool              `json:"limit_reached"`
	LimitReachedCamel bool              `json:"limitReached"`
	Allowed           *bool             `json:"allowed"`
}

func (r *codexRateLimitInfo) primary() *codexUsageWindow {
	if r.PrimaryWindow != nil {
		return r.PrimaryWindow
	}
	return r.PrimaryCamel
}

func (r *codexRateLimitInfo) secondary() *codexUsageWindow {
	if r.SecondaryWindow != nil {
		return r.SecondaryWindow
	}
	return r.SecondaryCamel
}

func (r *codexRateLimitInfo) limitReached() bool { return r.LimitReached || r.LimitReachedCamel }

type codexUsageWindow struct {
	UsedPercent        *float64    `json:"used_percent"`
	UsedPercentCamel   *float64    `json:"usedPercent"`
	ResetAt            flexInstant `json:"reset_at"`
	ResetAtCamel       flexInstant `json:"resetAt"`
	ResetAfterSeconds  *float64    `json:"reset_after_seconds"`
	ResetAfterCamel    *float64    `json:"resetAfterSeconds"`
	LimitWindowSeconds *float64    `json:"limit_window_seconds"`
	LimitWindowCamel   *float64    `json:"limitWindowSeconds"`
}

// flexInstant accepts the two reset_at shapes wham/usage actually returns:
// RFC3339 strings and unix-second numbers.
type flexInstant struct {
	raw   float64
	text  string
	isNum bool
}

func (f *flexInstant) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" || trimmed == "" {
		return nil
	}
	if trimmed[0] == '"' {
		_ = json.Unmarshal(raw, &f.text)
		return nil
	}
	var num float64
	if errNum := json.Unmarshal(raw, &num); errNum != nil {
		// 非数值非字符串：保持零值，解析侧按缺失处理。
		return nil
	}
	f.raw, f.isNum = num, true
	return nil
}

// instant resolves the absolute time; zero means unknown.
func (f flexInstant) instant() time.Time {
	if f.isNum {
		seconds := f.raw
		if seconds > 1e12 { // 毫秒
			return time.UnixMilli(int64(seconds))
		}
		if seconds > 0 {
			return time.Unix(int64(seconds), 0)
		}
		return time.Time{}
	}
	if parsed, errParse := time.Parse(time.RFC3339, strings.TrimSpace(f.text)); errParse == nil {
		return parsed
	}
	return time.Time{}
}

func (w *codexUsageWindow) usedPercent() *float64 {
	if w.UsedPercent != nil {
		return w.UsedPercent
	}
	return w.UsedPercentCamel
}

func (w *codexUsageWindow) resetAt() (time.Time, bool) {
	for _, candidate := range []flexInstant{w.ResetAt, w.ResetAtCamel} {
		if t := candidate.instant(); !t.IsZero() {
			return t, true
		}
	}
	return time.Time{}, false
}

func (w *codexUsageWindow) resetAfter() *float64 {
	if w.ResetAfterSeconds != nil {
		return w.ResetAfterSeconds
	}
	return w.ResetAfterCamel
}

func (w *codexUsageWindow) windowSeconds() *float64 {
	if w.LimitWindowSeconds != nil {
		return w.LimitWindowSeconds
	}
	return w.LimitWindowCamel
}

func parseCodexUsage(raw []byte) (codexUsagePayload, error) {
	var payload codexUsagePayload
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		return codexUsagePayload{}, errUnmarshal
	}
	return payload, nil
}

func (p codexUsagePayload) rateLimit() *codexRateLimitInfo {
	if p.RateLimit != nil {
		return p.RateLimit
	}
	return p.RateLimitCamel
}

func (p codexUsagePayload) codeReview() *codexRateLimitInfo {
	if p.CodeReviewRateLimit != nil {
		return p.CodeReviewRateLimit
	}
	return p.CodeReviewCamel
}

func (p codexUsagePayload) additionals() []codexAdditional {
	if len(p.AdditionalRateLimits) > 0 {
		return p.AdditionalRateLimits
	}
	return p.AdditionalCamel
}

// ── window → bucket mapping ─────────────────────────────────────────────────

// buildCodexQuota maps usage windows onto the CPA normalized shape. Window
// classification mirrors the web console: 18000s = primary (5h), 604800s or a
// 28–31d span = secondary (weekly/monthly); payloads without durations fall
// back to primary/secondary ordering.
func buildCodexQuota(payload codexUsagePayload, now time.Time) CPAQuotaResponse {
	var out CPAQuotaResponse
	if plan := strings.TrimSpace(payload.PlanType); plan != "" {
		out.Subscription = &CPASubscription{Plan: plan, TierName: plan, TierID: plan}
	}

	if rate := payload.rateLimit(); rate != nil {
		fiveHour, secondary := classifyCodexWindows(rate.primary(), rate.secondary(), true)
		group := CPAGroup{DisplayName: "Usage limits"}
		appendCodexBucket(&group, "5h", fiveHour, rate.limitReached(), rate.Allowed, now)
		appendCodexBucket(&group, secondaryWindowID(secondary), secondary, rate.limitReached(), rate.Allowed, now)
		if len(group.Buckets) > 0 {
			out.Groups = append(out.Groups, group)
		}
	}
	if review := payload.codeReview(); review != nil {
		fiveHour, secondary := classifyCodexWindows(review.primary(), review.secondary(), true)
		group := CPAGroup{DisplayName: "Code Review"}
		appendCodexBucket(&group, "code-review-5h", fiveHour, review.limitReached(), review.Allowed, now)
		appendCodexBucket(&group, "code-review-"+secondaryWindowID(secondary), secondary, review.limitReached(), review.Allowed, now)
		if len(group.Buckets) > 0 {
			out.Groups = append(out.Groups, group)
		}
	}
	for i, additional := range payload.additionals() {
		rate := additional.rateLimit()
		if rate == nil {
			continue
		}
		name := strings.TrimSpace(additional.name())
		if name == "" {
			name = fmt.Sprintf("additional-%d", i+1)
		}
		fiveHour, secondary := classifyCodexWindows(rate.primary(), rate.secondary(), true)
		group := CPAGroup{DisplayName: name}
		suffix := sanitizeWindowID(name)
		appendCodexBucket(&group, suffix+"-5h", fiveHour, rate.limitReached(), rate.Allowed, now)
		appendCodexBucket(&group, suffix+"-"+secondaryWindowID(secondary), secondary, rate.limitReached(), rate.Allowed, now)
		if len(group.Buckets) > 0 {
			out.Groups = append(out.Groups, group)
		}
	}
	return out
}

// classifyCodexWindows splits the primary/secondary pair into a 5h window and
// a weekly/monthly window using limit_window_seconds when present.
func classifyCodexWindows(primary, secondary *codexUsageWindow, orderFallback bool) (*codexUsageWindow, *codexUsageWindow) {
	var fiveHour, weekly *codexUsageWindow
	for _, window := range []*codexUsageWindow{primary, secondary} {
		if window == nil {
			continue
		}
		seconds := deref(window.windowSeconds())
		switch {
		case seconds == codexFiveHourSeconds && fiveHour == nil:
			fiveHour = window
		case (seconds == codexWeekSeconds || isMonthlySeconds(seconds)) && weekly == nil:
			weekly = window
		}
	}
	if orderFallback {
		if fiveHour == nil && primary != nil && primary != weekly {
			fiveHour = primary
		}
		if weekly == nil && secondary != nil && secondary != fiveHour {
			weekly = secondary
		}
	}
	return fiveHour, weekly
}

func isMonthlySeconds(seconds float64) bool {
	return seconds >= codexMinMonthSeconds && seconds <= codexMaxMonthSeconds
}

func secondaryWindowID(window *codexUsageWindow) string {
	if window == nil {
		return ""
	}
	seconds := deref(window.windowSeconds())
	if isMonthlySeconds(seconds) {
		return "monthly"
	}
	return "weekly"
}

// appendCodexBucket converts one usage window. used_percent is authoritative;
// without it a reached/allowed=false limit with a resolvable reset reads as a
// real 0, otherwise the fraction stays unknown (nil).
func appendCodexBucket(group *CPAGroup, window string, w *codexUsageWindow, limitReached bool, allowed *bool, now time.Time) {
	if w == nil || window == "" {
		return
	}
	used := w.usedPercent()
	var fraction *float64
	switch {
	case used != nil:
		value := 1 - *used/100
		if value < 0 {
			value = 0
		}
		if value > 1 {
			value = 1
		}
		fraction = &value
	case (limitReached || (allowed != nil && !*allowed)) && hasCodexReset(w, now):
		zero := 0.0
		fraction = &zero
	}
	bucket := CPABucket{
		Window:            window,
		RemainingFraction: fraction,
	}
	if reset, ok := w.resetAt(); ok {
		bucket.ResetTime = rfc3339(reset)
	} else if reset := codexResetTime(w, now); reset != "" {
		bucket.ResetTime = reset
	}
	if seconds := w.windowSeconds(); seconds != nil {
		bucket.Description = fmt.Sprintf("window %.0fs", *seconds)
	}
	group.Buckets = append(group.Buckets, bucket)
}

// hasCodexReset reports whether any reset signal (reset_at or
// reset_after_seconds) can be resolved — mirrors the web console's
// "resetLabel !== '-'" gate for exhausted windows.
func hasCodexReset(w *codexUsageWindow, now time.Time) bool {
	if _, ok := w.resetAt(); ok {
		return true
	}
	return codexResetTime(w, now) != ""
}

// codexResetTime resolves the fallback reset instant from
// reset_after_seconds relative to now (reset_at handled by resetAt()).
func codexResetTime(w *codexUsageWindow, now time.Time) string {
	if after := w.resetAfter(); after != nil && *after >= 0 {
		return rfc3339(now.Add(time.Duration(*after * float64(time.Second))))
	}
	return ""
}

func sanitizeWindowID(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
