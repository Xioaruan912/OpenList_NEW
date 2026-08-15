#!/usr/bin/env bash
# ============================================================================
# proxy_deploy.sh - 在【当前服务器】一键部署 115 防风控代理
#
# 与根目录 install.sh 一样，直接在当前 VPS 上运行（脚本自带 trafficd，无需额外文件）：
#   curl -fsSL https://github.com/Xioaruan912/OpenList_NEW/raw/main/scripts/proxy/proxy_deploy.sh \
#     | bash -s -- --type http --port 1080 --password 你的密码
# 或下载到本地运行：
#   bash proxy_deploy.sh --type ss --port 8388 --password 你的密码
#
# 统一使用 gost v2 单二进制（多协议）：
#   - http   : OpenList 直接使用
#   - ss     : 自动额外开放一个 http 端口（OpenList 端仅支持 http/socks5）
#   - socks5 : OpenList 直接使用
#
# 同时部署 trafficd 实时流量统计服务（内置）。
#
# 可选参数（均有默认值）：
#   --type http|ss|socks5    代理协议（默认 http）
#   --port 1080              代理端口（默认 1080）
#   --password <密码>        代理密码（默认自动生成并打印）
#   --http-port 2080         ss 模式下供 OpenList 使用的 http 端口（默认 代理端口+1000）
#   --traffic-port 9386      trafficd 统计服务端口（默认 9386）
#   --traffic-token <token>  trafficd 鉴权 token（默认自动生成并打印）
#   --admin-ip 0.0.0.0/0     允许访问统计的网段（默认 0.0.0.0/0，建议限定为 OpenList 服务器 IP）
#   --dev eth0               只统计指定网卡（默认统计全部网卡）
#   --no-trafficd            不部署流量统计
#   --dry-run                只打印将执行的命令，不实际部署
# ============================================================================
set -euo pipefail

GOST_VER="2.12.0"
GOST_REPO="https://github.com/ginuerzh/gost/releases/download/v${GOST_VER}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd 2>/dev/null || pwd)"
LOCAL_BIN="${SCRIPT_DIR}/bin"

log()  { echo -e "\033[32m[INFO]\033[0m $*"; }
warn() { echo -e "\033[33m[WARN]\033[0m $*"; }
err()  { echo -e "\033[31m[ERROR]\033[0m $*"; exit 1; }

# ---------- 参数 ----------
TYPE="http"
PORT="1080"
PASSWORD=""
HTTP_PORT=""
TRAFFIC_PORT="9386"
TRAFFIC_TOKEN=""
ADMIN_IP="0.0.0.0/0"
DEV=""
NO_TRAFFICD="0"
DRY_RUN="0"

usage() {
  cat <<'EOF'
用法:
  curl -fsSL https://github.com/Xioaruan912/OpenList_NEW/raw/main/scripts/proxy/proxy_deploy.sh \
    | bash -s -- --type http --port 1080 --password 你的密码
  或
  bash proxy_deploy.sh --type ss --port 8388 --password 你的密码

可选参数:
  --type http|ss|socks5    代理协议 (默认 http)
  --port 1080              代理端口 (默认 1080)
  --password <密码>        代理密码 (默认自动生成并打印)
  --http-port 2080         ss 模式下供 OpenList 使用的 http 端口 (默认 代理端口+1000)
  --traffic-port 9386      trafficd 统计服务端口 (默认 9386)
  --traffic-token <token>  trafficd 鉴权 token (默认自动生成并打印)
  --admin-ip 0.0.0.0/0     允许访问统计的网段 (默认 0.0.0.0/0)
  --dev eth0               只统计指定网卡 (默认全部)
  --no-trafficd            不部署流量统计
  --dry-run                只打印将执行的命令
  -h|--help                显示帮助
EOF
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --type) TYPE="$2"; shift 2;;
    --port) PORT="$2"; shift 2;;
    --password) PASSWORD="$2"; shift 2;;
    --http-port) HTTP_PORT="$2"; shift 2;;
    --traffic-port) TRAFFIC_PORT="$2"; shift 2;;
    --traffic-token) TRAFFIC_TOKEN="$2"; shift 2;;
    --admin-ip) ADMIN_IP="$2"; shift 2;;
    --dev) DEV="$2"; shift 2;;
    --no-trafficd) NO_TRAFFICD="1"; shift;;
    --dry-run) DRY_RUN="1"; shift;;
    -h|--help) usage;;
    *) echo "未知参数: $1"; usage;;
  esac
done

case "$TYPE" in http|ss|socks5) ;; *) err "type 仅支持 http|ss|socks5";; esac
if [[ -z "$PASSWORD" ]]; then
  PASSWORD="$(openssl rand -hex 12 2>/dev/null || head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  [[ -n "$PASSWORD" ]] || PASSWORD="$(date +%s%N | sha256sum | head -c 24)"
  log "已自动生成代理密码: $PASSWORD"
