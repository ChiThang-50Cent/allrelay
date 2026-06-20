# Changelog

## 2026-06-20

### Added
- **Wireless ADB one-click** from dashboard: backend exposes `POST /api/adb/connect`, `POST /api/adb/disconnect`, and `GET /api/adb/status` endpoints that drive phone-side ADB-over-TCP via the app control bridge.
- Phone app now runs a foreground service on port `5008` with HTTP control endpoints: `/health`, `/adb/status`, `/adb/enable`, `/adb/disable`, `/adb/authorize`.
- Phone app UI (ToggleActivity) now shows a **Wireless ADB** block with Enable / Disable / Refresh buttons and live status.
- Dashboard Wireless ADB section shows a colored indicator dot: 🟢 connected, 🟡 unauthorized, 🔴 disconnected/listening but no host, ⚪ idle.
- Host key auto-authorize: when `adb connect` returns `unauthorized`, the backend automatically sends the local `~/.android/adbkey.pub` to the phone via `/adb/authorize` and retries the connection.

### Changed
- ADB auto-off now checks **host connection health** every 30 seconds instead of using a fixed 15-minute countdown. ADB TCP stays alive as long as at least one host remains connected; it only disables after 15 minutes of idle (no established host connections).
- Backend `callPhoneADB` timeout increased from 3s to 10s to accommodate the phone-side `adbd` startup delay.

### Fixed
- Fixed deadlock in `/api/status` caused by calling `queryADBStatus()` while holding `ws.mu.Lock()`; ADB status is now queried outside the lock.
- Fixed `.deb` packaging race that could ship a stale binary; build now copies the freshly compiled binary into the staging area before calling `dpkg-deb`.
- Fixed ADB indicator flip-flop by using `hostState` (`device`/`unauthorized`/`disconnected`) as the source of truth rather than the occasionally unstable `hostConnected` boolean.

### Packaging
- Rebuilt and verified Ubuntu `.deb`, Android debug APK with control service + Wireless ADB UI.

## 2026-06-19

### Added
- Debian package now ships an `allrelay` helper command that opens the active Web UI URL without requiring users to know the current port.

### Changed
- User systemd service now binds the Web UI on a dynamic local port and writes the active URL to a runtime file for discovery by the helper command.

### Fixed
- Remote screen popup no longer steals focus on repeated status updates, so minimizing the popup now stays minimized.
- Remote screen mode now issues explicit display power off/on requests at screen start/stop and remote wake/restore boundaries.
- Android daemon launch now follows scrcpy-style remote power semantics with `power_on=false` and `keep_active=true` to avoid unintended wake-ups while keeping the device awake during remote screen sessions.
- Remote keyboard input now supports Shift-modified uppercase entry, common punctuation keys, printable text injection, richer Android modifier meta-state handling closer to scrcpy behavior, and the missing Android left/right Alt meta constants used by those modifier paths.
- Remote clipboard flow is now streamlined around browser-native paste behavior: local clipboard polling and `Ctrl+V`/paste events can push text from PC to Android without any dedicated clipboard UI.
- Remote screen rendering now uses DPR-aware canvas backing-store sizing, explicit contain-fit draw math, and corrected frame-relative pointer mapping for sharper enlarged output closer to scrcpy behavior.
- Remote logging now writes both server-side and selected browser-side remote diagnostics into a single append-only sliding-window log file, which keeps the newest ~10 MiB of entries in place for easier debugging.

### Known limitations
- Android → PC clipboard autosync remains deferred because browser clipboard write restrictions make reliable background sync difficult in the current Web UI architecture.

### Packaging
- Rebuilt and verified updated Ubuntu package, Android debug APK, and Magisk module artifacts for the Web UI helper, popup, screen power, keyboard compatibility, clipboard cleanup, sharper remote screen rendering, and single-file remote logging changes.
