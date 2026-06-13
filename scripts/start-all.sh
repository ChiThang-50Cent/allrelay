#!/bin/bash
# start-all.sh — Start AllRelay complete system
# Usage: ./scripts/start-all.sh [web-port]

set -e

WEB_PORT="${1:-9090}"
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

echo "=========================================="
echo "🚀 AllRelay Complete System"
echo "=========================================="
echo ""

# Step 1: Check ADB
echo "[1/4] Checking ADB connection..."
if ! adb devices 2>/dev/null | grep -q "device$"; then
    echo "❌ No ADB device found"
    exit 1
fi
echo "✅ ADB connected"

# Step 2: Deploy and start Android server
echo ""
echo "[2/4] Starting Android server..."
SRV="/data/local/tmp/allrelay-$(date +%s).jar"
adb push "$PROJECT_DIR/bin/scrcpy-server-allrelay" "$SRV" 2>&1 | tail -1

adb shell "su -c 'pkill -9 -f scrcpy-server'" 2>/dev/null || true
sleep 1

adb shell "su -c 'CLASSPATH=$SRV app_process / com.genymobile.scrcpy.Server 4.0 \
    log_level=info \
    max_size=2640 \
    wifi_mode=true \
    wifi_port=5000 \
    max_fps=30 \
    video_codec=h264 \
    audio_source=mic \
    multistream=true \
    speaker_enabled=true \
    daemon=true \
    >> /data/allrelay/logs/allrelay.log 2>&1 &'"

sleep 4

if adb shell "ps -ef | grep scrcpy" 2>/dev/null | grep -v grep | head -1 | grep -q scrcpy; then
    echo "✅ Android server started"
else
    echo "❌ Android server failed"
    exit 1
fi

# Step 3: Get phone IP
echo ""
echo "[3/4] Getting phone IP..."
PHONE_IP=$(adb shell "ip route | grep wlan0 | awk '{print \$9}'" 2>/dev/null | tr -d ' \r\n')
echo "📱 Phone IP: $PHONE_IP"

# Step 4: Start Go web server
echo ""
echo "[4/4] Starting Web UI..."
echo ""
echo "=========================================="
echo "🌐 Web UI: http://localhost:$WEB_PORT"
echo "📱 Phone IP: $PHONE_IP"
echo "=========================================="
echo ""
echo "Press Ctrl+C to stop"
echo ""

exec "$PROJECT_DIR/bin/allrelay-server" --web-port "$WEB_PORT" -v