fi
if [[ -z "$TRAFFIC_TOKEN" ]]; then
  TRAFFIC_TOKEN="$(openssl rand -hex 16 2>/dev/null || head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  [[ -n "$TRAFFIC_TOKEN" ]] || TRAFFIC_TOKEN="$(date +%s%N | sha256sum | head -c 32)"
  log "已自动生成统计 Token: $TRAFFIC_TOKEN"
fi
if [[ -z "$HTTP_PORT" ]]; then
  HTTP_PORT=$((PORT + 1000))
fi

# ---------- 0. Root / systemd 检查 ----------
if [[ "$(id -u)" -ne 0 ]]; then
  err "请以 root 运行（sudo bash proxy_deploy.sh）"
fi
HAVE_SYSTEMD="0"
if command -v systemctl >/dev/null 2>&1 && systemctl is-system-running >/dev/null 2>&1; then
  HAVE_SYSTEMD="1"
fi

# ---------- 1. 准备 gost 二进制 ----------
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) GOST_ARCH="linux_amd64";;
  aarch64|arm64) GOST_ARCH="linux_arm64";;
  armv7l) GOST_ARCH="linux_armv7";;
  armv6l) GOST_ARCH="linux_armv6";;
  armv5l) GOST_ARCH="linux_armv5";;
  *) err "不支持的架构: $ARCH";;
esac

install_gost() {
  local src=""
  if [[ -f "${LOCAL_BIN}/gost-${GOST_ARCH}" ]]; then
    src="${LOCAL_BIN}/gost-${GOST_ARCH}"
    log "使用本地预置二进制: $src"
    install -m 0755 "$src" /opt/gost/gost
  else
    log "从 GitHub 下载 gost v${GOST_VER} (${GOST_ARCH}) ..."
    command -v curl >/dev/null 2>&1 || err "缺少 curl，请先安装（apt install -y curl）"
    mkdir -p /opt/gost
    curl -fsSL -o /tmp/gost.tgz "${GOST_REPO}/gost_${GOST_VER}_${GOST_ARCH}.tar.gz"
    tar -xzf /tmp/gost.tgz -C /opt/gost
    rm -f /tmp/gost.tgz
    chmod +x /opt/gost/gost
  fi
}

