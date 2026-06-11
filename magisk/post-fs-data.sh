#!/system/bin/sh
# AllRelay Magisk Module — post-fs-data.sh
# Runs during post-fs-data phase (early boot, before zygote).
#
# This script:
#   1. Forces AAudio MMAP Exclusive mode for lowest audio latency
#   2. Loads SELinux policy for the AllRelay daemon
#   3. Applies system property overrides

MODDIR=${0%/*}

log() {
    echo "[AllRelay post-fs-data] $1" > /dev/kmsg
}

log "post-fs-data.sh starting..."

# ─── AAudio MMAP Force-Enable ─────────────────────────────────────
# Force AAudio to use MMAP Exclusive mode for all streams.
# This provides 10-20x lower audio latency compared to shared mode.
#
# mmap_policy:
#   0 = NEVER  — never use MMAP
#   1 = AUTO   — let AAudio decide (default)
#   2 = ALWAYS — always use MMAP (shared mode)
#   3 = ALWAYS — always use MMAP EXCLUSIVE (lowest latency)
#
# mmap_exclusive_policy:
#   0 = NEVER
#   1 = AUTO (default)
#   2 = ALWAYS

log "Forcing AAudio MMAP Exclusive mode..."

setprop aaudio.mmap_policy 3              # ALWAYS use MMAP EXCLUSIVE
setprop aaudio.mmap_exclusive_policy 2    # Allow EXCLUSIVE mode

# Also set PCM-related properties for lower latency
setprop media.aaudio.hw_burst_min_usec 2000  # Minimum burst for MMAP

log "AAudio MMAP Exclusive: enabled ✓"

# ─── Camera HAL properties ────────────────────────────────────────
# Some devices benefit from camera-specific tweaks
# setprop vendor.camera.aux.packagelist com.android.systemui 2>/dev/null || true

# ─── Security: Load SELinux policy ────────────────────────────────
# The policy is auto-loaded by Magisk from the sepolicy.rule file
# or from the sepolicy/ directory in the module.
# Magisk 20.4+ supports sepolicy.rule in the module root.
# For Magisk < 20.4, the manual approach below can be used.

if [ -f "$MODDIR/sepolicy/allrelay.te" ]; then
    log "SELinux policy file present at $MODDIR/sepolicy/allrelay.te"
    # Magisk will auto-load sepolicy rules
fi

# ─── Restart audioserver to apply new properties ──────────────────
# Properties set via setprop may require audioserver restart to take effect.
# This is done in post-fs-data to ensure audio is ready before apps start.
log "Restarting audioserver to apply AAudio properties..."
stop audioserver 2>/dev/null || true
sleep 1
start audioserver 2>/dev/null || true

log "post-fs-data.sh complete ✓"
