#!/bin/bash
# quota-hub 正式补丁后的 ZCode 重启编排（一次性 LaunchAgent 触发，与 ZCode 进程树解耦）。
# 流程：退出 ZCode → 重启 → 失败自动回滚并重启。全程日志 /tmp/quota-hub-relaunch.log
LOG=/tmp/quota-hub-relaunch.log
APP=/Applications/ZCode.app
ASAR=$APP/Contents/Resources/app.asar
PLIST=$APP/Contents/Info.plist
PRISTINE=$HOME/src/quota-hub/build/backups/app.asar.pristine-3.17.2

{
echo "=== $(date '+%F %T') relaunch start (patched asar in place) ==="
sleep 8  # 给 UI 留出最终消息时间
osascript -e 'tell application "ZCode" to quit' >/dev/null 2>&1
for i in $(seq 1 20); do pgrep -x ZCode >/dev/null || break; sleep 1; done
if pgrep -x ZCode >/dev/null; then
  echo "graceful quit 超时，killall"
  killall ZCode 2>/dev/null; sleep 3
  pgrep -x ZCode >/dev/null && { killall -9 ZCode 2>/dev/null; sleep 2; }
fi
pgrep -x ZCode >/dev/null && echo "ERROR: ZCode 无法退出" || echo "ZCode 已退出"

open -a ZCode
sleep 30
if pgrep -x ZCode >/dev/null; then
  echo "OK: 补丁版 ZCode 启动成功 (pid $(pgrep -x ZCode | head -1))"
else
  echo "首启失败（可能为首扫瞬时态），重试一次"
  open -a ZCode
  sleep 25
  if pgrep -x ZCode >/dev/null; then
    echo "OK: 二次启动成功 (pid $(pgrep -x ZCode | head -1))"
  else
    echo "仍失败 → 回滚原始 asar 保可用性"
    cp "$PRISTINE" "$ASAR"
    /usr/libexec/PlistBuddy -c "Set :ElectronAsarIntegrity:Resources/app.asar:hash $(shasum -a 256 "$ASAR" | awk '{print $1}')" "$PLIST"
    codesign --force --deep -s - "$APP" 2>&1
    open -a ZCode
    sleep 20
    if pgrep -x ZCode >/dev/null; then
      echo "OK: 已回滚原始版本并启动（补丁未生效，待排查后重打）"
    else
      echo "FATAL: 回滚后仍无法启动，可用工厂包整体恢复: ~/src/quota-hub/build/pristine-3.17.2/ZCode-factory.app"
    fi
  fi
fi
echo "=== $(date '+%F %T') relaunch end ==="
} >> "$LOG" 2>&1
rm -f "$HOME/Library/LaunchAgents/com.quota-hub-relaunch.plist"
launchctl bootout "gui/$(id -u)/com.quota-hub-relaunch" 2>/dev/null
exit 0
