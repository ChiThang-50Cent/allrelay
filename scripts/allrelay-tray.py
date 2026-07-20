#!/usr/bin/env python3
"""AllRelay desktop tray controls.

The tray is intentionally a thin graphical client for the local AllRelay HTTP
API. The media server remains headless and can still be used without a desktop
session or through the web dashboard.
"""

import ipaddress
import json
import os
from pathlib import Path
import subprocess
import sys
import threading
import time
from typing import Any, Callable
from urllib import error, request

try:
    import gi

    gi.require_version("Gtk", "3.0")
    gi.require_version("AyatanaAppIndicator3", "0.1")
    from gi.repository import AyatanaAppIndicator3 as AppIndicator
    from gi.repository import GLib, Gtk
except (ImportError, ValueError) as exc:
    print(
        "AllRelay tray requires python3-gi, gir1.2-gtk-3.0, and "
        "gir1.2-ayatanaappindicator3-0.1: " + str(exc),
        file=sys.stderr,
    )
    sys.exit(1)

SCAN_INTERVAL_SECONDS = 30
SCAN_MAX_RETRIES = 3
SCAN_RETRY_DELAY_SECONDS = 0.4
STREAMS = (
    ("screen", "Screen"),
    ("camera", "Camera"),
    ("mic", "Microphone"),
    ("speaker", "Speaker"),
)


class APIError(RuntimeError):
    """A local AllRelay API request failed."""


class AllRelayClient:
    def __init__(self) -> None:
        runtime_dir = os.environ.get("XDG_RUNTIME_DIR", f"/run/user/{os.getuid()}")
        self.url_file = Path(runtime_dir) / "allrelay" / "url"

    def base_url(self) -> str:
        try:
            url = self.url_file.read_text(encoding="utf-8").strip()
        except OSError as exc:
            raise APIError("AllRelay service is not running") from exc
        if not url.startswith("http://127.0.0.1:") and not url.startswith("http://localhost:"):
            raise APIError("AllRelay published an invalid local URL")
        return url.rstrip("/")

    def get_status(self) -> dict[str, Any]:
        # Match the dashboard: do not turn a slow controller operation into a
        # client-side timeout. refresh() allows only one status request at once.
        return self._request("/api/status")

    def scan(self) -> list[dict[str, Any]]:
        # Match the dashboard's three-attempt discovery flow. Wi-Fi UDP
        # discovery can legitimately miss one complete scan window.
        for attempt in range(SCAN_MAX_RETRIES):
            result = self._request("/api/phones/scan", method="POST")
            if not isinstance(result, list):
                raise APIError("AllRelay returned an invalid phone list")
            if result or attempt == SCAN_MAX_RETRIES - 1:
                return result
            time.sleep(SCAN_RETRY_DELAY_SECONDS)
        return []

    def connect(self, phone: dict[str, Any]) -> None:
        ports = phone.get("ports") or []
        if not ports:
            raise APIError("The selected phone did not provide a connection port")
        self.connect_address(str(phone["ip"]), int(ports[0]))

    def connect_address(self, ip: str, port: int = 5000) -> None:
        # The dashboard deliberately waits for /api/connect rather than
        # imposing a short client deadline. The operation runs in a tray
        # worker, so this never blocks GTK while Android starts its sockets.
        self._request("/api/connect", method="POST", body={"ip": ip, "port": port})

    def disconnect(self) -> None:
        self._request("/api/disconnect", method="POST")

    def set_stream(self, stream: str, active: bool) -> None:
        self._request(
            "/api/streams/toggle",
            method="POST",
            body={"stream": stream, "active": active},
        )

    def _request(
        self,
        path: str,
        method: str = "GET",
        body: dict[str, Any] | None = None,
        timeout: float | None = None,
    ) -> Any:
        data = None
        headers: dict[str, str] = {}
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        req = request.Request(self.base_url() + path, data=data, headers=headers, method=method)
        try:
            with request.urlopen(req, timeout=timeout) as response:
                return json.loads(response.read().decode("utf-8"))
        except error.HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace").strip()
            raise APIError(detail or f"AllRelay request failed ({exc.code})") from exc
        except (OSError, ValueError, json.JSONDecodeError) as exc:
            raise APIError("Cannot communicate with the AllRelay service") from exc


