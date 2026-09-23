# DISPATCH-FIX —— 派活通道修复报告

> 日期：2026-09-23
> 事故工单：`job-m0-msg-1790165641`（跑了 667s → SIGKILL / exit 137，`docs/M0-RESULT.md` 没生成）
> 修复文件：`~/minis-ops/bin/jobrun.sh`（派活通道）、`~/minis-ops/小马手册.md`（工单模板铁律）、
> 新增 `tools/dispatch-selftest.sh`（可复现自测）
> 结论：**两个缺陷均已修复，四项验收全部实测通过。**

---

## 0. TL;DR

| 缺陷 | 旧行为 | 修复后 |
|---|---|---|
| ① 工单正文进 argv → `pgrep -f` 命中 dsh 自杀 | `dsh --profile headless "$T"`，`/proc/<dsh>/cmdline` 含 3675 字节工单 | `dsh --profile headless --patch <短路径> task`，正文由 dsh **进程内**从 `DSH_TASK_FILE` 读；cmdline 仅 140 字节 |
| ② 产物目录指错 | `PAT="$HOME/minis-ops/data/*"`（调度器数据目录） | 默认 `~/tigertally`，可 per-job 覆盖，递归、排除 `.git/`、只算开工后新增/改动 |
| 附加：收尾写坏 `status.json` | `tail -c 400` 截断多字节字符 → `json.dump` 抛 `UnicodeEncodeError`，`jobrun` 退出 1 | 两处 python 先 `fsencode` 还原字节再 `replace` 解码 |

---

## 1. 事故与根因

`~/minis-ops/bin/jobrun.sh` 旧版第 43-44 行：

```bash
T=$(cat "$TASKFILE")
dsh --profile headless "$T" > "$LOG" 2>&1 &
```

工单全文被当成 argv 传给 dsh。dsh 的 `/proc/PID/cmdline` 因此包含工单正文。
工单正文里若出现 `xxx serve` 之类的字符串，收尾时任何一句宽泛匹配

```bash
pgrep -f "xxx serve" | xargs kill -9
```

都会命中 **dsh 自己（父进程）** → 自杀 → `exit 137`。

### 修复前实测证据（本工单运行期间抓到）

```console
$ pgrep -af "job-m0-msg-1790165641"
1770586 node /home/ubuntu/.local/share/pnpm/dsh --profile headless # 任务：修复派活通道的两个缺陷（重要，先于其他活）...

$ tr '\0' '\n' < /proc/1770586/cmdline | wc -c
3675
```

这条 dsh 就是正在跑本工单的进程，命令行里完整带着 3675 字节工单正文 —— 陷阱现场复现。

**这是自指陷阱，不是"小马笨"**：任何 `pgrep -f <工单里出现过的字符串>` 都会命中自己。

---

## 2. 环境：先搞清楚 dsh 为什么能跑通（不弄坏现有路子）

```console
$ /usr/bin/node --version
v18.19.1

$ ~/.local/node/bin/node --version
v24.19.0

# 用系统 node 直接调 dsh —— 就是工单提到的 parseEnv 报错
$ env -i HOME=$HOME PATH=/usr/bin:/bin /usr/bin/node ~/.local/share/pnpm/dsh --version
.../dsh-app-boot/lib/index.js:4
import { parseEnv } from "node:util";
         ^^^^^^^^
SyntaxError: The requested module 'node:util' does not provide an export named 'parseEnv'
```

**原因**：dsh 是 ESM 且 import 了 `node:util` 的 `parseEnv`，需要 **Node ≥ 20.12**；
系统 node 是 v18，`~/.local/node/bin/node` 是 v24。dsh 的软链在
`~/.local/share/pnpm/dsh`，非交互 shell 的 PATH 里没有它。

**jobrun.sh 能跑通的唯一原因**就是这一行（修复后保留在第 28 行）：

```bash
export PATH=$HOME/.local/share/pnpm:$HOME/.local/node/bin:$PATH
```

它同时把 `dsh` 和 v24 `node` 排到 PATH 最前，`#!/usr/bin/env node` 才会解析到 v24。
**改动过程中未触碰这一行。**

dsh headless 的接口确认：

```console
$ dsh --profile headless --help
Usage: dsh --profile headless [options] [task...]
Arguments:
  task        the task text; multiple words are joined by spaces
Options:
  -h, --help  show this help
```

**只有位置参数 task，没有 stdin / --task-file 入口。** 因此不能用 stdin。

---

## 3. 修复 1（核心）：工单正文不再进任何进程的 argv

