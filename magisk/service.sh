#!/system/bin/sh
# AllRelay Magisk Module — service.sh
# Runs during late_start service phase (after boot is complete).
#
# This script:
#   1. Waits for Wi-Fi to be connected
#   2. Hides recording indicators (privacy green dot)
#   3. Starts the AllRelay Java server as a background daemon
#   4. Sets up mDNS advertising
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
    # speaker_enabled=true: PC→phone audio on port 5003
    # camera_enabled=true: phone camera→PC on port 5001
    # no_video=true audio=false: disable screen+mic (handled separately)
    nohup sh -c "
        CLASSPATH='$SERVER_JAR' \
        app_process / com.genymobile.scrcpy.Server \
            4.0 \
            log_level=info \
            wifi_mode=true \
            wifi_port=5000 \
            no_video=true \
            audio=false \
            speaker_enabled=true \
            camera_enabled=true \
            daemon=true \
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

# ─── Monitor loop — restart if process dies ──────────────────────
# With daemon=true, the Java server handles stream reconnections
# internally. We only restart if the entire process crashes.
monitor_loop() {
    local max_restarts=10
    local restart_count=0
    
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
        
        # Server died — restart with backoff
        restart_count=$((restart_count + 1))
        
        if [ $restart_count -gt $max_restarts ]; then
            log "ERROR: Max restarts ($max_restarts) exceeded. Giving up."
            return 1
        fi
        
        log "Server crashed! Restarting (attempt $restart_count/$max_restarts)..."
        sleep 5
        start_server
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
