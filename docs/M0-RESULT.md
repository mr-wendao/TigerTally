# 虎符 M0 验收结果

- **验收对象**：`server/`（上一轮产出，8 个 `.go` 文件，含验收测试；commit `960517b`）
- **验收标准**：`docs/DESIGN-M0.md` 第「验收标准」节
- **验收时间**：2026-09-23 23:56 ~ 23:57（UTC）
- **验收人**：本轮工单
- **结论先行**：**5/5 通过**。外加 `kill -9` 崩溃持久化检查也通过。
- **代码改动**：无（只补 `docs/M0-RESULT.md` 与 `server/README.md`，未改一行实现）。

> 说明：下面每个代码块都是**实际敲的命令和实际终端输出**。没有任何"代码看着对"式的推断。
> 仅有的排版调整：少数多行 JSON 被压缩了换行、省去了 `curl` 的进度信息，
> **字段名与数值全部原样来自实际输出**。

---

## 0. 验收环境与构建

```
$ go version
go version go1.22.2 linux/amd64

$ cd ~/tigertally/server && go build -o tigertally .
$ ls -la tigertally
-rwxrwxr-x 1 ubuntu ubuntu 14166186 Sep 23 23:56 tigertally

$ go vet ./...
（无输出，退出码 0）
```

`file` 结果：

```
$ file server/tigertally
server/tigertally: ELF 64-bit LSB executable, x86-64, version 1 (SYSV), dynamically linked, interpreter /lib64/ld-linux-x86-64.so.2, Go BuildID=5zZ9nzXn0Vwwyd59_fP2/_h6HYt9yEeCWTJoAC-2L/hVsHqhfcFPBOaOJB2Ov2/v0enyZYlgiqjq4X3yTRh, with debug_info, not stripped
```

**判定**：编译、`go vet` 均无错。二进制未提交（`.gitignore` 第 41 行 `/server/tigertally` 已挡）。

---

## 1. 验收 1：重启不丢消息

新建一个干净目录 `/tmp/m0-acc`，用同一份二进制起服务，新建「重启验收群」，
发 3 条，`SIGTERM` 停服务，用**同一个 `--db` 文件**重启，再拉。

发 3 条并确认已落库：

```
$ curl -s -X POST http://127.0.0.1:18787/api/groups -H "Content-Type: application/json" \
    -d '{"name":"重启验收群","owner":"群主"}' > g2.json
$ GID2=$(jq -r .group_id g2.json); A2=$(jq -r .token g2.json); echo $GID2
77c296eebfba

$ for i in 1 2 3; do
    curl -s -X POST "http://127.0.0.1:18787/api/$GID2/messages" \
      -H "Authorization: Bearer $A2" -H 'Content-Type: application/json' \
      -d "{\"body\":\"重启前第 $i 条\"}" | jq -c '{id,body}'
  done
{"id":4,"body":"重启前第 1 条"}
{"id":5,"body":"重启前第 2 条"}
{"id":6,"body":"重启前第 3 条"}

$ curl -s "http://127.0.0.1:18787/api/$GID2/messages?since=0" -H "Authorization: Bearer $A2" \
    | jq -c '{count,latest,bodies:[.messages[].body]}'
{"count":3,"latest":6,"bodies":["重启前第 1 条","重启前第 2 条","重启前第 3 条"]}
```

停服务（**先列出完整命令行核对，再用精确 PID 杀；没有用宽泛的 `pgrep -f`**）：

```
$ pgrep -x tigertally | while read p; do ps -o pid=,ppid=,args= -p "$p"; done
1793561       1 ./tigertally serve --addr 127.0.0.1:18787 --db ./data.db

$ kill -TERM "$(cat pid)"        # pid 文件里就是上面这个 1793561
$ sleep 1
$ pgrep -x tigertally || echo "(已无 tigertally 进程)"
(已无 tigertally 进程)

$ tail -n 4 serve.log
2026/09/23 23:56:43 POST /api/77c296eebfba/messages 201 169B 2ms
2026/09/23 23:56:43 GET /api/77c296eebfba/messages 200 586B 0s
2026/09/23 23:56:43 收到退出信号，正在收尾…
2026/09/23 23:56:43 已停止
```

重启（同一 `--db ./data.db`）并拉取：

