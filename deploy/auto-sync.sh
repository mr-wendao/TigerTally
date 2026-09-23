#!/bin/bash
# ============================================================
#  自动同步到 GitHub —— 有改动才推，没改动什么都不做
#
#  为什么这么设计：
#  GitHub 政策禁止"automated excessive bulk activity"和
#  "inauthentic activity"。每天空提交凑数属于违规。
#  所以我们只在真有改动时提交，不产生无意义的提交记录。
#
#  防呆：提交前扫描敏感信息，命中就中止 —— 宁可漏推，不可推错。
# ============================================================
set -uo pipefail

REPO="$HOME/tigertally"
LOG="$HOME/tigertally-push.log"
MSG_FILE="$HOME/.tigertally-msg"
GUARD_FILE="$HOME/.tigertally-guard"
NOTIFY_FILE="$HOME/.tigertally-notify"
MAX_LOG_LINES=500

log() { echo "$(date '+%F %T')  $*" >> "$LOG"; }

# 日志轮转，防止无限增长
if [ -f "$LOG" ] && [ "$(wc -l < "$LOG")" -gt "$MAX_LOG_LINES" ]; then
  tail -n "$MAX_LOG_LINES" "$LOG" > "$LOG.tmp" && mv "$LOG.tmp" "$LOG"
fi

cd "$REPO" 2>/dev/null || { log "⛔ 仓库目录不存在：$REPO"; exit 1; }

# ---------- 1. 先拉取，避免冲突 ----------
if ! git pull --ff-only -q >>"$LOG" 2>&1; then
  log "⛔ 拉取失败（可能有冲突），本次跳过，人工处理"
  exit 1
fi

# ---------- 2. 有改动吗？没有就直接结束 ----------
git add -A 2>/dev/null
if git diff --cached --quiet; then
  log "无改动，跳过"
  exit 0
fi

# ---------- 3. 敏感信息扫描（防呆核心） ----------
if [ -f "$GUARD_FILE" ]; then
  HITS=$(git diff --cached -U0 \
    | grep -E '^\+' \
    | grep -E -f "$GUARD_FILE" 2>/dev/null)
  if [ -n "$HITS" ]; then
    log "⛔ 检出敏感信息，已中止推送（文件已取消暂存）"
    echo "$HITS" | sed 's/^/       /' | head -5 >> "$LOG"
    git reset -q
    # 告警：这种必须让人知道
    [ -f "$NOTIFY_FILE" ] && curl -s -m 10 -X POST "$(cat "$NOTIFY_FILE")" \
      -H "Title: 虎符同步已拦截" \
      -d "检出疑似密钥，已中止推送。请看服务器日志：$LOG" >/dev/null 2>&1
    exit 2
  fi
fi

# ---------- 4. 组装提交信息 ----------
# 如果事先写了提交说明，就用它（说得更清楚）
if [ -f "$MSG_FILE" ] && [ -s "$MSG_FILE" ]; then
  MSG=$(cat "$MSG_FILE")
  rm -f "$MSG_FILE"
else
  CHANGED=$(git diff --cached --name-only | head -6 | sed 's/^/  /')
  MSG="同步 $(date +%F)"$'\n\n'"改动文件："$'\n'"$CHANGED"
fi

# ---------- 5. 提交并推送 ----------
if git commit -q -m "$MSG" >>"$LOG" 2>&1 && git push -q >>"$LOG" 2>&1; then
  SUMMARY=$(echo "$MSG" | head -1)
  log "✅ 已推送：$SUMMARY"
  [ -f "$NOTIFY_FILE" ] && curl -s -m 10 -X POST "$(cat "$NOTIFY_FILE")" \
    -H "Title: 虎符已同步 GitHub" \
    -d "$SUMMARY" >/dev/null 2>&1
  exit 0
else
  log "⛔ 提交或推送失败"
  exit 1
fi
