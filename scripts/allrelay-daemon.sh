#!/bin/bash
# allrelay-daemon.sh — Keep AllRelay Android server running
# Usage: ./scripts/allrelay-daemon.sh

set -e

LOG_FILE="/data/allrelay/logs/allrelay.log"
PID_FILE="/data/allrelay/logs/daemon.pid"
JAR_PATH=""

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"
}

find_jar() {
    # Find the latest JAR
    JAR_PATH=$(adb shell "ls -t /data/local/tmp/allrelay-*.jar 2>/dev/null | head -1" 2>/dev/null | tr -d ' \r\n')
    
    if [ -z "$JAR_PATH" ]; then
        log "No JAR found, deploying..."
        JAR_PATH="/data/local/tmp/allrelay-$(date +%s).jar"
        adb push bin/scrcpy-server-allrelay "$JAR_PATH" 2>&1 | tail -1
    fi
    
    log "Using JAR: $JAR_PATH"
}

start_server() {
    log "Starting Android server..."
    
    # Kill existing
    adb shell "su -c 'pkill -9 -f scrcpy-server'" 2>/dev/null || true
    sleep 1
    
    # Start server
    adb shell "su -c 'CLASSPATH=$JAR_PATH app_process / com.genymobile.scrcpy.Server 4.0 \
        log_level=info \
        max_size=2640 \
        wifi_mode=true \
        wifi_port=5000 \
        max_fps=30 \
        video_codec=h264 \
        audio_source=mic \
        multistream=true \
        speaker_enabled=true \
        >> $LOG_FILE 2>&1 &'"
    
    sleep 3
    
    # Verify
    if adb shell "ps -ef | grep scrcpy" 2>/dev/null | grep -v grep | head -1 | grep -q scrcpy; then
        log "✅ Server started"
        return 0
    else
        log "❌ Server failed to start"
        return 1
    fi
}

check_server() {
    if adb shell "ps -ef | grep scrcpy" 2>/dev/null | grep -v grep | head -1 | grep -q scrcpy; then
        return 0
    else
        return 1
    fi
}

monitor_loop() {
    log "Starting monitor loop..."
    
    while true; do
        if ! check_server; then
            log "Server not running, restarting..."
            start_server
        fi
        
        sleep 10
    done
}

# Main
log "=== AllRelay Daemon Starting ==="

# Check ADB connection
if ! adb devices 2>/dev/null | grep -q "device$"; then
    log "No ADB device found"
    exit 1
fi

# Find or deploy JAR
find_jar

# Start server
start_server

# Start monitor
monitor_loop
