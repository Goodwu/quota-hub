// preload-append.cjs：追加到 /out/preload/index.cjs 末尾（CJS）。
// 只读桥：window.quotaHub.{query,capabilities}，不暴露任何其他能力。
// 标记 __QH_PRELOAD__ 用于幂等检测。
;(function () {
  var __QH_PRELOAD__ = true;
  try {
    var electron = require("electron");
    if (electron && electron.contextBridge && electron.ipcRenderer) {
      electron.contextBridge.exposeInMainWorld("quotaHub", {
        query: function (model, refresh) {
          return electron.ipcRenderer.invoke("quota-hub:query", { model: model, refresh: refresh === true });
        },
        capabilities: function () {
          return electron.ipcRenderer.invoke("quota-hub:capabilities");
        },
      });
    }
  } catch (_) {}
})();
