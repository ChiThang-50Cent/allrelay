#!/bin/bash
# AllRelay — Install SDL3 (minimal build)
# Usage: ./scripts/install-sdl3.sh
set -e

GREEN='\033[0;32m'; BLUE='\033[0;34m'; NC='\033[0m'
info() { echo -e "${BLUE}[*]${NC} $1"; }
ok()   { echo -e "  ${GREEN}✅ $1${NC}"; }

# Already installed?
if pkg-config --exists sdl3 2>/dev/null; then
    ok "SDL3 already installed: $(pkg-config --modversion sdl3)"
    exit 0
fi

# Try apt
if apt-cache show libsdl3-dev &>/dev/null 2>&1; then
    echo "Run: sudo apt-get install -y libsdl3-dev"
    exit 0
fi

info "Building SDL3 from source (minimal, ~1 min)..."

# Only essential build deps
sudo apt-get install -y -q build-essential cmake pkg-config git \
    libasound2-dev libpulse-dev libx11-dev libxext-dev 2>&1 | tail -1

# Clone + build
SDL_DIR="/tmp/sdl3-build"
rm -rf "$SDL_DIR"
git -c advice.detachedHead=false clone -q --branch release-3.2.8 --depth 1 \
    https://github.com/libsdl-org/SDL.git "$SDL_DIR"

cd "$SDL_DIR"
rm -rf build && mkdir build && cd build

info "cmake..."
cmake .. -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr/local \
    -DSDL_STATIC=OFF -DSDL_TEST=OFF -DSDL_DISABLE_INSTALL_DOCS=ON \
    2>&1 | tail -5

info "make (using 2 cores to avoid OOM)..."
make -j2 2>&1 | tail -3

info "install..."
sudo make install -j2 2>&1 | tail -1
sudo ldconfig

# Verify
if pkg-config --exists sdl3; then
    ok "SDL3 $(pkg-config --modversion sdl3) installed"
    rm -rf "$SDL_DIR"
    echo ""
    echo "Now run: ./scripts/build.sh client"
else
    echo "ERROR: SDL3 not found after install"
    echo "Check: ls /usr/local/lib/libSDL3*"
    exit 1
fi
