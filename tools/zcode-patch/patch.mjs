#!/usr/bin/env node
// patch.mjs：ZCode 3.17.2 三层注入（主进程 IPC / preload 桥 / 弹层 CPA 分区）。
//
// 用法：
//   node patch.mjs --app /path/to/ZCode.app            # 打补丁（默认副本，勿直指正式应用）
//   node patch.mjs --app ... --restore                 # 恢复原始 asar 并同步哈希
//   node patch.mjs --app ... --backup /path/to/dir     # 备份目录（默认 <repo>/build/backups）
//
// 吸收 3.17.2 补丁事故 7 条教训（见设计文档）：
//   1. asar 数据起点 8+headerSize；读取限制在文件自身范围（asar.mjs）
//   2. 版本读取失败立即终止，绝不把 unknown 当合法版本
//   3. 每个改动 JS 全量 node --check；注入标记唯一性校验
//   4. 全部有界搜索，无整包扫描
//   5. repack 后全量清单 + unpacked 标记 + 非目标文件逐一致比对（asar.mjs）
//   6. 只在 3.17.2 固定 unpack 规则上运行；其他版本拒绝
//   7. 备份精确命名 pristine-<version>，restore 前校验版本与来源
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { listFiles, readFile, repack, updateAsarHashInPlist } from "./asar.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const SUPPORTED_VERSION = "3.17.2";
const RENDERER_CHUNK = "out/renderer/assets/styles-Dv5E6Odo.js";
const MAIN_CHUNK = "out/main/index.js";
const PRELOAD_CHUNK = "out/preload/index.cjs";
// 弹层 children 数组尾锚点（b1t 面板 + 数组闭合 + U1t 函数头，组合唯一）。
const POPUP_ANCHOR = "separated:!!(v&&ye||x)}):null]})})]})}function U1t(";
const POPUP_INJECT = "separated:!!(v&&ye||x)}):null,(0,$.jsx)(__qhCpaPanel,{})]})})]})}function U1t(";
// contextUsage 组件提前 return 门：CPA provider 下即使无上下文/官方 plan 也
// 渲染弹层入口（圆圈），否则新建会话选 BYOK 模型时无法打开弹层。
const GATE_ANCHOR = "if((!v||!pe)&&!x&&!S)return null;";
const GATE_INJECT = "if((!v||!pe)&&!x&&!S&&!__qhCpaActive())return null;";

const args = process.argv.slice(2);
const appPath = flagValue("--app") || "/Applications/ZCode.app";
const restore = args.includes("--restore");
const backupDir = flagValue("--backup") || path.join(HERE, "..", "..", "build", "backups");

function flagValue(name) {
  const i = args.indexOf(name);
  return i !== -1 && args[i + 1] && !args[i + 1].startsWith("--") ? args[i + 1] : null;
}

function sh(cmd, cmdArgs) {
  return execFileSync(cmd, cmdArgs, { encoding: "utf8", stdio: ["ignore", "pipe", "inherit"] });
}

const asarPath = path.join(appPath, "Contents/Resources/app.asar");
const plistPath = path.join(appPath, "Contents/Info.plist");

// 教训 2：版本读取失败必须停止。
const version = sh("/usr/libexec/PlistBuddy", ["-c", "Print :CFBundleShortVersionString", plistPath]).trim();
if (version !== SUPPORTED_VERSION) {
  console.error(`拒绝：ZCode 版本 ${version}，仅支持 ${SUPPORTED_VERSION}`);
  process.exit(2);
}
if (!fs.existsSync(asarPath)) {
  console.error(`拒绝：app.asar 不存在 ${asarPath}`);
  process.exit(2);
}

const pristinePath = path.join(backupDir, `app.asar.pristine-${version}`);
fs.mkdirSync(backupDir, { recursive: true });

if (restore) {
  if (!fs.existsSync(pristinePath)) {
    console.error(`无备份可恢复：${pristinePath}`);
    process.exit(1);
  }
  fs.copyFileSync(pristinePath, asarPath);
  const hash = updateAsarHashInPlist(appPath);
  console.log(`已恢复原始 asar 并更新哈希（${hash.slice(0, 16)}…）。完全退出后重启 ZCode 生效。`);
  process.exit(0);
}

// 幂等：任一标记存在即拒绝（先 --restore 再重打）。
const marks = { main: "__QH_MAIN__", preload: "__QH_PRELOAD__", renderer: "__QH_RENDERER__" };
const existing = [];
for (const [name, mark] of Object.entries(marks)) {
  const chunk = name === "main" ? MAIN_CHUNK : name === "preload" ? PRELOAD_CHUNK : RENDERER_CHUNK;
  if (readFile(asarPath, chunk).toString("utf8").includes(mark)) existing.push(name);
}
if (existing.length > 0) {
  console.error(`已注入（${existing.join(",")}）。重打请先 --restore。`);
  process.exit(3);
}

