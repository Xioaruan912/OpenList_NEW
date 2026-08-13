#!/usr/bin/env bash
#
# OpenList (fork with 115 qrcode login + video thumbnails) one-click installer
# Supports Debian/Ubuntu (apt) and CentOS/RHEL/Fedora (yum/dnf)
#
# Usage:
#   curl -fsSL https://github.com/Xioaruan912/OpenList_NEW/raw/main/install.sh | bash
#   or
#   bash install.sh
#
# Optional env vars:
#   INSTALL_DIR=/opt/openlist    install directory (default /opt/openlist)
#   INSTALL_BRANCH=main          git branch to checkout (default main)
#   SKIP_BUILD=0                 1 to skip building (use pre-built binary)
#   OPENLIST_PORT=5244           http port override
#
set -euo pipefail

REPO_URL="https://github.com/Xioaruan912/OpenList_NEW.git"
BRANCH="${INSTALL_BRANCH:-main}"
INSTALL_DIR="${INSTALL_DIR:-/opt/openlist}"
OPENLIST_PORT="${OPENLIST_PORT:-5244}"
SERVICE_NAME="openlist"
BIN_NAME="openlist"

log()  { echo -e "\033[32m[INFO]\033[0m $*"; }
warn() { echo -e "\033[33m[WARN]\033[0m $*"; }
err()  { echo -e "\033[31m[ERROR]\033[0m $*"; exit 1; }

# ---------- 0. Root check ----------
if [ "$(id -u)" -ne 0 ]; then
  err "Please run as root (sudo bash install.sh)"
fi

# ---------- 1. Detect OS & install dependencies ----------
detect_pkg_mgr() {
  if command -v apt-get >/dev/null 2>&1; then echo "apt"
  elif command -v dnf >/dev/null 2>&1; then echo "dnf"
  elif command -v yum >/dev/null 2>&1; then echo "yum"
  else err "Unsupported package manager. Only apt/dnf/yum supported."
  fi
}
PKG_MGR="$(detect_pkg_mgr)"
log "Detected package manager: $PKG_MGR"

install_deps() {
  log "Installing system dependencies (git, curl, gcc, ffmpeg)..."
  case "$PKG_MGR" in
    apt)
      export DEBIAN_FRONTEND=noninteractive
      apt-get update -qq
      apt-get install -y -qq git curl ca-certificates build-essential ffmpeg >/dev/null 2>&1 || \
        apt-get install -y -qq git curl ca-certificates build-essential ffmpeg
      ;;
    dnf)
      dnf install -y -q git curl gcc make ffmpeg-free 2>/dev/null || \
      dnf install -y -q git curl gcc make
      ;;
    yum)
      yum install -y -q git curl gcc make
      ;;
  esac
}

# ---------- 2. Install Go (via GOTOOLCHAIN auto, but need a bootstrap go) ----------
install_go() {
  if command -v go >/dev/null 2>&1 && [ "$(go version | awk '{print $3}' | sed 's/go//' | cut -d. -f1-2)" = "1.2" ]; then
    : # assume recent enough
  fi
  # go.mod requires go 1.25 / toolchain 1.26. With GOTOOLCHAIN=auto, go will auto-download
  # the required toolchain, but we still need an initial go. Check if go exists.
  if ! command -v go >/dev/null 2>&1; then
    log "Go not found, installing Go 1.24.1 (toolchain auto-upgrades to 1.26)..."
    local GO_VERSION="1.24.1"
    local GO_TARBALL="go${GO_VERSION}.linux-$(uname -m).tar.gz"
    case "$(uname -m)" in
      x86_64) GO_TARBALL="go${GO_VERSION}.linux-amd64.tar.gz" ;;
      aarch64|arm64) GO_TARBALL="go${GO_VERSION}.linux-arm64.tar.gz" ;;
      *) err "Unsupported arch: $(uname -m)" ;;
    esac
    curl -fsSL "https://go.dev/dl/${GO_TARBALL}" -o /tmp/${GO_TARBALL}
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/${GO_TARBALL}
    rm -f /tmp/${GO_TARBALL}
    export PATH="/usr/local/go/bin:$PATH"
    echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh
  fi
  export GOTOOLCHAIN=auto
  export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
  log "Go: $(go version)"
}