### 3.1 采用的方案：`--patch` 覆盖层让 dsh 进程内读文件

dsh 的 headless profile 里，任务是这样装配的（`dsh --profile headless --dump-config`）：

```yaml
- id: headless-runner
  name: '@deepseek-ai/dsh-headless'
  inject:
    - headlessStartup
  config:
    task: !!js ctx.headlessStartup.task
```

我们不改 dsh，只用官方 `--patch` 覆盖层把这一行的 `task` 改成"进程内从文件读"：

```yaml
- id: headless-runner
  config:
    task: !!js process.getBuiltinModule('node:fs').readFileSync(process.env.DSH_TASK_FILE, 'utf8')
```

于是启动命令变成（正文在文件里，不在 argv）：

```bash
export DSH_TASK_FILE="$EFFECTIVE"   # 含"安全规矩前缀 + 工单正文"的文件
dsh --profile headless --patch "$PATCH" task > "$LOG" 2>&1 &
```

`task` 只是满足 headless-startup 必填校验的占位符，真正的任务由 patch 覆盖。

> 为什么优于 wrapper：dsh 官方扩展点，不改 dsh 运行时；实测可读文件、正文完整送达。
> 风险：patch 若找不到目标行会被 dsh **静默忽略**（实测未知 id 退出码仍为 0）。
> 因此加了 `--dump-config` 预检，失败就拒绝派活并告警，**绝不静默丢正文**。

### 3.2 实测：patch 命中 + 预检有效

```console
$ dsh --profile headless --patch /tmp/preflight.patch.yml --dump-config | grep -A3 'id: headless-runner'
- id: headless-runner
  name: '@deepseek-ai/dsh-headless'
  inject:
    - headlessStartup
  config:
    task: !!js >-
      process.getBuiltinModule('node:fs').readFileSync(process.env.DSH_TASK_FILE,
      'utf8')

$ # 未知 id 的 patch 会被静默忽略 —— 预检据此判失败
$ dsh --profile headless --patch bad.patch.yml --dump-config | grep -q DSH_TASK_FILE && echo HIT || echo MISS
MISS
```

### 3.3 实测：隔离验证 cmdline 与任务送达

```console
$ printf 'SELFTEST_MARKER_1\n请只回一句 OK。假命令示例：xxx serve\n' > /tmp/task.txt
$ DSH_TASK_FILE=/tmp/task.txt dsh --profile headless --patch /tmp/p.patch.yml task > /tmp/out.txt 2>&1 &
$ PID=$!; sleep 2; tr '\0' ' ' < /proc/$PID/cmdline; echo
node /home/ubuntu/.local/share/pnpm/dsh --profile headless --patch /tmp/p.patch.yml task

$ wait $PID; echo "exit=$?"; cat /tmp/out.txt
exit=0
OK
```

cmdline 不含 marker、不含 `xxx serve`；任务被完整执行（回 `OK`）。

### 3.4 实测：完整派活链路自测（可复现）

```console
$ cd ~/tigertally && bash tools/dispatch-selftest.sh
JOB=dispatch-selftest-1790175959
FAKE=ghostsvc-1774740 serve
--- launch fixed jobrun.sh ---
--- assert 1: dsh cmdline must not contain ticket text ---
PASS: dsh cmdline clean:
node /home/ubuntu/.local/share/pnpm/dsh --profile headless --patch /home/ubuntu/minis-ops/jobs/dispatch-selftest-1790175959.patch.yml task
--- assert 2: pgrep must not match a dsh process ---
PASS: no dsh in pgrep output
1774881 bash -c pgrep -af "ghostsvc-1774740 serve" || echo NO_MATCH
--- assert 3: exit code ---
PASS: jobrun_exit=0
--- assert 4: artifact broadcast ---
PASS: status exit=0 artifacts=['/home/ubuntu/tigertally/docs/_dispatch_selftest.md']
==================================
SELFTEST: ALL PASS (job=dispatch-selftest-1790175959)
```

注意 **assert 2**：`pgrep -af "ghostsvc-... serve"` 唯一命中项是执行 pgrep 的 shell 自身
（它的命令行里当然含被搜索字符串），**没有任何 dsh 进程**。这正是旧版的死因，现在不复现。

---

## 4. 修复 2：产物监控目录

旧代码：

```bash
PAT="$HOME/minis-ops/data/*"
```

监控的是调度器数据目录，工单写到 `~/tigertally` 的产物完全看不见，`status.json`
列的是无关文件。

新代码（`jobrun.sh` 第 35-66 行）：

