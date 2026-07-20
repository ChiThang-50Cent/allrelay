# AllRelay

> Turn a rooted Android phone into wireless screen, camera, mic, and speaker for Ubuntu.

AllRelay streams media directly over Wi‑Fi between Android and Ubuntu. The Ubuntu App Grid launcher and tray are the primary controls; the local web dashboard is available for detailed settings.

## What it does

- **Screen + control**: Android screen in a dedicated browser popup with touch/keyboard control
- **Camera**: Android camera exposed to Linux apps via `v4l2loopback`
- **Microphone**: Android mic exposed as a Linux audio input
- **Speaker**: PC audio played on the phone speaker
- **Independent toggles**: each stream can be turned on/off without killing the others
- **Tray controls**: scan, connect, and toggle streams directly from Ubuntu's system tray; discovery retries automatically and a direct IPv4 fallback is available
- **Phone discovery from PC**: route-aware UDP subnet scan; no phone-initiated session required

## Current architecture

### Android
- Forked scrcpy server running as a root daemon
- Can be launched by:
  - **AllRelay Android app** (recommended — one-tap daemon control)
  - **Magisk module** (auto-starts at boot; does NOT self-restart when killed)
  - **ADB** (manual testing)
- Streams:
  - `:5000` screen
  - `:5001` camera
  - `:5002` mic
  - `:5003` speaker
  - `:5004` control
  - `:5009/udp` discovery responder

### Ubuntu
- App Grid launcher and GTK/Ayatana tray client for everyday controls
- `allrelay-server` Go backend, started on demand with the tray
- Local web dashboard on a dynamic `127.0.0.1` port (`allrelay open`)
- Browser remote viewer uses **WebSocket + WebCodecs** for screen mirroring
- Camera uses **ffmpeg → v4l2loopback**
- Audio integrates with **PipeWire/PulseAudio**
- Backend and tray are user services, but neither auto-starts at login

## Requirements

### Android
- Android 12+
- Rooted with Magisk
- ARM64

### Ubuntu
- Ubuntu 22.04+
- PipeWire / pipewire-pulse
- `v4l2loopback`
- Go 1.22+ (only if building from source)

## Build

### Main package build

```bash
./scripts/build-deb.sh
```

Outputs:
- `bin/allrelay_<version>_amd64.deb` — Ubuntu package
- `bin/allrelay-magisk.zip` — flashable Android Magisk module
- `bin/scrcpy-server-allrelay` — Android server artifact for Magisk or manual ADB use

### Android controller app build

Build the Android server first, then build the controller APK:

```bash
./scripts/build-magisk.sh
(cd android && ./gradlew :app:assembleDebug)
cp android/app/build/outputs/apk/debug/app-debug.apk bin/allrelay-app-debug.apk
```

The debug APK is suitable for local testing. Create and sign a release APK before distributing it publicly.

## Install

### Ubuntu

```bash
sudo dpkg -i bin/allrelay_<version>_amd64.deb
```

Neither Ubuntu service starts automatically at boot/login. Open **AllRelay** from the App Grid to start the backend and tray together, or run:

```bash
allrelay tray
```

For advanced settings, run `allrelay open`; it starts the backend if needed and opens the local dashboard.

### Android

#### Option 1: AllRelay app (recommended)

Install `allrelay-app-debug.apk` for local testing (or a signed release APK when available), open it, and tap **Start** to launch the daemon.

The app is the primary control surface — it can start, stop, and restart the daemon regardless of whether it was originally launched by Magisk or ADB.

#### Option 2: Magisk module

```bash
adb push bin/allrelay-magisk.zip /sdcard/
```

Flash it from Magisk, then reboot.

The module auto-starts the daemon once at boot. It does **not** self-restart when the daemon is killed — use the app to control it.

#### Option 3: Manual ADB test

```bash
adb push bin/scrcpy-server-allrelay /data/local/tmp/allrelay.jar
adb shell "su -c 'CLASSPATH=/data/local/tmp/allrelay.jar app_process / \
  com.genymobile.scrcpy.Server 4.0 \
  log_level=info \
  wifi_mode=true \
  wifi_port=5000 \
  video=true \
  audio=true \
  audio_source=mic \
  speaker_enabled=true \
  camera_enabled=true \
  daemon=true \
  control=true \
  >/data/local/tmp/allrelay-unified.log 2>&1 &'"
```

## Use

1. Open **AllRelay** from the Ubuntu App Grid (or run `allrelay tray`).
2. From the tray, choose **Scan now**, then select a phone under **Devices**. Discovery retries automatically; use **Devices → Connect by IP…** (port `5000`) when needed.
3. Toggle Camera, Microphone, Speaker, or Screen from the tray.
4. Turning on **Screen** opens the remote viewer in the default browser.
5. Use **Open detailed settings** in the tray or `allrelay open` for dashboard-only options.

## Repository layout

```text
allrelay/
├── allrelay-server/   # Go backend + web UI
├── android/           # Android controller app
├── scrcpy/            # Forked scrcpy server/client sources
├── magisk/            # Magisk module
├── scripts/           # Build helpers / dev utilities
└── docs/              # Project notes and protocol docs
```

## Notes

- The App Grid launcher/tray is the primary Ubuntu control surface; the web UI is for detailed settings.
- Discovery uses a route-aware **UDP subnet scan**.
- Screen/control use the **raw binary scrcpy control protocol**, not JSON.
- The packaged Linux services are on-demand **user services**, not system services.

## Related docs

- `docs/USAGE.md`
- `docs/wifi-protocol.md`
- `docs/root-causes.md`

## License

Apache License 2.0, following scrcpy upstream.
