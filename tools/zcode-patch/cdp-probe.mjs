// cdp-probe.mjs：遍历目标页面全部执行上下文（顶层+iframe、主世界），
// 在含 window.zcode 的上下文里验证 window.quotaHub 与完整查询链路。
// 用法：node tools/zcode-patch/cdp-probe.mjs [port=9330] [--click-usage]
const port = process.argv[2] || "9330";
const wantClick = process.argv.includes("--click-usage");

const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
const pages = list.filter((t) => t.type === "page");
console.log("targets:", pages.map((t) => t.title.slice(0, 24)).join(" | "));
const page = pages[0];
if (!page) process.exit(1);

const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });

let seq = 0;
const pending = new Map();
const contexts = new Map(); // id -> {name, frame}
ws.onmessage = (ev) => {
  const msg = JSON.parse(ev.data);
  if (msg.id && pending.has(msg.id)) {
    pending.get(msg.id)(msg);
    pending.delete(msg.id);
    return;
  }
  if (msg.method === "Runtime.executionContextCreated") {
    const c = msg.params.context;
    contexts.set(c.id, { name: c.name || "", origin: c.origin || "" });
  }
};
const send = (method, params) => new Promise((resolve) => {
  const id = ++seq;
  pending.set(id, resolve);
  ws.send(JSON.stringify({ id, method, params }));
});
const evaluate = (expr, contextId) =>
  send("Runtime.evaluate", { expression: expr, awaitPromise: true, returnByValue: true, contextId }).then((r) => {
    if (r.exceptionDetails) return { __exception: r.exceptionDetails.exception?.description || r.exceptionDetails.text };
    return r.result && r.result.result && r.result.result.value;
  });

await send("Runtime.enable", {});
await new Promise((r) => setTimeout(r, 1500)); // 等 contextCreated 收集
console.log("contexts:", [...contexts.entries()].map(([id, c]) => `${id}:${c.name || c.origin || "?"}`).join(" | "));

for (const [id, info] of contexts) {
  const zcode = await evaluate("typeof window.zcode", id);
  const quotaHub = await evaluate("typeof window.quotaHub", id);
  const marker = zcode !== "undefined" || quotaHub !== "undefined" ? " ← 有桥" : "";
  console.log(`ctx#${id}(${(info.name || info.origin).slice(0, 50)}) zcode=${zcode} quotaHub=${quotaHub}${marker}`);

  if (quotaHub === "object") {
    console.log("  capabilities =", JSON.stringify(await evaluate("window.quotaHub.capabilities()", id)));
    console.log("  query cc     =", JSON.stringify(await evaluate("window.quotaHub.query('cc/deepseek/deepseek-v4-flash', false)", id)));
    console.log("  query codex  =", JSON.stringify(await evaluate("window.quotaHub.query('gpt-6-astra', false)", id)));
    console.log("  bad args     =", JSON.stringify(await evaluate("window.quotaHub.query('bad model', false)", id)));
  }
  // CPA 分区 DOM 与当前模型捕获
  const domCheck = await evaluate(`(() => {
    const panels = [...document.querySelectorAll('div')].filter(d => d.textContent && d.textContent.includes('CPA 额度'));
    return { cpaPanels: panels.length, qhModel: (globalThis.__qhModel && globalThis.__qhModel.model) || null };
  })()`, id);
  if (domCheck && typeof domCheck === "object" && (domCheck.cpaPanels > 0 || domCheck.qhModel)) {
    console.log("  CPA 分区 DOM =", JSON.stringify(domCheck));
  }
}

ws.close();
process.exit(0);