// 备份（首次）。
if (!fs.existsSync(pristinePath)) {
  fs.copyFileSync(asarPath, pristinePath);
  console.log(`已备份原始 asar → ${pristinePath}`);
} else {
  console.log(`备份已存在，沿用 ${pristinePath}`);
}

// ── 目标绑定前置校验（防版本漂移：注入代码引用的模块内标识符必须存在） ──────
const rendererNow = readFile(asarPath, RENDERER_CHUNK).toString("utf8");
for (const [label, probe] of [
  ["a2t 函数", "function a2t("],
  ["React 绑定 Q", "(0,Q.useState)"],
  ["jsx 工厂 $", "(0,$.jsx)"],
  ["弹层锚点", POPUP_ANCHOR],
  ["弹层门锚点", GATE_ANCHOR],
]) {
  if (!rendererNow.includes(probe)) {
    console.error(`渲染层缺少 ${label}（${JSON.stringify(probe)}），版本漂移，拒绝注入`);
    process.exit(4);
  }
}
for (const [label, chunk, probe] of [
  ["主进程 ipcMain", MAIN_CHUNK, "ipcMain as"],
  ["preload contextBridge", PRELOAD_CHUNK, 'exposeInMainWorld("zcode"'],
]) {
  if (!readFile(asarPath, chunk).toString("utf8").includes(probe)) {
    console.error(`${label} 缺少 ${JSON.stringify(probe)}，版本漂移，拒绝注入`);
    process.exit(4);
  }
}

// ── 构建替换内容 ────────────────────────────────────────────────────────────
const mainAppend = fs.readFileSync(path.join(HERE, "inject", "main-append.js"), "utf8");
const preloadAppend = fs.readFileSync(path.join(HERE, "inject", "preload-append.cjs"), "utf8");
const rendererAppend = fs.readFileSync(path.join(HERE, "inject", "renderer-append.js"), "utf8");

const mainOut = readFile(asarPath, MAIN_CHUNK).toString("utf8") + "\n" + mainAppend;
const preloadOut = readFile(asarPath, PRELOAD_CHUNK).toString("utf8") + "\n" + preloadAppend;

let renderer = readFile(asarPath, RENDERER_CHUNK).toString("utf8");
const anchorHits = renderer.split(POPUP_ANCHOR).length - 1;
if (anchorHits !== 1) {
  console.error(`弹层锚点出现 ${anchorHits} 次（需恰好 1 次），版本漂移，拒绝注入`);
  process.exit(4);
}
renderer = renderer.replace(POPUP_ANCHOR, POPUP_INJECT);
const gateHits = renderer.split(GATE_ANCHOR).length - 1;
if (gateHits !== 1) {
  console.error(`弹层门锚点出现 ${gateHits} 次（需恰好 1 次），版本漂移，拒绝注入`);
  process.exit(4);
}
renderer = renderer.replace(GATE_ANCHOR, GATE_INJECT);
renderer = renderer + "\n" + rendererAppend;
// 教训 3：注入恰好一次（弹层调用点 1 次 + 组件定义 1 次 = 2 处引用）。
if (renderer.split("__qhCpaPanel").length - 1 !== 2) {
  console.error(`渲染层注入自检失败（__qhCpaPanel 引用 ${renderer.split("__qhCpaPanel").length - 1} 处，需 2 处）`);
  process.exit(4);
}
if (renderer.split(POPUP_INJECT).length - 1 !== 1) {
  console.error("渲染层注入自检失败（弹层调用点不唯一）");
  process.exit(4);
}

// ── 重打包（含 node --check 与全量布局比对） ────────────────────────────────
console.log("重打包（extract → 注入 → check → pack → 布局比对）…");
const fileCount = repack(asarPath, {
  [MAIN_CHUNK]: mainOut,
  [PRELOAD_CHUNK]: preloadOut,
  [RENDERER_CHUNK]: renderer,
}, {
  // 教训 5/6：3.17.2 实测的 unpack 规则（与历史 PoC 一致）。
  unpackGlob: [
    "{**/sharp-darwin-arm64.node,**/libvips-cpp.8.17.3.dylib,**/sshcrypto.node}",
  ],
  unpackDirs: ["**/node-pty/prebuilds/darwin-arm64"],
});
console.log(`重打包完成（${fileCount} 个文件，布局与内容比对通过）`);

const hash = updateAsarHashInPlist(appPath);
console.log(`Info.plist asar 哈希 → ${hash.slice(0, 16)}…`);
console.log("完成。完全退出 ZCode（⌘Q）后重新打开验证。");
console.log("若启动被签名拦截：codesign --force --deep -s - <app>");
console.log(`回滚：node ${path.relative(process.cwd(), fileURLToPath(import.meta.url))} --app ${appPath} --restore`);
