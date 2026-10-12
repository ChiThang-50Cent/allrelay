#!/bin/bash
# Build AllRelay .deb package
set -e

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEB_DIR="$ROOT/deb"
BIN_DIR="$ROOT/bin"
WEB_DIR="$ROOT/allrelay-server/internal/web"

echo "=== [1/3] Build Go binary ==="
cd "$ROOT/allrelay-server"
go build -o "$BIN_DIR/allrelay-server" ./cmd/allrelay-server/
echo "   Binary: $BIN_DIR/allrelay-server ($(du -h "$BIN_DIR/allrelay-server" | cut -f1))"

echo "=== [2/3] Copy files to package ==="
mkdir -p "$DEB_DIR/usr/bin" "$DEB_DIR/usr/share/applications" "$DEB_DIR/usr/share/icons/hicolor/scalable/apps"
cp "$BIN_DIR/allrelay-server" "$DEB_DIR/usr/bin/"
cp "$ROOT/scripts/allrelay-helper.sh" "$DEB_DIR/usr/bin/allrelay"
cp "$ROOT/scripts/allrelay-tray.py" "$DEB_DIR/usr/bin/allrelay-tray"
chmod 755 "$DEB_DIR/usr/bin/allrelay-server"
chmod 755 "$DEB_DIR/usr/bin/allrelay"
chmod 755 "$DEB_DIR/usr/bin/allrelay-tray"
cp "$ROOT/assets/allrelay.desktop" "$DEB_DIR/usr/share/applications/allrelay.desktop"
cp "$ROOT/assets/allrelay.svg" "$DEB_DIR/usr/share/icons/hicolor/scalable/apps/allrelay.svg"

rm -rf "$DEB_DIR/usr/share/allrelay"
mkdir -p "$DEB_DIR/usr/share/allrelay/static" "$DEB_DIR/usr/share/allrelay/templates"
cp "$WEB_DIR/static/app.js" "$DEB_DIR/usr/share/allrelay/static/"
cp "$WEB_DIR/static/style.css" "$DEB_DIR/usr/share/allrelay/static/"
cp "$WEB_DIR/templates/index.html" "$DEB_DIR/usr/share/allrelay/templates/"
cp "$WEB_DIR/templates/remote.html" "$DEB_DIR/usr/share/allrelay/templates/"

chmod 755 "$DEB_DIR/DEBIAN/postinst"
chmod 755 "$DEB_DIR/DEBIAN/prerm"

echo "   Files:"
find "$DEB_DIR" -not -path "*/DEBIAN/*" -type f | sort | while IFS= read -r f; do
    relative_path=${f#"$DEB_DIR"}
    echo "   $relative_path ($(du -h "$f" | cut -f1))"
done


echo "=== [3/3] Build .deb ==="
VERSION=$(grep Version "$DEB_DIR/DEBIAN/control" | cut -d' ' -f2)
DEB_FILE="$BIN_DIR/allrelay_${VERSION}_amd64.deb"
dpkg-deb --root-owner-group --build "$DEB_DIR" "$DEB_FILE"
echo ""
echo "   Package: $DEB_FILE ($(du -h "$DEB_FILE" | cut -f1))"
echo ""
echo "Install: sudo dpkg -i $DEB_FILE"
echo "Remove:  sudo dpkg -r allrelay"
