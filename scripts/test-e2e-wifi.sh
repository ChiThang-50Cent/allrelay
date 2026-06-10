#!/bin/bash
# AllRelay End-to-End Wi-Fi Test
# Full pipeline: start server → connect client → verify video → cleanup
#
# Prerequisites:
#   1. Android device connected via ADB (USB)
#   2. Phone and PC on same Wi-Fi network
#   3. scrcpy client built: scrcpy/x/app/scrcpy
#   4. AllRelay server built: bin/scrcpy-server-allrelay

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

SERVER_JAR="$PROJECT_ROOT/bin/scrcpy-server-allrelay"
CLIENT_BIN="$PROJECT_ROOT/scrcpy/x/app/scrcpy"
SERVER_PATH="/data/local/tmp/scrcpy-server-allrelay.jar"
TEST_TIMEOUT=8  # seconds to let client run before verifying

cleanup() {
    echo -e "\n${BLUE}[cleanup]${NC} Stopping server..."
    adb shell "pkill -f scrcpy-server" 2>/dev/null || true
    if [ -n "$SERVER_SHELL_PID" ]; then
        kill $SERVER_SHELL_PID 2>/dev/null || true
    fi
    if [ -n "$CLIENT_PID" ]; then
        kill $CLIENT_PID 2>/dev/null || true
        wait $CLIENT_PID 2>/dev/null || true
    fi
    rm -f "$SERVER_LOG" "$CLIENT_OUT" 2>/dev/null
}

trap cleanup EXIT INT TERM

# ─── Prerequisites ────────────────────────────────────────────────

echo -e "${GREEN}AllRelay E2E Wi-Fi Test${NC}"
echo "========================="
echo ""

# Check ADB
echo -n "ADB device... "
if ! adb devices | grep -q "device$"; then
    echo -e "${RED}FAIL${NC} (no device)"
    exit 1
fi
DEVICE=$(adb devices | grep "device$" | awk '{print $1}')
echo -e "${GREEN}$DEVICE${NC}"

# Get phone IP
echo -n "Phone Wi-Fi IP... "
PHONE_IP=$(adb shell ip addr show wlan0 2>/dev/null | grep "inet " | awk '{print $2}' | cut -d/ -f1)
if [ -z "$PHONE_IP" ]; then
    echo -e "${RED}FAIL${NC} (no wlan0 IP)"
    exit 1
fi
echo -e "${GREEN}$PHONE_IP${NC}"

# Check client binary
if [ ! -x "$CLIENT_BIN" ]; then
    echo -e "${RED}Client binary not found: $CLIENT_BIN${NC}"
    echo "Run: scripts/build.sh client"
    exit 1
fi
echo -e "Client binary... ${GREEN}$CLIENT_BIN${NC}"

# Check server JAR
if [ ! -f "$SERVER_JAR" ]; then
    echo -e "${RED}Server JAR not found: $SERVER_JAR${NC}"
    echo "Run: scripts/build.sh server"
    exit 1
fi
echo -e "Server JAR... ${GREEN}$SERVER_JAR${NC}"

# ─── Deploy Server ─────────────────────────────────────────────────

echo ""
echo -e "${YELLOW}[deploy]${NC} Pushing server to device..."
adb push "$SERVER_JAR" "$SERVER_PATH" >/dev/null 2>&1
echo -e "${GREEN}Server deployed${NC}"

# ─── Start Server ──────────────────────────────────────────────────

echo ""
echo -e "${YELLOW}[server]${NC} Starting AllRelay Wi-Fi server..."

# Kill any stale server first
adb shell "pkill -f scrcpy-server" 2>/dev/null || true
sleep 1

# Start server in background, capture output
SERVER_LOG=$(mktemp)
adb shell "CLASSPATH=$SERVER_PATH app_process / com.genymobile.scrcpy.Server \
    4.0 \
    wifi_mode=true \
    wifi_port=5000 \
    video=true \
    audio=false \
    control=false \
    send_device_meta=true \
    send_frame_meta=true \
    send_stream_meta=true \
    log_level=debug" > "$SERVER_LOG" 2>&1 &
SERVER_SHELL_PID=$!

# Wait for server to be ready (check log output)
echo -n "Waiting for server..."
for i in $(seq 1 10); do
    sleep 1
    if grep -q "Wi-Fi" "$SERVER_LOG" 2>/dev/null; then
        echo -e " ${GREEN}ready${NC}"
        SERVER_READY=true
        break
    fi
    echo -n "."
