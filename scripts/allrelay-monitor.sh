#!/bin/bash
# allrelay-monitor.sh — Monitor and restart Android server when needed
# Usage: ./scripts/allrelay-monitor.sh

set -e

LOG_FILE="/data/allrelay/logs/monitor.log"

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"
}

deploy_jar() {
    SRV="/data/local/tmp/allrelay-$(date +%s).jar"
    adb push bin/scrcpy-server-allrelay "$SRV" 2>&1 | tail -1
    echo "$SRV"
}

start_server() {
    local jar=$1
    log "Starting Android server..."
    
    adb shell "su -c 'pkill -9 -f scrcpy-server'" 2>/dev/null || true
    sleep 1
    
    adb shell "su -c 'CLASSPATH=$jar app_process / com.genymobile.scrcpy.Server 4.0 \
        log_level=info \
        wifi_mode=true \
        wifi_port=5000 \
        audio_source=mic \
        multistream=true \
        speaker_enabled=true \
        daemon=true \
        >> /data/allrelay/logs/allrelay.log 2>&1 &'"
    
    sleep 4
    log "Android server started"
}

check_ports() {
    local count=$(adb shell "ss -tlnp | grep -c 500" 2>/dev/null | tr -d ' \r\n')
    [ "$count" -ge 4 ]
}

# Main
log "=== AllRelay Monitor Starting ==="

# Deploy JAR
JAR=$(deploy_jar)
log "Deployed JAR: $JAR"

# Start server
start_server "$JAR"

# Monitor loop
while true; do
    if ! check_ports; then
        log "⚠️  Ports not listening! Restarting server..."
        start_server "$JAR"
    fi
    sleep 3
done
