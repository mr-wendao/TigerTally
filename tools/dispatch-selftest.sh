#!/bin/bash
# dispatch-selftest.sh —— 派活通道自测
#
# 复现并验证「工单正文进 argv → pgrep -f 命中 dsh 自己 → exit 137 自杀」陷阱是否已根治：
#   1) 工单正文故意含一句假命令 "<fake> serve"；
#   2) 工单让 agent 执行 pgrep -af "<fake> serve"（只读）；
#   3) 断言：a) 运行期 dsh 的 /proc/<pid>/cmdline 不含工单正文；
#            b) pgrep 只可能命中执行 pgrep 的自身 shell，绝不命中 dsh；
#            c) 工单 exit=0 且产物被播报系统看见。
#
# 用法:
#   bash tools/dispatch-selftest.sh
#   NTFY_TOPIC=xxx bash tools/dispatch-selftest.sh     # 换播报主题（默认出站群总线）
#
# 退出码 0 = 全部通过；非 0 = 有断言失败。测试产物 docs/_dispatch_selftest.md 会自动删除。
set -u

JOBS="${MINIS_OPS:-$HOME/minis-ops}/jobs"
JOBRUN="${MINIS_OPS:-$HOME/minis-ops}/bin/jobrun.sh"
TOPIC="${NTFY_TOPIC:-minis-xiaoma-cde7fb9b0a1424bf}"
OUTDIR="${JOB_OUTDIR:-$HOME/tigertally}"
EV="$OUTDIR/docs/_dispatch_selftest.md"
JOB="dispatch-selftest-$(date +%s)"
TASK="$JOBS/$JOB.task"
FAKE="ghostsvc-$$ serve"          # 唯一假命令名：绝不出现在别的工单里
FAIL=0

cleanup() { rm -f "$EV"; }
trap cleanup EXIT

cat > "$TASK" <<TASK_EOF
# 派活通道自测工单（只读，绝对不要 kill 任何进程）

本工单正文故意含一句不存在的假命令：\`${FAKE}\`。
请严格按顺序执行，不要做额外探索、不要改代码、不要 kill 任何进程：

1) 只读执行，并保留完整原样输出：
   pgrep -af "${FAKE}" || echo NO_MATCH

2) 执行：
   mkdir -p "$OUTDIR/docs" && printf 'dispatch-selftest %s\n' "\$(date -u +%FT%TZ)" > "$EV"

3) 把第 1 步输出贴回来，最后一行只回：DISPATCH_SELFTEST_DONE
TASK_EOF
chmod 600 "$TASK"

echo "JOB=$JOB"
echo "FAKE=$FAKE"
echo "--- launch fixed jobrun.sh ---"
bash "$JOBRUN" "$JOB" "$TASK" "$JOBS" "$TOPIC" > "/tmp/$JOB.jobrun.out" 2>&1 &
JR=$!

: > "/tmp/$JOB.cmdlines"
for _ in $(seq 1 300); do
  kill -0 "$JR" 2>/dev/null || break
  for pid in $(pgrep -f -- "--profile headless" 2>/dev/null); do
    ppid=$(ps -o ppid= -p "$pid" 2>/dev/null | tr -d ' ')
    [ "$ppid" = "$JR" ] && { tr '\0' ' ' < "/proc/$pid/cmdline"; echo; } >> "/tmp/$JOB.cmdlines" 2>/dev/null
  done
  sleep 1
done
wait "$JR"; RC=$?

echo "--- assert 1: dsh cmdline must not contain ticket text ---"
if grep -qF "$FAKE" "/tmp/$JOB.cmdlines"; then
  echo "FAIL: dsh cmdline leaked ticket text"; FAIL=1
else
  echo "PASS: dsh cmdline clean:"; sort -u "/tmp/$JOB.cmdlines" | grep -v -- '--dump-config' | head -1
fi

echo "--- assert 2: pgrep must not match a dsh process ---"
LOG="$JOBS/$JOB.log"
if grep -E "^[0-9]+ .*dsh --profile headless" "$LOG" 2>/dev/null | grep -q .; then
  echo "FAIL: pgrep output matched a dsh process"; grep -E "^[0-9]+ .*dsh --profile headless" "$LOG"; FAIL=1
else
  echo "PASS: no dsh in pgrep output"; grep -E "^[0-9]+ " "$LOG" 2>/dev/null | head -3
fi

echo "--- assert 3: exit code ---"
if [ "$RC" = "0" ]; then echo "PASS: jobrun_exit=0"; else echo "FAIL: jobrun_exit=$RC"; cat "/tmp/$JOB.jobrun.out"; FAIL=1; fi

echo "--- assert 4: artifact broadcast ---"
python3 - "$JOBS/$JOB.status.json" "$EV" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
paths = [a["path"] for a in d.get("artifacts", [])]
ok = sys.argv[2] in paths and d.get("exit") == 0
print(("PASS" if ok else "FAIL") + ": status exit=%s artifacts=%s" % (d.get("exit"), paths))
sys.exit(0 if ok else 1)
PY
[ $? -eq 0 ] || FAIL=1

echo "=================================="
[ "$FAIL" = "0" ] && echo "SELFTEST: ALL PASS (job=$JOB)" || echo "SELFTEST: FAILED (job=$JOB)"
exit "$FAIL"
