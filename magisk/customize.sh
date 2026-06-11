#!/system/bin/sh
# AllRelay Magisk Module — customize.sh
# Runs during module installation (after extraction, before reboot).
#
# This script:
#   1. Checks architecture compatibility (arm64 only)
#   2. Sets up SELinux context for the daemon binary
#   3. Creates required directories with proper permissions

MODDIR=${0%/*}

ui_print "========================================"
ui_print "  AllRelay v0.4.0-alpha"
ui_print "  Wireless phone peripherals for Ubuntu"
ui_print "========================================"

# Check architecture — only ARM64 is supported
ARCH=$(getprop ro.product.cpu.abi)
case "$ARCH" in
    arm64-v8a)
        ui_print "- Architecture: arm64-v8a ✓"
        ;;
    *)
        ui_print "! Unsupported architecture: $ARCH"
        ui_print "! AllRelay requires ARM64 (arm64-v8a)"
        abort "Unsupported architecture"
        ;;
esac

# Check Android version
API=$(getprop ro.build.version.sdk)
ui_print "- Android API level: $API"
if [ "$API" -lt 31 ]; then
    ui_print "! Android 12+ (API 31) required for Camera2 + AAudio MMAP"
    ui_print "! AllRelay may not work correctly on this device"
fi

# Set SELinux context for the daemon binary
if [ -f "$MODDIR/system/bin/allrelay-daemon" ]; then
    chmod 755 "$MODDIR/system/bin/allrelay-daemon"
    chcon u:object_r:allrelay_daemon_exec:s0 "$MODDIR/system/bin/allrelay-daemon" 2>/dev/null
    ui_print "- Daemon binary: installed ✓"
else
    ui_print "! Daemon binary not found — will use classpath launcher"
fi

# Create runtime directories
mkdir -p /data/allrelay/logs
chmod 755 /data/allrelay
chmod 755 /data/allrelay/logs

ui_print "- Runtime dirs: /data/allrelay/ ✓"
ui_print ""
ui_print "Installation complete!"
ui_print ""
ui_print "AllRelay will start automatically on next boot."
ui_print "To start now: su -c /data/adb/modules/allrelay/service.sh"
