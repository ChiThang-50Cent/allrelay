#!/system/bin/sh
# AllRelay Magisk Module — service.sh
# Runs during late_start service phase (after boot is complete).
#
# This script:
#   1. Waits for Wi-Fi to be connected
#   2. Hides recording indicators (privacy green dot)
#   3. Starts the AllRelay Java server as a background daemon
#   4. Starts discovery responders (UDP fallback; mDNS best-effort)
#   5. Monitors and restarts if crashed

MODDIR=${0%/*}
LOG_DIR=/data/allrelay/logs
LOG_FILE="$LOG_DIR/allrelay.log"
PID_FILE="$LOG_DIR/allrelay.pid"
HEARTBEAT_FILE="$LOG_DIR/heartbeat.ts"

# Ensure log directory exists
mkdir -p "$LOG_DIR"
chmod 755 "$LOG_DIR"

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" >> "$LOG_FILE"
}

log "AllRelay service starting..."
log "MODDIR=$MODDIR"

# ─── Hide recording indicators (Android 12+) ──────────────────────
# Disable the green privacy dot for screen/camera/mic.
# Uses device_config which requires the SET_DEVICE_CONFIG permission
# (granted via root or system-level permissions).
hide_indicators() {
    log "Hiding recording indicators..."
    device_config put privacy media_projection_indicators_enabled false default 2>/dev/null || true
    device_config put privacy camera_indicators_enabled false default 2>/dev/null || true
    device_config put privacy microphone_indicators_enabled false default 2>/dev/null || true
    # Also hide the screen record chip indicator (Android 15+)
    device_config put privacy camera_privacy_allow_list "*" default 2>/dev/null || true
    log "Indicators hidden ✓"
}

# ─── Wait for Wi-Fi connectivity ──────────────────────────────────
wait_for_wifi() {
    local max_wait=30
    local waited=0
    
    log "Waiting for Wi-Fi..."
    while [ $waited -lt $max_wait ]; do
        # Check if Wi-Fi is connected and has an IP
        WIFI_IP=$(cmd wifi status 2>/dev/null | sed -n 's/.*IP Address: *\([0-9.]*\).*/\1/p' || true)
        if [ -z "$WIFI_IP" ]; then
            # Fallback: check if wlan0 has an IP
            WIFI_IP=$(ip addr show wlan0 2>/dev/null | sed -n 's/.*inet \([0-9.]*\).*/\1/p' || true)
        fi
        
        if [ -n "$WIFI_IP" ] && [ "$WIFI_IP" != "0.0.0.0" ]; then
            log "Wi-Fi connected: $WIFI_IP"
            return 0
        fi
        
        sleep 2
        waited=$((waited + 2))
    done
    
    log "Wi-Fi not connected after ${max_wait}s, starting anyway"
    return 1
}

# ─── Start AllRelay Java server ───────────────────────────────────
start_server() {
    local SERVER_JAR="$MODDIR/system/bin/scrcpy-server-allrelay.jar"
    
    if [ ! -f "$SERVER_JAR" ]; then
        log "ERROR: Server JAR not found at $SERVER_JAR"
        return 1
    fi
    
    log "Starting AllRelay server..."
    
    # Kill existing instances
    pkill -f "scrcpy-server" 2>/dev/null || true
    sleep 1
    
    # Launch with app_process (runs in system_server context with root)
    # daemon=true: keep alive, accept reconnections without restart
    # screen/control enabled: remote popup uses ports 5000 + 5004
    # speaker_enabled=true: PC→phone audio on port 5003
    # camera_enabled=true: phone camera→PC on port 5001
    # audio=true + audio_source=mic: phone mic daemon on port 5002
    # power_on=false: do not wake the phone when the control channel attaches
    # keep_active=true: keep the device awake while remote screen mode is in use
    nohup sh -c "
        CLASSPATH='$SERVER_JAR' \
        app_process / com.genymobile.scrcpy.Server \
            4.0 \
            log_level=info \
            wifi_mode=true \
            wifi_port=5000 \
            video=true \
            audio=true \
            audio_source=mic \
            speaker_enabled=true \
            camera_enabled=true \
            daemon=true \
            control=true \
            power_on=false \
            keep_active=true \
            >> '$LOG_FILE' 2>&1
    " &
    
    local pid=$!
    echo $pid > "$PID_FILE"
    log "Server started with PID: $pid"
    
    # Verify it's running after 2 seconds
    sleep 2
    if kill -0 $pid 2>/dev/null; then
        log "Server running ✓"
        return 0
    else
        log "ERROR: Server failed to start (check $LOG_FILE)"
        return 1
    fi
}

# ─── Heartbeat updater (for status display on phone) ─────────────
update_heartbeat() {
    date +%s > "$HEARTBEAT_FILE"
}

# ─── Monitor loop — log when daemon dies, but do NOT restart
# Restart control is delegated to the AllRelay app (ToggleActivity).
# Magisk service only auto-starts at boot time.
monitor_loop() {
    while true; do
        local pid
        if [ -f "$PID_FILE" ]; then
            pid=$(cat "$PID_FILE")
            if kill -0 "$pid" 2>/dev/null; then
                update_heartbeat
                sleep 30
                continue
            fi
        fi
        
        # Server died — log and exit, do NOT restart
        log "Server stopped (not restarting — use AllRelay app to control)"
        rm -f "$PID_FILE"
        return 0
    done
}

# ─── Main ─────────────────────────────────────────────────────────
log "===== AllRelay boot ====="
hide_indicators
wait_for_wifi
start_server
monitor_loop &

# Update heartbeat to indicate service started
update_heartbeat

log "AllRelay service initialization complete"
