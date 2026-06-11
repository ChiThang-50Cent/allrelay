# AllRelay

> Turn your rooted Android phone into wireless peripherals for your Ubuntu PC.

AllRelay transforms your Android phone into four wireless peripherals — a **monitor**, **webcam**, **microphone**, and **speaker** — all connected over Wi-Fi with low latency.

Built as a fork of [scrcpy](https://github.com/Genymobile/scrcpy), AllRelay extends it with multi-stream support, reverse audio, direct Wi-Fi transport, and Magisk root integration for background operation.

---

## Features

| # | Function | Direction | Description |
|---|----------|-----------|-------------|
| 1 | **Monitor** | Phone → PC | Screen mirroring with touch/keyboard input |
| 2 | **Camera** | Phone → PC | Phone camera as virtual webcam (v4l2loopback) |
| 3 | **Microphone** | Phone → PC | Phone mic as PipeWire audio source |
| 4 | **Speaker** | PC → Phone | PC system audio through phone speaker |

- **Independent toggles**: Enable/disable each stream without affecting others
- **Low latency**: <100ms glass-to-glass for video, <50ms for audio
- **Wi-Fi Direct**: No USB/ADB needed after initial setup
- **Auto-start**: Magisk module for silent background operation on boot
- **Heartbeat monitoring**: Real-time device status (battery, Wi-Fi signal, CPU)
- **Auto-reconnection**: Exponential backoff on Wi-Fi disconnection
- **Input injection**: Forward PC keyboard/mouse to phone

---

## Requirements

### Android (Phone)
- Android 12+ (API 31)
- **Rooted** with [Magisk](https://github.com/topjohnwu/Magisk) 20.4+
- ARM64 architecture
- 5GHz Wi-Fi recommended

### Ubuntu (PC)
- Ubuntu 22.04 LTS or later
- [PipeWire](https://pipewire.org/) (for audio)
- [v4l2loopback](https://github.com/umlaeute/v4l2loopback) (for camera)
- [GStreamer](https://gstreamer.freedesktop.org/) 1.0 (for video display)
- Go 1.22+ (to build the server)

---

## Quick Start

### 1. Build AllRelay

```bash
git clone https://github.com/yourusername/allrelay.git
cd allrelay

# Build everything (server JAR + Go server + tools)
./scripts/build.sh all
```

### 2. Install on Phone (Magisk Module)

```bash
# Build the Magisk module ZIP
./scripts/build-magisk.sh

# Push to phone
adb push bin/allrelay-magisk-v0.4.0-alpha.zip /sdcard/

# Flash via Magisk Manager, then reboot
```

### 3. Install on Ubuntu

```bash
# System-wide installation (requires sudo)
sudo ./scripts/install.sh

# Or user-only installation
./scripts/install.sh --user

# Configure your phone's IP
sudoedit /etc/allrelay/phone_ip   # system install
# or edit ~/.config/systemd/user/allrelay.service (user install)
```

### 4. Start

```bash
# On the PC (systemd auto-start or manual):
sudo systemctl enable --now allrelay

# Or just start the server directly:
allrelay-server --host 192.168.1.100

# Discover phone via mDNS:
allrelay-discover
```

---

## Architecture

```
┌─────────────────────────┐         ┌──────────────────────────┐
│     Android Phone       │         │      Ubuntu PC           │
│                         │         │                          │
│  Screen ──→ H.264 ──────┼─TCP:5000→│── GStreamer → SDL2      │
│  Camera ──→ H.264 ──────┼─TCP:5001→│── GStreamer → v4l2     │
│  Mic ─────→ Opus ───────┼─TCP:5002→│── PipeWire source       │
│  Speaker ←── Opus ──────┼─TCP:5003─│── PipeWire sink         │
│  Control ←── JSON ──────┼─TCP:5004─│── Input injection       │
│  Heartbeat ──→ UDP:5005 ┼─────────→│── Status monitor        │
└─────────────────────────┘         └──────────────────────────┘
```

- **Android**: Forked scrcpy Java server (MediaCodec, AAudio, Camera2)
- **Ubuntu**: Go server (protocol parsing, GStreamer pipelines, PipeWire routing)
- **Protocol**: 16-byte header per packet (stream_id + PTS + flags + payload_size)
- **Transport**: TCP with Nagle disabled for low latency

---

## Project Structure

```
allrelay/
├── allrelay-server/       # Go server (Ubuntu side)
│   ├── cmd/
│   │   ├── allrelay-server/   # Main server binary
│   │   └── mock-android-server/ # Test tool
│   └── internal/
│       ├── control/       # Control protocol & toggles
│       ├── heartbeat/     # UDP heartbeat monitor
│       ├── input/         # X11 keyboard/mouse capture
│       ├── protocol/      # 16-byte packet parser + demuxer
│       ├── reconnect/     # Auto-reconnection logic
│       ├── transport/     # TCP connection manager
│       └── video/         # GStreamer pipelines + v4l2
├── scrcpy/                # Forked scrcpy (Android + C client)
│   └── server/src/main/java/com/genymobile/scrcpy/
├── magisk/                # Magisk module
│   ├── module.prop
│   ├── customize.sh
│   ├── service.sh         # Boot service (starts Java server)
│   ├── post-fs-data.sh    # AAudio MMAP + SELinux
│   ├── system/bin/        # Server JAR + daemon wrapper
│   └── sepolicy/          # SELinux policy
├── configs/
│   ├── pipewire/          # PipeWire virtual device configs
│   └── systemd/           # systemd service unit
├── scripts/               # Build, install, test scripts
├── docs/                  # Technical documentation
├── plans/                 # Project planning & tracking
└── SPEC.md                # Full technical specification
```

---

## CLI Reference

### allrelay-server

```
Usage:
  allrelay-server --host 192.168.1.100 [flags]

Flags:
  --host string        Phone IP address (required)
  --port int           Base TCP port (default 5000)
  --no-screen          Disable screen stream
  --no-camera          Disable camera stream
  --no-mic             Disable microphone stream
  --no-speaker         Disable speaker stream
  --no-control         Disable control channel
  --no-input           Disable input capture (keyboard/mouse → phone)
  --no-heartbeat       Disable heartbeat/status monitoring
  --no-reconnect       Disable auto-reconnection
  -v                   Verbose debug output
```

### allrelay-discover

```
Usage:
  allrelay-discover [--timeout 5s]

Discovers AllRelay phones on the local network via mDNS.
```

---

## Development

### Build Environment

```bash
# Android SDK (for Java server)
export ANDROID_SDK_ROOT=/path/to/android-sdk

# Go (for Ubuntu server)
cd allrelay-server && go build ./cmd/allrelay-server/

# Run tests
go test ./...                              # Go server tests
cd scrcpy && ./gradlew :server:test        # Java server tests
cd scrcpy && ninja -C x test               # C client tests
```

### Testing

```bash
# End-to-end Wi-Fi test (requires phone connected via ADB)
./scripts/test-e2e-wifi.sh

# Wi-Fi transport test
./scripts/test-wifi-transport.sh

# Mock Android server (for testing without a phone)
cd allrelay-server && go run ./cmd/mock-android-server/
```

---

## Documentation

| Document | Description |
|----------|-------------|
| [SPEC.md](SPEC.md) | Full technical specification |
| [docs/wifi-protocol.md](docs/wifi-protocol.md) | Wire protocol format |
| [docs/architecture-research.md](docs/architecture-research.md) | Architecture decisions |
| [docs/open-source-research.md](docs/open-source-research.md) | Reuse analysis |
| [plans/CURRENT_WORK.md](plans/CURRENT_WORK.md) | Current development status |

---

## Performance

| Stream | Resolution | FPS | Bitrate | Latency |
|--------|-----------|-----|---------|---------|
| Monitor | 1080×2640 | 60 | 4-8 Mbps | <35ms |
| Camera | 1920×1080 | 30 | 2-5 Mbps | <40ms |
| Microphone | 48kHz mono | — | 32-64 Kbps | <36ms |
| Speaker | 48kHz mono | — | 64-128 Kbps | <36ms |

*Measured on Samsung SM-F711B with 5GHz Wi-Fi 6 (802.11ax)*

---

## FAQ

### Why root?

Root enables:
- **No consent dialog** for screen capture (VirtualDisplay without MediaProjection)
- **No recording indicator** (hidden green privacy dot)
- **AAudio MMAP Exclusive** for 10-20x lower audio latency
- **Silent background operation** (Magisk daemon, no notification)
- **FLAG_SECURE content capture** (secure apps like banking)

Without root, you can still use AllRelay, but with a consent dialog and visible recording indicator.

### Why not just use scrcpy?

scrcpy is excellent for screen mirroring over USB. AllRelay adds:
- **Wi-Fi direct** (no ADB tunnel)
- **Multiple streams** (screen + camera + mic + speaker simultaneously)
- **Reverse audio** (PC → phone speaker)
- **Independent toggles** per function
- **Auto-start daemon** via Magisk

### What Wi-Fi do I need?

5GHz 802.11ac or 802.11ax is recommended. 2.4GHz may work for audio-only but will have too much jitter for video. A dedicated 5GHz hotspot on the phone works well.

---

## License

AllRelay is licensed under the **Apache License 2.0**, matching scrcpy upstream.

```
Copyright 2026 AllRelay Contributors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
```

---

## Acknowledgments

AllRelay is built on the shoulders of giants:

- [**scrcpy**](https://github.com/Genymobile/scrcpy) — The base project we forked (Apache 2.0)
- [**GStreamer**](https://gstreamer.freedesktop.org/) — Media pipeline framework (LGPL)
- [**FFmpeg**](https://ffmpeg.org/) — H.264/Opus decoding (LGPL)
- [**PipeWire**](https://pipewire.org/) — Linux audio/video routing (MIT)
- [**v4l2loopback**](https://github.com/umlaeute/v4l2loopback) — Virtual video device (GPL)
- [**Magisk**](https://github.com/topjohnwu/Magisk) — Android root solution
