#!/bin/bash
# allrelay-web.sh — Start AllRelay with Web UI
# Usage: ./scripts/allrelay-web.sh [port]

set -e

WEB_PORT="${1:-9090}"
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SERVER_BIN="$PROJECT_DIR/bin/allrelay-server"

echo "=========================================="
echo "🚀 AllRelay Web UI Server"
echo "=========================================="
echo ""
echo "Web UI: http://localhost:$WEB_PORT"
echo ""

# Kill existing instances
pkill -f "allrelay-server.*--web" 2>/dev/null || true
sleep 1

# Start server with web UI
# Note: --host is optional when using --web (web-only mode)
# The web UI will handle phone connections
exec "$SERVER_BIN" \
    --web \
    --web-port "$WEB_PORT" \
    --no-heartbeat \
    --no-reconnect \
    --no-adaptive \
    -v
