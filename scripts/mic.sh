#!/bin/bash
# mic.sh — Test AllRelay microphone (phone mic → PC)
# Usage: ./mic.sh [--host IP]
set -e

HOST="${2:-}"
if [ -z "$HOST" ]; then
    HOST=$(adb shell "ip route | grep wlan0 | awk '{print \$9}'" 2>/dev/null || echo "192.168.1.83")
fi

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SERVER_BIN="$PROJECT_DIR/bin/allrelay-server"
JAR="$PROJECT_DIR/bin/scrcpy-server-allrelay"

echo "=== AllRelay Mic Test ==="
echo "Host: $HOST"

# Step 1: Start Android server
echo "[1/4] Starting Android server..."
SRV="/data/local/tmp/allrelay-$(date +%s).jar"
adb push "$JAR" "$SRV" 2>/dev/null
adb shell "su -c 'pkill -9 -f scrcpy-server; sleep 1'" 2>/dev/null || true
adb shell "su -c 'CLASSPATH=$SRV app_process / com.genymobile.scrcpy.Server 4.0 log_level=info max_size=2640 wifi_mode=true wifi_port=5000 max_fps=30 video_codec=h264 audio_source=mic multistream=true >> /data/allrelay/logs/allrelay.log 2>&1 &'"

# Step 2: Wait for ports
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
    adb shell "su -c 'tail -20 /data/allrelay/logs/allrelay.log'" 2>/dev/null
    exit 1
fi

# Step 3: Start Go relay (mic only, verbose)
echo "[3/4] Starting mic test..."
"$SERVER_BIN" \
    --host "$HOST" \
    --no-screen --no-camera --no-speaker --no-control \
    --no-heartbeat --no-input \
    -v \
    2>&1 | tee /tmp/allrelay-mic.log &
SERVER_PID=$!
sleep 5

if kill -0 $SERVER_PID 2>/dev/null; then
    echo "  Mic test running (PID $SERVER_PID)"
else
    echo "ERROR: Go server exited immediately"
    tail -20 /tmp/allrelay-mic.log
    exit 1
fi

echo ""
echo "============================================"
echo " Mic test running — watch for 'mic stream'"
echo " packets every 500 (~5 seconds of audio)"
echo " Make some noise near the phone!"
echo "============================================"
echo ""
echo "Press Ctrl+C to stop"
wait $SERVER_PID
