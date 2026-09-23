# 虎符 tigertally 服务端（M0）

自托管群聊消息服务。Go + SQLite，**编译出来是单个可执行文件**，
不依赖 Node/Python 运行时；一条命令就能起。

设计文档：`../docs/DESIGN-M0.md` ｜ 验收结果：`../docs/M0-RESULT.md`

---

## 1. 起服务（一条命令）

```bash
cd server
go build -o tigertally .      # 编译一次，得到一个自包含的可执行文件
./tigertally serve            # 起服务
```

没带任何参数时的默认值：

| 项 | 默认值 |
|---|---|
| 监听地址 | `127.0.0.1:8787`（**只绑本机**，不是 `0.0.0.0`） |
| 数据库 | `./tigertally.db`（当前目录，权限 `0600`） |
| 建群口令 | 无（见下文"关门"） |

想换地址/库文件：

```bash
./tigertally serve --addr 0.0.0.0:8787 --db /var/lib/tigertally/data.db
```

也可以全部用环境变量（命令行优先）：

| 命令行 | 环境变量 | 说明 |
|---|---|---|
| `--addr` | `TIGERTALLY_ADDR` | 监听地址 |
| `--db` | `TIGERTALLY_DB` | SQLite 文件路径 |
| `--bootstrap-token` | `TIGERTALLY_BOOTSTRAP_TOKEN` | 建群口令，设了就没人能随便建群 |
| `--allow-origin`（可重复） | `TIGERTALLY_ALLOW_ORIGINS`（逗号分隔） | 额外放行的跨域来源；`*` 表示不检查 |

存活检查：

```bash
curl -s http://127.0.0.1:8787/healthz
# {"ok":true,"service":"tigertally","time":"...","version":"0.1.0"}
```

停止：`Ctrl-C` 或 `kill -TERM <pid>`，会优雅收尾（合并 WAL、关库）。

### 让公网/局域网访问，并且关门

```bash
# 绑所有网卡，并给"建群"上口令，防止陌生人自建群
./tigertally serve --addr 0.0.0.0:8787 --db /var/lib/tigertally/data.db \
  --bootstrap-token "$(openssl rand -hex 16)"
```

> 建议放在反向代理/防火墙后面。**无令牌的请求一律 401**，
> 但 `--addr 0.0.0.0` 仍然意味着端口对网络开放，请自行控制边界。

---

## 2. 生成令牌 / 加成员

### 2.1 建群 → 拿到管理员令牌

```bash
curl -s -X POST http://127.0.0.1:8787/api/groups \
  -H 'Content-Type: application/json' \
  -d '{"name":"我的群","owner":"我"}'
```

返回（**`token` 明文只出现这一次**，服务端只存哈希，丢了只能重新加成员）：

```json
{
  "group_id": "1001d7f09999",
  "group_name": "我的群",
  "member_id": 1,
  "member_name": "我",
  "role": "admin",
  "token": "tt_b16ba9b4...3fd8dca",
  "created_at": "2026-09-23T15:56:26.576Z"
}
```

把这两样存起来，后面都用得上（`group_id` 也可以直接用群名，接口认两种）：

```bash
export GID=1001d7f09999
export ADMIN=tt_b16ba9b4...3fd8dca
```

如果起服务时设了 `--bootstrap-token`，建群请求要带上它：

```bash
curl -s -X POST http://127.0.0.1:8787/api/groups \
  -H "Authorization: Bearer $BOOTSTRAP" -H 'Content-Type: application/json' \
  -d '{"name":"我的群","owner":"我"}'
```

### 2.2 加成员 → 每个成员一把独立钥匙

`kind` 取 `human` / `agent` / `device`；**需要管理员令牌**。

```bash
# 给浏览器里的人
curl -s -X POST "http://127.0.0.1:8787/api/$GID/members" \
  -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' \
  -d '{"name":"小明","kind":"human"}'

# 给 ESP32 之类的设备
curl -s -X POST "http://127.0.0.1:8787/api/$GID/members" \
  -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' \
  -d '{"name":"客厅灯","kind":"device"}'
```

返回里同样只有这一次能看到成员自己的 `token`：

```json
{"id":2,"group_id":"1001d7f09999","name":"小明","kind":"human","role":"member",
 "token":"tt_fbc4e015...7d1b98d","created_at":"..."}
```

看名册（任何成员都能看，**不会返回任何令牌**）：

```bash
curl -s "http://127.0.0.1:8787/api/$GID/members" -H "Authorization: Bearer $ADMIN"
```

> 群主（建群那个人）是 `admin`。目前**没有踢人/改权限接口**——
> 令牌泄露的处理方式是二期的事（见 `../PROJECT.md` M2）。

---

## 3. 用 curl 收发消息

所有收发接口都要 `Authorization: Bearer <成员令牌>`。

### 发一条（JSON）

```bash
curl -s -X POST "http://127.0.0.1:8787/api/$GID/messages" \
  -H "Authorization: Bearer $ADMIN" \
  -H 'Content-Type: application/json' \
  -d '{"body":"大家好"}'
```

### 发一条（裸文本，给不会 JSON 的单片机）

```bash
curl -s -X POST "http://127.0.0.1:8787/api/$GID/messages" \
  -H "Authorization: Bearer $DEVICE_TOKEN" \
  -H 'Content-Type: text/plain; charset=utf-8' \
  --data-binary '温度 23.5℃'
```

**发信人由令牌决定，请求体说了不算**——想冒名得先有别人的钥匙。

### 拉消息

```bash
# 从头拉（最多默认 200 条）
curl -s "http://127.0.0.1:8787/api/$GID/messages?since=0" \
  -H "Authorization: Bearer $ADMIN"

# 设备轮询：只拉 id > 10 的，最多 50 条
curl -s "http://127.0.0.1:8787/api/$GID/messages?since=10&limit=50" \
  -H "Authorization: Bearer $DEVICE_TOKEN"
```

