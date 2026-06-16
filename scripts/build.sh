#!/bin/bash
# AllRelay Build Script
# Builds the Android server artifact and optional helper binaries

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}AllRelay Build Script${NC}"
echo "===================="

# Check prerequisites
check_prerequisites() {
    echo -e "\n${YELLOW}Checking prerequisites...${NC}"
    
    # Check Java
    if ! command -v java &> /dev/null; then
        echo -e "${RED}Error: Java not found. Please install JDK 17.${NC}"
        exit 1
    fi
    
    # Check Android SDK
    if [ -z "$ANDROID_SDK_ROOT" ] && [ -z "$ANDROID_HOME" ]; then
        echo -e "${RED}Error: Android SDK not found.${NC}"
        echo "Please set ANDROID_SDK_ROOT or ANDROID_HOME environment variable."
        echo "Install Android Studio or Android SDK command-line tools."
        exit 1
    fi
    
    SDK_ROOT="${ANDROID_SDK_ROOT:-$ANDROID_HOME}"
    echo -e "${GREEN}Android SDK: $SDK_ROOT${NC}"
    
    # Check if SDK has required components
    if [ ! -d "$SDK_ROOT/platforms/android-34" ]; then
        echo -e "${YELLOW}Installing Android SDK platform 34...${NC}"
        "$SDK_ROOT/cmdline-tools/latest/bin/sdkmanager" "platforms;android-34"
    fi
    
    echo -e "${GREEN}Prerequisites OK${NC}"
}

# Build Android server artifact used by ADB/Magisk/app bundling
build_server() {
    echo -e "\n${YELLOW}Building Android server artifact...${NC}"
    
    cd "$PROJECT_ROOT/scrcpy"
    
    # Use Gradle wrapper if available
    if [ -f "gradlew" ]; then
        ./gradlew :server:assembleRelease
    else
        gradle :server:assembleRelease
    fi
    
    SERVER_JAR="$PROJECT_ROOT/scrcpy/server/build/outputs/apk/release/server-release-unsigned.apk"
    
    if [ -f "$SERVER_JAR" ]; then
        echo -e "${GREEN}Server built: $SERVER_JAR${NC}"
        
        # Copy to bin directory
        mkdir -p "$PROJECT_ROOT/bin"
        cp "$SERVER_JAR" "$PROJECT_ROOT/bin/scrcpy-server-allrelay"
        echo -e "${GREEN}Copied to: $PROJECT_ROOT/bin/scrcpy-server-allrelay${NC}"
    else
        echo -e "${RED}Error: Server build failed${NC}"
        exit 1
    fi
}

# Build client (Linux)
build_client() {
    echo -e "\n${YELLOW}Building client...${NC}"
    
    cd "$PROJECT_ROOT/scrcpy"
    
    # Set PKG_CONFIG_PATH for SDL3
    export PKG_CONFIG_PATH="$HOME/.local/lib/x86_64-linux-gnu/pkgconfig:$HOME/.local/lib/pkgconfig:$PKG_CONFIG_PATH"
    
    # Clean and rebuild
    rm -rf x
    meson setup x --buildtype=release --strip -Db_lto=true \
        -Dcompile_server=false \
        -Dprebuilt_server="$PROJECT_ROOT/bin/scrcpy-server-allrelay"
    
    ninja -Cx
    
    echo -e "${GREEN}Client built: $PROJECT_ROOT/scrcpy/x/app/scrcpy${NC}"
}

# Build legacy mDNS discovery helper (optional; web UI uses UDP subnet scan)
build_mdns_tool() {
    echo -e "\n${YELLOW}Building legacy mDNS discovery helper...${NC}"
    
    cd "$PROJECT_ROOT"
    go build -o bin/mdns-discover ./cmd/mdns-discover
    
    echo -e "${GREEN}Legacy discovery helper built: $PROJECT_ROOT/bin/mdns-discover${NC}"
}

# Main menu
case "${1:-all}" in
    server)
        check_prerequisites
        build_server
        ;;
    client)
        build_client
        ;;
    mdns)
        build_mdns_tool
        ;;
    all)
        check_prerequisites
        build_server
        build_client
        build_mdns_tool
        ;;
    *)
        echo "Usage: $0 {server|client|mdns|all}"
        echo ""
        echo "Commands:"
        echo "  server  - Build Android server artifact (requires Android SDK)"
        echo "  client  - Build Linux client"
        echo "  mdns    - Build legacy mDNS discovery helper"
        echo "  all     - Build everything"
        exit 1
        ;;
esac

echo -e "\n${GREEN}Build complete!${NC}"
