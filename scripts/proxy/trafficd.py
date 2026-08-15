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
import re
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
        self._last = None
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