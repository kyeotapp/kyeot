#!/bin/sh
# Called once every 20 seconds (Mac launchd, Linux systemd timer). Writes one line to a per-day file,
# saves a snapshot when the state turns abnormal (not right after waking), and deletes records older than 30 days.
#
# One record line: time \t st= gw= gwmac= ip= gw_ms= wan_ms= dns= tcp= udp= top= pwr= gap= asleep=
# state st: OK, NO_ROUTE (no route), GW_DOWN (router not responding), WAN_DOWN (external down), DNS_DOWN
PATH=/usr/bin:/bin:/usr/sbin:/sbin
OS=$(uname -s)
if [ "$OS" = Darwin ]; then
  DIR="${NETWATCH_DIR:-$HOME/Library/Logs/netwatch}"
else
  DIR="${NETWATCH_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/netwatch}"
fi
STATE="$DIR/.state"
WAN_PROBE="${NETWATCH_WAN:-1.1.1.1}"
mkdir -p "$DIR/snap"

now=$(date +%s)
ts=$(date '+%Y-%m-%d %H:%M:%S')
day=$(date +%Y-%m-%d)

if [ "$OS" = Darwin ]; then
  gw=$(route -n get default 2>/dev/null | awk '/gateway:/{print $2}')
  ifc=$(route -n get default 2>/dev/null | awk '/interface:/{print $2}')
  ip=$(ipconfig getifaddr "${ifc:-en0}" 2>/dev/null)
  rtt() { ping -c1 -t1 "$1" 2>/dev/null | sed -n 's/.*time=\([0-9.]*\).*/\1/p'; }
  pwr=$(pmset -g ps | head -1 | sed -n "s/.*'\(.*\) Power'.*/\1/p")
else
  gw=$(ip route show default 2>/dev/null | awk '{print $3; exit}')
  ifc=$(ip route show default 2>/dev/null | awk '{print $5; exit}')
  ip=$(ip -4 addr show "$ifc" 2>/dev/null | awk '/inet /{sub(/\/.*/, "", $2); print $2; exit}')
  rtt() { ping -c1 -W1 "$1" 2>/dev/null | sed -n 's/.*time=\([0-9.]*\).*/\1/p'; }
  pwr=AC
  for b in /sys/class/power_supply/BAT*/status; do [ -r "$b" ] && [ "$(cat "$b")" = Discharging ] && pwr=Battery; done
fi
gw_ms=$( [ -n "$gw" ] && rtt "$gw" )
gwmac=$(arp -n "$gw" 2>/dev/null | awk '{for (i = 1; i <= NF; i++) if ($i ~ /^([0-9a-f]{1,2}:){5}/) {print $i; exit}}')
wan_ms=$(rtt "$WAN_PROBE")

# Ask for a nonexistent name. NXDOMAIN also means DNS answered, so it is normal
dns=$(dig +time=2 +tries=1 "nw$now$$.example.com" 2>/dev/null | sed -n 's/.*status: \([A-Z]*\).*/\1/p')

if [ -z "$gw" ]; then st=NO_ROUTE
elif [ -z "$gw_ms" ]; then st=GW_DOWN
elif [ -z "$wan_ms" ] && [ -z "$dns" ]; then st=WAN_DOWN
elif [ -z "$dns" ]; then st=DNS_DOWN
else st=OK; fi

socks=$(lsof -nP -i 2>/dev/null | awk 'NR>1')
tcp=$(printf '%s\n' "$socks" | grep -c ' TCP ')
udp=$(printf '%s\n' "$socks" | grep -c ' UDP ')
top=$(printf '%s\n' "$socks" | awk 'NF{print $1}' | sort | uniq -c | sort -rn | head -5 | awk '{printf "%s%s=%s", (NR>1?",":""), $2, $1}')

prev_ts=0; prev_st=OK
[ -f "$STATE" ] && read -r prev_ts prev_st < "$STATE"
gap=$(( now - prev_ts )); [ "$prev_ts" -eq 0 ] && gap=0
asleep=0; [ "$gap" -gt 120 ] && asleep=1 # with a 20-second interval, a gap over 2 minutes means it was asleep

printf '%s\tst=%s\tgw=%s\tgwmac=%s\tip=%s\tgw_ms=%s\twan_ms=%s\tdns=%s\ttcp=%s\tudp=%s\ttop=%s\tpwr=%s\tgap=%s\tasleep=%s\n' \
  "$ts" "$st" "${gw:--}" "${gwmac:--}" "${ip:--}" "${gw_ms:--}" "${wan_ms:--}" "${dns:--}" \
  "$tcp" "$udp" "$top" "${pwr:--}" "$gap" "$asleep" >> "$DIR/$day.tsv"

if [ "$st" != OK ] && [ "$st" != "$prev_st" ] && [ "$asleep" = 0 ]; then
  f="$DIR/snap/$(date +%Y%m%d-%H%M%S)-$st.txt"
  {
    echo "### $ts $st gw=$gw ($gwmac) ip=$ip"
    echo "### 직전 기록"; tail -n 30 "$DIR/$day.tsv"
    echo "### 열린 소켓"; printf '%s\n' "$socks"
    echo "### 프로세스별 소켓 수"; printf '%s\n' "$socks" | awk 'NF{print $1}' | sort | uniq -c | sort -rn
    echo "### 원격 주소별 연결 수 상위"; printf '%s\n' "$socks" | awk '{split($9,a,"->"); if(a[2]!="")print a[2]}' | sed 's/:[0-9]*$//' | sort | uniq -c | sort -rn | head -30
    echo "### CPU 상위"; ps -axo pid,etime,%cpu,command 2>/dev/null | sort -k3 -rn | head -25 | cut -c1-200
    if [ "$OS" = Darwin ]; then
      echo "### route"; netstat -rn -f inet | head -30
      echo "### dns"; scutil --dns | head -20
      echo "### 전원"; pmset -g ps; pmset -g assertions | head -30
    else
      echo "### route"; ip route
      echo "### dns"; cat /etc/resolv.conf
    fi
    echo "### arp"; arp -an
    echo "### 공유기 첫 화면 (http://$gw/)"; curl -s -m 3 -i "http://$gw/" | head -40
  } > "$f" 2>&1
  # Hook that reads the router admin page. Created only if the user supplies the admin address and key
  hook="$HOME/.config/netwatch/router-snapshot.sh"
  [ -x "$hook" ] && "$hook" "$gw" >> "$f" 2>&1
fi

echo "$now $st" > "$STATE"
find "$DIR" -name '*.tsv' -mtime +30 -delete 2>/dev/null
find "$DIR/snap" -name '*.txt' -mtime +30 -delete 2>/dev/null
exit 0
