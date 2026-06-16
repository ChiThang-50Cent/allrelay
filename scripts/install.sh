#!/bin/bash
# AllRelay Ubuntu Installation Script
#
# Installs the AllRelay Ubuntu server and all dependencies.
# Run as root or with sudo for system-wide installation.
#
# Usage:
#   sudo ./scripts/install.sh [--user]
#
#   --user    Install for current user only (no sudo required)
#
# What this installs:
#   - AllRelay Go server binary → /usr/local/bin/allrelay-server
#   - PipeWire config files for mic/speaker virtual devices
#   - systemd service (auto-start on boot/login)
#   - Required dependencies (v4l2loopback, PipeWire)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

# ─── Configuration ────────────────────────────────────────────────
MODE="system"
INSTALL_PREFIX="/usr/local"
BIN_DIR="/usr/local/bin"
CONFIG_DIR="/etc/allrelay"
PIPEWIRE_DIR="/etc/pipewire/pipewire.conf.d"
SYSTEMD_DIR="/etc/systemd/system"
USER_PIPEWIRE_DIR="$HOME/.config/pipewire/pipewire.conf.d"
USER_SYSTEMD_DIR="$HOME/.config/systemd/user"

while [[ $# -gt 0 ]]; do
    case $1 in
        --user)
            MODE="user"
            BIN_DIR="$HOME/.local/bin"
            CONFIG_DIR="$HOME/.config/allrelay"
            shift
            ;;
        --help|-h)
            echo "Usage: $0 [--user]"
            echo ""
            echo "  --user    Install for current user only (no sudo)"
            echo ""
            echo "  Default:  System-wide installation (requires sudo)"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# ─── Banner ───────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}${BOLD}AllRelay Ubuntu Installer${NC}"
echo -e "${GREEN}===========================${NC}"
echo ""

if [ "$MODE" = "system" ] && [ "$EUID" -ne 0 ]; then
    echo -e "${RED}System installation requires sudo.${NC}"
    echo "  sudo $0"
    echo ""
    echo "Or install for current user only:"
    echo "  $0 --user"
    exit 1
fi

echo -e "Install mode: ${YELLOW}$MODE${NC}"
echo -e "Binary dir:   ${BLUE}$BIN_DIR${NC}"
echo -e "Config dir:   ${BLUE}$CONFIG_DIR${NC}"
echo ""

# ─── Step 1: Check prerequisites ──────────────────────────────────
echo -e "${YELLOW}[1/6] Checking prerequisites...${NC}"

# Check Ubuntu version
if [ -f /etc/os-release ]; then
    . /etc/os-release
    echo -e "  OS: ${GREEN}$NAME $VERSION_ID${NC}"
    if [ "${VERSION_ID%%.*}" -lt 22 ]; then
        echo -e "  ${YELLOW}⚠ Ubuntu 22.04+ recommended for PipeWire support${NC}"
    fi
fi

# Check Go (for building)
if command -v go &>/dev/null; then
    echo -e "  Go:  ${GREEN}$(go version | awk '{print $3}')${NC}"
else
    echo -e "  Go:  ${RED}not found (required to build server)${NC}"
    echo -e "       Install: sudo apt install golang-go"
    exit 1
fi

# Check PipeWire
if pactl info 2>/dev/null | grep -q "Server Name:.*[Pp]ipe[Ww]ire"; then
    echo -e "  Audio: ${GREEN}PipeWire ✓${NC}"
else
    echo -e "  Audio: ${YELLOW}⚠ PipeWire not detected (required for mic/speaker)${NC}"
    echo -e "         Install: sudo apt install pipewire pipewire-pulse wireplumber"
fi

# Check v4l2loopback
if lsmod 2>/dev/null | grep -q v4l2loopback; then
    echo -e "  v4l2:  ${GREEN}v4l2loopback loaded ✓${NC}"
else
    echo -e "  v4l2:  ${YELLOW}⚠ v4l2loopback not loaded (required for camera)${NC}"
    echo -e "         Install: sudo apt install v4l2loopback-dkms v4l2loopback-utils"
    echo -e "         Then:    sudo modprobe v4l2loopback devices=1 video_nr=10 card_label=\"AllRelay Camera\""
