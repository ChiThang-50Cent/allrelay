#!/bin/sh
set -eu

SERVICE="allrelay.service"
RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
URL_FILE="$RUNTIME_DIR/allrelay/url"

wait_for_url() {
    systemctl --user start "$SERVICE" >/dev/null
    i=0
    while [ "$i" -lt 100 ]; do
        if [ -s "$URL_FILE" ]; then
            return 0
        fi
        sleep 0.1
        i=$((i + 1))
    done
    echo "Timed out waiting for AllRelay URL file: $URL_FILE" >&2
    return 1
}

usage() {
    cat <<EOF
Usage: allrelay [open|url|status|start|stop|restart|logs]
EOF
}

cmd="${1:-open}"

case "$cmd" in
    open)
        wait_for_url
        url=$(tr -d '\n' < "$URL_FILE")
        if command -v xdg-open >/dev/null 2>&1; then
            xdg-open "$url" >/dev/null 2>&1 || echo "$url"
        else
            echo "$url"
        fi
        ;;
    url)
        wait_for_url
        tr -d '\n' < "$URL_FILE"
        echo
        ;;
    status)
        if systemctl --user is-active --quiet "$SERVICE"; then
            echo "AllRelay: running"
            if [ -s "$URL_FILE" ]; then
                echo "URL: $(tr -d '\n' < "$URL_FILE")"
            else
                echo "URL: starting..."
            fi
        else
            echo "AllRelay: stopped"
        fi
        ;;
    start)
        systemctl --user start "$SERVICE"
        wait_for_url
        echo "Started: $(tr -d '\n' < "$URL_FILE")"
        ;;
    stop)
        systemctl --user stop "$SERVICE"
        ;;
    restart)
        systemctl --user restart "$SERVICE"
        wait_for_url
        echo "Restarted: $(tr -d '\n' < "$URL_FILE")"
        ;;
    logs)
        exec journalctl --user -u "$SERVICE" -f
        ;;
    *)
        usage >&2
        exit 1
        ;;
esac
