"""Uploads the netwatch records as the netwatch data bundle of the kyeot network tab.

## Target
Today's records written by netwatch.sh (Mac ~/Library/Logs/netwatch, Linux ~/.local/state/netwatch, overridable with NETWATCH_DIR).

## Rules
State values are used as-is from netwatch.sh: OK, NO_ROUTE, GW_DOWN, WAN_DOWN, DNS_DOWN.
An outage is an abnormal stretch in today's records. Samples taken while asleep (asleep=1), and a NO_ROUTE right after waking on battery before Wi-Fi reconnects, are boundaries of a stretch, not outages.
Under 60 seconds is shown only as a "순간 끊김" (blip) and not counted.
The response-time flow is the latest 60 samples (about 20 minutes). DNS is normal as long as it answers (it asks for a nonexistent name, so NXDOMAIN is also normal).

## Notifications
Sends no notifications. Prints nothing on success; on failure prints one line with the reason.

## Does not
Does not modify or delete record files. The router address and MAC are not put in the bundle.
"""
import json, os, platform, sys, urllib.request
from datetime import datetime

DEFAULT_DIR = "~/Library/Logs/netwatch" if platform.system() == "Darwin" else os.path.join(os.environ.get("XDG_STATE_HOME", "~/.local/state"), "netwatch")
DIR = os.path.expanduser(os.environ.get("NETWATCH_DIR", DEFAULT_DIR))
DECK = os.environ.get("DECK", "http://127.0.0.1:7701")
FLOW_N = 60
RECENT_INCIDENTS = 8
MIN_INCIDENT_SEC = 60

LABEL = {"OK": "정상", "NO_ROUTE": "경로 없음", "GW_DOWN": "공유기 응답 없음", "WAN_DOWN": "외부 연결 끊김", "DNS_DOWN": "DNS 실패"}
LAMP = {"OK": "ok", "DNS_DOWN": "warn"}  # all other abnormal states are danger


def parse_line(line):
    parts = line.rstrip("\n").split("\t")
    if len(parts) < 2:
        return None
    rec = {"ts": parts[0]}
    for p in parts[1:]:
        if "=" in p:
            k, v = p.split("=", 1)
            rec[k] = v
    return rec


def num(v):
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


def asleep(r):
    if r.get("asleep") in ("0", "1"):
        return r["asleep"] == "1"
    g = num(r.get("gap"))
    return g is not None and g > 120


def dur_text(sec):
    sec = int(sec)
    if sec < 60:
        return f"{sec}초"
    if sec < 3600:
        return f"{sec // 60}분"
    return f"{sec // 3600}시간 {sec % 3600 // 60}분"


def read_today():
    path = os.path.join(DIR, datetime.now().strftime("%Y-%m-%d") + ".tsv")
    if not os.path.exists(path):
        return []
    with open(path, encoding="utf-8", errors="replace") as f:
        return [r for r in map(parse_line, f) if r]


def outage_runs(recs):
    runs, cur, prev_dozing = [], None, False
    for r in recs:
        dozing = asleep(r) or (prev_dozing and r.get("st") == "NO_ROUTE" and r.get("pwr") == "Battery")
        prev_dozing = dozing
        if dozing:
            if cur:
                runs.append(cur)
                cur = None
            continue
        if r.get("st", "OK") != "OK":
            if cur is None:
                cur = {"start": r["ts"], "end": r["ts"], "states": {}}
            cur["end"] = r["ts"]
            cur["states"][r["st"]] = cur["states"].get(r["st"], 0) + 1
        elif cur:
            cur["recovered"] = r["ts"]
            runs.append(cur)
            cur = None
    if cur:
        runs.append(cur)  # outage still ongoing
    return runs


def incidents_of(runs):
    out, counted_n = [], 0
    for run in reversed(runs):
        t0 = datetime.strptime(run["start"], "%Y-%m-%d %H:%M:%S")
        t1 = datetime.strptime(run.get("recovered", run["end"]), "%Y-%m-%d %H:%M:%S")
        sec = (t1 - t0).total_seconds()
        main = max(run["states"], key=run["states"].get)
        counted = sec >= MIN_INCIDENT_SEC
        counted_n += counted
        parts = ", ".join(f"{LABEL.get(k, k)} {v}" for k, v in sorted(run["states"].items(), key=lambda x: -x[1]))
        item = {
            "time": t0.strftime("%m/%d %H:%M"),
            "title": LABEL.get(main, main) if counted else "순간 끊김",
            "subtitle": f"{t0:%H:%M}–{t1:%H:%M} · {dur_text(sec)} · {parts}" + ("" if "recovered" in run else " · 진행 중"),
        }
        item["tone"] = ("warn" if main == "DNS_DOWN" else "danger") if counted else "info"
        out.append(item)
    return out[:RECENT_INCIDENTS], counted_n


def build(recs):
    last = recs[-1] if recs else {}
    st = last.get("st", "")
    flow = recs[-FLOW_N:]
    incidents, today_n = incidents_of(outage_runs(recs))
    tcp = num(last.get("tcp"))
    dns = last.get("dns", "")
    return {
        "now": {
            "state": LABEL.get(st, st or "기록 없음"),
            "color": LAMP.get(st, "danger" if st else "neutral"),
            "checked": last.get("ts", "—"),
            "gw_ms": num(last.get("gw_ms")),
            "wan_ms": num(last.get("wan_ms")),
            "tcp": int(tcp) if tcp is not None else None,
        },
        "status": [
            {"label": "지금", "state": LAMP.get(st, "danger" if st else "off"), "note": f"{LABEL.get(st, st or '기록 없음')} · {last.get('ts', '—')[11:]}"},
            {"label": "DNS", "state": "ok" if dns not in ("", "-") else "warn", "note": "응답함" if dns not in ("", "-") else "응답 없음"},
            {"label": "오늘 장애 기록", "state": "ok" if today_n == 0 else "warn", "note": f"{today_n}건"},
        ],
        "flow": {
            "labels": [r["ts"][11:19] for r in flow],
            "series": [
                {"name": "공유기", "values": [num(r.get("gw_ms")) for r in flow], "color": "blue"},
                {"name": "외부", "values": [num(r.get("wan_ms")) for r in flow], "color": "purple"},
            ],
        },
        "incidents": incidents,
    }


def main():
    body = json.dumps({"name": "netwatch", "data": build(read_today())}, ensure_ascii=False).encode()
    req = urllib.request.Request(DECK + "/data/put", data=body, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            ack = json.load(r)
    except Exception as e:  # on 400 the body has a reason
        msg = getattr(e, "read", lambda: b"")().decode(errors="replace") or str(e)
        print(f"netwatch 묶음을 올리지 못했다: {msg[:300]}")
        return 1
    if not ack.get("ok"):
        print(f"netwatch 묶음이 거절됐다: {ack.get('reason', '')}")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