fi

echo ""

# ─── Step 2: Build Go server ──────────────────────────────────────
echo -e "${YELLOW}[2/6] Building AllRelay Go server...${NC}"

cd "$PROJECT_ROOT/allrelay-server"
go build -o "$PROJECT_ROOT/bin/allrelay-server" ./cmd/allrelay-server/
echo -e "  Binary: ${GREEN}$PROJECT_ROOT/bin/allrelay-server${NC}"

# ─── Step 3: Install binary ──────────────────────────────────────
echo -e "${YELLOW}[3/6] Installing binary...${NC}"

mkdir -p "$BIN_DIR"
cp "$PROJECT_ROOT/bin/allrelay-server" "$BIN_DIR/allrelay-server"
chmod 755 "$BIN_DIR/allrelay-server"
echo -e "  ${GREEN}✓ $BIN_DIR/allrelay-server${NC}"

# ─── Step 4: Install PipeWire configs ─────────────────────────────
echo -e "${YELLOW}[4/6] Installing PipeWire audio configs...${NC}"

# Determine PipeWire config directory
if [ "$MODE" = "system" ]; then
    PW_DIR="$PIPEWIRE_DIR"
else
    PW_DIR="$USER_PIPEWIRE_DIR"
fi

mkdir -p "$PW_DIR"

# Install mic config (phone mic → PipeWire source)
if [ -f "$PROJECT_ROOT/configs/pipewire/allrelay-mic.conf" ]; then
    cp "$PROJECT_ROOT/configs/pipewire/allrelay-mic.conf" "$PW_DIR/30-allrelay-mic.conf"
    echo -e "  ${GREEN}✓ $PW_DIR/30-allrelay-mic.conf${NC}"
else
    echo -e "  ${YELLOW}⚠ allrelay-mic.conf not found${NC}"
fi

# Install speaker config (PC audio → phone speaker)
if [ -f "$PROJECT_ROOT/configs/pipewire/allrelay-speaker.conf" ]; then
    cp "$PROJECT_ROOT/configs/pipewire/allrelay-speaker.conf" "$PW_DIR/30-allrelay-speaker.conf"
    echo -e "  ${GREEN}✓ $PW_DIR/30-allrelay-speaker.conf${NC}"
    echo -e "  ${YELLOW}⚠ Edit this file and replace REPLACE_ME with your phone's IP${NC}"
else
    echo -e "  ${YELLOW}⚠ allrelay-speaker.conf not found${NC}"
fi

echo ""

# ─── Step 5: Install systemd service ──────────────────────────────
echo -e "${YELLOW}[5/6] Installing systemd service...${NC}"

if [ "$MODE" = "system" ]; then
    # System-wide service
    mkdir -p "$SYSTEMD_DIR"
    
    # Create config directory for phone IP
    mkdir -p "$CONFIG_DIR"
    if [ ! -f "$CONFIG_DIR/phone_ip" ]; then
        echo "PHONE_IP=192.168.1.100" > "$CONFIG_DIR/phone_ip"
        echo -e "  ${YELLOW}⚠ Created default phone IP config: $CONFIG_DIR/phone_ip${NC}"
        echo -e "  ${YELLOW}   Edit this file with your phone's IP address${NC}"
    fi
    
    if [ -f "$PROJECT_ROOT/configs/systemd/allrelay.service" ]; then
        # Customize the service file for this installation
        sed "s|/usr/local/bin/allrelay-server|$BIN_DIR/allrelay-server|g" \
            "$PROJECT_ROOT/configs/systemd/allrelay.service" \
            > "$SYSTEMD_DIR/allrelay.service"
        chmod 644 "$SYSTEMD_DIR/allrelay.service"
        echo -e "  ${GREEN}✓ $SYSTEMD_DIR/allrelay.service${NC}"
        
        systemctl daemon-reload
        echo -e "  ${GREEN}✓ systemd reloaded${NC}"
        
        echo ""
        echo -e "${YELLOW}To enable auto-start on boot:${NC}"
        echo "  sudo systemctl enable allrelay"
        echo ""
        echo -e "${YELLOW}To start now:${NC}"
        echo "  sudo systemctl start allrelay"
    else
        echo -e "  ${YELLOW}⚠ allrelay.service not found${NC}"
    fi