```
$ nohup ./tigertally serve --addr 127.0.0.1:18787 --db ./data.db >> serve.log 2>&1 &
$ ps -o pid=,ppid=,args= -p "$NEWPID"
1793896 1793862 ./tigertally serve --addr 127.0.0.1:18787 --db ./data.db

$ curl -s "http://127.0.0.1:18787/api/$GID2/messages?since=0" -H "Authorization: Bearer $A2" | jq .
{
  "count": 3,
  "group_id": "77c296eebfba",
  "has_more": false,
  "latest": 6,
  "messages": [
    {
      "id": 4, "group_id": "77c296eebfba", "sender_id": 3, "sender": "群主", "kind": "human",
      "body": "重启前第 1 条", "created_at": "2026-09-23T15:56:43.658Z", "ts": 1790179003658
    },
    {
      "id": 5, "group_id": "77c296eebfba", "sender_id": 3, "sender": "群主", "kind": "human",
      "body": "重启前第 2 条", "created_at": "2026-09-23T15:56:43.665Z", "ts": 1790179003665
    },
    {
      "id": 6, "group_id": "77c296eebfba", "sender_id": 3, "sender": "群主", "kind": "human",
      "body": "重启前第 3 条", "created_at": "2026-09-23T15:56:43.673Z", "ts": 1790179003673
    }
  ]
}
```

**判定**：**通过**。3 条一条不少，`count=3`、`latest=6`。

### 附加：`kill -9` 崩溃后也不丢（比"重启"更狠）

```
$ curl -s -X POST .../api/$GID2/messages -d '{"body":"kill -9 之前的最后一条"}' | jq -c '{id,body}'
{"id":7,"body":"kill -9 之前的最后一条"}

$ pgrep -x tigertally | while read p; do ps -o pid=,ppid=,args= -p "$p"; done
1793896       1 ./tigertally serve --addr 127.0.0.1:18787 --db ./data.db

$ kill -9 "$(cat pid)"
$ sleep 1
$ pgrep -x tigertally || echo "(已无 tigertally 进程)"
(已无 tigertally 进程)

$ nohup ./tigertally serve --addr 127.0.0.1:18787 --db ./data.db >> serve.log 2>&1 &
$ curl -s "http://127.0.0.1:18787/api/$GID2/messages?since=0" -H "Authorization: Bearer $A2" \
    | jq -c '{count,latest,bodies:[.messages[].body]}'
{"count":4,"latest":7,"bodies":["重启前第 1 条","重启前第 2 条","重启前第 3 条","kill -9 之前的最后一条"]}
```

**判定**：**通过**。这与 `db.go` 的 `journal_mode(WAL) + synchronous(FULL)` 一致，
进程被强杀也不丢已提交消息。

---

## 2. 验收 2：浏览器实时收到（WebSocket）

用 Go 写了个临时 WebSocket 客户端探针（模拟浏览器：浏览器不能设
`Authorization` 头，只能走子协议 `bearer.<token>`），源码见文末附录。
先连上保持监听，再用 curl 从另一个通道发消息：

```
$ cd ~/tigertally/server && go build -o /tmp/m0-acc/wsprobe ./cmd/wsprobe
$ cd /tmp/m0-acc
$ ./wsprobe -url "ws://127.0.0.1:18787/api/$GID/ws" -token "$MEMBER" -timeout 15s > ws.out 2>&1 &
$ sleep 1.5
$ cat ws.out
已连接 subprotocol="bearer.tt_fbc4e015b8de7751e0962bd8304db5ba485f83e12ecf4d75e39b466567d1b98d"
收到帧: {"group_id":"1001d7f09999","kind":"agent","latest":2,"member":"小马","member_id":2,"online":1,"role":"member","since":0,"type":"hello"}

$ curl -s -X POST "http://127.0.0.1:18787/api/$GID/messages" \
    -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' \
    -d '{"body":"WS 实时测试：喂——"}' | jq -c .
{"id":3,"group_id":"1001d7f09999","sender_id":1,"sender":"群主","kind":"human","body":"WS 实时测试：喂——","created_at":"2026-09-23T15:56:37.456Z","ts":1790178997456}

$ wait $WP; echo "wsprobe exit=$?"
wsprobe exit=0

$ cat ws.out
已连接 subprotocol="bearer.tt_fbc4e015b8de7751e0962bd8304db5ba485f83e12ecf4d75e39b466567d1b98d"
收到帧: {"group_id":"1001d7f09999","kind":"agent","latest":2,"member":"小马","member_id":2,"online":1,"role":"member","since":0,"type":"hello"}
收到帧: {"body":"WS 实时测试：喂——","created_at":"2026-09-23T15:56:37.456Z","group_id":"1001d7f09999","id":3,"kind":"human","sender":"群主","sender_id":1,"ts":1790178997456,"type":"message"}
```