```bash
OUTDIR="${JOB_OUTDIR:-$HOME/tigertally}"
# 也可在工单里声明:  # OUTDIR: /path/to/project
if [ -f "$TASKFILE" ] && [ -z "${JOB_OUTDIR:-}" ]; then
  _d=$(grep -m1 -E '^[[:space:]]*#?[[:space:]]*OUTDIR[[:space:]]*[:=]' "$TASKFILE" ...)
  [ -n "$_d" ] && OUTDIR="$_d"
fi
ARTIFACT_LIMIT="${JOB_ARTIFACT_LIMIT:-5}"

latest_artifacts() {   # 递归、排除 .git/、只算开工后新增/改动
  find "$OUTDIR" -type f -newermt "@$START" -not -path '*/.git/*' \
    -printf '%T@ %p\n' | sort -rn | head -n "$ARTIFACT_LIMIT" | cut -d' ' -f2-
}
```

规则：

1. **默认 `~/tigertally`**（项目根）；
2. 可用环境变量 `JOB_OUTDIR` 按工单覆盖（每个工单可不同）；
3. 也可在工单里用一行注释 `# OUTDIR: /path` 声明；
4. **递归**扫描（否则子目录里的产物看不见）；
5. **排除 `.git/`**（否则每次 git 操作都触发播报噪音）；
6. **只算 `mtime >= START`** 的新增/改动文件，不把项目旧文件当产物。

### 实测：目录指令解析

```console
$ parse() { grep -m1 -E '^[[:space:]]*#?[[:space:]]*OUTDIR[[:space:]]*[:=]' "$1" | sed -E 's/^[^:=]*[:=][[:space:]]*//'; }
$ printf 'blah\n# OUTDIR: /home/ubuntu/tigertally\nmore\n' > t1.task
$ printf 'OUTDIR=/tmp/other\n' > t2.task
$ printf 'no directive here\n' > t3.task
$ for f in t1.task t2.task t3.task; do printf '%s -> [%s]\n' "$f" "$(parse $f)"; done
t1.task -> [/home/ubuntu/tigertally]
t2.task -> [/tmp/other]
t3.task -> []
```

### 实测：递归 + 排除 .git + 只算新文件

```console
$ START=$(date +%s); latest_artifacts   # 开工前
（空）
$ echo test > ~/tigertally/docs/_selftest_artifact.tmp
$ echo noise >> ~/tigertally/.git/FETCH_HEAD
$ latest_artifacts
/home/ubuntu/tigertally/docs/_selftest_artifact.tmp      # 新文件被捕获，.git 噪音被排除
```

### 实测：播报系统看见产物（ntfy 实抓）

```console
$ curl -s "https://ntfy.sh/minis-xiaoma-cde7fb9b0a1424bf/json?poll=1&since=3m" | ...
1790175792 | [小马] 📦 产物更新 (10s) | _dispatch_selftest.md 39B
1790175802 | [小马] ✅ 完成 (21s, exit=0) | _dispatch_selftest.md 39B
```

`status.json`：

```json
{
 "job": "dispatch-selftest2-1790175844",
 "state": "done",
 "exit": 0,
 "elapsed": 12,
 "last_log": "...DISPATCH_SELFTEST_V2_DONE",
 "artifacts": [
  { "path": "/home/ubuntu/tigertally/docs/_dispatch_selftest.md", "size": 42 }
 ]
}
```

测试产物已按要求删除（自测脚本退出时自动 `rm -f`）。

---

## 5. 修复 3：安全规矩写进代码与模板

写进三处，保证"规则不只写在文档里"：

**① `jobrun.sh` 顶部注释**（第 5-17 行）+ **每个工单自动附带的前缀**（第 110-125 行）：

```text
【派活通道安全规矩 · 必须遵守】
一切杀进程的操作，必须：
  ① 先列出完整命令行（pgrep -af）供核对；
  ② 必须排除自身（$$）与父进程（$PPID）；
  ③ 优先精确匹配（pgrep -x），不用宽泛的 -f。
（原因：工单正文里的字符串可能出现在别的进程命令行里；用 -f 宽泛匹配极易误杀
  父进程，曾导致工单收到 SIGKILL / exit 137 自杀。）
```

`jobrun.sh` 会生成 `$JOB.effective.task = 安全前缀 + 工单正文`，再喂给 dsh。
**这样无论工单来自 boss_listener 还是别的入口，每张工单都带着这条铁律开工。**

**② `~/minis-ops/小马手册.md` 铁律第 12 条**（每次 AI 任务由 `runner.py` 全文注入）：
含正确姿势示例 `pgrep -af` → `grep -vE "^($$|$PPID) "` → `pgrep -x`。

