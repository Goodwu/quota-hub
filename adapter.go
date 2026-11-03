// 适配器选择与通用插件额度适配。
// 有宿主选定 quota plugin 的账号 → 调用该插件的管理 quota API（commandcode-go
// 等统一走这里）；无插件且 provider=codex → Codex usage；其余 unsupported。
package main

import (
	"context"
	"fmt"
	"strings"
)

// adapterKind picks the quota source for one account.
type adapterKind int

const (
	adapterNone adapterKind = iota
	adapterPluginQuota
	adapterCodexUsage
)

func adapterFor(a Account) (adapterKind, string) {
	if id := strings.TrimSpace(a.QuotaPluginID); id != "" {
		return adapterPluginQuota, id
	}
	if equalFold(a.Provider, "codex") {
		return adapterCodexUsage, ""
	}
	return adapterNone, ""
}

// fetchPluginQuota reads a plugin-served quota and preserves its source
// semantics: commandcode-go's local estimate keeps an "estimated" marker in
// the source label instead of pretending to be live.
func fetchPluginQuota(ctx context.Context, mgmt *MgmtClient, account Account, pluginID string) (AccountQuota, error) {
	resp, errFetch := mgmt.FetchPluginQuota(ctx, pluginID, account.AuthIndex)
	if errFetch != nil {
		return AccountQuota{}, errFetch
	}
	source := "plugin/" + pluginID
	if isLocalEstimate(resp) {
		source += "/estimated"
	}
	return AccountQuota{
		AccountID: account.AccountID(),
		Label:     accountLabel(account),
		Provider:  account.Provider,
		Status:    accountOK,
		Source:    source,
		Quota:     &resp,
	}, nil
}

// isLocalEstimate detects commandcode-go's local-fallback marker (a text
// "source=local" summary entry or a "(estimated)" group name).
func isLocalEstimate(resp CPAQuotaResponse) bool {
	for _, m := range resp.Summary {
		if m.Format == "text" && (m.Key == "source" || m.Key == "billing_error") {
			return true
		}
	}
	for _, g := range resp.Groups {
		if strings.Contains(strings.ToLower(g.DisplayName), "estimated") {
			return true
		}
	}
	return false
}

func accountLabel(a Account) string {
	if label := strings.TrimSpace(a.Label); label != "" {
		return label
	}
	if name := strings.TrimSpace(a.Name); name != "" {
		return name
	}
	if email := strings.TrimSpace(a.Email); email != "" {
		return email
	}
	return fmt.Sprintf("%s/%s", a.Provider, shorten(a.AuthIndex, 8))
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