返回：

```json
{
  "group_id": "1001d7f09999",
  "messages": [
    {"id":1,"sender_id":1,"sender":"我","kind":"human","body":"大家好",
     "created_at":"2026-09-23T15:56:29.357Z","ts":1790178989357}
  ],
  "count": 1,
  "latest": 1,
  "has_more": false
}
```

轮询标准写法：记住上次的 `latest`，下次把它当 `since` 传回来；
没有新消息时 `latest` 等于传进去的 `since`，不会漏也不会重复。

```bash
since=0
while :; do
  resp=$(curl -s "http://127.0.0.1:8787/api/$GID/messages?since=$since" \
           -H "Authorization: Bearer $DEVICE_TOKEN")
  echo "$resp" | jq -c '.messages[]'
  since=$(echo "$resp" | jq -r .latest)
  sleep 2
done
```

### 响应状态码

| 码 | 含义 |
|---|---|
| `201` | 消息/群/成员创建成功 |
| `200` | 拉取成功 |
| `400` | 请求体或参数不合法（空消息、空群名、`since` 非数字……） |
| `401` | 缺少令牌或令牌无效 |
| `403` | 令牌有效但不是管理员（加成员时） |
| `404` | 群不存在，或令牌不属于这个群 |
| `409` | 冲突（群名/成员名重复） |

---

## 4. 用 WebSocket 接（浏览器实时通道）

WebSocket 是**只出不进**：订阅实时消息用 WS，发消息还是走上面的 HTTP POST。
所以"一条 HTTP 就是一条消息"这条信条在实时通道上也成立。

**校验方式二选一：**

- 脚本 / 设备：`Authorization: Bearer <token>` 头。
- 浏览器：`new WebSocket(url, ["bearer." + token])` —— 浏览器不能给 WS 设
  `Authorization` 头，这是唯一合法的带头方式。服务端会把该子协议原样回一个。

> 令牌不要放进 URL 查询串（`?token=`）走公网：查询串会进各种 access log。
> 它只是内网/调试兜底。

### 浏览器示例

```html
<!doctype html>
<meta charset="utf-8">
<title>虎符</title>
<pre id="log"></pre>
<script>
const GID   = "1001d7f09999";
const TOKEN = "tt_换成成员令牌";
const since = 0;                       // 想补历史就填上次看到的最后 id
const ws = new WebSocket(
  `ws://127.0.0.1:8787/api/${GID}/ws?since=${since}`,
  ["bearer." + TOKEN]                  // ← 令牌走子协议
);

const log = (s) => document.getElementById("log").textContent += s + "\n";

ws.onopen    = () => log("已连接");
ws.onmessage = (ev) => {
  const m = JSON.parse(ev.data);
  if (m.type === "hello")   log(`你好 ${m.member}（在线 ${m.online}）`);
  if (m.type === "message") log(`[${m.sender}] ${m.body}`);
};
ws.onclose   = () => log("断开，稍后重连");
</script>
```

### 命令行示例

```bash
# websocat（推荐）
websocat -H="Authorization: Bearer $ADMIN" \
  "ws://127.0.0.1:8787/api/$GID/ws?since=0"

# 或 wscat（走子协议）
wscat -c "ws://127.0.0.1:8787/api/$GID/ws" -s "bearer.$ADMIN"
```

### 服务端会推两种帧

连上先来一个 `hello`：

```json
{"type":"hello","group_id":"1001d7f09999","member_id":2,"member":"小明",
 "kind":"human","role":"member","since":0,"latest":2,"online":1}
```

之后每来一条消息推一个 `message`：

```json
{"type":"message","id":3,"group_id":"1001d7f09999","sender_id":1,
 "sender":"我","kind":"human","body":"大家好","created_at":"...","ts":1790178997456}
```

- `?since=<id>` 可选：连上后先把 `id > since` 的历史补给你，再进实时流。
  **先订阅、后补历史**，所以宁可重复、绝不漏；客户端按 `id` 去重即可。
- 掉线是常态：重连时带上 `?since=<本地最后一条 id>` 就能补齐，消息永远不会丢。
- 服务端每 30s 发一次 ping 探活；太慢、缓冲区塞满的客户端会被主动断开，
  由它自己重连补齐（不会拖慢整个群）。

---

## 5. 安全模型（一句话版）

1. **每个成员一把独立令牌**（`tt_` + 64 位十六进制 = 256 位随机），泄露一把只影响一人。
2. **令牌只存 SHA-256 哈希**，数据库文件里没有明文；明文只在创建时返回一次。
3. **无令牌一律拒绝**：`/api/` 下没有"不验令牌就能拿到响应"的路由，
   WebSocket 在握手阶段就被拦。
4. **默认只绑 `127.0.0.1`**，默认拒绝而不是默认开放。
5. 数据库文件权限 `0600`；`WAL + synchronous=FULL`，进程被 `kill -9` 也不丢已提交消息。

---

## 6. 接口速查

```
GET  /healthz                        存活检查（无需令牌）
POST /api/groups                     建群 → 返回管理员令牌

POST /api/{群}/messages              发消息        （Bearer 成员令牌）{"body":"..."} 或裸文本
GET  /api/{群}/messages?since=&limit= 拉消息        （Bearer 成员令牌）
GET  /api/{群}/ws?since=            实时订阅       （Bearer 或子协议 bearer.<token>）

POST /api/{群}/members               加成员        （Bearer 管理员令牌）{"name":"...","kind":"human|agent|device"}
GET  /api/{群}/members               成员列表      （Bearer 成员令牌）
```

`{群}` 处填 `group_id` 或群名都可以。
