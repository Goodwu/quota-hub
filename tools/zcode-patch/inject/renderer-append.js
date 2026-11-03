// renderer-append.js：追加到 /out/renderer/assets/styles-Dv5E6Odo.js 末尾（ESM）。
// 依赖该模块内的绑定：Q = React，$ = jsx 工厂（与 d1t/弹层同源，勿改名）。
//
// 行为：
// - 仅 CPA provider（配置白名单或自动判定非 account: 官方 provider）渲染分区
//   与弹层入口；官方 provider 保持原生弹层不变
// - a2t 捕获当前模型并后台预热；面板先渲染缓存（无 loading 闪烁），后台刷新
// - 关闭弹层即卸载停止轮询；模型切换重查并忽略旧请求
// 标记 __QH_RENDERER__ 用于幂等检测。
var __QH_RENDERER__ = true;

var __qhA2tBase = a2t;
a2t = function (e) {
  var result = __qhA2tBase(e);
  try {
    if (result && result.model) {
      globalThis.__qhModel = { provider: result.provider || "", model: result.model };
      __qhPrefetch(result.model);
    }
  } catch (_) {}
  return result;
};

// ── 预热与缓存（模块级，弹层多次开关复用；daemon 60s 缓存兜底上游压力） ──
// 冷却期：选中同一模型在冷却期内不重复预热；切走的模型不再触发任何刷新；
// 缓存条目上限淘汰最旧，长期未用模型的条目自然被挤掉（停止保留/刷新）。
var __QH_PREFETCH_COOLDOWN_MS = 5 * 60 * 1000;
var __QH_CACHE_MAX_ENTRIES = 16;
function __qhCacheStore(model, data) {
  try {
    var cache = (globalThis.__qhCache = globalThis.__qhCache || {});
    var keys = Object.keys(cache);
    if (!cache[model] && keys.length >= __QH_CACHE_MAX_ENTRIES) {
      keys.sort(function (a, b) { return cache[a].at - cache[b].at; });
      delete cache[keys[0]];
    }
    cache[model] = { data: data, at: Date.now() };
  } catch (_) {}
}
function __qhPrefetch(model) {
  try {
    var api = typeof window !== "undefined" ? window.quotaHub : null;
    if (!api || typeof api.query !== "function") return;
    var cache = (globalThis.__qhCache = globalThis.__qhCache || {});
    var entry = cache[model];
    if (entry && Date.now() - entry.at < __QH_PREFETCH_COOLDOWN_MS) return; // 冷却期内跳过
    api.query(model, false).then(
      function (res) {
        if (res && res.ok && res.data) __qhCacheStore(model, res.data);
      },
      function () {},
    );
  } catch (_) {}
}

// ── provider 白名单：capabilities.popupProviders 非空则精确匹配；否则非官方
//    provider（不以 account: 开头）视为 CPA。未加载完成前用自动判定。 ──
function __qhLoadWhitelist() {
  try {
    if (globalThis.__qhWhitelistLoading) return;
    globalThis.__qhWhitelistLoading = true;
    var api = typeof window !== "undefined" ? window.quotaHub : null;
    if (!api || typeof api.capabilities !== "function") return;
    api.capabilities().then(
      function (res) {
        var list = res && res.ok && res.data && res.data.popupProviders;
        globalThis.__qhPopupWhitelist = Array.isArray(list) ? list : [];
        globalThis.__qhWhitelistLoading = false;
      },
      function () {
        globalThis.__qhWhitelistLoading = false;
      },
    );
  } catch (_) {}
}
function __qhCpaActive() {
  try {
    var m = globalThis.__qhModel || {};
    var provider = m.provider || "";
    if (!provider) return false;
    var wl = globalThis.__qhPopupWhitelist;
    if (wl && wl.length > 0) return wl.indexOf(provider) !== -1;
    if (provider.indexOf("account:") === 0) return false; // 官方 provider
    // BYOK provider 下，查询结果为 unmapped 的模型（不经 CPA）不展示；
    // 预热未返回前先显示，结果就绪后自然收敛。
    var cached = globalThis.__qhCache && globalThis.__qhCache[m.model];
    if (cached && cached.data && cached.data.status === "unmapped") return false;
    return true;
  } catch (_) {
    return false;
  }
}

function __qhWindowLabel(w) {
  var map = {
    "5h": "5 小时",
    "7d": "每周",
    weekly: "每周",
    monthly: "每月",
    "code-review-5h": "代码评审 · 5 小时",
    "code-review-weekly": "代码评审 · 每周",
    "code-review-monthly": "代码评审 · 每月",
  };
  if (map[w]) return map[w];
  return w || "额度";
}

