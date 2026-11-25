#!/usr/bin/env bash
# calls.sh: what a Say log says of its calls, briefly: for the last call (or
# the call number N from the end), who, which version, the path (YGG/P2P),
# the round trip, the voice's delay, the video's rates, losses, and how it
# ended. Reads only the log's tail (big logs).
#
#   bash scripts/calls.sh bin/server-server.log      # the last call
#   bash scripts/calls.sh bin/server-server.log 2    # the one before it
#   TAIL=50000000 bash scripts/calls.sh LOG          # read more of the log
set -u
log=${1:?usage: calls.sh LOG [N]}
n=${2:-1}
tail -c "${TAIL:-20000000}" "$log" | python3 -c '
import re, sys, statistics as st
n = int(sys.argv[1])
lines = sys.stdin.read().splitlines()
starts = [i for i, l in enumerate(lines) if re.search(r"\[p2p\] (call from|dial established to)|\[media\] started", l)]
# a call: from its first start line to the next call (or the end)
calls, last = [], -10**9
for i in starts:
    if i - last > 50:
        calls.append(i)
    last = i
if len(calls) < n:
    print("no call %d in the log tail (%d found)" % (n, len(calls))); sys.exit()
a = calls[-n]
b = calls[-n + 1] if n > 1 else len(lines)
for j in range(a, b):  # the call ends with its Say at the latest
    if "shutting down" in lines[j] or "config path:" in lines[j]:
        b = j + 1
        break
c = lines[max(0, a - 20):b]
def t(l): return l[11:19] if len(l) > 19 else "?"
def find(p):
    for l in c:
        if re.search(p, l): return l
who = find(r"\[p2p\] (call from|dial established to|dialing)")
ver = find(r"peer runs Say")
print("начало   ", t(c[20] if len(c) > 20 else c[0]), (who or "").split("] ", 1)[-1][:90])
if ver: print("версия   ", ver.split("peer runs ", 1)[-1][:80])
paths = [t(l) + " " + l.split("goes ", 1)[-1] for l in c if "the call goes" in l]
if paths: print("путь     ", ", ".join(paths[:4]))
rtt = [float(m.group(1)) * (1000 if m.group(2) == "s" else 1) for l in c for m in [re.search(r"RTT=([\d.]+)(ms|s)", l)] if m]
if rtt: print("RTT мс    медиана %.0f, p90 %.0f, макс %.0f (%d замеров)" % (st.median(rtt), sorted(rtt)[int(len(rtt) * .9)], max(rtt), len(rtt)))
wait = [int(m.group(2)) for l in c for m in [re.search(r"packets wait (\d+)\.\.(\d+) ms", l)] if m]
if wait: print("голос мс  ожидание макс: медиана %d, макс %d" % (st.median(wait), max(wait)))
for d in ("rx", "tx"):
    r = [float(m.group(1)) for l in c for m in [re.search(r"\[video/udp\] " + d + r": \d+ pkts, ([\d.]+) kB/s", l)] if m]
    if r: print("видео %s  кБ/с медиана %.1f, макс %.1f" % (d, st.median(r), max(r)))
loss = [l for l in c if re.search(r"loss [1-9]\d*%|lost|NoCarrier|failed|error", l, re.I) and "0 dropped" not in l]
if loss:
    print("потери/ошибки (%d), первые:" % len(loss))
    for l in loss[:5]: print("   ", t(l), l.split("] ", 1)[-1][:100])
end = find(r"connection to .* lost|the call with .* is over|hung up|call .* lost|control stream .* ended") or find(r"shutting down")
print("конец    ", t(end) if end else "?", (end or "не видно в этом куске")[27:].strip()[:90])
' "$n"
