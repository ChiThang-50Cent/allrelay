#!/system/bin/sh
# [DEPRECATED] AllRelay Wi-Fi Mode Wrapper — superseded by native Wi-Fi mode
# Legacy wrapper: uses socat/netcat to forward connections
# The server now natively supports Wi-Fi mode via wifi_mode=true.

# Configuration
WIFI_PORT=${WIFI_PORT:-5000}
VIDEO=${VIDEO:-true}
AUDIO=${AUDIO:-false}
CONTROL=${CONTROL:-false}

# Get device IP
DEVICE_IP=$(ip addr show wlan0 2>/dev/null | grep "inet " | awk '{print $2}' | cut -d/ -f1)

echo "AllRelay Wi-Fi Mode Wrapper"
echo "Device IP: $DEVICE_IP"
echo "Port: $WIFI_PORT"

# Start scrcpy-server in ADB mode (forward mode)
# The server will listen on localhost, and we forward to it
CLASSPATH=/data/local/tmp/scrcpy-server.jar app_process / com.genymobile.scrcpy.Server \
    4.0 \
    tunnel_forward=true \
    video=$VIDEO \
    audio=$AUDIO \
    control=$CONTROL \
    send_device_meta=true \
    send_frame_meta=true \
    send_dummy_byte=true \
    send_stream_meta=true