消息 `ts=1790178997456` 与 `created_at` 时间一致，客户端收到的就是刚发的那条，
没有轮询延迟。

**判定**：**通过**。两个方向都验过：`hello` 握手帧 + `message` 实时帧。
（`go test` 里的 `TestAcceptance2_WebSocketRealtime` 也做了同样的事，
且额外验证了 `Authorization` 头和子协议两种连法，见第 7 节。）

---

## 3. 验收 3：curl 也能收发

### 发

```
$ GID=1001d7f09999; ADMIN=tt_b16ba9b46...; MEMBER=tt_fbc4e015b...

$ curl -s -w '\nHTTP %{http_code}\n' -X POST "http://127.0.0.1:18787/api/$GID/messages" \
    -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' \
    -d '{"body":"curl 发的第一条"}'
{"id":1,"group_id":"1001d7f09999","sender_id":1,"sender":"群主","kind":"human","body":"curl 发的第一条","created_at":"2026-09-23T15:56:29.357Z","ts":1790178989357}

HTTP 201

$ curl -s -w '\nHTTP %{http_code}\n' -X POST "http://127.0.0.1:18787/api/$GID/messages" \
    -H "Authorization: Bearer $MEMBER" -H 'Content-Type: text/plain; charset=utf-8' \
    -d '裸文本也行'
{"id":2,"group_id":"1001d7f09999","sender_id":2,"sender":"小马","kind":"agent","body":"裸文本也行","created_at":"2026-09-23T15:56:29.366Z","ts":1790178989366}

HTTP 201
```

### 拉

```
$ curl -s -w '\nHTTP %{http_code}\n' "http://127.0.0.1:18787/api/$GID/messages?since=0" \
    -H "Authorization: Bearer $MEMBER"
{"count":2,"group_id":"1001d7f09999","has_more":false,"latest":2,"messages":[
 {"id":1,"group_id":"1001d7f09999","sender_id":1,"sender":"群主","kind":"human","body":"curl 发的第一条","created_at":"2026-09-23T15:56:29.357Z","ts":1790178989357},
 {"id":2,"group_id":"1001d7f09999","sender_id":2,"sender":"小马","kind":"agent","body":"裸文本也行","created_at":"2026-09-23T15:56:29.366Z","ts":1790178989366}]}

HTTP 200
```

增量拉（设备轮询要的 `since` 游标）：

```
$ curl -s -w '\nHTTP %{http_code}\n' "http://127.0.0.1:18787/api/$GID/messages?since=1" \
    -H "Authorization: Bearer $MEMBER"
{"count":1,"group_id":"1001d7f09999","has_more":false,"latest":2,"messages":[
 {"id":2,"group_id":"1001d7f09999","sender_id":2,"sender":"小马","kind":"agent","body":"裸文本也行","created_at":"2026-09-23T15:56:29.366Z","ts":1790178989366}]}

HTTP 200
```

**判定**：**通过**。JSON 和裸文本都能发（`201`），`curl` 能整拉也能按 `since` 增量拉（`200`）。
发信人由令牌决定：小马的令牌发出来 `sender=小马`，不能冒名。

---

## 4. 验收 4：无令牌被拒

