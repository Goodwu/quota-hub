// Socket 协议类型：schemaVersion 1 的稳定契约 + CPA 归一 quota 结构。
// 账号额度里的时间一律 RFC3339 绝对时间；remainingFraction 用指针区分
// null(未知)与 0(真实耗尽)。
package main

import "time"

const socketSchemaVersion = 1

// Top-level query status values.
const (
	statusOK          = "ok"
	statusPartial     = "partial"     // 部分账号失败，其余成功
	statusUnmapped    = "unmapped"    // 模型未匹配任何账号/映射
	statusUnavailable = "unavailable" // 服务级故障（密钥缺失、管理 API 不可达）
)

// Per-account status values.
const (
	accountOK           = "ok"
	accountUnsupported  = "unsupported"
	accountAuthRequired = "auth_required"
	accountRateLimited  = "rate_limited"
	accountTempUnavail  = "temporarily_unavailable"
)

// CapabilitiesResponse is GET /v1/capabilities.
type CapabilitiesResponse struct {
	SchemaVersion int      `json:"schemaVersion"`
	Service       string   `json:"service"`
	Status        string   `json:"status"`           // ok | degraded
	Reason        string   `json:"reason,omitempty"` // degraded 的安全原因
	Adapters      []string `json:"adapters"`
	ServerTime    string   `json:"serverTime"`
}

// QueryRequest is POST /v1/quota/query. Only the full model id and refresh
// flag are accepted — no URLs, headers, credentials or paths.
type QueryRequest struct {
	Model   string `json:"model"`
	Refresh bool   `json:"refresh"`
}

// QueryResponse is the unified answer for one model across accounts.
type QueryResponse struct {
	SchemaVersion int            `json:"schemaVersion"`
	Model         string         `json:"model"`
	Status        string         `json:"status"`
	Reason        string         `json:"reason,omitempty"`
	Accounts      []AccountQuota `json:"accounts"`
	QueriedAt     string         `json:"queriedAt"`
}

// newQueryResponse seeds accounts as an empty slice so clients never see null.
func newQueryResponse(model string) QueryResponse {
	return QueryResponse{
		SchemaVersion: socketSchemaVersion,
		Model:         model,
		Status:        statusUnavailable,
		Accounts:      []AccountQuota{},
	}
}

// AccountQuota is one credential's quota view.
type AccountQuota struct {
	AccountID string            `json:"accountId"` // stable opaque id (hash)
	Label     string            `json:"label"`
	Provider  string            `json:"provider"`
	Status    string            `json:"status"`
	Source    string            `json:"source"` // e.g. commandcode-live / codex-usage
	FetchedAt string            `json:"fetchedAt"`
	Stale     bool              `json:"stale"`
	Quota     *CPAQuotaResponse `json:"quota,omitempty"`
	Error     string            `json:"error,omitempty"` // safe text only
}

// CPAQuotaResponse mirrors the host-normalized quota payload
// (subscription / groups[].buckets[] / summary), snake_case per CPA v8.
type CPAQuotaResponse struct {
	Subscription       *CPASubscription `json:"subscription,omitempty"`
	Summary            []CPAMetric      `json:"summary,omitempty"`
	ServerTimeOffsetMs int64            `json:"serverTimeOffsetMs,omitempty"`
	Groups             []CPAGroup       `json:"groups,omitempty"`
}

type CPASubscription struct {
	Plan     string `json:"plan,omitempty"`
	TierName string `json:"tierName,omitempty"`
	TierID   string `json:"tierId,omitempty"`
}

type CPAMetric struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit,omitempty"`
	Format   string  `json:"format,omitempty"`
	Currency string  `json:"currency,omitempty"`
}

type CPAGroup struct {
	DisplayName string      `json:"displayName,omitempty"`
	Buckets     []CPABucket `json:"buckets,omitempty"`
}

type CPABucket struct {
	Window            string   `json:"window,omitempty"`
	RemainingFraction *float64 `json:"remainingFraction"` // nil = unknown
	ResetTime         string   `json:"resetTime,omitempty"`
	Description       string   `json:"description,omitempty"`
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
