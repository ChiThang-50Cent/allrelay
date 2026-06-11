#!/bin/bash
# build-magisk.sh — Package the AllRelay Magisk module into a flashable ZIP.
#
# This script:
#   1. Builds the Android server JAR (if not already built)
#   2. Copies it into the Magisk module structure
#   3. Creates a ZIP file ready to flash via Magisk Manager
#
# Usage:
#   ./scripts/build-magisk.sh
#
# Output:
#   bin/allrelay-magisk-{version}.zip

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${GREEN}AllRelay Magisk Module Builder${NC}"
echo "==============================="

# Determine version
VERSION=$(grep '^version=' "$PROJECT_ROOT/magisk/module.prop" | cut -d= -f2)
echo -e "Version: ${YELLOW}$VERSION${NC}"

# ─── Ensure Android server JAR exists ─────────────────────────────
SERVER_JAR_SRC="$PROJECT_ROOT/bin/scrcpy-server-allrelay"
SERVER_JAR_DST="$PROJECT_ROOT/magisk/system/bin/scrcpy-server-allrelay.jar"

if [ ! -f "$SERVER_JAR_SRC" ]; then
    echo -e "${YELLOW}Server JAR not found, building...${NC}"
    
    # Check Android SDK
    if [ -z "$ANDROID_SDK_ROOT" ] && [ -z "$ANDROID_HOME" ]; then
        echo -e "${RED}ERROR: Android SDK not found.${NC}"
        echo "Set ANDROID_SDK_ROOT or ANDROID_HOME."
        exit 1
    fi
    
    cd "$PROJECT_ROOT/scrcpy"
    ANDROID_SDK_ROOT="${ANDROID_SDK_ROOT:-$ANDROID_HOME}" \
        ./gradlew :server:assembleRelease
    
    # Copy from build output
    APK="$PROJECT_ROOT/scrcpy/server/build/outputs/apk/release/server-release-unsigned.apk"
    if [ -f "$APK" ]; then
        mkdir -p "$PROJECT_ROOT/bin"
        cp "$APK" "$SERVER_JAR_SRC"
        echo -e "${GREEN}Server JAR built: $SERVER_JAR_SRC${NC}"
    else
        echo -e "${RED}ERROR: Server JAR build failed${NC}"
        exit 1
    fi
fi

# ─── Copy server JAR into module ─────────────────────────────────
echo -e "${YELLOW}Copying server JAR into Magisk module...${NC}"
cp "$SERVER_JAR_SRC" "$SERVER_JAR_DST"
echo -e "${GREEN}  ✓ $SERVER_JAR_DST${NC}"

# ─── Make scripts executable ──────────────────────────────────────
chmod +x "$PROJECT_ROOT/magisk/customize.sh"
chmod +x "$PROJECT_ROOT/magisk/service.sh"
chmod +x "$PROJECT_ROOT/magisk/post-fs-data.sh"
chmod +x "$PROJECT_ROOT/magisk/system/bin/allrelay-daemon"
chmod +x "$PROJECT_ROOT/magisk/META-INF/com/google/android/update-binary"

# ─── Create ZIP ───────────────────────────────────────────────────
OUTPUT_DIR="$PROJECT_ROOT/bin"
OUTPUT_FILE="$OUTPUT_DIR/allrelay-magisk-${VERSION}.zip"

mkdir -p "$OUTPUT_DIR"

echo -e "${YELLOW}Creating Magisk module ZIP...${NC}"

cd "$PROJECT_ROOT/magisk"
zip -r "$OUTPUT_FILE" \
    module.prop \
    customize.sh \
    service.sh \
    post-fs-data.sh \
    system/ \
    sepolicy/ \
    META-INF/ \
    -x "*.DS_Store" \
    -x "*.swp"

echo -e "${GREEN}  ✓ $OUTPUT_FILE${NC}"

# ─── Summary ──────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}Magisk module built successfully!${NC}"
echo ""
echo "To install on your phone:"
echo "  1. Push the ZIP to your phone:"
echo "     adb push $OUTPUT_FILE /sdcard/"
echo ""
echo "  2. Open Magisk Manager → Modules → Install from storage"
echo "     Select: allrelay-magisk-${VERSION}.zip"
echo ""
echo "  3. Reboot your phone"
echo ""
echo "  4. Verify:"
echo "     adb shell su -c allrelay-daemon status"
echo ""
echo "File sizes:"
ls -lh "$OUTPUT_FILE"
