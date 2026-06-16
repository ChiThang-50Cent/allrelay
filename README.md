# AllRelay

> Turn a rooted Android phone into wireless screen, camera, mic, and speaker for Ubuntu.

AllRelay streams media directly over Wi‑Fi between Android and Ubuntu, with a web dashboard to discover the phone, connect, and toggle streams independently.

## What it does

- **Screen + control**: Android screen in a dedicated browser popup with touch/keyboard control
- **Camera**: Android camera exposed to Linux apps via `v4l2loopback`
- **Microphone**: Android mic exposed as a Linux audio input
- **Speaker**: PC audio played on the phone speaker
- **Independent toggles**: each stream can be turned on/off without killing the others
- **Phone discovery from PC**: web UI uses a UDP subnet scan; no phone-initiated session required

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
- `allrelay-server` Go backend
- Web dashboard on `http://localhost:9090`
- Browser popup uses **WebSocket + WebCodecs** for screen mirroring
- Camera uses **ffmpeg → v4l2loopback**
- Audio integrates with **PipeWire/PulseAudio**
- Packaged as a **user systemd service** so audio works in the user session

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
- `bin/allrelay_0.1.0_amd64.deb`
- `bin/scrcpy-server-allrelay`
- `bin/allrelay-magisk.zip`

### Android-only build

```bash
./scripts/build-magisk.sh
```

This also refreshes:
- `bin/scrcpy-server-allrelay`
- `bin/allrelay-magisk.zip`

## Install

### Ubuntu

```bash
sudo dpkg -i bin/allrelay_0.1.0_amd64.deb
systemctl --user enable --now allrelay
```

### Android

#### Option 1: AllRelay app (recommended)

Install the APK, open it, and tap **Start** to launch the daemon.

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

1. Open `http://localhost:9090`
2. Click **Scan** to find the phone via UDP subnet scan
3. Click **Connect**
4. Toggle streams independently
5. Turning on **Screen** opens the dedicated remote popup automatically

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

- The web UI is the primary control surface.
- Discovery in the dashboard uses **UDP subnet scan**.
- Screen/control use the **raw binary scrcpy control protocol**, not JSON.
- The packaged Linux service is a **user service**, not a system service.

## Related docs

- `docs/USAGE.md`
- `docs/wifi-protocol.md`
- `docs/root-causes.md`

## License

Apache License 2.0, following scrcpy upstream.