**③ 本仓库自测脚本** `tools/dispatch-selftest.sh`：把规矩变成断言，随时可回归。

---

## 6. 验收逐条对照

| # | 验收项 | 结果 | 证据 |
|---|---|---|---|
| 1 | cmdline 不含工单正文 | ✅ | 第 3.4 节 assert 1：dsh cmdline 仅 140B，`--patch <短路径> task`；运行期 `pgrep -af dsh` 全量快照不含测试工单正文 |
| 2 | 自杀陷阱复现，不命中 dsh，exit=0 | ✅ | 第 3.4 节 assert 2/3：`pgrep` 只命中自身 shell，`jobrun_exit=0` |
| 3 | 产物播报能看见 | ✅ | 第 4 节：ntfy 实抓「📦 产物更新」+「✅ 完成」；`status.json.artifacts` 指向 `~/tigertally/docs/_dispatch_selftest.md` |
| 4 | 现有功能不坏 | ✅ | 同一自测走完"派发 → 运行 → 收尾 → 播报"全链路，`state=done / exit=0` |

**修复前后对照（同一命令）**

| | 旧版 | 新版 |
|---|---|---|
| 启动命令 | `dsh --profile headless "<3675B 工单全文>"` | `dsh --profile headless --patch <短路径> task` |
| dsh cmdline 字节数 | **3675** | **140** |
| `pgrep -f "<正文里的假命令>"` | 命中 dsh → 自杀 | 不命中任何 dsh |

---

## 7. 顺带修掉的收尾 bug（`status.json` 写坏）

v1 自测暴露：`TAIL=$(tail -c 400 "$LOG")` 可能把多字节 UTF-8 字符拦腰截断，
Python 从 argv 读到 surrogateescape 代理字符，`json.dump(..., ensure_ascii=False)`
抛 `UnicodeEncodeError`：

```text
UnicodeEncodeError: 'utf-8' codec can't encode characters in position 1-2: surrogates not allowed
```

结果 `status.json` 写到一半、`jobrun` 退出 1（收尾失败）。已在 `snapshot()` 与最终
写状态的 python 里加 `clean()`（`os.fsencode` 还原字节 → `decode('utf-8','replace')`）。
v2/v3 自测 `last_log` 完整、`jobrun_exit=0`。

---

## 8. 残留风险（同类调用点，建议后续同法收口）

以下脚本仍把任务文本放进 dsh 的 argv，遇到"正文含假命令 + 收尾 pgrep -f"同样会中招。
本次按要求只改派活主通道 `jobrun.sh`，未动它们以免破坏在线服务：

| 文件:行 | 代码 |
|---|---|
| `~/minis-ops/bin/runner.py:748` | `cmd = f"{node} {dsh} --profile headless {shlex.quote(full)}"` |
| `~/minis-ops/bin/room_agent.py:320` | `cmd = [DSH, "--profile", DSH_PROFILE, prompt]` |
| `~/minis-ops/jobs/run_task.sh:11` | `dsh --profile headless "$(cat "$TASKFILE")"` |
| `~/minis-ops/scripts/run_job.sh:12` | `dsh --profile headless "$TASK"` |

收口方式与 `jobrun.sh` 相同：写 patch → `DSH_TASK_FILE=<任务文件>` →
`dsh --profile headless --patch <patch> task`。`runner.py` 的 `full`（手册+payload）
当前只在内存里，可先落临时文件再走同一路径。

---

## 9. 改动文件清单

| 文件 | 改动 |
|---|---|
| `~/minis-ops/bin/jobrun.sh` | 核心修复：patch 文件读任务、预检、产物目录、安全前缀、UTF-8 收尾加固、`push` 走 stdin |
| `~/minis-ops/小马手册.md` | 铁律第 12 条「杀进程前必须先排除自己」 |
| `~/tigertally/tools/dispatch-selftest.sh` | 新增：可复现的派活通道自测（4 项断言，全 PASS） |
| `~/tigertally/docs/DISPATCH-FIX.md` | 本报告 |

原始 `jobrun.sh` 备份：`~/minis-ops/bin/jobrun.sh.bak-20260923_230203`。

### 如何复跑验收

```bash
bash ~/tigertally/tools/dispatch-selftest.sh   # 退出码 0 = 全部通过
```

---

*本报告中的命令与输出均为本次修复过程中在本机实测抓取，未做手工修饰（仅截断超长的进程命令行以利排版）。*