else
    # User service
    mkdir -p "$USER_SYSTEMD_DIR"
    
    if [ -f "$PROJECT_ROOT/configs/systemd/allrelay.service" ]; then
        # Create a user-adapted service file
        cat > "$USER_SYSTEMD_DIR/allrelay.service" << USEREOF
[Unit]
Description=AllRelay Server (User) — Wireless phone peripherals
After=network-online.target pipewire.service pipewire-pulse.service
Wants=network-online.target

[Service]
Type=simple
Environment=PHONE_IP=192.168.1.100
ExecStart=$BIN_DIR/allrelay-server --host \${PHONE_IP}
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
USEREOF
        chmod 644 "$USER_SYSTEMD_DIR/allrelay.service"
        echo -e "  ${GREEN}✓ $USER_SYSTEMD_DIR/allrelay.service${NC}"
        
        systemctl --user daemon-reload
        echo -e "  ${GREEN}✓ user systemd reloaded${NC}"
        
        echo ""
        echo -e "${YELLOW}To enable auto-start on login:${NC}"
        echo "  systemctl --user enable allrelay"
        echo ""
        echo -e "${YELLOW}To start now:${NC}"
        echo "  systemctl --user start allrelay"
    else
        echo -e "  ${YELLOW}⚠ allrelay.service not found${NC}"
    fi
fi

# ─── Step 6: Install optional legacy discovery helper ────────────
echo -e "${YELLOW}[6/6] Installing optional legacy discovery helper...${NC}"

if [ -f "$PROJECT_ROOT/bin/mdns-discover" ]; then
    cp "$PROJECT_ROOT/bin/mdns-discover" "$BIN_DIR/allrelay-discover"
    chmod 755 "$BIN_DIR/allrelay-discover"
    echo -e "  ${GREEN}✓ $BIN_DIR/allrelay-discover${NC}"
    echo -e "  ${YELLOW}⚠ Optional only: primary discovery now uses the web dashboard UDP scan${NC}"
else
    echo -e "  ${YELLOW}⚠ Optional helper not built (run scripts/build.sh mdns if you still want it)${NC}"
fi

echo ""

# ─── Post-install instructions ────────────────────────────────────
echo -e "${GREEN}${BOLD}Installation complete! ✓${NC}"
echo ""
echo -e "${BOLD}Next steps:${NC}"
echo ""
echo -e "  1. ${BOLD}Configure phone IP:${NC}"
if [ "$MODE" = "system" ]; then
    echo "     sudoedit $CONFIG_DIR/phone_ip"
else
    echo "     edit $USER_SYSTEMD_DIR/allrelay.service (PHONE_IP)"
fi
echo ""
echo -e "  2. ${BOLD}Set up v4l2loopback (for camera):${NC}"
echo "     sudo modprobe v4l2loopback devices=1 video_nr=10 card_label=\"AllRelay Camera\""
echo ""
echo -e "  3. ${BOLD}Set up PipeWire (for mic/speaker):${NC}"
echo "     bash $PROJECT_ROOT/scripts/setup-pipewire.sh"
echo "     systemctl --user restart pipewire pipewire-pulse"
echo ""
echo -e "  4. ${BOLD}Install Magisk module on phone:${NC}"
echo "     bash $PROJECT_ROOT/scripts/build-magisk.sh"
echo "     adb push $PROJECT_ROOT/bin/allrelay-magisk.zip /sdcard/"
echo "     (flash via Magisk Manager)"
echo ""
echo -e "  5. ${BOLD}Start AllRelay:${NC}"
if [ "$MODE" = "system" ]; then
    echo "     sudo systemctl enable --now allrelay"
else
    echo "     systemctl --user enable --now allrelay"
fi
echo ""
echo -e "  6. ${BOLD}Discover phone:${NC}"
echo "     Open the web UI and use Scan (UDP subnet scan)"
echo "     Optional legacy helper: allrelay-discover"
echo ""
echo -e "${BLUE}Enjoy your wireless phone peripherals! 🎉${NC}"
