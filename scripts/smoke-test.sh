#!/bin/bash
# AllRelay Smoke Test — Verify PC-side health after web connect
# Run this after installing .deb and connecting from dashboard

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

PASS=0
FAIL=0

ok()   { echo -e "  ${GREEN}✓${NC} $1"; PASS=$((PASS+1)); }
warn() { echo -e "  ${YELLOW}⚠${NC} $1"; }
err()  { echo -e "  ${RED}✗${NC} $1"; FAIL=$((FAIL+1)); }

info() {
    echo ""
    echo -e "${YELLOW}▸ $1${NC}"
}

# ─── 1. Service status ────────────────────────────────────────
info "1. AllRelay user service"
if systemctl --user is-active allrelay >/dev/null 2>&1; then
    ok "allrelay.service is active"
else
    err "allrelay.service is NOT active"
fi

# ─── 2. Web UI reachable ──────────────────────────────────────
info "2. Web UI"
if curl -fs http://localhost:9090 >/dev/null 2>&1; then
    ok "http://localhost:9090 reachable"
else
    err "localhost:9090 not responding"
fi

# ─── 3. Phone connection status ─────────────────────────────────
info "3. Phone connection"
STATUS=$(curl -fs http://localhost:9090/api/status 2>/dev/null || true)
if [ -n "$STATUS" ]; then
    if echo "$STATUS" | grep -q '"connected":true'; then
        ok "API reports connected"
    else
        err "API reports disconnected"
    fi
    IP=$(echo "$STATUS" | grep -oP '"ip":"\K[^"]+' | head -1)
    [ -n "$IP" ] && ok "Phone IP: $IP"
else
    err "Cannot fetch /api/status"
fi

# ─── 4. TCP stream sockets ─────────────────────────────────────
info "4. TCP connections to phone"
if [ -n "$IP" ]; then
    for PORT in 5000 5001 5002 5003 5004; do
        if ss -t | grep -q "ESTAB.*$IP:$PORT"; then
            ok "TCP $PORT connected"
        else
            warn "TCP $PORT not established (may be inactive stream)"
        fi
    done
else
    warn "Skipping TCP check (no IP)"
fi

# ─── 5. v4l2loopback camera ───────────────────────────────────
info "5. Virtual camera (v4l2loopback)"
if [ -c /dev/video10 ]; then
    ok "/dev/video10 exists"
    if v4l2-ctl -d /dev/video10 --all 2>/dev/null | grep -q "AllRelay"; then
        ok "Device label matches 'AllRelay'"
    else
        warn "Device label may differ"
    fi
else
    warn "/dev/video10 missing (camera not active or module not loaded)"
fi

# ─── 6. PulseAudio mic source ─────────────────────────────────
info "6. Virtual microphone (PulseAudio)"
if pactl list sources short 2>/dev/null | grep -q "allrelay-phone-mic\|AllRelay-Phone-Mic"; then
    ok "Mic source visible"
else
    warn "Mic source not found (mic stream off?)"
fi

# ─── 7. PulseAudio speaker sink ───────────────────────────────
info "7. Virtual speaker sink (PulseAudio)"
if pactl list sinks short 2>/dev/null | grep -q "allrelay-mic-sink\|AllRelay-Mic-Sink"; then
    ok "Speaker sink visible"
else
    warn "Speaker sink not found (speaker stream off?)"
fi

# ─── 8. Server logs (last errors) ─────────────────────────────
info "8. Recent server logs"
ERRORS=$(journalctl --user -u allrelay --since "5 minutes ago" -p err --no-pager 2>/dev/null || true)
if [ -n "$ERRORS" ]; then
    warn "Errors in last 5 min:"
    echo "$ERRORS" | tail -6
else
    ok "No errors in last 5 min"
fi

# ─── Summary ──────────────────────────────────────────────────
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo -e "${GREEN}PASS: $PASS${NC}  ${RED}FAIL: $FAIL${NC}"
if [ "$FAIL" -eq 0 ]; then
    echo -e "${GREEN}Smoke test OK — ready to open dashboard and toggle streams${NC}"
else
    echo -e "${YELLOW}Some checks failed — review logs above${NC}"
fi