// 窗口排序：与官方一致的主三窗口在前，code review/命名额度随后。
function __qhWindowRank(w) {
  var rank = { "5h": 0, "7d": 1, weekly: 1, monthly: 2 };
  if (rank[w] !== undefined) return rank[w];
  if (w && w.indexOf("code-review") === 0) return 3;
  return 4;
}

function __qhAccountStatusText(s) {
  var map = {
    unsupported: "该渠道暂不支持额度查询",
    auth_required: "凭证认证失败",
    rate_limited: "限流中，稍后自动重试",
    temporarily_unavailable: "额度服务暂时不可用",
  };
  return map[s] || "";
}

function __qhPercentText(fraction) {
  if (fraction === null || fraction === undefined || !isFinite(fraction)) return "—";
  var pct = Math.max(0, Math.min(1, fraction)) * 100;
  return (Math.round(pct * 10) / 10) + "%";
}

// 当天只显示 HH:mm；跨天/跨月显示 M月d日 HH:mm。
function __qhFormatReset(iso) {
  if (!iso) return "";
  try {
    var d = new Date(iso);
    if (isNaN(d.getTime())) return "";
    var pad = function (n) { return (n < 10 ? "0" : "") + n; };
    var hm = pad(d.getHours()) + ":" + pad(d.getMinutes());
    var now = new Date();
    var sameDay =
      d.getFullYear() === now.getFullYear() &&
      d.getMonth() === now.getMonth() &&
      d.getDate() === now.getDate();
    if (sameDay) return hm;
    return d.getMonth() + 1 + "月" + d.getDate() + "日";
  } catch (_) {
    return "";
  }
}

function __qhMeter(color, label, valueText, resetText, fraction, description) {
  var widthPct = fraction !== null && fraction !== undefined && isFinite(fraction)
    ? Math.max(0, Math.min(1, fraction)) * 100
    : 0;
  var reset = resetText ? (0, $.jsx)("span", { className: "text-ui-xs text-foreground-subtle", children: " · " + resetText }) : null;
  return (0, $.jsxs)("div", {
    className: "min-w-0 space-y-1.5",
    title: description || "",
    children: [
      (0, $.jsx)("div", {
        className: "flex min-h-5 min-w-0 items-center gap-1",
        children: (0, $.jsx)("span", { className: "min-w-0 truncate text-foreground-subtle", children: label }),
      }),
      (0, $.jsxs)("div", { className: "relative min-w-0 overflow-hidden whitespace-nowrap text-ui-sm tabular-nums", children: [
        (0, $.jsx)("span", { className: "font-mono text-foreground", children: valueText }),
        reset,
      ] }),
      (0, $.jsx)("div", {
        className: "h-1.5 overflow-hidden rounded-full bg-surface-hover",
        children: (0, $.jsx)("div", {
          className: "h-full rounded-full transition-[width] duration-500 ease-out motion-reduce:transition-none",
          style: { backgroundColor: color, width: widthPct + "%" },
        }),
      }),
    ],
  });
}

function __qhNotice(text) {
  return (0, $.jsx)("div", { className: "flex items-center gap-2 p-1 text-ui-base text-foreground-subtle", children: text });
}

function __qhRenderAccounts(data) {
  if (!data || typeof data !== "object") return __qhNotice("额度数据为空");
  if (data.status === "unmapped") return __qhNotice("该模型未映射到 CPA 账号");
  if (data.status === "unavailable") return __qhNotice(data.reason || "额度服务不可用");
  var accounts = Array.isArray(data.accounts) ? data.accounts : [];
  if (accounts.length === 0) return __qhNotice("该模型未映射到 CPA 账号");
  var colors = ["var(--color-usage-chart-1)", "var(--color-usage-chart-2)", "var(--color-usage-chart-3)"];

  // 收集一个账号的全部窗口（合并各组、去组名），按官方顺序排列。
  function bucketsOf(account) {
    var out = [];
    var quota = account.quota;
    if (quota && Array.isArray(quota.groups)) {
      for (var gi = 0; gi < quota.groups.length; gi++) {
        var buckets = Array.isArray(quota.groups[gi] && quota.groups[gi].buckets) ? quota.groups[gi].buckets : [];
        for (var bi = 0; bi < buckets.length; bi++) {
          if (buckets[bi]) out.push(buckets[bi]);
        }
      }
    }
    out.sort(function (a, b) { return __qhWindowRank(a.window) - __qhWindowRank(b.window); });
    return out;
  }

  var blocks = [];
  for (var ai = 0; ai < accounts.length; ai++) {
    var account = accounts[ai];
    if (!account || typeof account !== "object") continue;
    var statusText = __qhAccountStatusText(account.status);
    var buckets = bucketsOf(account);
    if (buckets.length === 0) {
      blocks.push(__qhNotice(statusText || "暂无额度数据"));
      continue;
    }
    var cols = buckets.length >= 3 ? "grid-cols-3" : buckets.length === 2 ? "grid-cols-2" : "grid-cols-1";
    var cells = [];
    for (var wi = 0; wi < buckets.length; wi++) {
      var bucket = buckets[wi];
      cells.push(__qhMeter(
        colors[wi % colors.length],
        __qhWindowLabel(bucket.window),
        __qhPercentText(bucket.remainingFraction),
        __qhFormatReset(bucket.resetTime),
        bucket.remainingFraction,
        bucket.description || "",
      ));
    }
    var grid = (0, $.jsx)("div", { className: "grid gap-2 " + cols, children: cells });
    blocks.push((0, $.jsxs)("div", {
      className: "min-w-0 space-y-1.5" + (blocks.length > 0 ? " pt-2 border-t border-border/50" : ""),
      children: [
        (0, $.jsxs)("div", { className: "flex min-w-0 items-center gap-1.5", children: [
          (0, $.jsx)("span", { className: "min-w-0 truncate text-ui-sm font-medium text-foreground", children: account.label || account.provider || "账号" }),
          (0, $.jsx)("span", { className: "shrink-0 text-ui-xs text-foreground-subtle", children: account.provider || "" }),
          account.stale ? (0, $.jsx)("span", { className: "shrink-0 text-ui-xs text-foreground-subtle", children: "缓存" }) : null,
          statusText ? (0, $.jsx)("span", { className: "min-w-0 truncate text-ui-xs text-warning", children: statusText }) : null,
        ] }),
        grid,
      ],
    }));
  }
  return (0, $.jsx)("div", { className: "space-y-2", children: blocks });
}

