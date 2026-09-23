#!/usr/bin/env python3
# 导出最近的对话 -> chat.js / chat.json，供工作群页面读取
#
# v2 (2026-09-23)：不再写死会话 id。
#   1. 自动取「最近 N 天内有动静」的会话，合并成一条时间线
#   2. 同时把机器名册(hosts.json)导出成 window.ROSTER —— 加员工只改名册，页面自动多一行
#
# 为什么必须本机跑：minis-sessions-cli 只在手机侧有，服务器侧没有。
# 所以刷新时机 = 我每次动手时顺手跑一次（0.5 秒）。
import subprocess, json, re, os, time

HERE    = os.path.dirname(os.path.abspath(__file__))
OUT_JS  = os.path.join(HERE, "chat.js")
OUT_JSON= os.path.join(HERE, "chat.json")
HOSTS   = "/var/minis/shared/fleet/hosts.json"

DAYS      = 3     # 只导最近几天的会话
MAX_SESS  = 8     # 最多合并几个会话
PER_SESS  = 40    # 每个会话取最近多少条
TXT_CAP   = 600   # 单条文本上限
TOTAL_CAP = 320   # 总条数上限（防止页面过大）


def run(args):
    """调 minis-sessions-cli，容忍它输出前缀噪音"""
    r = subprocess.run(["minis-sessions-cli"] + args,
                       capture_output=True, text=True, timeout=180)
    raw = r.stdout
    i = raw.find("{")
    if i < 0:
        return {}
    raw = raw[i:]
    raw = re.sub(r",(\s*[}\]])", r"\1", raw)   # 去掉尾逗号
    try:
        return json.loads(raw)
    except Exception:
        return {}


def to_ms(s):
    """'2026-09-23 05:47' -> 毫秒时间戳（本地时区）"""
    m = re.match(r"(\d{4})-(\d{2})-(\d{2})\s+(\d{2}):(\d{2})", s or "")
    if not m:
        return 0
    y, mo, d, h, mi = (int(x) for x in m.groups())
    return int(time.mktime((y, mo, d, h, mi, 0, 0, 0, -1))) * 1000


def pick_sessions():
    d = run(["list", "--limit", "40"])
    sess = (d.get("data") or {}).get("sessions") or []
    cutoff = time.time() - DAYS * 86400
    out = []
    for s in sess:
        t = to_ms(s.get("last_active")) / 1000.0
        if t and t < cutoff:
            continue
        out.append(s)
        if len(out) >= MAX_SESS:
            break
    return out


def fetch_msgs(sid):
    """取某会话最近 PER_SESS 条"""
    d = run(["messages", "--id", sid, "--limit", "1"])
    total = (d.get("data") or {}).get("total", 0)
    if not total:
        return []
    off = max(0, total - PER_SESS)
    d = run(["messages", "--id", sid, "--offset", str(off),
             "--limit", str(PER_SESS), "--full"])
    return (d.get("data") or {}).get("messages") or []


def load_roster():
    """机器名册：固定成员(老板/职业经理) + hosts.json 里的工人"""
    roster = [
        {"id": "boss", "name": "老板",   "kind": "human", "meta": ""},
        {"id": "mgr",  "name": "职业经理", "kind": "agent", "meta": "本机 · 出方案"},
    ]
    try:
        with open(HOSTS, encoding="utf-8") as f:
            for h in json.load(f).get("hosts", []):
                roster.append({
                    "id": "w-" + h.get("alias", "?"),
                    "name": h.get("alias", "?"),
                    "kind": "worker",
                    "meta": h.get("note", "") or h.get("ip", ""),
                })
    except Exception as e:
        roster.append({"id": "w-?", "name": "小马", "kind": "worker",
                       "meta": "名册读取失败: %s" % e})
    return roster


def main():
    sessions = pick_sessions()
    if not sessions:
        print("⚠️ 最近没有活跃会话")
        return

    seen, out = set(), []
    for s in sessions:
        sid = s.get("session_id")
        title = (s.get("title") or "").strip()
        # 有些会话的 title 是原始 JSON 串或 None，别让它露到页面上
        if not title or title.startswith("{") or title == "None":
            title = (s.get("preview") or "未命名").strip()[:18]
        title = title[:18]
        for m in fetch_msgs(sid):
            mid = m.get("message_id")
            if not mid or mid in seen:
                continue
            seen.add(mid)
            txt = (m.get("text") or m.get("content") or "").strip()
            if not txt:
                continue
            role = m.get("role", "")
            if txt.startswith("[Tool result") or txt.startswith("[工具结果]"):
                role = "tool"
            if role not in ("user", "assistant", "tool"):
                continue
            ms = to_ms(m.get("created_at"))
            if not ms:
                continue
            out.append({
                "role": role,
                "text": txt[:TXT_CAP] + ("…" if len(txt) > TXT_CAP else ""),
                "ms": ms,
                "ts": time.strftime("%m-%d %H:%M", time.localtime(ms / 1000)),
                "sess": title[:18],
            })

    out.sort(key=lambda x: x["ms"])
    if len(out) > TOTAL_CAP:
        out = out[-TOTAL_CAP:]

    roster = load_roster()
    with open(OUT_JSON, "w", encoding="utf-8") as f:
        json.dump({"chat": out, "roster": roster}, f, ensure_ascii=False, indent=1)
    with open(OUT_JS, "w", encoding="utf-8") as f:
        f.write("window.CHAT=")
        json.dump(out, f, ensure_ascii=False)
        f.write(";\nwindow.ROSTER=")
        json.dump(roster, f, ensure_ascii=False)
        f.write(";\n")

    print("会话 %d 个 -> 合并 %d 条" % (len(sessions), len(out)))
    print("时间跨度: %s  ~  %s" % (out[0]["ts"], out[-1]["ts"]))
    print("名册 %d 人: %s" % (len(roster), ", ".join(r["name"] for r in roster)))
    print("最近 3 条:")
    for x in out[-3:]:
        print("  ", x["ts"], x["role"][:9], "[" + x["sess"] + "]", x["text"][:45].replace("\n", " "))


if __name__ == "__main__":
    main()
