// 管理 API 客户端：仅回环、固定路径白名单、拒重定向、不读环境代理。
// 错误只携带安全文案（HTTP 状态 + 稳定 code），不含上游 body、密钥或本地路径。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	mgmtPathAuthFiles = "/v0/management/auth-files"
	mgmtPathModels    = "/v0/management/auth-files/models"
	mgmtPathAPICall   = "/v0/management/api-call"
	mgmtPathPluginQuo = "/v0/management/plugins/%s/quota"
)

// MgmtError is a typed management-API failure. Message is safe to expose to
// the socket client; Detail is internal-only (logs), never serialized.
type MgmtError struct {
	Status  int    // upstream HTTP status (0 = transport failure)
	Code    string // stable machine code: auth_required | rate_limited | unavailable | ...
	Message string // safe user-facing text
	Detail  string // internal diagnostics (never leave the process)
}

func (e *MgmtError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("mgmt %d %s: %s (%s)", e.Status, e.Code, e.Message, e.Detail)
	}
	return fmt.Sprintf("mgmt %d %s: %s", e.Status, e.Code, e.Message)
}

func mgmtErr(status int, code, message, detail string) *MgmtError {
	return &MgmtError{Status: status, Code: code, Message: message, Detail: detail}
}

func isMgmtAuthError(err error) bool {
	var me *MgmtError
	if !errors.As(err, &me) {
		return false
	}
	return me.Status == http.StatusUnauthorized || me.Status == http.StatusForbidden
}

func isMgmtRateLimited(err error) bool {
	var me *MgmtError
	if !errors.As(err, &me) {
		return false
	}
	return me.Status == http.StatusTooManyRequests
}

type MgmtClient struct {
	base    *url.URL
	key     string
	client  *http.Client
	maxBody int64
}