# ---------- 3. Clone repository ----------
clone_repo() {
  if [ -d "$INSTALL_DIR/.git" ]; then
    log "Repo already exists at $INSTALL_DIR, pulling latest..."
    git -C "$INSTALL_DIR" fetch --all
    git -C "$INSTALL_DIR" checkout "$BRANCH" 2>/dev/null || true
    git -C "$INSTALL_DIR" pull --ff-only 2>/dev/null || true
  else
    log "Cloning OpenList_NEW to $INSTALL_DIR..."
    mkdir -p "$INSTALL_DIR"
    git clone --depth 1 --branch "$BRANCH" "$REPO_URL" "$INSTALL_DIR"
  fi
}

# ---------- 4. Build ----------
build() {
  cd "$INSTALL_DIR"
  log "Building openlist binary (this may take several minutes)..."
  go build -o "${BIN_NAME}" ./main.go
  [ -f "${BIN_NAME}" ] || err "Build failed, no binary produced"
  log "Build done: $(ls -lh ${BIN_NAME} | awk '{print $5}')"
}

# ---------- 5. Prepare data dir ----------
prepare_data() {
  mkdir -p "$INSTALL_DIR/data"
  chmod 700 "$INSTALL_DIR/data"
  log "Data directory ready: $INSTALL_DIR/data"
}

# ---------- 6. Create systemd service ----------
create_service() {
  cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=OpenList (fork) - a file list program that supports multiple storage
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
ExecStart=${INSTALL_DIR}/${BIN_NAME} server
Restart=always
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable "${SERVICE_NAME}" >/dev/null 2>&1
  log "Systemd service created: ${SERVICE_NAME}"
}

# ---------- 7. Start & verify ----------
start_and_verify() {
  systemctl restart "${SERVICE_NAME}"
  log "Waiting for server to start on port ${OPENLIST_PORT}..."
  local ok=""
  for i in $(seq 1 30); do
    sleep 2
    if curl -fsS "http://127.0.0.1:${OPENLIST_PORT}/ping" 2>/dev/null | grep -q pong; then
      ok=1
      break
    fi
  done
  if [ -z "$ok" ]; then
    warn "Server did not respond to /ping in time. Check logs: journalctl -u ${SERVICE_NAME} -f"
    return 1
  fi
  log "Server is up: http://127.0.0.1:${OPENLIST_PORT}"
}

# ---------- 8. Show admin credentials ----------
show_credentials() {
  local ip
  ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
  [ -z "$ip" ] && ip="<server-ip>"
  echo
  echo "================================================================"
  echo "  OpenList installed successfully!"
  echo "  Web:    http://${ip}:${OPENLIST_PORT}  (or http://localhost:${OPENLIST_PORT})"
  echo
  echo "  First-run admin credentials are printed in the startup log."
  echo "  View them with:"
  echo "    journalctl -u ${SERVICE_NAME} --no-pager | grep -i password"
  echo "  Or reset manually (from source tree):"
  echo "    sudo -u root ${INSTALL_DIR}/${BIN_NAME} admin"
  echo
  echo "  Manage service:"
  echo "    systemctl status ${SERVICE_NAME}"
  echo "    systemctl restart ${SERVICE_NAME}"
  echo "    systemctl stop ${SERVICE_NAME}"
  echo "  Logs: journalctl -u ${SERVICE_NAME} -f"
  echo "================================================================"
}

# ================= main =================
install_deps
install_go
clone_repo
build
prepare_data
create_service
start_and_verify || true
show_credentials
