#!/usr/bin/env bash
# ============================================================================
# proxy_status.sh - 查看代理节点实时流量（trafficd 统计）
#
# 用法：
#   ./proxy_status.sh --host <VPS地址> [--traffic-port 9386] [--traffic-token <token>]
# ============================================================================
set -euo pipefail

HOST=""
TRAFFIC_PORT="9386"
TRAFFIC_TOKEN=""

usage() {
  sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host) HOST="$2"; shift 2;;
    --traffic-port) TRAFFIC_PORT="$2"; shift 2;;
    --traffic-token) TRAFFIC_TOKEN="$2"; shift 2;;
    -h|--help) usage;;
    *) echo "未知参数: $1"; usage;;
  esac
done

[[ -n "${HOST:-}" ]] || { echo "缺少 --host"; usage; }
[[ -n "$TRAFFIC_TOKEN" ]] || { echo "缺少 --traffic-token"; usage; }

fmt_bytes() {
  local n=$1
  if (( n < 1024 )); then echo "${n} B"
  elif (( n < 1048576 )); then awk "BEGIN{printf \"%.1f KB\", $n/1024}"
  elif (( n < 1073741824 )); then awk "BEGIN{printf \"%.1f MB\", $n/1048576}"
  else awk "BEGIN{printf \"%.2f GB\", $n/1073741824}"; fi
}

fmt_uptime() {
  local s=$1 d h m
  d=$((s/86400)); h=$(((s%86400)/3600)); m=$(((s%3600)/60))
  echo "${d}天${h}时${m}分"
}

echo ">>> 查询 ${HOST}:${TRAFFIC_PORT} 流量统计 ..."
json="$(curl -s --max-time 8 "http://${HOST}:${TRAFFIC_PORT}/stats?token=${TRAFFIC_TOKEN}")"
if [[ -z "$json" ]]; then
  echo "查询失败（无法连接或 token 错误）"; exit 1
fi

hostname="$(echo "$json" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("hostname",""))' 2>/dev/null)"
ok="$(echo "$json" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("ok",False))' 2>/dev/null)"
[[ "$ok" == "True" ]] || { echo "trafficd 返回: $json"; exit 1; }

read_rx="$(echo "$json" | python3 -c 'import sys,json;print(json.load(sys.stdin)["rx_bytes"])')"
read_tx="$(echo "$json" | python3 -c 'import sys,json;print(json.load(sys.stdin)["tx_bytes"])')"
rate_rx="$(echo "$json" | python3 -c 'import sys,json;print(json.load(sys.stdin)["rx_rate"])')"
rate_tx="$(echo "$json" | python3 -c 'import sys,json;print(json.load(sys.stdin)["tx_rate"])')"
conns="$(echo "$json" | python3 -c 'import sys,json;print(json.load(sys.stdin)["conns"])')"
uptime="$(echo "$json" | python3 -c 'import sys,json;print(json.load(sys.stdin)["uptime"])')"
time="$(echo "$json" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("time",""))')"

echo "  节点      : $hostname ($HOST)"
echo "  时间      : $time"
echo "  运行时长  : $(fmt_uptime "$uptime")"
echo "  当前连接  : $conns"
echo "  累计接收 ↓: $(fmt_bytes "$read_rx")"
echo "  累计发送 ↑: $(fmt_bytes "$read_tx")"
echo "  接收速率 ↓: $(fmt_bytes "$rate_rx")/s"
echo "  发送速率 ↑: $(fmt_bytes "$rate_tx")/s"