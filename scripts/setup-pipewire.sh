#!/bin/bash
# AllRelay PipeWire Setup Script
# Installs PipeWire config files for AllRelay phone mic and speaker.
#
# Usage:
#   ./scripts/setup-pipewire.sh [--user] [--system]
#
#   --user    Install to ~/.config/pipewire/ (default)
#   --system  Install to /etc/pipewire/ (requires sudo)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

MODE="user"
TARGET_DIR=""

while [[ $# -gt 0 ]]; do
    case $1 in
        --user)   MODE="user"; shift ;;
        --system) MODE="system"; shift ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

if [ "$MODE" = "user" ]; then
    TARGET_DIR="$HOME/.config/pipewire/pipewire.conf.d"
    mkdir -p "$TARGET_DIR"
    echo -e "${GREEN}Installing to user config: $TARGET_DIR${NC}"
else
    TARGET_DIR="/etc/pipewire/pipewire.conf.d"
    if [ "$EUID" -ne 0 ]; then
        echo -e "${RED}--system requires sudo${NC}"
        exit 1
    fi
    mkdir -p "$TARGET_DIR"
    echo -e "${GREEN}Installing to system config: $TARGET_DIR${NC}"
fi

# Copy mic config
cp "$PROJECT_ROOT/configs/pipewire/allrelay-mic.conf" \
   "$TARGET_DIR/30-allrelay-mic.conf"
echo -e "${GREEN}  ✓ allrelay-mic.conf${NC}"

# Copy speaker config (warn about REPLACE_ME)
SPEAKER_CONF="$TARGET_DIR/30-allrelay-speaker.conf"
cp "$PROJECT_ROOT/configs/pipewire/allrelay-speaker.conf" "$SPEAKER_CONF"
echo -e "${GREEN}  ✓ allrelay-speaker.conf${NC}"
echo -e "${YELLOW}  ⚠ Speaker config needs phone IP (search for REPLACE_ME)${NC}"

echo ""
echo -e "${YELLOW}Restart PipeWire to apply changes:${NC}"
echo "  systemctl --user restart pipewire pipewire-pulse"
echo ""
echo -e "${YELLOW}Verify the virtual devices:${NC}"
echo "  pactl list sources short | grep allrelay"
echo "  pactl list sinks short | grep allrelay"