done

if [ "$SERVER_READY" != "true" ]; then
    echo -e " ${RED}timeout${NC}"
    echo "Server log:"
    cat "$SERVER_LOG"
    exit 1
fi

# ─── Run Client ────────────────────────────────────────────────────

echo ""
echo -e "${YELLOW}[client]${NC} Starting scrcpy client (${TEST_TIMEOUT}s timeout)..."
echo ""

CLIENT_OUT=$(mktemp)
timeout $TEST_TIMEOUT "$CLIENT_BIN" \
    --wifi \
    --tunnel-host="$PHONE_IP" \
    --no-audio \
    --no-control \
    --video-bit-rate=2M \
    --max-size=720 \
    > "$CLIENT_OUT" 2>&1 &
CLIENT_PID=$!

# Let client run for the timeout
wait $CLIENT_PID 2>/dev/null || true
CLIENT_PID=""

# ─── Verify Results ────────────────────────────────────────────────

echo ""
echo -e "${YELLOW}[verify]${NC} Checking results..."

PASS=true

# Check: Wi-Fi connection established
if grep -q "Wi-Fi connected to" "$CLIENT_OUT"; then
    DEVICE_NAME=$(grep "Wi-Fi connected to" "$CLIENT_OUT" | head -1 | sed 's/.*connected to //')
    echo -e "  Wi-Fi connection... ${GREEN}PASS${NC} (device: $DEVICE_NAME)"
else
    echo -e "  Wi-Fi connection... ${RED}FAIL${NC}"
    PASS=false
fi

# Check: Video renderer started
if grep -q "Renderer:" "$CLIENT_OUT"; then
    RENDERER=$(grep "Renderer:" "$CLIENT_OUT" | head -1)
    echo -e "  Video renderer...  ${GREEN}PASS${NC} ($RENDERER)"
else
    echo -e "  Video renderer...  ${RED}FAIL${NC}"
    PASS=false
fi

# Check: Video frames decoded (stream_id in demuxer output)
FRAME_COUNT=$(grep -c "stream_id=" "$CLIENT_OUT" 2>/dev/null || echo 0)
if [ "$FRAME_COUNT" -gt 0 ]; then
    echo -e "  Video frames...    ${GREEN}PASS${NC} ($FRAME_COUNT frames decoded)"
else
    echo -e "  Video frames...    ${RED}FAIL${NC} (no frames)"
    PASS=false
fi

# Check: Texture resolution detected
if grep -q "Texture:" "$CLIENT_OUT"; then
    RES=$(grep "Texture:" "$CLIENT_OUT" | head -1 | awk '{print $3}')
    echo -e "  Screen resolution... ${GREEN}PASS${NC} ($RES)"
else
    echo -e "  Screen resolution... ${YELLOW}WARN${NC} (not detected)"
fi

# Check: No crash/error
if grep -qEi "ERROR:|FATAL:|SIGABRT|SIGSEGV|Aborted" "$CLIENT_OUT"; then
    echo -e "  Errors...          ${RED}FAIL${NC} (errors detected)"
    PASS=false
else
    echo -e "  Errors...          ${GREEN}PASS${NC} (no errors)"
fi

# Check: Server didn't crash
if grep -q "Broken pipe" "$SERVER_LOG" 2>/dev/null; then
    echo -e "  Server cleanup...  ${GREEN}PASS${NC} (graceful disconnect)"
else
    echo -e "  Server cleanup...  ${YELLOW}WARN${NC}"
fi

# ─── Summary ───────────────────────────────────────────────────────

echo ""
if $PASS; then
    echo -e "${GREEN}═══ ALL CHECKS PASSED ═══${NC}"
    echo ""
    echo "Screen mirroring over Wi-Fi works!"
    echo "Device: $DEVICE_NAME"
    echo "Frames: $FRAME_COUNT decoded"
    exit 0
else
    echo -e "${RED}═══ SOME CHECKS FAILED ═══${NC}"
    echo ""
    echo "Client output (last 30 lines):"
    tail -30 "$CLIENT_OUT"
    echo ""
    echo "Server log (last 20 lines):"
    tail -20 "$SERVER_LOG"
    exit 1
fi
