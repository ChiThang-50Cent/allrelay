#!/bin/bash
# AllRelay — Setup build environment
# Installs missing dependencies and exports env vars.
#
# Usage:
#   chmod +x scripts/setup-env.sh
#   ./scripts/setup-env.sh          # check + print export commands
#   source ./scripts/setup-env.sh   # apply to current shell
#
# Parts requiring sudo will print the command for you to run.

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

NEED_SUDO=""

info()  { echo -e "${BLUE}[*]${NC} $1"; }
ok()    { echo -e "  ${GREEN}✅ $1${NC}"; }
warn()  { echo -e "  ${YELLOW}⚠️  $1${NC}"; }
err()   { echo -e "  ${RED}❌ $1${NC}"; }
sudo_cmd() { NEED_SUDO="${NEED_SUDO}$1"$'\n'; }

# ─── 1. Android SDK ───────────────────────────────────────────

info "Android SDK..."
if [ -d "$HOME/android-sdk" ]; then
    export ANDROID_SDK_ROOT="$HOME/android-sdk"
    ok "Found at $ANDROID_SDK_ROOT"
elif [ -n "$ANDROID_SDK_ROOT" ] && [ -d "$ANDROID_SDK_ROOT" ]; then
    ok "Found at $ANDROID_SDK_ROOT"
else
    err "Not found. Install Android SDK or set ANDROID_SDK_ROOT"
fi

# ─── 2. Java ──────────────────────────────────────────────────

info "Java..."
if command -v java &>/dev/null; then
    ok "java $(java -version 2>&1 | head -1)"
else
    err "Java not found — install JDK 17+"
    sudo_cmd "sudo apt-get install -y openjdk-17-jdk"
fi

# ─── 3. Go ────────────────────────────────────────────────────

info "Go..."
if command -v go &>/dev/null; then
    ok "go $(go version | awk '{print $3}')"
else
    err "Go not found"
    sudo_cmd "sudo snap install go --classic"
fi

# ─── 4. SDL3 (for C client) ───────────────────────────────────

info "SDL3..."
if pkg-config --exists sdl3 2>/dev/null; then
    ok "SDL3 $(pkg-config --modversion sdl3)"
else
    warn "SDL3 not installed — run: ./scripts/install-sdl3.sh"
fi

# ─── 5. Meson + Ninja (for C client) ─────────────────────────

info "Meson & Ninja..."
if command -v meson &>/dev/null; then
    ok "meson $(meson --version)"
else
    err "meson not found"
    sudo_cmd "sudo apt-get install -y meson"
fi

if command -v ninja &>/dev/null; then
    ok "ninja $(ninja --version)"
else
    err "ninja not found"
    sudo_cmd "sudo apt-get install -y ninja-build"
fi

# ─── 6. Other build tools ─────────────────────────────────────

info "Build tools..."
for tool in gcc cmake pkg-config git; do
    if command -v $tool &>/dev/null; then
        ok "$tool"
    else
        err "$tool not found"
        sudo_cmd "sudo apt-get install -y $tool"
    fi
done

# ─── Print exports ────────────────────────────────────────────

echo ""
echo -e "${GREEN}━━━ Environment variables ━━━${NC}"
echo ""
echo "  export ANDROID_SDK_ROOT=$ANDROID_SDK_ROOT"
echo "  export PATH=\"\$ANDROID_SDK_ROOT/cmdline-tools/latest/bin:\$PATH\""
echo ""

# ─── Print sudo commands if needed ───────────────────────────

if [ -n "$NEED_SUDO" ]; then
    echo -e "${YELLOW}━━━ Commands cần sudo (bác chạy tay nhé) ━━━${NC}"
    echo ""
    echo "$NEED_SUDO"
fi

echo -e "${GREEN}Done. Chạy 'source scripts/setup-env.sh' để apply env vars.${NC}"