```
$ curl -s -o /dev/null -w 'HTTP %{http_code}\n' "http://127.0.0.1:18787/api/$GID/messages?since=0"
HTTP 401

$ curl -s -w '\nHTTP %{http_code}\n' -X POST "http://127.0.0.1:18787/api/$GID/messages" \
    -H 'Content-Type: application/json' -d '{"body":"偷发"}'
{"error":"缺少令牌或令牌无效","code":"unauthorized"}

HTTP 401

$ curl -s -w '\nHTTP %{http_code}\n' "http://127.0.0.1:18787/api/$GID/messages?since=0" \
    -H "Authorization: Bearer tt_0000...0000"       # 结构合法但错的令牌
{"error":"缺少令牌或令牌无效","code":"unauthorized"}

HTTP 401

$ curl -s -w '\nHTTP %{http_code}\n' -X POST "http://127.0.0.1:18787/api/$GID/messages" \
    -H "Authorization: Bearer hunter2" -H 'Content-Type: application/json' -d '{"body":"偷发"}'
{"error":"缺少令牌或令牌无效","code":"unauthorized"}

HTTP 401

# WebSocket 握手阶段就被拒：
$ go run ./cmd/wsprobe -url "ws://127.0.0.1:18787/api/$GID/ws" -token "wrong-token"
拨号失败 (HTTP 401): websocket: bad handshake

$ curl -s "http://127.0.0.1:18787/api/$GID/messages?since=0" -H "Authorization: Bearer $ADMIN" | jq '.count'
2
```

**判定**：**通过**。不带令牌 → `401`；带错令牌 → `401`；WebSocket 错令牌握手阶段 → `401`。
被拒的请求没有产生任何消息（消息数仍为 2）。

`go test` 里 `TestAcceptance4_NoTokenRejected` 覆盖更全，12 个子用例全绿：
发/拉/成员列表/加成员/WebSocket、不存在的接口、不存在的群、别的群的令牌（`404`）、
普通成员越权加成员（`403`）。

---

## 5. 验收 5：一条命令起

在一个**空目录**里，用**完全干净的环境变量**（`env -i`）执行**不带任何参数**的命令：

```
$ cd /tmp/m0-acc5 && ls -la
total 13880
drwxrwxr-x 2 ubuntu ubuntu     4096 Sep 23 23:56 .
-rwxrwxr-x 1 ubuntu ubuntu 14166186 Sep 23 23:56 tigertally

$ env -i ./tigertally serve > serve.log 2>&1 &
$ echo $!
1793496

$ curl -s -w '\nHTTP %{http_code}\n' http://127.0.0.1:8787/healthz
{"ok":true,"service":"tigertally","time":"2026-09-23T15:56:21Z","version":"0.1.0"}

HTTP 200

$ cat serve.log
2026/09/23 23:56:21 虎符 tigertally 0.1.0 已启动
2026/09/23 23:56:21 监听   http://127.0.0.1:8787
2026/09/23 23:56:21 数据库 /tmp/m0-acc5/tigertally.db
2026/09/23 23:56:21 建群当前无需口令（任何人可建新群）；要关门请设 --bootstrap-token
2026/09/23 23:56:21 第一个群：curl -X POST http://127.0.0.1:8787/api/groups -H 'Content-Type: application/json' -d '{"name":"我的群","owner":"我"}'
2026/09/23 23:56:21 GET /healthz 200 83B 0s
2026/09/23 23:56:21 GET /healthz 200 83B 0s

$ ps -o pid=,ppid=,args= -p 1793496
1793496 1793491 ./tigertally serve

$ kill -TERM 1793496
$ tail -n 2 serve.log
2026/09/23 23:56:21 收到退出信号，正在收尾…
2026/09/23 23:56:21 已停止

$ ls -la        # 默认在 CWD 建库，权限 0600
-rw-r--r-- 1 ubuntu ubuntu     602 serve.log
-rwxrwxr-x 1 ubuntu ubuntu 14166186 tigertally
-rw------- 1 ubuntu ubuntu   45056 tigertally.db
```

**判定**：**通过**。空目录 + 空环境 + 零参数，一条 `./tigertally serve` 就起来了，
默认监听 `127.0.0.1:8787`、默认库 `./tigertally.db`（`0600`）。
默认绑定 `127.0.0.1`（不是 `0.0.0.0`），符合"默认关闭"的安全取向。

---

## 6. 附加：`go test ./...` 全量输出

命令：

```
$ cd ~/tigertally/server && go test -v -count=1 ./...
```

`acceptance_test.go` 里共 **11 个顶层测试函数**，全部 `PASS`：