class AllRelayTray:
    def __init__(self) -> None:
        self.client = AllRelayClient()
        self.refreshing = False
        self.updating_menu = False
        self.scanning = False
        self.action_in_progress = False
        self.have_status = False
        self.connected = False
        self.last_scan_started = 0.0
        self.phones: list[dict[str, Any]] = []

        self.indicator = AppIndicator.Indicator.new(
            "allrelay",
            "smartphone-symbolic",
            AppIndicator.IndicatorCategory.APPLICATION_STATUS,
        )
        self.indicator.set_title("AllRelay")

        self.menu = Gtk.Menu()
        self.status_item = Gtk.MenuItem.new_with_label("AllRelay: starting…")
        self.status_item.set_sensitive(False)
        self.menu.append(self.status_item)

        self.connect_item = Gtk.MenuItem.new_with_label("Devices")
        self.phone_menu = Gtk.Menu()
        self.connect_item.set_submenu(self.phone_menu)
        self.menu.append(self.connect_item)
        self.set_phone_menu()

        self.scan_item = Gtk.MenuItem.new_with_label("Scan now")
        self.scan_item.connect("activate", self.on_scan)
        self.menu.append(self.scan_item)

        self.disconnect_item = Gtk.MenuItem.new_with_label("Disconnect phone")
        self.disconnect_item.connect("activate", self.on_disconnect)
        self.disconnect_item.set_sensitive(False)
        self.menu.append(self.disconnect_item)
        self.menu.append(Gtk.SeparatorMenuItem())

        self.stream_items: dict[str, Gtk.CheckMenuItem] = {}
        for stream, label in STREAMS:
            item = Gtk.CheckMenuItem.new_with_label(label)
            item.set_sensitive(False)
            item.connect("toggled", self.on_stream_toggled, stream)
            self.stream_items[stream] = item
            self.menu.append(item)

        self.menu.append(Gtk.SeparatorMenuItem())
        self.web_item = Gtk.MenuItem.new_with_label("Open detailed settings")
        self.web_item.connect("activate", self.on_open_web)
        self.menu.append(self.web_item)

        self.quit_item = Gtk.MenuItem.new_with_label("Quit AllRelay")
        self.quit_item.connect("activate", self.on_quit)
        self.menu.append(self.quit_item)

        self.menu.show_all()
        # AppIndicator does not register or render without a Gtk.Menu.
        self.indicator.set_menu(self.menu)
        self.indicator.set_status(AppIndicator.IndicatorStatus.ACTIVE)
        self.refresh()
        GLib.timeout_add_seconds(2, self.refresh)
        GLib.timeout_add_seconds(SCAN_INTERVAL_SECONDS, self.auto_scan)

    def run(self) -> None:
        Gtk.main()

    def set_phone_menu(self) -> None:
        for child in self.phone_menu.get_children():
            self.phone_menu.remove(child)

        if self.scanning:
            scanning = Gtk.MenuItem.new_with_label("Scanning for phones…")
            scanning.set_sensitive(False)
            self.phone_menu.append(scanning)

        if not self.phones:
            if not self.scanning:
                empty = Gtk.MenuItem.new_with_label("No phones found")
                empty.set_sensitive(False)
                self.phone_menu.append(empty)
        else:
            if self.scanning:
                self.phone_menu.append(Gtk.SeparatorMenuItem())
            for phone in self.phones:
                name = phone.get("name") or "Unnamed phone"
                ip = phone.get("ip") or "unknown address"
                item = Gtk.MenuItem.new_with_label(f"{name} ({ip})")
                item.connect("activate", self.on_connect, phone)
                self.phone_menu.append(item)

        if self.phones or self.scanning:
            self.phone_menu.append(Gtk.SeparatorMenuItem())
        manual_connect = Gtk.MenuItem.new_with_label("Connect by IP…")
        manual_connect.connect("activate", self.on_connect_by_ip)
        self.phone_menu.append(manual_connect)

        if self.scanning:
            self.connect_item.set_label("Devices (scanning…)")
        elif self.phones:
            self.connect_item.set_label(f"Devices ({len(self.phones)})")
        else:
            self.connect_item.set_label("Devices")
        self.phone_menu.show_all()

    def refresh(self) -> bool:
        # /api/connect holds the controller lock while Android brings up its
        # TCP streams. Dashboard fetches simply wait; do the same here instead
        # of racing it with a short /api/status request that says "offline".
        if self.refreshing or self.action_in_progress:
            return True
        self.refreshing = True
        self.run_background(self.client.get_status, self.apply_status)
        return True

    def run_background(self, operation: Callable[[], Any], callback: Callable[[Any, str | None], None]) -> None:
        def worker() -> None:
            try:
                result = operation()
                failure = None
            except APIError as exc:
                result = None
                failure = str(exc)
            GLib.idle_add(callback, result, failure)

        threading.Thread(target=worker, daemon=True).start()

    def apply_status(self, status: Any, failure: str | None) -> None:
        self.refreshing = False
        if failure:
            # The dashboard leaves its last known state untouched when a
            # background status fetch fails. In particular, a slow connection
            # attempt must not make the tray look disconnected.
            if not self.have_status:
                self.status_item.set_label("AllRelay: waiting for service…")
            return

        assert isinstance(status, dict)
        self.have_status = True
        connected = bool(status.get("connected"))
        self.connected = connected
        phone = status.get("phone") or {}
        phone_name = phone.get("name") or phone.get("ip") or "phone"
        if connected:
            self.status_item.set_label(f"AllRelay: connected to {phone_name}")
        elif not self.scanning:
            self.status_item.set_label(self.device_status_label())
        self.connect_item.set_sensitive(not connected)
        self.scan_item.set_sensitive(not connected)
        self.disconnect_item.set_sensitive(connected)

        states = {stream.get("name"): bool(stream.get("active")) for stream in status.get("streams", [])}
        self.updating_menu = True
        for stream, item in self.stream_items.items():
            item.set_sensitive(connected)
            item.set_active(states.get(stream, False))
        self.updating_menu = False
        if not connected:
            self.maybe_scan()

    def device_status_label(self) -> str:
        if len(self.phones) == 1:
            return "AllRelay: 1 device found"
        if self.phones:
            return f"AllRelay: {len(self.phones)} devices found"
        return "AllRelay: ready"

    def maybe_scan(self, force: bool = False) -> None:
        if self.connected or self.scanning:
            return
        if not force and time.monotonic() - self.last_scan_started < SCAN_INTERVAL_SECONDS:
            return

        self.scanning = True
        self.last_scan_started = time.monotonic()
        self.set_phone_menu()
        self.status_item.set_label("AllRelay: scanning devices…")

        def done(result: Any, failure: str | None) -> None:
            self.scanning = False
            if failure:
                self.set_phone_menu()
                self.status_item.set_label(f"AllRelay: {failure}")
                return
            self.phones = result
            self.set_phone_menu()
            self.status_item.set_label(self.device_status_label())

        self.run_background(self.client.scan, done)

    def auto_scan(self) -> bool:
        self.maybe_scan()
        return True

    def restore_connection_controls(self) -> None:
        self.connect_item.set_sensitive(not self.connected)
        self.scan_item.set_sensitive(not self.connected)
        self.disconnect_item.set_sensitive(self.connected)
        for item in self.stream_items.values():
            item.set_sensitive(self.connected)

    def report_action(self, action: str, operation: Callable[[], Any]) -> None:
        self.action_in_progress = True
        self.status_item.set_label(f"AllRelay: {action}…")
        self.connect_item.set_sensitive(False)
        self.scan_item.set_sensitive(False)
        self.disconnect_item.set_sensitive(False)
        for item in self.stream_items.values():
            item.set_sensitive(False)

        def done(_result: Any, failure: str | None) -> None:
            self.action_in_progress = False
            if failure:
                self.status_item.set_label(f"AllRelay: {failure}")
                # Like the dashboard's finally block, a failed operation must
                # leave the user able to retry instead of a disabled tray.
                self.restore_connection_controls()
            self.refresh()

        self.run_background(operation, done)

    def on_scan(self, _item: Gtk.MenuItem) -> None:
        self.maybe_scan(force=True)

    def on_connect(self, _item: Gtk.MenuItem, phone: dict[str, Any]) -> None:
        name = phone.get("name") or phone.get("ip") or "phone"
        self.report_action(f"connecting to {name}", lambda: self.client.connect(phone))

    def on_disconnect(self, _item: Gtk.MenuItem) -> None:
        self.report_action("disconnecting", self.client.disconnect)

    def on_connect_by_ip(self, _item: Gtk.MenuItem) -> None:
        dialog = Gtk.Dialog(title="Connect to phone")
        dialog.add_button("Cancel", Gtk.ResponseType.CANCEL)
        dialog.add_button("Connect", Gtk.ResponseType.OK)
        dialog.set_default_response(Gtk.ResponseType.OK)

        content = dialog.get_content_area()
        content.set_border_width(12)
        content.set_spacing(8)
        content.add(Gtk.Label(label="Phone IPv4 address:"))
        entry = Gtk.Entry()
        entry.set_placeholder_text("192.168.1.24")
        entry.set_activates_default(True)
        content.add(entry)
        dialog.show_all()
        response = dialog.run()
        ip = entry.get_text().strip()
        dialog.destroy()

        if response != Gtk.ResponseType.OK:
            return
        try:
            parsed = ipaddress.ip_address(ip)
            if parsed.version != 4:
                raise ValueError
        except ValueError:
            self.status_item.set_label("AllRelay: enter a valid IPv4 address")
            return
        self.report_action(f"connecting to {ip}", lambda: self.client.connect_address(ip))

    def on_stream_toggled(self, item: Gtk.CheckMenuItem, stream: str) -> None:
        if self.updating_menu:
            return
        active = item.get_active()
        # Open the remote first, matching the dashboard flow. The server also
        # replays decoder state if the browser takes longer to start.
        if stream == "screen" and active and not self.open_screen_viewer():
            # Keep the checkmark consistent with dashboard behavior: if a
            # viewer cannot be launched, do not start an unviewable stream.
            self.updating_menu = True
            item.set_active(False)
            self.updating_menu = False
            return
        self.report_action(
            f"turning {stream} {'on' if active else 'off'}",
            lambda: self.client.set_stream(stream, active),
        )

    def open_screen_viewer(self) -> bool:
        try:
            remote_url = self.client.base_url() + "/remote"
            subprocess.Popen(
                ["xdg-open", remote_url],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )
        except (APIError, OSError) as exc:
            self.status_item.set_label(f"AllRelay: cannot open screen viewer ({exc})")
            return False
        return True

    def on_open_web(self, _item: Gtk.MenuItem) -> None:
        subprocess.Popen(["/usr/bin/allrelay", "open"])

    def on_quit(self, _item: Gtk.MenuItem) -> None:
        self.indicator.set_status(AppIndicator.IndicatorStatus.PASSIVE)
        # Stop the media server and this tray process as one user action.
        subprocess.Popen(["systemctl", "--user", "stop", "allrelay.service", "allrelay-tray.service"])
        Gtk.main_quit()


def main() -> None:
    AllRelayTray().run()


if __name__ == "__main__":
    main()
