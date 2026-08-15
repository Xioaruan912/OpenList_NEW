#!/usr/bin/env bash
# ============================================================================
# proxy_deploy.sh - 一键部署 115 防风控代理（在你的其他 VPS 上运行）
#
# 统一使用 gost v2 单二进制（多协议），并附带 trafficd 实时流量统计服务：
#   - http  :  gost 起 http 代理（OpenList 直接使用）
#   - ss    :  gost 起 ss 代理 + 自动补一个 http 端口（OpenList 只能消费 http/socks5，
#              故 ss 模式下会额外开放一个 http 端口供 OpenList 填写）
#   - socks5:  gost 起 socks5 代理（OpenList 直接使用）
#
# 用法：
#   ./proxy_deploy.sh --host <VPS地址> [--ssh-user root] [--ssh-port 22] \
#       --type http|ss|socks5 --port <代理端口> --password <密码> \
#       [--http-port <OpenList用http端口>] [--traffic-port 9386] \
#       [--traffic-token <token>] [--admin-ip 0.0.0.0/0] [--dev eth0]
#
# 二进制来源（优先级）：
#   1. scripts/proxy/bin/ 下预置的 gost-linux-<arch>（离线场景）
#   2. VPS 上从 GitHub 下载 gost v2.12.0 官方 release
# ============================================================================
set -euo pipefail

GOST_VER="2.12.0"
GOST_REPO="https://github.com/ginuerzh/gost/releases/download/v${GOST_VER}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOCAL_BIN="${SCRIPT_DIR}/bin"

SSH_USER="root"
SSH_PORT="22"
TYPE="http"
PORT=""
PASSWORD=""
HTTP_PORT=""
TRAFFIC_PORT="9386"
TRAFFIC_TOKEN=""
ADMIN_IP="0.0.0.0/0"
DEV=""

usage() {
  sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host) HOST="$2"; shift 2;;
    --ssh-user) SSH_USER="$2"; shift 2;;
    --ssh-port) SSH_PORT="$2"; shift 2;;
    --type) TYPE="$2"; shift 2;;
    --port) PORT="$2"; shift 2;;
    --password) PASSWORD="$2"; shift 2;;
    --http-port) HTTP_PORT="$2"; shift 2;;
    --traffic-port) TRAFFIC_PORT="$2"; shift 2;;
    --traffic-token) TRAFFIC_TOKEN="$2"; shift 2;;
    --admin-ip) ADMIN_IP="$2"; shift 2;;
    --dev) DEV="$2"; shift 2;;
    -h|--help) usage;;
    *) echo "未知参数: $1"; usage;;
  esac
done

[[ -n "${HOST:-}" ]] || { echo "缺少 --host"; usage; }
[[ -n "$PORT" ]] || { echo "缺少 --port"; usage; }
case "$TYPE" in http|ss|socks5) ;; *) echo "type 仅支持 http|ss|socks5"; usage;; esac
if [[ -z "$PASSWORD" ]]; then
  PASSWORD="$(openssl rand -hex 12 2>/dev/null || head -c 12 /dev/urandom | xxd -p)"
  echo ">>> 已自动生成代理密码: $PASSWORD"
fi
if [[ -z "$TRAFFIC_TOKEN" ]]; then
  TRAFFIC_TOKEN="$(openssl rand -hex 16 2>/dev/null || head -c 16 /dev/urandom | xxd -p)"
  echo ">>> 已自动生成统计 Token: $TRAFFIC_TOKEN"
fi
if [[ -z "$HTTP_PORT" ]]; then
  HTTP_PORT=$((PORT + 1000))
fi

SSH="ssh -o StrictHostKeyChecking=no -o ConnectTimeout=10 -p $SSH_PORT $SSH_USER@$HOST"
SCP="scp -o StrictHostKeyChecking=no -o ConnectTimeout=10 -P $SSH_PORT"

echo ">>> 检查 SSH 连接 $SSH_USER@$HOST ..."
$SSH "uname -m; command -v systemctl >/dev/null && echo SYSTEMD_OK || echo SYSTEMD_MISSING"

# ---- 1. 准备 gost 二进制 ----
ARCH="$($SSH "uname -m")"
case "$ARCH" in
  x86_64|amd64) GOST_ARCH="linux_amd64";;
  aarch64|arm64) GOST_ARCH="linux_arm64";;
  armv7l|armv6l|armv5l) GOST_ARCH="linux_${ARCH%l}";;
  *) GOST_ARCH="";;