function __qhCpaPanel(props) {
  var noContext = Boolean(props && props.noContext);
  var ctx = (globalThis && globalThis.__qhModel) || {};
  var model = ctx.model || "";
  var stateHook = (0, Q.useState)(function () {
    // 先显缓存：弹层重开时无 loading 闪烁。
    var cached = globalThis.__qhCache && globalThis.__qhCache[model];
    return cached ? { phase: "done", model: model, data: cached.data } : { phase: "loading", model: model };
  });
  var state = stateHook[0];
  var setState = stateHook[1];
  var seqRef = (0, Q.useRef)(0);
  (0, Q.useEffect)(function () {
    __qhLoadWhitelist();
    if (!model) {
      setState({ phase: "no-model" });
      return undefined;
    }
    var seq = ++seqRef.current;
    var api = typeof window !== "undefined" ? window.quotaHub : null;
    if (!api || typeof api.query !== "function") {
      setState({ phase: "unlinked", model: model });
      return undefined;
    }
    // 打开时刷新：绕过本地缓存直接查询（daemon 缓存命中时毫秒返回）。
    setState(function (prev) {
      return prev.phase === "done" && prev.model === model ? prev : { phase: "loading", model: model };
    });
    api.query(model, false).then(
      function (res) {
        if (seqRef.current !== seq) return;
        var data = res && res.ok ? res.data : null;
        if (data) __qhCacheStore(model, data);
        setState({ phase: data ? "done" : "error", model: model, data: data, error: res && res.error });
      },
      function (err) {
        if (seqRef.current !== seq) return;
        setState({ phase: "error", model: model, error: (err && err.error) || (err && err.message) || "查询失败" });
      },
    );
    return function () {
      seqRef.current++;
    };
  }, [model]);

  // 官方/未指定 provider 或模型不经 CPA（unmapped）：不渲染分区，
  // 原生弹层保持不变。
  if (!__qhCpaActive()) return null;
  if (state.phase === "done" && state.data && state.data.status === "unmapped") return null;

  try {
    var body;
    if (state.phase === "loading") {
      body = __qhNotice("正在查询 CPA 额度…");
    } else if (state.phase === "no-model") {
      body = __qhNotice("尚未选择模型");
    } else if (state.phase === "unlinked") {
      body = __qhNotice("未连接额度服务（quota-hub 未运行或 ZCode 版本不匹配）");
    } else if (state.phase === "error") {
      // 查询失败但有缓存数据时仍展示缓存。
      body = state.data ? __qhRenderAccounts(state.data) : __qhNotice(state.error || "查询失败");
    } else {
      body = __qhRenderAccounts(state.data);
    }
    return (0, $.jsxs)("div", {
      // 弹层里没有官方 context 用量块时（新建会话），不画顶部分隔线。
      className: noContext ? "space-y-2" : "border-t border-border pt-2 space-y-2",
      children: [
        (0, $.jsx)("div", { className: "flex min-w-0 items-center mb-2", children: [
          (0, $.jsx)("span", { className: "min-w-0 truncate text-ui-base font-medium text-foreground", children: "CPA 额度" }),
        ] }),
        body,
      ],
    });
  } catch (_) {
    return null;
  }
}
