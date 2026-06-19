# Changelog

## 2026-06-19

### Added
- Debian package now ships an `allrelay` helper command that opens the active Web UI URL without requiring users to know the current port.

### Changed
- User systemd service now binds the Web UI on a dynamic local port and writes the active URL to a runtime file for discovery by the helper command.

### Fixed
- Remote screen popup no longer steals focus on repeated status updates, so minimizing the popup now stays minimized.
- Remote screen mode now issues explicit display power off/on requests at screen start/stop and remote wake/restore boundaries.
- Android daemon launch now follows scrcpy-style remote power semantics with `power_on=false` and `keep_active=true` to avoid unintended wake-ups while keeping the device awake during remote screen sessions.
- Remote keyboard input now supports Shift-modified uppercase entry, common punctuation keys, printable text injection, and richer Android modifier meta-state handling closer to scrcpy behavior.
- Remote clipboard flow is now streamlined around browser-native paste behavior: local clipboard polling and `Ctrl+V`/paste events can push text from PC to Android without any dedicated clipboard UI.

### Known limitations
- Android → PC clipboard autosync remains deferred because browser clipboard write restrictions make reliable background sync difficult in the current Web UI architecture.

### Packaging
- Rebuilt and verified updated Ubuntu package, Android debug APK, and Magisk module artifacts for the Web UI helper, popup, screen power, keyboard compatibility, and clipboard cleanup changes.
