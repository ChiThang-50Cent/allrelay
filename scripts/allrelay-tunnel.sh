#!/bin/bash
# [DEPRECATED] AllRelay Wi-Fi Tunnel — superseded by native Wi-Fi mode (--wifi)
# Legacy helper: creates ADB reverse tunnel to use original scrcpy client over Wi-Fi
# Use "scrcpy --wifi --tunnel-host=<ip>" instead.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${GREEN}AllRelay Wi-Fi Tunnel${NC}"
echo "===================="

# Check if socat is installed
if ! command -v socat &> /dev/null; then
    echo -e "${RED}Error: socat not found.${NC}"
    echo "Install with: sudo apt-get install socat"
    exit 1
fi

# Get phone IP
PHONE_IP=$(adb shell ip addr show wlan0 2>/dev/null | grep "inet " | awk '{print $2}' | cut -d/ -f1)
if [ -z "$PHONE_IP" ]; then
    echo -e "${RED}Could not get phone's Wi-Fi IP.${NC}"
    exit 1
fi

echo -e "${GREEN}Phone IP: $PHONE_IP${NC}"

# Start scrcpy-server on phone in forward mode
echo -e "${YELLOW}Starting scrcpy-server on phone...${NC}"
adb shell "CLASSPATH=/data/local/tmp/scrcpy-server.jar app_process / com.genymobile.scrcpy.Server 4.0 tunnel_forward=true video=true audio=true control=true" &
SERVER_PID=$!

sleep 2

# Create SSH tunnel from PC to phone
# This forwards local ports to the phone's scrcpy-server
echo -e "${YELLOW}Creating SSH tunnel...${NC}"

# Use ADB reverse to forward ports
# This makes the phone's scrcpy-server accessible on localhost
adb reverse tcp:27183 tcp:27183 2>/dev/null || true
adb reverse tcp:27184 tcp:27184 2>/dev/null || true
adb reverse tcp:27185 tcp:27185 2>/dev/null || true

echo -e "${GREEN}Tunnel created!${NC}"
echo ""
echo "Now run scrcpy in another terminal:"
echo "  scrcpy --tunnel-host=localhost --tunnel-port=27183"
echo ""
echo "Press Ctrl+C to stop the tunnel"

# Wait for interrupt
trap "kill $SERVER_PID 2>/dev/null; adb reverse --remove tcp:27183 2>/dev/null; exit" INT TERM
wait $SERVER_PID
