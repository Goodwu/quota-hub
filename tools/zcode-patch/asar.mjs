// asar.mjs：无依赖 asar 读取/重打包工具（ZCode 3.17.2 实测格式）。
// 数据起点 = 8 + headerSize（headerSize 是文件偏移 4 的小端 uint32）——
// 这是旧补丁事故的第一条教训：16+jsonSize 是错的。
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

export function readHeader(asarPath) {
  const buf = fs.readFileSync(asarPath);
  const headerSize = buf.readUInt32LE(4);
  const jsonSize = buf.readUInt32LE(12);
  if (headerSize < jsonSize || headerSize > buf.length) {
    throw new Error(`asar header 异常: headerSize=${headerSize} jsonSize=${jsonSize}`);
  }
  const header = JSON.parse(buf.slice(16, 16 + jsonSize).toString("utf8"));
  return { buf, header, base: 8 + headerSize };
}

export function listFiles(asarPath) {
  const { header, base } = readHeader(asarPath);
  const files = [];
  (function walk(node, prefix) {
    if (node.files) {
      for (const [name, child] of Object.entries(node.files)) walk(child, prefix + "/" + name);
      return;
    }
    files.push({ path: prefix, offset: base + Number(node.offset), size: node.size, unpacked: Boolean(node.unpacked) });
  })(header, "");
  return files;
}

export function readFile(asarPath, filePath) {
  return readFileFrom(readHeader(asarPath), filePath);
}

// readFileFrom：基于已加载的 {buf,header,base} 读单个文件，避免整包重读。
export function readFileFrom({ buf, header, base }, filePath) {
  let node = header;
  for (const seg of filePath.split("/")) {
    if (!seg) continue;
    node = node?.files?.[seg];
    if (!node) throw new Error(`asar 内文件不存在: ${filePath}`);
  }
  if (node.files || node.unpacked) throw new Error(`不是可读的打包文件: ${filePath}`);
  if (node.size === undefined || node.offset === undefined) throw new Error(`文件无内容区: ${filePath}`);
  return buf.subarray(base + Number(node.offset), base + Number(node.offset) + node.size);
}

// 找函数体边界（简易括号配平，忽略字符串/模板/正则细节——对压缩产物足够，
// 调用方必须用 node --check 验证改写结果）。
export function functionBodyEnd(source, fnStart) {
  const open = source.indexOf("{", fnStart);
  if (open === -1) throw new Error("未找到函数体起点");
  let depth = 0;
  for (let i = open; i < source.length; i++) {
    const c = source[i];
    if (c === '"' || c === "'" || c === "`") {
      const quote = c;
      i++;
      while (i < source.length && source[i] !== quote) {
        if (source[i] === "\\") i++;
        i++;
      }
      continue;
    }
    if (c === "(" || c === "{" || c === "[") depth++;
    else if (c === ")" || c === "}" || c === "]") {
      depth--;
      if (depth === 0) return i + 1;
    }
  }
  throw new Error("括号配平失败");
}

// 重打包：extract → 替换文件 → node --check 每个改动的 JS → pack →
// 布局比对（文件清单、unpacked 标记、非目标文件内容一致）→ 原子替换。
// unpack 规则仅适用于 3.17.2（见 patch.mjs 历史教训 5）。
export function repack(asarPath, replacements, { unpackGlob, unpackDirs }) {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "zcode-asar-"));
  const tree = path.join(tmp, "tree");
  const packed = path.join(tmp, "app.asar");
  try {
    sh("npx", ["-y", "@electron/asar", "extract", asarPath, tree]);
    for (const [filePath, content] of Object.entries(replacements)) {
      const target = path.join(tree, filePath);
      fs.writeFileSync(target, content);
      if (filePath.endsWith(".js") || filePath.endsWith(".cjs") || filePath.endsWith(".mjs")) {
        // node --check 按扩展决定模块目标：.cjs 走 CommonJS，其余（ESM 产物）写 .mjs。
        const ext = filePath.endsWith(".cjs") ? ".cjs" : ".mjs";
        const check = path.join(tmp, "check-" + path.basename(filePath).replace(/\.(js|cjs|mjs)$/, "") + ext);
        fs.writeFileSync(check, content);
        sh("node", ["--check", check]);
      }
    }
    const packArgs = ["-y", "@electron/asar", "pack", tree, packed];
    for (const glob of unpackGlob || []) packArgs.push("--unpack", glob);
    for (const dir of unpackDirs || []) packArgs.push("--unpack-dir", dir);
    sh("npx", packArgs);

    const before = listFiles(asarPath);
    const after = listFiles(packed);
    if (before.length !== after.length) throw new Error(`重打包文件数量不一致: ${before.length} → ${after.length}`);
    const beforeMap = new Map(before.map((f) => [f.path, f]));
    // 性能：整包各读一次，逐文件 slice 比对（此前逐文件重读整包是灾难）。
    // 键规范化：listFiles 路径带前导 /，replacements 键两种写法都接受。
    const normalizedKeys = new Map(Object.entries(replacements).map(([k, v]) => [k.replace(/^\//, ""), v]));
    const beforeRaw = readHeader(asarPath);
    const afterRaw = readHeader(packed);
    for (const f of after) {
      const old = beforeMap.get(f.path);
      if (!old || old.unpacked !== f.unpacked) throw new Error(`包外布局不一致: ${f.path}`);
      if (!normalizedKeys.has(f.path.replace(/^\//, "")) && !f.unpacked) {
        const a = readFileFrom(beforeRaw, f.path);
        const b = readFileFrom(afterRaw, f.path);
        if (!a.equals(b)) throw new Error(`非目标文件内容变化: ${f.path}`);
      }
    }
    fs.copyFileSync(packed, asarPath);
    return after.length;
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

export function sha256(file) {
  return execFileSync("shasum", ["-a", "256", file], { encoding: "utf8" }).split(/\s+/)[0];
}

export function updateAsarHashInPlist(appPath) {
  const plist = path.join(appPath, "Contents/Info.plist");
  const hash = sha256(path.join(appPath, "Contents/Resources/app.asar"));
  execFileSync("/usr/libexec/PlistBuddy", [
    "-c",
    `Set :ElectronAsarIntegrity:Resources/app.asar:hash ${hash}`,
    plist,
  ]);
  return hash;
}

function sh(cmd, args) {
  return execFileSync(cmd, args, { encoding: "utf8", stdio: ["ignore", "pipe", "inherit"] });
}