# ---------- 2. 部署 trafficd（内置） ----------
install_trafficd() {
  cat > /usr/local/bin/trafficd.py <<'TRAFFICD_PY'
#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
trafficd - 代理节点实时流量统计服务（零依赖，仅用 Python 标准库）

由 proxy_deploy.sh 自动部署到用户的代理 VPS 上，systemd 管理。
统计来源：
  - 网卡累计流量：/proc/net/dev 差值
  - 实时速率：窗口（默认 60s）内的字节增量
  - 当前 TCP 连接数：/proc/net/tcp + /proc/net/tcp6 中 ESTABLISHED 数量

HTTP 接口：
  GET /stats?token=<TOKEN>  返回 JSON：
    {"ok":true,"hostname":"...","uptime":<秒>,"time":"...",
     "rx_bytes":<累计接收>,"tx_bytes":<累计发送>,
     "rx_rate":<接收速率 B/s>,"tx_rate":<发送速率 B/s>,"conns":<连接数>}

用法：
  trafficd.py --port 9386 --token SECRET
可选：
  --dev eth0       指定统计网卡（默认统计所有网卡总和）
  --window 60      速率统计窗口（秒）
  --admin-ip CIDR  允许访问的网段（默认仅本机 127.0.0.1，可通过 {admin_ip} 模板修改）
"""
import argparse
import ipaddress
import json
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

try:
    import secrets

    def rand_hex(n):
        return secrets.token_hex(n)
except Exception:  # pragma: no cover
    import random

    def rand_hex(n):
        return "".join(random.choice("0123456789abcdef") for _ in range(n * 2))


class Stats(object):
    def __init__(self, dev, window):
        self.dev = dev
        self.window = window
        self.uptime_start = time.time()
        self.history = []
        self._lock = threading.Lock()
        self._snap = {"rx": 0, "tx": 0, "conns": 0}

    def _dev_bytes(self):
        """返回 (总接收字节, 总发送字节)；dev=None 时统计所有网卡总和"""
        rx = tx = 0
        try:
            with open("/proc/net/dev", "r") as f:
                for line in f:
                    if ":" not in line:
                        continue
                    name, rest = line.split(":", 1)
                    name = name.strip()
                    if name == "lo" or (self.dev and name != self.dev):
                        continue
                    parts = rest.split()
                    if len(parts) < 9:
                        continue
                    rx += int(parts[0])
                    tx += int(parts[8])
        except Exception:
            pass
        return rx, tx

    def _conns(self):
        n = 0
        for p in ("/proc/net/tcp", "/proc/net/tcp6"):
            try:
                with open(p, "r") as f:
                    next(f, None)
                    for line in f:
                        cols = line.split()
                        if len(cols) > 3 and cols[3] == "01":
                            n += 1
            except Exception:
                pass
        return n

    def sample(self):
        rx, tx = self._dev_bytes()
        conns = self._conns()
        with self._lock:
            now = time.time()
            rate_rx = rate_tx = 0
            self.history.append((now, rx, tx))
            self.history = [(t, r, x) for (t, r, x) in self.history if now - t <= self.window]
            if len(self.history) >= 2:
                span = self.history[-1][0] - self.history[0][0]
                if span > 0:
                    rate_rx = int((rx - self.history[0][1]) / span)
                    rate_tx = int((tx - self.history[0][2]) / span)
            self._snap = {
                "rx": rx,
                "tx": tx,
                "rx_rate": rate_rx,
                "tx_rate": rate_tx,
                "conns": conns,
                "time": now,
            }

    def snap(self):
        with self._lock:
            return dict(self._snap)


def main():
    ap = argparse.ArgumentParser(description="proxy traffic stats daemon")
    ap.add_argument("--port", type=int, default=9386)
    ap.add_argument("--token", default="")
    ap.add_argument("--dev", default="")
    ap.add_argument("--window", type=int, default=60)
    ap.add_argument("--admin-ip", default="127.0.0.0/8")
    args = ap.parse_args()

    token = args.token or rand_hex(16)
    stats = Stats(args.dev or None, args.window)
    allowed = ipaddress.ip_network(args.admin_ip, strict=False)
    stats.sample()

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *a):  # 静默访问日志
            pass

        def _ok(self, obj):
            body = json.dumps(obj).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def _deny(self):
            body = b'{"ok":false,"error":"forbidden"}'
            self.send_response(403)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            try:
                peer = ipaddress.ip_address(self.client_address[0])
            except ValueError:
                peer = None
            if peer is not None and peer not in allowed:
                self._deny()
                return
            parsed = self.path.split("?", 1)
            if parsed[0] != "/stats":
                self._ok({"ok": False, "error": "not found"})
                return
            q = {}
            if len(parsed) > 1:
                for kv in parsed[1].split("&"):
                    if "=" in kv:
                        k, v = kv.split("=", 1)
                        q[k] = v
            if args.token and q.get("token") != args.token:
                self._deny()
                return
            s = stats.snap()
            self._ok({
                "ok": True,
                "hostname": os.uname().nodename,
                "uptime": int(time.time() - stats.uptime_start),
                "time": time.strftime("%Y-%m-%d %H:%M:%S"),
                "rx_bytes": s["rx"],
                "tx_bytes": s["tx"],
                "rx_rate": s["rx_rate"],
                "tx_rate": s["tx_rate"],
                "conns": s["conns"],
            })

    def sampler():
        while True:
            stats.sample()
            time.sleep(2)

    threading.Thread(target=sampler, daemon=True).start()
    httpd = ThreadingHTTPServer(("0.0.0.0", args.port), Handler)
    print("trafficd listening on 0.0.0.0:%d token=%s" % (args.port, token))
    httpd.serve_forever()


if __name__ == "__main__":
    main()
TRAFFICD_PY
  chmod +x /usr/local/bin/trafficd.py
}

# ---------- 3. 构造代理启动命令 ----------
case "$TYPE" in
  http)   GOST_ARGS="-L http://proxy:${PASSWORD}@:${PORT}";;
  socks5) GOST_ARGS="-L socks5://proxy:${PASSWORD}@:${PORT}";;
  ss)     GOST_ARGS="-L ss://AEAD_CHACHA20_POLY1305:${PASSWORD}@:${PORT} -L http://proxy:${PASSWORD}@:${HTTP_PORT}";;
esac

write_units() {
  cat > /etc/systemd/system/gost.service <<UNIT
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

  cat > /etc/systemd/system/trafficd.service <<UNIT
[Unit]
Description=proxy traffic stats
After=network.target

[Service]
ExecStart=/usr/bin/python3 /usr/local/bin/trafficd.py --port ${TRAFFIC_PORT} --token ${TRAFFIC_TOKEN} --admin-ip ${ADMIN_IP}${DEV:+ --dev ${DEV}}
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT
}

deploy() {
  [[ -d /opt/gost ]] || mkdir -p /opt/gost
  install_gost
  if [[ "$NO_TRAFFICD" == "0" ]]; then
    install_trafficd
  fi
  if [[ "$HAVE_SYSTEMD" == "1" ]]; then
    write_units
    systemctl daemon-reload
    systemctl enable gost >/dev/null 2>&1
    systemctl restart gost
    if [[ "$NO_TRAFFICD" == "0" ]]; then
      systemctl enable trafficd >/dev/null 2>&1
      systemctl restart trafficd
    fi
    sleep 1
    systemctl is-active gost trafficd 2>/dev/null || true
  else
    warn "未检测到 systemd，使用 nohup 后台运行（重启后需重新部署或手动配置自启）"
    start_nohup
  fi
}

# 无 systemd 环境的兜底：nohup 后台运行 + 尝试写入 /etc/rc.local
start_nohup() {
  nohup /opt/gost/gost ${GOST_ARGS} >/var/log/gost.log 2>&1 &
  echo $! > /var/run/gost.pid
  if [[ "$NO_TRAFFICD" == "0" ]]; then
    nohup /usr/bin/python3 /usr/local/bin/trafficd.py --port ${TRAFFIC_PORT} --token ${TRAFFIC_TOKEN} --admin-ip ${ADMIN_IP}${DEV:+ --dev ${DEV}} >/var/log/trafficd.log 2>&1 &
    echo $! > /var/run/trafficd.pid
  fi
  sleep 1
  local p=""
  for p in gost trafficd; do
    [[ "$NO_TRAFFICD" == "1" && "$p" == "trafficd" ]] && continue
    kill -0 "$(cat /var/run/${p}.pid 2>/dev/null)" 2>/dev/null && log "${p} 运行中 (pid $(cat /var/run/${p}.pid))" || warn "${p} 启动失败，查看 /var/log/${p}.log"
  done
  # 持久化（尽力而为）
  local RCL=/etc/rc.local
  if [[ -f "$RCL" && ! "$HAVE_SYSTEMD" == "1" ]] && ! grep -q gost "$RCL"; then
    sed -i "/^exit 0/i nohup /opt/gost/gost ${GOST_ARGS} >/var/log/gost.log 2>&1 &\n" "$RCL" 2>/dev/null || true
  fi
}

if [[ "$DRY_RUN" == "1" ]]; then
  log "===== dry-run 将执行以下操作 ====="
  log "1) 安装 gost（架构 $GOST_ARCH）到 /opt/gost/gost"
  if [[ -f "${LOCAL_BIN}/gost-${GOST_ARCH}" ]]; then
    log "   来源: ${LOCAL_BIN}/gost-${GOST_ARCH}（本地预置）"
  else
    log "   来源: ${GOST_REPO}/gost_${GOST_VER}_${GOST_ARCH}.tar.gz"
  fi
  log "2) 写入 /usr/local/bin/trafficd.py（内置）"$([ "$NO_TRAFFICD" == "1" ] && echo "  [跳过]")
  log "3) 写入 systemd 单元 gost.service / trafficd.service"
  log "    gost 启动命令: /opt/gost/gost ${GOST_ARGS}"
  log "    trafficd 启动命令: /usr/bin/python3 /usr/local/bin/trafficd.py --port ${TRAFFIC_PORT} --token ${TRAFFIC_TOKEN} --admin-ip ${ADMIN_IP}"
  log "4) systemctl enable --now gost trafficd"
  exit 0
fi

deploy

echo
echo "============================ 部署完成 ============================"
echo "  节点地址   : $(hostname -I 2>/dev/null | awk '{print $1}')"
echo "  代理类型   : $TYPE"
case "$TYPE" in
  http)   echo "  http 代理  : http://proxy:${PASSWORD}@<本机IP>:${PORT}"
          echo "  OpenList填 : http://proxy:${PASSWORD}@<本机IP>:${PORT}";;
  socks5) echo "  socks5代理 : socks5://proxy:${PASSWORD}@<本机IP>:${PORT}"
          echo "  OpenList填 : socks5://proxy:${PASSWORD}@<本机IP>:${PORT}";;
  ss)     echo "  ss 代理    : ss://AEAD_CHACHA20_POLY1305:${PASSWORD}@<本机IP>:${PORT}"
          echo "  OpenList填 : http://proxy:${PASSWORD}@<本机IP>:${HTTP_PORT}";;
esac
if [[ "$NO_TRAFFICD" == "0" ]]; then
  echo "  统计服务   : http://<本机IP>:${TRAFFIC_PORT}/stats (token=${TRAFFIC_TOKEN})"
fi
echo
echo "  查看实时流量:"
if [[ "$NO_TRAFFICD" == "0" ]]; then
  echo "    ./proxy_status.sh --host <本机IP> --traffic-port ${TRAFFIC_PORT} --traffic-token ${TRAFFIC_TOKEN}"
  echo "    或在 OpenList 管理后台 - 代理管理与流量监控 中新增节点实时查看"
else
  echo "    （未部署 trafficd）"
fi
echo "  建议将 --admin-ip 限定为 OpenList 服务器的公网 IP（当前为 ${ADMIN_IP}，靠 token 鉴权）"
echo "=================================================================="