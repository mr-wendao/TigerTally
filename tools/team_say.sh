#!/bin/sh
# team_say.sh —— 往「群」上说一句话
#
# 用法：
#   team_say.sh "职业经理" "这单我接了，交给小马"
#   team_say.sh "小马"     "渲染完了，产物在 png/"
#   team_say.sh "温湿度计" "客厅温度 26.3 度"
#
# 任何机器、任何语言，只要能发一条 HTTP POST，就能进这个群。
# 这就是全部的接入方式 —— 没有别的对接要谈。
#
# ============ 配置 ============
# 复制 config.example.sh 成 config.sh 并填上你自己的设置；
# 或者直接用环境变量覆盖：
#   GROUP_BASE=https://你的服务器 GROUP_TOPIC=你的主题 team_say.sh 名字 内容
HERE=$(dirname "$0")
[ -f "$HERE/config.sh" ] && . "$HERE/config.sh"
GROUP_BASE="${GROUP_BASE:-https://ntfy.sh}"
GROUP_TOPIC="${GROUP_TOPIC:-}"
# ==============================

NAME="$1"; shift
MSG="$*"

if [ -z "$GROUP_TOPIC" ]; then
  echo "还没配置：先设置 GROUP_TOPIC（见上方说明）" >&2
  exit 1
fi
if [ -z "$NAME" ] || [ -z "$MSG" ]; then
  echo "用法: $0 <你的名字> <内容>" >&2
  exit 1
fi

code=$(curl -s -o /dev/null -w "%{http_code}" --max-time 15 \
  -X POST "$GROUP_BASE/$GROUP_TOPIC" \
  -H "Title: $NAME" \
  -H "Tags: speech_balloon" \
  -H "Priority: default" \
  --data-binary "$MSG")

if [ "$code" = "200" ]; then
  echo "✅ 已进群 [$NAME] $MSG"
else
  echo "❌ 失败 http=$code" >&2
  exit 1
fi
