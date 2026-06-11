#!/bin/bash
# camera.sh — Start AllRelay camera on Ubuntu
# Usage: ./camera.sh [--host IP]
# Default host: 192.168.1.83 (auto-detected via adb if available)
set -e

HOST="${2:-}"
if [ -z "$HOST" ]; then
    # Try to get device IP from adb
    HOST=$(adb shell "ip route | grep wlan0 | awk '{print \$9}'" 2>/dev/null || echo "192.168.1.83")
fi

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SERVER_BIN="$PROJECT_DIR/bin/allrelay-server"
JAR="$PROJECT_DIR/bin/scrcpy-server-allrelay"

echo "=== AllRelay Camera ==="
echo "Host: $HOST"
echo "Output: /dev/video10"

# Step 1: Start Android server
echo "[1/4] Starting Android server..."
SRV="/data/local/tmp/allrelay-$(date +%s).jar"
adb push "$JAR" "$SRV" 2>/dev/null
adb shell "su -c 'pkill -9 -f scrcpy-server; sleep 1'" 2>/dev/null || true
adb shell "su -c 'CLASSPATH=$SRV app_process / com.genymobile.scrcpy.Server 4.0 log_level=info max_size=2640 camera_size=1920x1080 wifi_mode=true wifi_port=5000 max_fps=30 video_codec=h264 audio_source=mic multistream=true >> /data/allrelay/logs/allrelay.log 2>&1 &'"
# Step 2: Wait for all 5 ports to be open
echo "[2/4] Waiting for server..."
for i in $(seq 1 20); do
    PORTS=$(adb shell "su -c 'ss -tlnp | grep -c 500[0-9] || echo 0'" 2>/dev/null | tr -d ' \r\n')
    if [ "$PORTS" = "5" ]; then
        echo "  Server ready ($PORTS ports open)"
        break
    fi
    [ $((i % 5)) -eq 0 ] && echo "  Still waiting... ($PORTS/5 ports, ${i}s)"
    sleep 1
done

if [ "$PORTS" != "5" ]; then
    echo "ERROR: Server only has $PORTS ports (expected 5)"
    echo "Check log: adb shell su -c 'tail -20 /data/allrelay/logs/allrelay.log'"
    exit 1
fi

# Step 3: Start Go relay server
echo "[3/4] Starting AllRelay relay..."
if [ ! -x "$SERVER_BIN" ]; then
    echo "Building Go server..."
    (cd "$PROJECT_DIR/allrelay-server" && go build -o "$SERVER_BIN" ./cmd/allrelay-server/)
fi

"$SERVER_BIN" \
    --host "$HOST" \
    --no-screen --no-mic --no-speaker --no-control \
    --no-heartbeat --no-input \
    2>&1 &
SERVER_PID=$!
sleep 5

# Step 3: Verify v4l2
echo "[4/4] Verifying camera..."
sleep 10  # Wait for camera encoding to start (server accept timeouts)

FMT=$(v4l2-ctl -d /dev/video10 --get-fmt-video-out 2>&1 | grep "Width/Height" || echo "No signal")
if echo "$FMT" | grep -q "1920"; then
    echo "  Camera LIVE! $FMT"
    echo ""
    echo "============================================"
    echo " Camera is now available at /dev/video10"
    echo " Resolution: 1920x1080 YUYV"
    echo " Test with:  ffplay /dev/video10"
    echo "             cheese --device=/dev/video10"
    echo "             v4l2-ctl -d /dev/video10 --get-fmt-video-out"
    echo "============================================"
    echo ""
    echo "Press Ctrl+C to stop"
    wait $SERVER_PID
else
    echo "  Camera still initializing (current: $FMT)"
    echo "  Waiting for stream..."
    wait $SERVER_PID
fi
