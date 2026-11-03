// poc：阶段 1 前置验证（只读，quota-hub -poc）。逐项核实管理 API 链路，
// 输出证据供人工对照管理页面。凭据 token 始终留在宿主内（$TOKEN$）。
//
// 检查项：
//  1. auth-files 列表（provider/status/quota_plugin_id）
//  2. 逐账号 auth-files/models 汇总（模型→账号映射覆盖度）
//  3. CommandCode 插件额度回环（POST /plugins/commandcode-go/quota）
//  4. Codex api-call wham/usage + 窗口解析
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// runPoC executes the stage-1 read-only verification.
func runPoC() {
	cfg, errLoad := LoadConfig("")
	if errLoad != nil {
		pocFatal("配置加载失败: %v", errLoad)
	}
	keyFile := cfg.ManagementKeyFile
	if env := strings.TrimSpace(os.Getenv("QUOTA_HUB_MANAGEMENT_KEY_FILE")); env != "" {
		keyFile = env
	}
	key, errKey := LoadManagementKey(keyFile)
	if errKey != nil {
		pocFatal("管理密钥读取失败: %v\n请先创建 %s（内容为 CPA 管理密钥明文，单行，权限 0600）", errKey, keyFile)
	}
	mgmt, errNew := NewMgmtClient(cfg.APIBase, key)
	if errNew != nil {
		pocFatal("管理客户端初始化失败: %v", errNew)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	step := 0
	next := func(name string) { step++; fmt.Printf("\n=== [%d] %s ===\n", step, name) }

	// 1. auth-files
	next("auth-files 列表")
	files, errList := mgmt.ListAuthFiles(ctx)
	if errList != nil {
		pocFatal("auth-files 失败: %v", errList)
	}
	modelsByAccount := map[string][]string{}
	var ccAccount, codexAccount *string
	for i := range files {
		f := files[i]
		fmt.Printf("  %-12s provider=%-18s status=%-8s disabled=%-5v quota_plugin=%s",
			maskAuthIndex(f.AuthIndex), f.Provider, f.Status, f.Disabled, f.QuotaPlugin)
		if f.Disabled {
			fmt.Println("  (跳过模型查询：disabled)")
			continue
		}
		models, errModels := mgmt.ListAuthModels(ctx, f.Name)
		if errModels != nil {
			fmt.Printf("  models 查询失败: %v\n", errModels)
			continue
		}
		modelsByAccount[f.AuthIndex] = models
		fmt.Printf("  models=%d\n", len(models))
		if f.QuotaPlugin == "commandcode-go" && ccAccount == nil {
			index := strings.TrimSpace(f.AuthIndex)
			ccAccount = &index
		}
		if strings.EqualFold(f.Provider, "codex") && codexAccount == nil && f.QuotaPlugin == "" {
			index := strings.TrimSpace(f.AuthIndex)
			codexAccount = &index
		}
	}

	// 2. 模型覆盖汇总
	next("模型 → 账号映射（auth-files/models 汇总）")
	accountsByModel := map[string][]string{}
	for authIndex, models := range modelsByAccount {
		for _, m := range models {
			accountsByModel[m] = append(accountsByModel[m], maskAuthIndex(authIndex))
		}
	}
	sampleModels := make([]string, 0, len(accountsByModel))
	for m := range accountsByModel {
		sampleModels = append(sampleModels, m)
	}
	sort.Strings(sampleModels)
	fmt.Printf("  模型总数: %d\n", len(sampleModels))
	const showMax = 25
	for i, m := range sampleModels {
		if i >= showMax {
			fmt.Printf("  …（其余 %d 个略）\n", len(sampleModels)-showMax)
			break
		}
		fmt.Printf("  %-50s ← %s\n", m, strings.Join(accountsByModel[m], ", "))
	}

	// 3. CommandCode 插件额度回环
	next("CommandCode 插件额度回环")
	if ccAccount == nil {
		fmt.Println("  没有 quota_plugin=commandcode-go 的启用账号，跳过")
	} else if quota, errQuota := mgmt.FetchPluginQuota(ctx, "commandcode-go", *ccAccount); errQuota != nil {
		fmt.Printf("  失败: %v\n", errQuota)
	} else {
		printPocQuota(quota)
	}

	// 4. Codex usage
	next("Codex api-call wham/usage")
	if codexAccount == nil {
		fmt.Println("  没有 provider=codex 的启用账号，跳过")
	} else {
		account := Account{AuthIndex: *codexAccount, Provider: "codex"}
		if result, errFetch := fetchCodexQuota(ctx, mgmt, account); errFetch != nil {
			fmt.Printf("  失败: %v\n", errFetch)
		} else {
			printPocQuota(*result.Quota)
		}
	}

	fmt.Println("\nPoC 完成。请与管理页面同刻额度对照。")
}

func printPocQuota(quota CPAQuotaResponse) {
	if quota.Subscription != nil {
		fmt.Printf("  plan=%s\n", quota.Subscription.Plan)
	}
	for _, m := range quota.Summary {
		fmt.Printf("  summary %-22s = %v %s (%s)\n", m.Key, m.Value, m.Unit, m.Format)
	}
	for _, g := range quota.Groups {
		fmt.Printf("  group %s\n", g.DisplayName)
		for _, b := range g.Buckets {
			fraction := "unknown"
			if b.RemainingFraction != nil {
				fraction = fmt.Sprintf("%.1f%%", *b.RemainingFraction*100)
			}
			fmt.Printf("    %-28s remaining=%-9s reset=%s\n", b.Window, fraction, b.ResetTime)
		}
	}
}

func maskAuthIndex(index string) string {
	if len(index) <= 6 {
		return "***"
	}
	return index[:6] + "…"
}

func pocFatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "poc: "+format+"\n", args...)
	os.Exit(1)
}