// NewMgmtClient validates the base URL is plain-http loopback and builds a
// locked-down client. proxyFromEnvironment is never consulted.
func NewMgmtClient(apiBase, key string) (*MgmtClient, error) {
	parsed, errParse := url.Parse(apiBase)
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid api_base %q", apiBase)
	}
	if parsed.Scheme != "http" {
		return nil, fmt.Errorf("api_base must be http (loopback only), got %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if !isLoopbackHost(host) {
		return nil, fmt.Errorf("api_base host %q is not loopback", host)
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		MaxIdleConns:          8,
		IdleConnTimeout:       90 * time.Second,
		DisableCompression:    true,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return &MgmtClient{
		base: parsed,
		key:  key,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxBody: 8 << 20,
	}, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// SetKey replaces the management key (config reload / tests).
func (c *MgmtClient) SetKey(key string) { c.key = key }

func (c *MgmtClient) HasKey() bool { return c.key != "" }

// AuthFileEntry mirrors one item of GET /v0/management/auth-files.
type AuthFileEntry struct {
	AuthIndex     string              `json:"auth_index"`
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Type          string              `json:"type"`
	Provider      string              `json:"provider"`
	Label         string              `json:"label"`
	Status        string              `json:"status"`
	Disabled      bool                `json:"disabled"`
	Unavailable   bool                `json:"unavailable"`
	Email         string              `json:"email"`
	QuotaPlugin   string              `json:"quota_plugin_id"`
	UpdatedAt     string              `json:"updated_at"`
	ModTime       string              `json:"modtime"`
	IDTokenClaims *CodexIDTokenClaims `json:"id_token,omitempty"`
}

// CodexIDTokenClaims carries only what the Codex adapter needs.
type CodexIDTokenClaims struct {
	ChatGPTAccountID string `json:"https://api.openai.com/auth.chatgpt_account_id"`
}

type authFilesEnvelope struct {
	Files []AuthFileEntry `json:"files"`
}

// AuthModel mirrors one model of GET /v0/management/auth-files/models.
type AuthModel struct {
	ID string `json:"id"`
}

type authModelsEnvelope struct {
	Models []AuthModel `json:"models"`
}

// APICallResponse mirrors POST /v0/management/api-call.
type APICallResponse struct {
	StatusCode int                 `json:"status_code"`
	Header     map[string][]string `json:"header"`
	Body       string              `json:"body"`
}

func (c *MgmtClient) do(ctx context.Context, method, path string, body []byte, out any) error {
	if !isAllowedPath(method, path) {
		return fmt.Errorf("path not allowed: %s %s", method, path)
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, errNew := http.NewRequestWithContext(ctx, method, c.base.String()+path, reader)
	if errNew != nil {
		return fmt.Errorf("build request: %w", errNew)
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, errDo := c.client.Do(req)
	if errDo != nil {
		return mgmtErr(0, "unavailable", "管理服务暂时不可用", errDo.Error())
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, c.maxBody))
	if errRead != nil {
		return mgmtErr(0, "unavailable", "管理服务响应读取失败", errRead.Error())
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return mgmtErr(resp.StatusCode, "auth_required", "管理密钥未配置或已失效", "")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return mgmtErr(resp.StatusCode, "rate_limited", "管理接口限流，稍后重试", retryAfterHint(resp.Header.Get("Retry-After")))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return mgmtErr(resp.StatusCode, "unavailable", "管理接口返回 "+strconv.Itoa(resp.StatusCode), truncateForLog(string(raw)))
	}
	if out == nil {
		return nil
	}
	if errUnmarshal := json.Unmarshal(raw, out); errUnmarshal != nil {
		return mgmtErr(resp.StatusCode, "unavailable", "管理接口响应解析失败", errUnmarshal.Error())
	}
	return nil
}

func isAllowedPath(method, path string) bool {
	if method == http.MethodGet && (path == mgmtPathAuthFiles || strings.HasPrefix(path, mgmtPathModels+"?")) {
		return true
	}
	if method == http.MethodPost && path == mgmtPathAPICall {
		return true
	}
	if method == http.MethodPost && strings.HasPrefix(path, "/v0/management/plugins/") && strings.HasSuffix(path, "/quota") {
		plugin := strings.TrimSuffix(strings.TrimPrefix(path, "/v0/management/plugins/"), "/quota")
		return isSafePluginID(plugin)
	}
	return false
}

// isSafePluginID guards the interpolated plugin quota path ([a-z0-9-]+ only).
func isSafePluginID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

func retryAfterHint(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if seconds, errParse := strconv.Atoi(value); errParse == nil && seconds >= 0 {
		return fmt.Sprintf("retry-after=%ds", seconds)
	}
	return "retry-after=<unparsable>"
}

func truncateForLog(s string) string {
	const limit = 200
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}

// ListAuthFiles returns the credential list (providers, status, quota plugin).
func (c *MgmtClient) ListAuthFiles(ctx context.Context) ([]AuthFileEntry, error) {
	var env authFilesEnvelope
	if errCall := c.do(ctx, http.MethodGet, mgmtPathAuthFiles, nil, &env); errCall != nil {
		return nil, errCall
	}
	return env.Files, nil
}

// ListAuthModels returns the models registered for one auth file (by name).
func (c *MgmtClient) ListAuthModels(ctx context.Context, fileName string) ([]string, error) {
	query := mgmtPathModels + "?name=" + url.QueryEscape(fileName)
	var env authModelsEnvelope
	if errCall := c.do(ctx, http.MethodGet, query, nil, &env); errCall != nil {
		return nil, errCall
	}
	models := make([]string, 0, len(env.Models))
	for _, m := range env.Models {
		if id := strings.TrimSpace(m.ID); id != "" {
			models = append(models, id)
		}
	}
	return models, nil
}

// APICall performs a generic upstream call on behalf of a credential. The
// $TOKEN$ placeholder is substituted by the host; quota-hub never sees the
// credential token.
func (c *MgmtClient) APICall(ctx context.Context, authIndex, method, target string, headers map[string]string, data string) (APICallResponse, error) {
	payload := map[string]any{
		"auth_index": authIndex,
		"method":     method,
		"url":        target,
	}
	if len(headers) > 0 {
		payload["header"] = headers
	}
	if data != "" {
		payload["data"] = data
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return APICallResponse{}, fmt.Errorf("encode api-call: %w", errMarshal)
	}
	var out APICallResponse
	if errCall := c.do(ctx, http.MethodPost, mgmtPathAPICall, raw, &out); errCall != nil {
		return APICallResponse{}, errCall
	}
	return out, nil
}

// FetchPluginQuota calls POST /v0/management/plugins/<id>/quota with an
// auth_index; the response is the host-normalized quota payload.
func (c *MgmtClient) FetchPluginQuota(ctx context.Context, pluginID, authIndex string) (CPAQuotaResponse, error) {
	if !isSafePluginID(pluginID) {
		return CPAQuotaResponse{}, fmt.Errorf("unsafe plugin id")
	}
	body, errMarshal := json.Marshal(map[string]string{"auth_index": authIndex})
	if errMarshal != nil {
		return CPAQuotaResponse{}, fmt.Errorf("encode quota request: %w", errMarshal)
	}
	var out CPAQuotaResponse
	path := fmt.Sprintf(mgmtPathPluginQuo, pluginID)
	if errCall := c.do(ctx, http.MethodPost, path, body, &out); errCall != nil {
		return CPAQuotaResponse{}, errCall
	}
	return out, nil
}
