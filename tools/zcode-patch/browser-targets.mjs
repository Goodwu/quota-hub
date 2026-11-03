// browser-targets.mjs：browser 级 CDP 枚举全部 targets（含 BrowserView/webview
// 等不进 /json/list 的），并在含 zcode 桥的页面里验证 quotaHub。
// 用法：node tools/zcode-patch/browser-targets.mjs [port=9330]
const port = process.argv[2] || "9330";

const version = await (await fetch(`http://127.0.0.1:${port}/json/version`)).json();
const ws = new WebSocket(version.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });

let seq = 0;
const pending = new Map();
ws.onmessage = (ev) => {
  const msg = JSON.parse(ev.data);
  if (msg.id && pending.has(msg.id)) {
    pending.get(msg.id)(msg);
    pending.delete(msg.id);
  }
};
const send = (method, params) => new Promise((resolve) => {
  const id = ++seq;
  pending.set(id, resolve);
  ws.send(JSON.stringify({ id, method, params }));
});

const { result } = await send("Target.getTargets", {});
for (const t of result.targetInfos) {
  console.log(`${t.type.padEnd(10)} ${t.attached ? "A" : "-"} ${t.title.slice(0, 30).padEnd(30)} ${t.url.slice(0, 110)}`);
}

// 逐个 page/iframe target 附着并探测桥
for (const t of result.targetInfos) {
  if (t.type !== "page" && t.type !== "iframe" && t.type !== "webview") continue;
  if (!/zcode|file:/.test(t.url)) continue;
  const { sessionId } = (await send("Target.attachToTarget", { targetId: t.targetId, flatten: true })).result;
  const evalIn = async (expr) => {
    const resp = await send("Runtime.evaluate", {
      expression: expr,
      awaitPromise: true,
      returnByValue: true,
    }, sessionId && { sessionId });
    return resp;
  };
  // 附着后用 session 送命令
  const evalSession = (expr) =>
    new Promise((resolve) => {
      const id = ++seq;
      pending.set(id, (msg) => resolve(msg));
      ws.send(JSON.stringify({ id, method: "Runtime.evaluate", params: { expression: expr, awaitPromise: true, returnByValue: true }, sessionId }));
    });
  const zcode = await evalSession("typeof window.zcode");
  const quotaHub = await evalSession("typeof window.quotaHub");
  console.log(`\n== ${t.url.slice(0, 80)} ==`);
  console.log("  zcode:", zcode.result ? JSON.stringify(zcode.result.value) : zcode.error?.message);
  console.log("  quotaHub:", quotaHub.result ? JSON.stringify(quotaHub.result.value) : quotaHub.error?.message);
  if (quotaHub.result?.value === "object") {
    const caps = await evalSession("window.quotaHub.capabilities()");
    const q = await evalSession("window.quotaHub.query('cc/deepseek/deepseek-v4-flash', false)");
    console.log("  capabilities:", JSON.stringify(caps.result?.value).slice(0, 200));
    console.log("  query:", JSON.stringify(q.result?.value).slice(0, 300));
    const dom = await evalSession(`(() => ({ qhModel: (globalThis.__qhModel && globalThis.__qhModel.model) || null, cpaText: document.body.textContent.includes('CPA 额度') }))()`);
    console.log("  dom:", JSON.stringify(dom.result?.value));
  }
  await send("Target.detachFromTarget", { sessionId });
}
ws.close();
process.exit(0);
