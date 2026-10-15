# AllRelay

> Turn a rooted Android phone into wireless screen, camera, mic, and speaker for Ubuntu.

AllRelay streams media directly over Wi‑Fi between Android and Ubuntu. The Ubuntu App Grid launcher and tray are the primary controls; the local web dashboard is available for detailed settings.

## What it does

- **Screen + control**: Android screen in a dedicated browser popup with touch/keyboard control and fold-aware, physical-panel-off mirroring
- **Camera**: Android camera exposed to Linux apps via `v4l2loopback`
- **Microphone**: Android mic exposed as a Linux audio input
- **Speaker**: PC audio played on the phone speaker
- **Independent toggles**: each stream can be turned on/off without killing the others
- **Tray controls**: scan, connect, and toggle streams directly from Ubuntu's system tray; discovery retries automatically and a direct IPv4 fallback is available
- **Phone discovery from PC**: route-aware UDP subnet scan; no phone-initiated session required

## Current architecture

### Android
- Forked scrcpy server, launched only by the **AllRelay Android app**
- After reboot, open the app and tap **Start** to launch the daemon
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
- Root access for app-controlled daemon lifecycle and wireless ADB
- ARM64

### Ubuntu
- Ubuntu 22.04+
- PipeWire / pipewire-pulse
- `v4l2loopback`
- Go 1.24+, `pkg-config`, and `libopus-dev` (only if building the backend from source)

## Build

For APK builds, use a full JDK 21 (including `javac`) and an Android SDK with platforms 34/36 and build tools 34/36. Set `JAVA_HOME` and `ANDROID_HOME` to those installations before running the build scripts.

### Main package build

```bash
./scripts/build-deb.sh
```

Output:
- `bin/allrelay_<version>_amd64.deb` — Ubuntu package

### Android controller app build

```bash
./scripts/build-apk.sh
```

Output:
- `bin/allrelay-app-debug.apk` — debug APK with the bundled Android server

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

Install `bin/allrelay-app-debug.apk` for local testing (or a signed release APK when available), open it, and tap **Start** to launch the daemon with the selected streams.
If an older AllRelay Magisk module is installed, remove it in **Magisk → Modules** and reboot before starting the APK daemon. This removes the former boot-time daemon; after reboot, open the APK and tap **Start**.

Root access is required only while the app starts or stops the `app_process` daemon, or when it changes wireless ADB settings. After reboot, open the app and tap **Start** again; the daemon does not auto-start.

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
├── scripts/           # Build helpers / dev utilities
└── docs/              # Project notes and protocol docs
```

## Notes

- The App Grid launcher/tray is the primary Ubuntu control surface; the web UI is for detailed settings.
- Discovery uses a route-aware **UDP subnet scan**.
- Screen/control use the **raw binary scrcpy control protocol**, not JSON.
- The packaged Linux services are on-demand **user services**, not system services.
- The dashboard listener defaults to loopback. Mismatched browser Origins are rejected; non-browser clients without an Origin header remain supported. These checks are not authentication.

## Related docs

- `docs/USAGE.md`
- `docs/wifi-protocol.md`
- `docs/root-causes.md`

## License

Apache License 2.0, following scrcpy upstream.
