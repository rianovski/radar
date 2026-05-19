#!/usr/bin/env bash
set -euo pipefail

REPO_URL="${REPO_URL:-https://tfs.astra.co.id/tfs/CIST/DevSecOps/_git/radar}"
SRC_DIR="${SRC_DIR:-/opt/radar/src}"
RADAR_SH="${RADAR_SH:-/opt/radar/radar.sh}"
GO_VERSION="${GO_VERSION:-1.26.0}"
NODE_VERSION="${NODE_VERSION:-22}"

export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"

# ── Go ───────────────────────────────────────────────────────────────────────
install_go() {
    echo "→ Installing Go ${GO_VERSION}..."
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz
}

current_go=$(go version 2>/dev/null | awk '{print $3}' | tr -d 'go' || echo "")
if [[ "$current_go" != "$GO_VERSION" ]]; then
    install_go
    echo "✓ Go ${GO_VERSION} installed"
else
    echo "✓ Go ${current_go} already installed"
fi

# ── Node.js ──────────────────────────────────────────────────────────────────
if ! command -v node &>/dev/null; then
    echo "→ Installing Node.js ${NODE_VERSION}..."
    curl -fsSL https://deb.nodesource.com/setup_${NODE_VERSION}.x | bash -
    apt-get install -y nodejs
    echo "✓ Node.js $(node --version) installed"
else
    echo "✓ Node.js $(node --version) already installed"
fi

# ── Clone or pull ────────────────────────────────────────────────────────────
if [[ -d "${SRC_DIR}/.git" ]]; then
    echo "→ Pulling latest..."
    git -C "${SRC_DIR}" pull
else
    echo "→ Cloning repo..."
    git clone "${REPO_URL}" "${SRC_DIR}"
fi

# ── Build ────────────────────────────────────────────────────────────────────
echo "→ Building..."
cd "${SRC_DIR}"
make build

# ── Smoke test ───────────────────────────────────────────────────────────────
echo "→ Testing binary..."
if ! ./radar --version &>/dev/null; then
    echo "✗ Build smoke test failed — aborting, existing binary untouched"
    exit 1
fi
echo "✓ Binary OK"

# ── Replace binary ───────────────────────────────────────────────────────────
RADAR_BIN=$(which radar)
echo "→ Stopping radar..."
"${RADAR_SH}" stop || true

echo "→ Replacing ${RADAR_BIN}..."
cp "${RADAR_BIN}" "${RADAR_BIN}.bak"
cp radar "${RADAR_BIN}"

# ── Restart ──────────────────────────────────────────────────────────────────
echo "→ Starting radar..."
"${RADAR_SH}" start
echo "✓ Done"