| # | 测试函数 | 覆盖 |
|---|---|---|
| 1 | `TestAcceptance2_WebSocketRealtime` | 验收 2：两个 WS，一头发另一头秒收 |
| 2 | `TestWebSocketBacklog` | 连上带 `?since=` 先补历史 |
| 3 | `TestAcceptance1_MessagesSurviveReopen` | 验收 1：关库重开消息仍在 |
| 4 | `TestAcceptance3_HTTPRoundTrip` | 验收 3：纯 HTTP 收发 + since 游标 |
| 5 | `TestPlainTextBody` | 裸文本（设备）也能发 |
| 6 | `TestAcceptance4_NoTokenRejected` | 验收 4：无/错令牌 12 个子用例 |
| 7 | `TestTokensStoredHashedOnly` | 库里无明文令牌 |
| 8 | `TestTokensAreUniqueAndRandom` | 500 把令牌不重复 |
| 9 | `TestGroupAndMemberValidation` | 重名/空值等边界 |
| 10 | `TestBootstrapTokenLocksGroupCreation` | 建群口令 |
| 11 | `TestMessagesAreAppendOnly` | 消息只增不删 |

实际输出（完整）：

```
=== RUN   TestAcceptance2_WebSocketRealtime
    acceptance_test.go:202: A hello: map[group_id:19e91dc76335 kind:human latest:0 member:群主 member_id:1 online:1 role:admin since:0 type:hello]
    acceptance_test.go:203: B hello: map[group_id:19e91dc76335 kind:agent latest:0 member:小马 member_id:2 online:2 role:member since:0 type:hello]
    acceptance_test.go:229: A 实时收到 id=1 sender=群主 body="实时测试：喂——" 用时 2ms
    acceptance_test.go:229: B 实时收到 id=1 sender=群主 body="实时测试：喂——" 用时 2ms
--- PASS: TestAcceptance2_WebSocketRealtime (0.02s)
=== RUN   TestWebSocketBacklog
    acceptance_test.go:252: 补到历史: id=2 body=历史 2
    acceptance_test.go:252: 补到历史: id=3 body=历史 3
--- PASS: TestWebSocketBacklog (0.02s)
=== RUN   TestAcceptance1_MessagesSurviveReopen
    acceptance_test.go:278: 已写入 3 条并关闭数据库（模拟重启）
    acceptance_test.go:294: 重启后仍在: id=1 sender=群主 body="第 1 条"
    acceptance_test.go:294: 重启后仍在: id=2 sender=群主 body="第 2 条"
    acceptance_test.go:294: 重启后仍在: id=3 sender=群主 body="第 3 条"
--- PASS: TestAcceptance1_MessagesSurviveReopen (0.01s)
=== RUN   TestAcceptance3_HTTPRoundTrip
    acceptance_test.go:307: POST 返回 201: {"id":1,"group_id":"ca2fc91246dd","sender_id":1,"sender":"群主","kind":"human","body":"curl 发的","created_at":"2026-09-23T15:57:02.391Z","ts":1790179022391}
    acceptance_test.go:313: GET 返回 200: {"count":1,"group_id":"ca2fc91246dd","has_more":false,"latest":1,"messages":[{"id":1,"group_id":"ca2fc91246dd","sender_id":1,"sender":"群主","kind":"human","body":"curl 发的","created_at":"2026-09-23T15:57:02.391Z","ts":1790179022391}]}
    acceptance_test.go:335: 成员令牌发言，服务端记为 sender=小马 kind=agent
    acceptance_test.go:346: since=1 只返回 id=2：{"count":1,"group_id":"ca2fc91246dd","has_more":false,"latest":2,"messages":[{"id":2,"group_id":"ca2fc91246dd","sender_id":2,"sender":"小马","kind":"agent","body":"第二条","created_at":"2026-09-23T15:57:02.392Z","ts":1790179022392}]}
--- PASS: TestAcceptance3_HTTPRoundTrip (0.02s)
=== RUN   TestPlainTextBody
    acceptance_test.go:361: 裸文本收到: {"id":1,"group_id":"336552ee5f05","sender_id":2,"sender":"小马","kind":"agent","body":"裸文本也能发","created_at":"2026-09-23T15:57:02.41Z","ts":1790179022410}
--- PASS: TestPlainTextBody (0.02s)
=== RUN   TestAcceptance4_NoTokenRejected
=== RUN   TestAcceptance4_NoTokenRejected/拉消息·不带令牌
    acceptance_test.go:398: 拉消息·不带令牌                 → 401  {"error":"缺少令牌或令牌无效","code":"unauthorized"}
=== RUN   TestAcceptance4_NoTokenRejected/拉消息·错令牌
    acceptance_test.go:398: 拉消息·错令牌                  → 401  {"error":"缺少令牌或令牌无效","code":"unauthorized"}
=== RUN   TestAcceptance4_NoTokenRejected/拉消息·乱令牌
    acceptance_test.go:398: 拉消息·乱令牌                  → 401  {"error":"缺少令牌或令牌无效","code":"unauthorized"}
=== RUN   TestAcceptance4_NoTokenRejected/发消息·不带令牌
    acceptance_test.go:398: 发消息·不带令牌                 → 401  {"error":"缺少令牌或令牌无效","code":"unauthorized"}
=== RUN   TestAcceptance4_NoTokenRejected/发消息·错令牌
    acceptance_test.go:398: 发消息·错令牌                  → 401  {"error":"缺少令牌或令牌无效","code":"unauthorized"}
=== RUN   TestAcceptance4_NoTokenRejected/成员列表·不带令牌
    acceptance_test.go:398: 成员列表·不带令牌                → 401  {"error":"缺少令牌或令牌无效","code":"unauthorized"}
=== RUN   TestAcceptance4_NoTokenRejected/加成员·不带令牌
    acceptance_test.go:398: 加成员·不带令牌                 → 401  {"error":"缺少令牌或令牌无效","code":"unauthorized"}
=== RUN   TestAcceptance4_NoTokenRejected/加成员·普通成员令牌
    acceptance_test.go:398: 加成员·普通成员令牌               → 403  {"error":"需要管理员令牌","code":"forbidden"}
=== RUN   TestAcceptance4_NoTokenRejected/WebSocket·不带令牌
    acceptance_test.go:398: WebSocket·不带令牌           → 401  {"error":"缺少令牌或令牌无效","code":"unauthorized"}
=== RUN   TestAcceptance4_NoTokenRejected/别的群的令牌
    acceptance_test.go:398: 别的群的令牌                   → 404  {"error":"群不存在或令牌不属于这个群","code":"not_found"}
=== RUN   TestAcceptance4_NoTokenRejected/不存在的接口·不带令牌
    acceptance_test.go:398: 不存在的接口·不带令牌              → 401  {"error":"缺少令牌","code":"unauthorized"}
=== RUN   TestAcceptance4_NoTokenRejected/不存在的群·不带令牌
    acceptance_test.go:398: 不存在的群·不带令牌               → 401  {"error":"缺少令牌或令牌无效","code":"unauthorized"}
=== NAME  TestAcceptance4_NoTokenRejected
    acceptance_test.go:411: WebSocket 错令牌握手被拒: 401
--- PASS: TestAcceptance4_NoTokenRejected (0.02s)
    --- PASS: TestAcceptance4_NoTokenRejected/拉消息·不带令牌 (0.00s)
    --- PASS: TestAcceptance4_NoTokenRejected/拉消息·错令牌 (0.00s)
    --- PASS: TestAcceptance4_NoTokenRejected/拉消息·乱令牌 (0.00s)
    --- PASS: TestAcceptance4_NoTokenRejected/发消息·不带令牌 (0.00s)
    --- PASS: TestAcceptance4_NoTokenRejected/发消息·错令牌 (0.00s)
    --- PASS: TestAcceptance4_NoTokenRejected/成员列表·不带令牌 (0.00s)
    --- PASS: TestAcceptance4_NoTokenRejected/加成员·不带令牌 (0.00s)
    --- PASS: TestAcceptance4_NoTokenRejected/加成员·普通成员令牌 (0.00s)
    --- PASS: TestAcceptance4_NoTokenRejected/WebSocket·不带令牌 (0.00s)
    --- PASS: TestAcceptance4_NoTokenRejected/别的群的令牌 (0.00s)
    --- PASS: TestAcceptance4_NoTokenRejected/不存在的接口·不带令牌 (0.00s)
    --- PASS: TestAcceptance4_NoTokenRejected/不存在的群·不带令牌 (0.00s)
=== RUN   TestTokensStoredHashedOnly
    acceptance_test.go:451: 库文件 45056 字节；明文令牌不在其中；哈希 84dbb061defff454… 在
--- PASS: TestTokensStoredHashedOnly (0.01s)
=== RUN   TestTokensAreUniqueAndRandom
    acceptance_test.go:473: 500 把令牌互不相同，长度与格式正确
--- PASS: TestTokensAreUniqueAndRandom (0.00s)
=== RUN   TestGroupAndMemberValidation
    acceptance_test.go:484: 重名建群 → 409
    acceptance_test.go:489: 空名建群 → 400
    acceptance_test.go:495: 重名成员 → 409
    acceptance_test.go:501: 空消息 → 400
--- PASS: TestGroupAndMemberValidation (0.02s)
=== RUN   TestBootstrapTokenLocksGroupCreation
    acceptance_test.go:535: 没带 bootstrap 口令建群 → 401
    acceptance_test.go:539: 错 bootstrap 口令建群 → 401
    acceptance_test.go:543: 对 bootstrap 口令建群 → 201
--- PASS: TestBootstrapTokenLocksGroupCreation (0.01s)
=== RUN   TestMessagesAreAppendOnly
    acceptance_test.go:560: DELETE /api/8bfea2ac56f4/messages → 404（没有删除接口）
    acceptance_test.go:560: DELETE /api/8bfea2ac56f4/messages/1 → 404（没有删除接口）
    acceptance_test.go:560: POST /api/8bfea2ac56f4/messages/1/delete → 404（没有删除接口）
    acceptance_test.go:569: 消息仍然 5 条，一条没少
--- PASS: TestMessagesAreAppendOnly (0.02s)
PASS
ok  	github.com/mr-wendao/TigerTally/server	0.176s
```

