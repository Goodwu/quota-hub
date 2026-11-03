# quota-hub

本机额度查询守护进程:ZCode 原生用量弹层 → Unix Socket → quota-hub → CPA 管理 API → 各渠道(CommandCode / Codex)真实额度。设计文档见 `cpa-plugin-commandcode/archives/conversations/zcode-quota-hub-design.md`。

**独立守护进程,不是 CPA 插件**:与 CPA 的唯一交互是本机管理 API(`http://127.0.0.1:8317`)。进程级解耦——任一方重启只导致查询暂时 `unavailable`,自愈;CPA 升级零影响。

## 协议(schemaVersion 1)

- `GET /v1/capabilities` → `{schemaVersion, service, status, reason?, adapters[], serverTime}`
- `POST /v1/quota/query` `{"model":"cc/deepseek/deepseek-v4-flash","refresh":false}`
  → `{schemaVersion, model, status, accounts[], queriedAt}`
  - 顶层 `status`: `ok` / `partial` / `unmapped` / `unavailable`
  - `accounts[]`: `{accountId(稳定哈希), label, provider, status, source, fetchedAt, stale, quota?, error?}`
  - `quota` 沿用 CPA 归一结构(`subscription` / `groups[].buckets[]` / `summary`);`remainingFraction:null`=未知、`0`=真实耗尽;时间 RFC3339
  - 请求只接受 `model`+`refresh`,其余字段(URL/头/凭证)一律 400

## 安装

```bash
# 1. 管理密钥(明文,单行,仅本人可读;与 CPA 管理页登录密钥相同)
mkdir -p ~/.quota-hub && chmod 700 ~/.quota-hub
printf '%s' '<你的管理密钥>' > ~/.quota-hub/management.key && chmod 600 ~/.quota-hub/management.key

# 2. 安装并启动(LaunchAgent com.quota-hub,RunAtLoad+KeepAlive)
make install

# 3. 验证
curl --unix-socket ~/.quota-hub/quota-hub.sock http://localhost/v1/capabilities
curl --unix-socket ~/.quota-hub/quota-hub.sock -X POST http://localhost/v1/quota/query \
  -H 'Content-Type: application/json' -d '{"model":"cc/deepseek/deepseek-v4-flash"}'
```

## 配置(`~/.quota-hub/config.json`,可缺省)

```json
{
  "api_base": "http://127.0.0.1:8317",
  "model_mappings": [{"prefix": "cc", "provider": "commandcode-go"}],
  "quota_cache_ttl_seconds": 60,
  "route_cache_ttl_seconds": 30,
  "stale_after_seconds": 900,
  "total_timeout_seconds": 15,
  "account_timeout_seconds": 10,
  "max_concurrency": 4,
  "default_backoff_seconds": 60
}
```

环境变量覆盖:`QUOTA_HUB_CONFIG` / `QUOTA_HUB_API_BASE` / `QUOTA_HUB_SOCKET_PATH` / `QUOTA_HUB_MANAGEMENT_KEY_FILE`。

## 行为要点

- **路由**:完整模型 ID 与各账号注册模型精确匹配(宿主全局 registry 实况);未匹配先刷新快照;显式 `model_mappings` 按完整路径段最长前缀兜底;`model-router` 动态别名不推测,匹配不到即 `unmapped`
- **渠道**:账号有 `quota_plugin_id` → 走该插件管理 quota API(commandcode-go);provider=codex → api-call `wham/usage`(主/周/月/Code Review/命名窗口不混算);其余 `unsupported`
- **缓存**:账号额度 60s、路由快照 30s;`refresh` 绕缓存不绕退避;429 遵循 Retry-After(缺省 60s);401/403 报 `auth_required`;最后成功数据保留 15 分钟,标 `stale`+原始 `fetchedAt`
- **安全**:socket 目录 0700、socket 0600;管理客户端仅回环、固定路径白名单、拒重定向、不走代理;凭证 `$TOKEN$` 由宿主替换,本进程不接触 token;日志与错误文案不含密钥/上游响应体/本地路径;拒绝接管正在服务的 socket

## PoC / 测试

```bash
make race            # 单测 + race
go run . -poc         # 阶段1只读验证(需要管理密钥文件)
make uninstall       # 完全移除服务(保留 ~/.quota-hub)
```
