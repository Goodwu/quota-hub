// deep-diagnose.mjs：单次启动副本的深度诊断——两个世界分别探测桥、
// 收集 renderer 控制台错误、确认 UI 加载状态、必要时点击弹层触发 CPA 分区。
// 用法：node tools/zcode-patch/deep-diagnose.mjs [port=9330]
const port = process.argv[2] || "9330";

const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
const page = list.find((t) => t.type === "page");
if (!page) { console.error("无 page target"); process.exit(1); }

const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
let seq = 0;
const pending = new Map();
const contexts = new Map();
const consoleLogs = [];
ws.onmessage = (ev) => {
  const msg = JSON.parse(ev.data);
  if (msg.id && pending.has(msg.id)) { pending.get(msg.id)(msg); pending.delete(msg.id); return; }
  if (msg.method === "Runtime.executionContextCreated") {
    contexts.set(msg.params.context.id, msg.params.context);
  }
  if (msg.method === "Runtime.consoleAPICalled") {
    const text = (msg.params.args || []).map((a) => a.value ?? a.description ?? "").join(" ");
    consoleLogs.push(`[${msg.params.type}] ${text.slice(0, 300)}`);
  }
  if (msg.method === "Runtime.exceptionThrown") {
    const d = msg.params.exceptionDetails;
    consoleLogs.push(`[exception] ${(d.exception?.description || d.text || "").slice(0, 400)}`);
  }
};
const send = (method, params) => new Promise((resolve) => {
  const id = ++seq; pending.set(id, resolve);
  ws.send(JSON.stringify({ id, method, params }));
});
const evaluate = (expr, contextId) =>
  send("Runtime.evaluate", { expression: expr, awaitPromise: true, returnByValue: true, contextId }).then((r) => {
    
    if (r.exceptionDetails) return { __exception: (r.exceptionDetails.exception?.description || r.exceptionDetails.text || "").slice(0, 300) };
    return r.result && r.result.result && r.result.result.value;
  });

await send("Runtime.enable", {});
await new Promise((r) => setTimeout(r, 2000));

console.log("contexts:", [...contexts.values()].map((c) => `${c.id}:${c.name || c.origin}`).join(" | "));
for (const c of contexts.values()) {
  console.log(`\n== ctx#${c.id} (${(c.name || c.origin).slice(0, 60)}) ==`);
  console.log("  zcode/quotaHub/zcodePluginSandbox =",
    await evaluate("typeof window.zcode + '/' + typeof window.quotaHub + '/' + typeof window.zcodePluginSandbox", c.id));
  console.log("  UI 状态 =", await evaluate(`(() => ({
    title: document.title,
    rootChildren: document.getElementById('root') ? document.getElementById('root').children.length : -1,
    bodyHasModelPicker: !!document.querySelector('[data-testid*=model], [class*=composer]'),
    textLen: document.body.innerText.length
  }))()`, c.id));
}

// 若主世界无 quotaHub，检查是否有模块级错误——打印收集到的 console 错误
console.log("\nconsole/errors（最多 20 条）:");
for (const l of consoleLogs.slice(-20)) console.log(" ", l);

ws.close();
process.exit(0);