不带 `-v` 的汇总：

```
$ go test -count=1 ./...
ok  	github.com/mr-wendao/TigerTally/server	0.171s
```

**判定**：**通过**。11/11 顶层测试、12 个 `TestAcceptance4` 子用例全部通过，`exit code 0`。

---

## 7. 逐条对照表

| # | 验收项 | 实测结果 | 判定 |
|---|---|---|---|
| 1 | 重启不丢消息 | 发 3 条 → `SIGTERM` 重启 → `count=3` 全在（另 `kill -9` 也全在） | ✅ 通过 |
| 2 | 浏览器实时收到 | WS 子协议连上，`hello` + 秒收 `message` 帧 | ✅ 通过 |
| 3 | curl 也能收发 | `POST` 201、`GET` 200、`since` 增量拉通 | ✅ 通过 |
| 4 | 无令牌被拒 | 无令牌 401、错令牌 401、WS 握手 401 | ✅ 通过 |
| 5 | 一条命令起 | `env -i ./tigertally serve` 空目录零参数起，`/healthz` 200 | ✅ 通过 |

---

## 附录：WS 探针源码

验收用的临时文件（跑完已从仓库删除，未提交）。要复现第 2 节，
把它放回 `server/cmd/wsprobe/main.go`，再 `go build -o /tmp/wsprobe ./cmd/wsprobe`：

```go
// wsprobe 是 M0 验收用的临时 WebSocket 客户端探针（模拟浏览器）。
//
// 用法: go run ./cmd/wsprobe -url ws://.../api/<群>/ws -token <令牌>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	url := flag.String("url", "", "WebSocket URL")
	token := flag.String("token", "", "成员令牌")
	timeout := flag.Duration("timeout", 10*time.Second, "总超时")
	flag.Parse()

	d := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		Subprotocols:     []string{"bearer." + *token},
	}
	conn, resp, err := d.Dial(*url, nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		fmt.Fprintf(os.Stderr, "拨号失败 (HTTP %d): %v\n", status, err)
		os.Exit(1)
	}
	defer conn.Close()
	fmt.Printf("已连接 subprotocol=%q\n", conn.Subprotocol())

	deadline := time.Now().Add(*timeout)
	for {
		_ = conn.SetReadDeadline(deadline)
		_, raw, err := conn.ReadMessage()
		if err != nil {
			fmt.Fprintf(os.Stderr, "读帧失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("收到帧: %s\n", raw)
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil && m["type"] == "message" {
			return
		}
	}
}
```

---

M0 验收结论：5/5 通过（第 1~5 条全部通过；无未通过项，无未实现项）