esac
[[ -n "$GOST_ARCH" ]] || { echo "不支持的架构: $ARCH"; exit 1; }

LOCAL_FILE="${LOCAL_BIN}/gost-${GOST_ARCH}"
if [[ -f "$LOCAL_FILE" ]]; then
  echo ">>> 使用本地预置二进制: $LOCAL_FILE"
  $SSH "mkdir -p /opt/gost"
  $SCP "$LOCAL_FILE" "$SSH_USER@$HOST:/opt/gost/gost"
else
  echo ">>> 从 GitHub 下载 gost v${GOST_VER} (${GOST_ARCH}) 到 VPS ..."
  $SSH "set -e; mkdir -p /opt/gost && cd /opt/gost && \
    curl -fsSL -o gost.tgz ${GOST_REPO}/gost_${GOST_VER}_${GOST_ARCH}.tar.gz && \
    tar -xzf gost.tgz --strip-components=1 && chmod +x gost && rm -f gost.tgz LICENSE"
fi

# ---- 2. 上传 trafficd ----
echo ">>> 上传 trafficd 统计服务 ..."
$SSH "mkdir -p /usr/local/bin"
$SCP "${SCRIPT_DIR}/trafficd.py" "$SSH_USER@$HOST:/usr/local/bin/trafficd.py"

# ---- 3. 构造代理启动命令 ----
case "$TYPE" in
  http)   GOST_ARGS="-L http://proxy:${PASSWORD}@:${PORT}";;
  socks5) GOST_ARGS="-L socks5://proxy:${PASSWORD}@:${PORT}";;
  ss)     GOST_ARGS="-L ss://AEAD_CHACHA20_POLY1305:${PASSWORD}@:${PORT} -L http://proxy:${PASSWORD}@:${HTTP_PORT}";;
esac

echo ">>> 写入 systemd 服务并启动 ..."
$SSH "cat > /etc/systemd/system/gost.service <<'UNIT'
[Unit]
Description=gost proxy (${TYPE})
After=network.target

[Service]
ExecStart=/opt/gost/gost ${GOST_ARGS}
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT

cat > /etc/systemd/system/trafficd.service <<'UNIT'
[Unit]
Description=proxy traffic stats
After=network.target

[Service]
ExecStart=/usr/bin/python3 /usr/local/bin/trafficd.py --port ${TRAFFIC_PORT} --token ${TRAFFIC_TOKEN} --admin-ip ${ADMIN_IP} ${DEV:+--dev ${DEV}}
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now gost trafficd
sleep 1
systemctl is-active gost trafficd"

echo
echo "============================ 部署完成 ============================"
echo "  节点地址   : $HOST"
echo "  代理类型   : $TYPE"
case "$TYPE" in
  http)   echo "  http 代理  : http://proxy:${PASSWORD}@${HOST}:${PORT}";;
  socks5) echo "  socks5代理 : socks5://proxy:${PASSWORD}@${HOST}:${PORT}";;
  ss)     echo "  ss 代理    : ss://AEAD_CHACHA20_POLY1305:${PASSWORD}@${HOST}:${PORT}"
          echo "  OpenList用 : http://proxy:${PASSWORD}@${HOST}:${HTTP_PORT}";;
esac
echo "  统计服务   : http://${HOST}:${TRAFFIC_PORT}/stats (token=${TRAFFIC_TOKEN})"
echo
echo "  OpenList 端填入（115 存储配置的 proxy 字段 或 管理后台 代理管理）:"
case "$TYPE" in
  http)   echo "    http://proxy:${PASSWORD}@${HOST}:${PORT}";;
  socks5) echo "    socks5://proxy:${PASSWORD}@${HOST}:${PORT}";;
  ss)     echo "    http://proxy:${PASSWORD}@${HOST}:${HTTP_PORT}";;
esac
echo
echo "  查看实时流量:"
echo "    ./proxy_status.sh --host ${HOST} --traffic-port ${TRAFFIC_PORT} --traffic-token ${TRAFFIC_TOKEN}"
echo "  或在 OpenList 管理后台 - 代理管理与流量监控 中新增节点实时查看"
echo "  建议将 --admin-ip 限定为 OpenList 服务器的公网 IP（当前为 ${ADMIN_IP}，靠 token 鉴权）"
echo "=================================================================="