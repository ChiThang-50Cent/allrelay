#!/bin/bash
# AllRelay Wi-Fi Transport Test
# Tests the TCP connection between PC and Android

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${GREEN}AllRelay Wi-Fi Transport Test${NC}"
echo "============================="

# Check if phone is connected via ADB
check_adb() {
    echo -e "\n${YELLOW}Checking ADB connection...${NC}"
    
    if ! adb devices | grep -q "device$"; then
        echo -e "${RED}No Android device connected via ADB.${NC}"
        echo "Please connect your phone via USB and enable USB debugging."
        exit 1
    fi
    
    DEVICE=$(adb devices | grep "device$" | awk '{print $1}')
    echo -e "${GREEN}Device connected: $DEVICE${NC}"
}

# Get phone's Wi-Fi IP
get_phone_ip() {
    echo -e "\n${YELLOW}Getting phone's Wi-Fi IP...${NC}"
    
    PHONE_IP=$(adb shell ip addr show wlan0 2>/dev/null | grep "inet " | awk '{print $2}' | cut -d/ -f1)
    
    if [ -z "$PHONE_IP" ]; then
        echo -e "${RED}Could not get phone's Wi-Fi IP.${NC}"
        echo "Make sure Wi-Fi is enabled on the phone."
        exit 1
    fi
    
    echo -e "${GREEN}Phone IP: $PHONE_IP${NC}"
}

# Test TCP connectivity
test_tcp_connection() {
    echo -e "\n${YELLOW}Testing TCP connectivity...${NC}"
    
    PORT=5000
    
    # Start a simple TCP listener on phone
    echo "Starting TCP listener on phone (port $PORT)..."
    adb shell "nc -l -p $PORT &" &
    NC_PID=$!
    sleep 1
    
    # Try to connect from PC
    echo "Connecting from PC to $PHONE_IP:$PORT..."
    if timeout 5 bash -c "echo 'test' | nc -w 3 $PHONE_IP $PORT" 2>/dev/null; then
        echo -e "${GREEN}TCP connection successful!${NC}"
    else
        echo -e "${RED}TCP connection failed.${NC}"
        echo "Possible issues:"
        echo "  1. Phone and PC are on different networks"
        echo "  2. Firewall blocking connection"
        echo "  3. Phone's Wi-Fi is not connected"
        kill $NC_PID 2>/dev/null || true
        exit 1
    fi
    
    kill $NC_PID 2>/dev/null || true
}

# Test port range
test_port_range() {
    echo -e "\n${YELLOW}Testing port range (5000-5004)...${NC}"
    
    for PORT in 5000 5002 5004; do
        echo -n "Testing port $PORT... "
        
        # Start listener
        adb shell "nc -l -p $PORT &" &
        NC_PID=$!
        sleep 0.5
        
        # Test connection
        if timeout 3 bash -c "echo 'test' | nc -w 2 $PHONE_IP $PORT" 2>/dev/null; then
            echo -e "${GREEN}OK${NC}"
        else
            echo -e "${RED}FAILED${NC}"
        fi
        
        kill $NC_PID 2>/dev/null || true
    done
}

# Test with real scrcpy-server
test_with_server() {
    echo -e "\n${YELLOW}Testing with scrcpy-server...${NC}"
    
    # Push server to phone
    echo "Pushing scrcpy-server to phone..."
    adb push "$PROJECT_ROOT/bin/scrcpy-server-allrelay" /data/local/tmp/scrcpy-server-allrelay.jar
    
    # Start server in Wi-Fi mode
    echo "Starting scrcpy-server in Wi-Fi mode..."
    adb shell "CLASSPATH=/data/local/tmp/scrcpy-server-allrelay.jar app_process / com.genymobile.scrcpy.Server 4.0 wifi_mode=true wifi_port=5000 video=true audio=false control=false" &
    SERVER_PID=$!
    
    sleep 3
    
    # Test connection
    echo "Testing connection to $PHONE_IP:5000..."
    if timeout 5 bash -c "echo '' | nc -w 3 $PHONE_IP 5000" >/dev/null 2>&1; then
        echo -e "${GREEN}Server is listening on port 5000!${NC}"
    else
        echo -e "${RED}Server not responding on port 5000${NC}"
    fi
    
    # Cleanup
    kill $SERVER_PID 2>/dev/null || true
    adb shell "pkill -f scrcpy-server" 2>/dev/null || true
}

# Main
check_adb
get_phone_ip
test_tcp_connection
test_port_range
test_with_server

echo -e "\n${GREEN}Transport layer test complete!${NC}"
echo ""
echo "For full end-to-end test with video, run:"
echo "  scripts/test-e2e-wifi.sh"
