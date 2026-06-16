#!/bin/bash
# AllRelay Build Script
# Builds the Android server artifact used by ADB/Magisk/app bundling.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

check_prerequisites() {
    echo -e "\n${YELLOW}Checking prerequisites...${NC}"

    if ! command -v java &> /dev/null; then
        echo -e "${RED}Error: Java not found. Please install JDK 17.${NC}"
        exit 1
    fi

    if [ -z "$ANDROID_SDK_ROOT" ] && [ -z "$ANDROID_HOME" ]; then
        echo -e "${RED}Error: Android SDK not found.${NC}"
        echo "Please set ANDROID_SDK_ROOT or ANDROID_HOME."
        exit 1
    fi

    SDK_ROOT="${ANDROID_SDK_ROOT:-$ANDROID_HOME}"
    echo -e "${GREEN}Android SDK: $SDK_ROOT${NC}"

    if [ ! -d "$SDK_ROOT/platforms/android-34" ]; then
        echo -e "${YELLOW}Installing Android SDK platform 34...${NC}"
        "$SDK_ROOT/cmdline-tools/latest/bin/sdkmanager" "platforms;android-34"
    fi

    echo -e "${GREEN}Prerequisites OK${NC}"
}

build_server() {
    echo -e "\n${YELLOW}Building Android server artifact...${NC}"

    cd "$PROJECT_ROOT/scrcpy"
    ./gradlew :server:assembleRelease

    SERVER_APK="$PROJECT_ROOT/scrcpy/server/build/outputs/apk/release/server-release-unsigned.apk"
    if [ ! -f "$SERVER_APK" ]; then
        echo -e "${RED}Error: server build failed${NC}"
        exit 1
    fi

    mkdir -p "$PROJECT_ROOT/bin"
    cp "$SERVER_APK" "$PROJECT_ROOT/bin/scrcpy-server-allrelay"
    echo -e "${GREEN}Copied to: $PROJECT_ROOT/bin/scrcpy-server-allrelay${NC}"
}

case "${1:-server}" in
    server)
        check_prerequisites
        build_server
        ;;
    *)
        echo "Usage: $0 [server]"
        exit 1
        ;;
esac

echo -e "\n${GREEN}Build complete!${NC}"
