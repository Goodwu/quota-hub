// popup-verify.mjs：单次启动补丁副本后，hover 打开用量弹层并 dump CPA 分区文本。
// 用法：node tools/zcode-patch/popup-verify.mjs [port=9330]
const port = process.argv[2] || "9330";

const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
const page = list.find((t) => t.type === "page");
if (!page) { console.error("无 page target"); process.exit(1); }

const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
let seq = 0;
const pending = new Map();
ws.onmessage = (ev) => {
  const msg = JSON.parse(ev.data);
  if (msg.id && pending.has(msg.id)) { pending.get(msg.id)(msg); pending.delete(msg.id); }
};
const send = (method, params) => new Promise((resolve) => {
  const id = ++seq; pending.set(id, resolve);
  ws.send(JSON.stringify({ id, method, params }));
});
const evaluate = (expr) =>
  send("Runtime.evaluate", { expression: expr, awaitPromise: true, returnByValue: true }).then((r) => {
    if (r.exceptionDetails) return { __exception: r.exceptionDetails.exception?.description?.slice(0, 300) };
    return r.result?.result?.value;
  });

await send("Runtime.enable", {});

// 1. 当前模型捕获
console.log("当前模型捕获:", JSON.stringify(await evaluate("globalThis.__qhModel || null")));

// 2. 找用量弹层 trigger（context usage 图标）坐标并 hover
const trigger = await evaluate(`(() => {
  const cands = [...document.querySelectorAll('[data-testid]')].filter(el => {
    const t = el.getAttribute('data-testid');
    return /usage|context/i.test(t) && el.offsetParent !== null;
  });
  if (!cands.length) return null;
  const el = cands[0];
  const r = el.getBoundingClientRect();
  return { testid: el.getAttribute('data-testid'), x: r.x + r.width / 2, y: r.y + r.height / 2 };
})()`);
console.log("trigger:", JSON.stringify(trigger));
if (!trigger) { console.log("未找到 trigger（composer 可能在别的视图）"); process.exit(0); }

await send("Input.dispatchMouseEvent", { type: "mouseMoved", x: trigger.x, y: trigger.y, button: "none", pointerType: "mouse" });
await new Promise((r) => setTimeout(r, 1200));

// 3. dump 弹层内容
const popup = await evaluate(`(() => {
  const panels = [...document.querySelectorAll('div')].filter(d => d.textContent && d.textContent.includes('CPA 额度') && d.children.length > 0);
  if (!panels.length) return { found: false, bodyHasUsage: document.body.innerText.includes('额度') || document.body.innerText.includes('usage') };
  // 取最内层面板
  const panel = panels[panels.length - 1];
  return { found: true, text: panel.innerText.slice(0, 600) };
})()`);
console.log("CPA 分区:", JSON.stringify(popup, null, 2));

// 4. 若分区显示中，再取弹层整体文本确认官方区并存
const whole = await evaluate(`(() => {
  const pop = [...document.querySelectorAll('[data-radix-popper-content-wrapper], [role=dialog]')].map(d => d.innerText).filter(Boolean);
  return pop.length ? pop[0].slice(0, 500) : null;
})()`);
console.log("弹层整体(含官方区):", JSON.stringify(whole));

ws.close();
process.exit(0);
