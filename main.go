// quota-hub：独立额度守护进程。本机 Unix Socket → CPA 管理 API → 各渠道额度。
// 详见 cpa-plugin-commandcode/archives/conversations/zcode-quota-hub-design.md。
//
// 用法：quota-hub [-config <path>] [-version]
// 配置：~/.quota-hub/config.json（可缺省，全默认值）+ QUOTA_HUB_* 环境变量。
// 退出：SIGTERM/SIGINT 优雅停止（停新查询→等在途→关 socket）。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

var buildVersion = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	configPath := flag.String("config", "", "配置文件路径（默认 ~/.quota-hub/config.json）")
	showVersion := flag.Bool("version", false, "打印版本并退出")
	runPoCMode := flag.Bool("poc", false, "阶段1只读验证（auth-files/模型映射/额度回环/Codex usage）")
	flag.Parse()
	if *showVersion {
		fmt.Println("quota-hub", buildVersion)
		return
	}
	if *runPoCMode {
		runPoC()
		return
	}

	cfg, errLoad := LoadConfig(*configPath)
	if errLoad != nil {
		log.Fatalf("配置加载失败: %v", errLoad)
	}

	mgmt, errMgmt := NewMgmtClient(cfg.APIBase, "")
	if errMgmt != nil {
		log.Fatalf("管理客户端初始化失败: %v", errMgmt)
	}
	if key, errKey := LoadManagementKey(cfg.ManagementKeyFile); errKey == nil {
		mgmt.SetKey(key)
		log.Printf("管理密钥已加载（%s）", filepath.Base(cfg.ManagementKeyFile))
	} else {
		// 密钥缺失只降级额度服务，进程继续（capabilities 会报 degraded）。
		log.Printf("警告: %v（额度查询将不可用，直到密钥文件就绪）", errKey)
	}

	router := NewRouter(mgmt, cfg.ModelMappings, cfg.RouteCacheTTL)
	engine := NewEngine(cfg, mgmt, router)

	server, errServer := NewServer(cfg.SocketPath, engine)
	if errServer != nil {
		log.Fatalf("Socket 服务初始化失败: %v", errServer)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	log.Printf("quota-hub %s 启动：socket=%s api=%s 并发=%d", buildVersion, cfg.SocketPath, cfg.APIBase, cfg.MaxConcurrency)
	if errServe := server.Serve(ctx); errServe != nil {
		log.Printf("服务退出: %v", errServe)
	}
	server.Close()
	_ = os.Remove(cfg.SocketPath)
	log.Printf("quota-hub 已退出")
}
