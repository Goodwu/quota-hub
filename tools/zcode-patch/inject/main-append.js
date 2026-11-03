// main-append.js：追加到 /out/main/index.js 末尾（ESM，import 声明提升）。
// 仅暴露两个限定 IPC 通道：quota-hub:query / quota-hub:capabilities。
// 通道实现只接受 {model, refresh}，其余参数一律拒绝；Unix Socket 固定路径。
//
// 标记 __QH_MAIN__ 用于幂等检测。
// import 卫语句：本文件会被 node --check 校验，但注入目标是 ESM chunk，
// 这里 import 的模块（electron / node:net / node:os）在 Electron 主进程可用。
import { ipcMain as __qhIpcMain } from "electron";
import { connect as __qhConnect } from "node:net";
import { homedir as __qhHomedir } from "node:os";

var __QH_MAIN__ = true;

const __QH_SOCKET = __qhHomedir() + "/.quota-hub/quota-hub.sock";
const __QH_TIMEOUT_MS = 15000;

function __qhRoundTripper() {
  return {
    socketPath: __QH_SOCKET,
    timeoutMs: __QH_TIMEOUT_MS,
  };
}

// __qhSocketRequest：单次 HTTP over unix socket。失败只返回 {ok:false,error}，
// 错误文案固定为安全短句，不含路径/密钥/上游细节。
function __qhSocketRequest(method, path, bodyObj) {
  const config = __qhRoundTripper();
  return new Promise((resolve) => {
    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      try {
        socket.destroy();
      } catch (_) {}
      resolve(value);
    };
    const body = bodyObj ? JSON.stringify(bodyObj) : null;
    const headers = [
      method + " " + path + " HTTP/1.1",
      "Host: quota-hub",
      "Connection: close",
    ];
    if (body !== null) {
      headers.push("Content-Type: application/json");
      headers.push("Content-Length: " + Buffer.byteLength(body));
    }
    let socket;
    try {
      socket = __qhConnect(config.socketPath);
    } catch (_) {
      finish({ ok: false, error: "额度服务不可用" });
      return;
    }
    const timer = setTimeout(() => finish({ ok: false, error: "额度服务响应超时" }), config.timeoutMs);
    socket.on("error", () => {
      clearTimeout(timer);
      finish({ ok: false, error: "额度服务未连接" });
    });
    socket.on("connect", () => {
      try {
        socket.write(headers.join("\r\n") + "\r\n\r\n" + (body === null ? "" : body));
      } catch (_) {
        clearTimeout(timer);
        finish({ ok: false, error: "额度服务请求失败" });
      }
    });
    let raw = Buffer.alloc(0);
    socket.on("data", (chunk) => {
      raw = Buffer.concat([raw, chunk]);
      if (raw.length > 1 << 20) {
        clearTimeout(timer);
        finish({ ok: false, error: "额度服务响应过大" });
      }
    });
    socket.on("close", () => {
      clearTimeout(timer);
      if (settled) return;
      const text = raw.toString("utf8");
      const split = text.indexOf("\r\n\r\n");
      if (split === -1) {
        finish({ ok: false, error: "额度服务响应异常" });
        return;
      }
      const statusLine = text.slice(0, text.indexOf("\r\n"));
      const status = Number.parseInt(statusLine.split(" ")[1], 10);
      let payload = null;
      try {
        // 连接已关闭且 Connection: close，body 即剩余字节。
        payload = JSON.parse(text.slice(split + 4));
      } catch (_) {
        finish({ ok: false, error: "额度服务响应解析失败" });
        return;
      }
      finish({ ok: status >= 200 && status < 300, status: status, data: payload });
    });
  });
}

try {
  __qhIpcMain.handle("quota-hub:query", async (_event, payload) => {
    if (!payload || typeof payload !== "object") {
      return { ok: false, error: "非法请求" };
    }
    const model = payload.model;
    if (typeof model !== "string" || !model || model.length > 256 || /\s/.test(model)) {
      return { ok: false, error: "非法模型名" };
    }
    const refresh = payload.refresh === true;
    return __qhSocketRequest("POST", "/v1/quota/query", { model: model, refresh: refresh });
  });
  __qhIpcMain.handle("quota-hub:capabilities", async () => {
    return __qhSocketRequest("GET", "/v1/capabilities", null);
  });
} catch (_) {}
