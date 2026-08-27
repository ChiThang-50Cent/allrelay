#!/bin/bash
# Build the AllRelay debug APK with its bundled Android server.
set -e

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN_DIR="$ROOT/bin"
SERVER_ARTIFACT="$ROOT/scrcpy/server/build/outputs/apk/release/server-release-unsigned.apk"
APK_SOURCE="$ROOT/android/app/build/outputs/apk/debug/app-debug.apk"
APK_DESTINATION="$BIN_DIR/allrelay-app-debug.apk"

mkdir -p "$BIN_DIR"
rm -f "$SERVER_ARTIFACT" "$APK_DESTINATION" "$BIN_DIR/scrcpy-server-allrelay" "$BIN_DIR/allrelay-magisk.zip"

echo "=== [1/2] Build Android server ==="
(
    cd "$ROOT/scrcpy"
    ./gradlew :server:assembleRelease
)

if [ ! -f "$SERVER_ARTIFACT" ]; then
    echo "ERROR: Android server artifact was not produced" >&2
    exit 1
fi

echo "=== [2/2] Build debug APK ==="
(
    cd "$ROOT/android"
    ./gradlew :app:assembleDebug
)
cp "$APK_SOURCE" "$APK_DESTINATION"

echo "APK: $APK_DESTINATION"
