// renderer-append.js：追加到 /out/renderer/assets/styles-Dv5E6Odo.js 末尾（ESM）。
// 依赖该模块内的绑定：Q = React，$ = jsx 工厂（与 d1t/弹层同源，勿改名）。
// 组件挂载在用量弹层 children 尾部（patch.mjs 注入调用点），首次打开查询、
// 关闭即卸载（Popover content 卸载天然停止轮询）、模型切换重查并忽略旧请求。
// 标记 __QH_RENDERER__ 用于幂等检测。
var __QH_RENDERER__ = true;

// 当前 provider/model 捕获：a2t 是 composer 选择解析（返回 {provider, model}），
// 每次选择变化都会经过这里。包装而非内联改写，保住原语义。
var __qhA2tBase = a2t;
a2t = function (e) {
  var result = __qhA2tBase(e);
  try {
    if (result && result.model) {
      globalThis.__qhModel = { provider: result.provider || "", model: result.model };
    }
  } catch (_) {}
  return result;
};

function __qhWindowLabel(w) {
  var map = {
    "5h": "5 小时窗口",
    weekly: "每周窗口",
    monthly: "每月窗口",
    "code-review-5h": "代码评审 · 5 小时",
    "code-review-weekly": "代码评审 · 每周",
    "code-review-monthly": "代码评审 · 每月",
  };
  if (map[w]) return map[w];
  return w || "额度";
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

function __qhFormatReset(iso) {
  if (!iso) return "";
  try {
    var d = new Date(iso);
    if (isNaN(d.getTime())) return "";
    var pad = function (n) { return (n < 10 ? "0" : "") + n; };
    return pad(d.getHours()) + ":" + pad(d.getMinutes());
  } catch (_) {
    return "";
  }
}

// 一条额度条（仿 ChatCodingPlanUsageMeter 的结构与样式类）。
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
  var blocks = [];
  for (var ai = 0; ai < accounts.length; ai++) {
    var account = accounts[ai];
    if (!account || typeof account !== "object") continue;
    var statusText = __qhAccountStatusText(account.status);
    var staleMark = account.stale ? " · 缓存" : "";
    var accountRows = [];
    var quota = account.quota;
    if (quota && Array.isArray(quota.groups)) {
      for (var gi = 0; gi < quota.groups.length; gi++) {
        var group = quota.groups[gi];
        var buckets = Array.isArray(group && group.buckets) ? group.buckets : [];
        if (group && group.displayName && quota.groups.length > 1) {
          accountRows.push((0, $.jsx)("div", {
            className: "text-ui-xs font-medium text-foreground-subtle pt-1",
            children: group.displayName,
          }));
        }
        for (var bi = 0; bi < buckets.length; bi++) {
          var bucket = buckets[bi];
          if (!bucket) continue;
          accountRows.push(__qhMeter(
            colors[(ai + bi) % colors.length],
            __qhWindowLabel(bucket.window),
            __qhPercentText(bucket.remainingFraction),
            __qhFormatReset(bucket.resetTime),
            bucket.remainingFraction,
            bucket.description || "",
          ));
        }
      }
    }
    if (accountRows.length === 0) {
      accountRows.push(__qhNotice(statusText || "暂无额度数据"));
    }
    blocks.push((0, $.jsxs)("div", {
      className: "min-w-0 space-y-1.5" + (ai > 0 ? " pt-2 border-t border-border/50" : ""),
      children: [
        (0, $.jsxs)("div", { className: "flex min-w-0 items-center gap-1.5", children: [
          (0, $.jsx)("span", { className: "min-w-0 truncate text-ui-sm font-medium text-foreground", children: (account.label || account.provider || "账号") }),
          (0, $.jsx)("span", { className: "shrink-0 text-ui-xs text-foreground-subtle", children: (account.provider || "") + staleMark }),
          statusText
            ? (0, $.jsx)("span", { className: "min-w-0 truncate text-ui-xs text-warning", children: statusText })
            : null,
        ] }),
      ].concat(accountRows),
    }));
  }
  return (0, $.jsxs)("div", { className: "space-y-2", children: blocks });
}

function __qhCpaPanel() {
  var ctx = (globalThis && globalThis.__qhModel) || {};
  var model = ctx.model || "";
  var stateHook = (0, Q.useState)({ phase: "loading", model: model });
  var state = stateHook[0];
  var setState = stateHook[1];
  var seqRef = (0, Q.useRef)(0);
  (0, Q.useEffect)(function () {
    if (!model) {
      setState({ phase: "no-model" });
      return undefined;
    }
    var seq = ++seqRef.current;
    setState({ phase: "loading", model: model });
    var api = typeof window !== "undefined" ? window.quotaHub : null;
    if (!api || typeof api.query !== "function") {
      setState({ phase: "unlinked", model: model });
      return undefined;
    }
    api.query(model, false).then(
      function (res) {
        if (seqRef.current !== seq) return;
        setState({ phase: "done", model: model, data: res && res.data });
      },
      function (err) {
        if (seqRef.current !== seq) return;
        setState({ phase: "error", model: model, error: (err && err.error) || (err && err.message) || "查询失败" });
      },
    );
    return function () {
      // 关闭弹层或模型切换：作废在途请求，不再有轮询。
      seqRef.current++;
    };
  }, [model]);

  try {
    var body;
    if (state.phase === "loading") {
      body = __qhNotice("正在查询 CPA 额度…");
    } else if (state.phase === "no-model") {
      body = __qhNotice("尚未选择模型");
    } else if (state.phase === "unlinked") {
      body = __qhNotice("未连接额度服务（quota-hub 未运行或 ZCode 版本不匹配）");
    } else if (state.phase === "error") {
      body = __qhNotice(state.error || "查询失败");
    } else {
      body = __qhRenderAccounts(state.data);
    }
    return (0, $.jsxs)("div", {
      className: "border-t border-border pt-2 space-y-2",
      children: [
        (0, $.jsxs)("div", { className: "flex min-w-0 items-center mb-1 gap-2", children: [
          (0, $.jsx)("span", { className: "text-ui-base font-medium text-foreground", children: "CPA 额度" }),
          (0, $.jsx)("span", { className: "text-ui-xs text-foreground-subtle truncate font-mono", children: model || "" }),
        ] }),
        body,
      ],
    });
  } catch (_) {
    return null;
  }
}
