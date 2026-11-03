// inspect.mjs：dist 结构探针（只读）。用法：
//   node tools/zcode-patch/inspect.mjs <app-or-asar> [probe...]
// probe: d1t | anchors | preload | main
import { readFile, listFiles, functionBodyEnd } from "./asar.mjs";
import path from "node:path";
import fs from "node:fs";

const target = process.argv[2];
let asar = target;
if (target.endsWith(".app")) asar = path.join(target, "Contents/Resources/app.asar");
if (!fs.existsSync(asar)) throw new Error(`不存在: ${asar}`);

const renderer = readFile(asar, "out/renderer/assets/styles-Dv5E6Odo.js").toString("utf8");
const probe = process.argv[3] || "anchors";

if (probe === "d1t") {
  const start = renderer.indexOf("function d1t(");
  if (start === -1) throw new Error("d1t 未找到（版本漂移）");
  const end = functionBodyEnd(renderer, start);
  const fn = renderer.slice(start, end);
  console.log(`d1t @${start} len=${fn.length}`);
  console.log("--- head 400 ---");
  console.log(fn.slice(0, 400));
  console.log("--- tail 1500 ---");
  console.log(fn.slice(-1500));
} else if (probe === "anchors") {
  for (const pat of [
    "function d1t(",
    "function g2t(",
    "sidebar.usage.plan.title",
    "quotaHub",
    "exposeInMainWorld(\"zcode\"",
  ]) {
    console.log(JSON.stringify(pat), "→", renderer.indexOf(pat));
  }
} else if (probe === "preload") {
  const pre = readFile(asar, "out/preload/index.cjs").toString("utf8");
  console.log("preload len:", pre.length);
  console.log("tail 300:", JSON.stringify(pre.slice(-300)));
} else if (probe === "main") {
  const main = readFile(asar, "out/main/index.js").toString("utf8");
  console.log("main len:", main.length);
  console.log("tail 300:", JSON.stringify(main.slice(-300)));
  console.log("whenReady ctx:", JSON.stringify(main.slice(main.indexOf("whenReady") - 100, main.indexOf("whenReady") + 200)));
}
