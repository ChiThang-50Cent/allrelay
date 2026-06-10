# AllRelay — Open-Source Reuse Research Report

> **Date:** 2026-06-09
> **Goal:** Identify existing open-source repos that can be reused/forked/integrated for building AllRelay — a unified Android app providing screen mirroring, camera, microphone, and speaker functionality over Wi-Fi to a Linux/Ubuntu PC.

---

## Category 1: Android Screen Mirroring / Streaming Libraries

### ⭐ RootEncoder (pedroSG94/RootEncoder) — PRIMARY CANDIDATE
| Field | Value |
|-------|-------|
| **URL** | https://github.com/pedroSG94/RootEncoder |
| **Stars** | 2,991 |
| **Language** | Kotlin/Java |
| **License** | Apache-2.0 ✅ |
| **Last Active** | Actively maintained (2024+) |
| **Protocols** | RTMP, RTSP, SRT, **UDP** ✅ |

**Capabilities:**
- ✅ **Screen capture via MediaProjection** — has `DisplayRtmpDisplay`, `DisplayRtspDisplay`, `DisplayUdpDisplay` classes
- ✅ **Camera streaming** — Camera1 and Camera2 APIs
- ✅ **Audio capture** — AAC, G711, Opus codecs
- ✅ **UDP/RTP transport** — direct UDP streaming support
- ✅ **Background service** — has a screen streaming example that runs as a foreground service
- ✅ **H.264/H.265/AV1** video encoding
- ⚠️ **Screen + Camera simultaneously** — Discussion #961 confirms: camera stream yes, screen stream yes, but **simultaneous screen+camera is NOT natively supported** in a single instance. Would need two instances or custom integration.
- ✅ **No root required**

**What we can extract:**
- The entire encoding pipeline (MediaCodec wrappers)
- UDP/RTP packet sending logic
- Screen capture via MediaProjection (DisplayBase classes)
- Camera capture (CameraBase classes)
- Audio capture (AudioBase classes)
- Service lifecycle management for background streaming

**Modifications needed:**
- Extend to run multiple capture pipelines simultaneously (screen + camera + mic)
- Add custom multiplexing over UDP (multiple logical streams on different ports)
- Remove RTMP/RTSP dependencies if not needed (or keep for compatibility)

**Effort saved:** ~60-70% of Android-side encoding + capture code

---

### ScreenStream (dkrivoruchko/screenstream)
| Field | Value |
|-------|-------|
| **URL** | https://github.com/dkrivoruchko/screenstream |
| **Stars** | 2,434 |
| **Language** | Kotlin |
| **License** | MIT ✅ |
| **Modes** | WebRTC (Global), MJPEG (Local), RTSP |

**Capabilities:**
- ✅ Screen capture via MediaProjection
- ✅ Audio streaming support
- ✅ Clean, modern Kotlin code
- ❌ No UDP/RTP direct transport (WebRTC/MJPEG/RTSP only)
- ❌ No camera support
- ❌ No background service daemon mode

**What we can extract:**
- MediaProjection permission handling code
- Screen capture initialization patterns
- Audio capture integration

**Modifications needed:** Significant — would need to replace transport layer entirely

**Effort saved:** ~20% (good reference, limited reuse)

---

### libstreaming (fyhertz/libstreaming)
| Field | Value |
|-------|-------|
| **URL** | https://github.com/fyhertz/libstreaming |
| **Stars** | 3,588 |
| **Language** | Java |
| **License** | Apache-2.0 ✅ |
| **Last Active** | **Stale** (last significant update years ago) |

**Capabilities:**
- ✅ Camera + Microphone RTP streaming over UDP
- ✅ H.264, H.263, AMR, AAC encoding
- ✅ Clean RTP implementation
- ❌ **No screen capture** (camera/mic only)
- ❌ **Stale/abandoned** — no recent updates
- ❌ Uses deprecated APIs

**What we can extract:**
- RTP packet construction code (Java-based, easy to understand)
- H.264 RTP packetization (RFC 3984 compliant)
- SDP generation

**Modifications needed:** Would need significant modernization

**Effort saved:** ~15% (good RTP reference, but RootEncoder is better)

---

### Android-RTSP-ScreenCaster (warren-bank)
| Field | Value |
|-------|-------|
| **URL** | https://github.com/warren-bank/Android-RTSP-ScreenCaster |
| **Stars** | 19 |
| **License** | GPL-3.0 ✅ |

**Capabilities:**
- ✅ Screen capture via MediaProjection → RTSP
- ❌ Very small project, limited features
- ❌ No UDP transport

**Verdict:** Reference only — too small to reuse meaningfully.

---

### openstf/minicap
| Field | Value |
|-------|-------|
| **URL** | https://github.com/openstf/minicap |
| **Stars** | 1,849 |
| **Language** | C/C++ (NDK) |
| **License** | NOASSERTION ⚠️ |

**Capabilities:**
- ✅ Native (C++) screen capture using virtual display
- ✅ Very low latency
- ✅ Socket-based streaming
- ❌ No encoding (raw frame streaming)
- ❌ No audio support
- ⚠️ License unclear

**What we can extract:**
- Virtual display creation pattern (NDK-level)
- Socket streaming architecture
- Screen capture initialization via NDK

**Effort saved:** ~10% (good NDK reference)

---

## Category 2: Android Camera Streaming Libraries

### RootEncoder Camera Support (covered above)
- Camera1 and Camera2 API support
- H.264/H.265 encoding
- UDP/RTSP/RTMP transport
- **Reuse strategy:** Same as screen — use RootEncoder's camera classes

### AndroidUSBCamera (jiangdongguo/androidusbcamera)
| Field | Value |
|-------|-------|
| **URL** | https://github.com/jiangdongguo/androidusbcamera |
| **Stars** | 2,719 |
| **Language** | C (NDK) + Java |
| **License** | Apache-2.0 ✅ |

**Capabilities:**
- ✅ USB UVC camera support on Android
- ✅ Multi-camera support
- ✅ Native C implementation (libuvc-based)
- ✅ YUV/MJPEG capture

**What we can extract:**
- UVC device enumeration and streaming
- USB host permission handling
- Native camera capture pipeline

**Relevance:** Useful if AllRelay wants to support USB cameras connected to the phone. Low priority for MVP.

---

### UVCCamera (saki4510t)
| Field | Value |
|-------|-------|
| **URL** | https://github.com/saki4510t/UVCCamera |
| **Stars** | 3,800+ (historical) |
| **License** | Apache-2.0 ✅ |
| **Status** | Deprecated (superseded by AndroidUSBCamera) |

**Verdict:** Use AndroidUSBCamera instead.

---

## Category 3: Android Audio Streaming

### RootEncoder Audio Support (covered above)
- AAC, G711, **Opus** encoding
- Microphone capture
- **Reuse strategy:** Use RootEncoder's audio classes

### AndroidMic (teamclouday/AndroidMic)
| Field | Value |
|-------|-------|
| **URL** | https://github.com/teamclouday/AndroidMic |
| **Stars** | 1,254 |
| **Language** | Rust + Kotlin |
| **License** | GPL-3.0 ✅ |

**Capabilities:**
- ✅ Phone → PC microphone streaming
- ✅ TCP/UDP transport
- ✅ RNNoise denoising
- ✅ USB serial support
- ❌ PC → Phone (speaker) not supported
- ❌ No video component

**What we can extract:**
- Audio capture patterns (Kotlin side)
- Virtual audio device setup (PC side, Rust)
- Network audio streaming protocol

**Relevance:** Good reference for mic streaming. The Rust PC-side code is interesting but may be overkill.

---

### phonespeakermic (TREEofbusybeaver)
| Field | Value |
|-------|-------|
| **URL** | https://github.com/TREEofbusybeaver/phonespeakermic |
| **Stars** | 1 |
| **License** | MIT ✅ |

**Capabilities:**
- ✅ Bidirectional audio (mic + speaker) ← **This is exactly what we need for audio**
- ✅ WiFi + USB tethering
- ✅ Python server + Android app
- ✅ Low-latency audio streaming

**What we can extract:**
- Bidirectional audio streaming protocol
- Virtual microphone/speaker setup on PC (Python + PyAudio)
- Android AudioRecord + AudioTrack usage patterns

**Modifications needed:** Would need to integrate with our architecture, replace Python server with native Linux daemon

**Effort saved:** ~15% (good audio architecture reference)

---

### sndlink (koraa/sndlink)
| Field | Value |
|-------|-------|
| **URL** | https://github.com/koraa/sndlink |
| **Stars** | 17 |
| **Language** | C++ |
| **License** | MIT ✅ |

**Capabilities:**
- ✅ Realtime audio streaming over UDP
- ✅ Linux + Android (Termux) support
- ✅ C++ native implementation

**What we can extract:**
- Low-latency UDP audio streaming patterns
- C++ audio capture/playback code

**Relevance:** Good low-level reference for native audio streaming.

---

### PC-Audio-Stream-to-Phone (anton1615)
| Field | Value |
|-------|-------|
| **URL** | https://github.com/anton1615/PC-Audio-Stream-to-Phone |
| **Stars** | 3 |
| **License** | MIT ✅ |

**Capabilities:**
- ✅ PC → Phone audio streaming (speaker direction)
- ✅ Opus codec
- ✅ UDP transport
- ✅ Oboe (Android low-latency audio)

**Relevance:** Good reference for the PC→Phone audio direction. Uses Rust + Oboe.

---

### tinyalsa-ndk
| Field | Value |
|-------|-------|
| **URL** | https://github.com/hasanbulat/tinyalsa-ndk |
| **Stars** | 0 |
| **License** | N/A |

**Relevance:** tinyalsa for Android NDK. Useful if we need direct ALSA access from native code. The official tinyalsa (https://github.com/tinyalsa/tinyalsa) is more maintained.

---

## Category 4: Linux Server / Client Side

### scrcpy (Genymobile/scrcpy)
| Field | Value |
|-------|-------|
| **URL** | https://github.com/Genymobile/scrcpy |
| **Stars** | 115,000+ |
| **Language** | C (server) + C (client) |
| **License** | Apache-2.0 ✅ |
| **Status** | Very actively maintained |

**Capabilities:**
- ✅ Screen mirroring via MediaProjection (server side)
- ✅ Camera mirroring (`--video-source=camera`)
- ✅ Audio forwarding (`--audio-source=mic` / `--audio-source=display`)
- ✅ **v4l2loopback output** (`--v4l2-sink`) — creates virtual webcam
- ✅ Native C implementation (very efficient)
- ✅ Works over ADB (USB + TCP)

**What we can extract:**
- **scrcpy server JAR** — can be pushed to device and started, provides screen+camera+audio capture
- **v4l2 output code** — for creating virtual webcam on Linux
- **H.264 decoding pipeline**
- **Socket protocol** for device↔host communication

**Key insight:** scrcpy already does screen + camera + audio capture, and outputs to v4l2loopback on Linux. We could potentially use scrcpy as a backend and build our control layer on top.

**Modifications needed:**
- scrcpy uses ADB for transport (not WiFi RTP directly)
- Would need to add WiFi RTP/UDP transport layer
- Would need to customize the multiplexing

**Effort saved:** ~40% if we fork scrcpy; ~20% if we extract components

---

### DroidCam Linux Client (dev47apps/droidcam-linux-client)
| Field | Value |
|-------|-------|
| **URL** | https://github.com/dev47apps/droidcam-linux-client |
| **Stars** | 1,212 |
| **Language** | C |
| **License** | GPL-2.0 ✅ |

**Capabilities:**
- ✅ Creates v4l2loopback virtual webcam from phone camera
- ✅ Has its own v4l2loopback variant (v4l2loopback-dc)
- ✅ JPEG transport over proprietary protocol
- ❌ No screen mirroring
- ❌ No audio support
- ❌ Proprietary Android app (only Linux client is open source)

**What we can extract:**
- v4l2loopback device creation code
- Video frame → v4l2loopback write pattern
- USB/ADB connection handling

**Effort saved:** ~10% (good v4l2 reference)

---

### v4l2loopback GStreamer Pipelines
**Reference:**
```
# Receive H.264 RTP and display on virtual webcam:
gst-launch-1.0 -v udpsrc port=5000 caps='application/x-rtp, media=video, clock-rate=90000, encoding-name=H264' \
  ! rtph264depay ! h264parse ! avdec_h264 ! videoconvert ! v4l2sink device=/dev/video0
```

**Relevance:** This is the exact pipeline we'll need on the Linux side. Well-documented in GStreamer and v4l2loopback communities.

---

### PipeWire RTP Modules
**Built-in PipeWire modules:**
- `libpipewire-module-rtp-source` — receives RTP audio, creates PipeWire source
- `libpipewire-module-rtp-sink` — sends audio as RTP packets
- Supports Opus, PCM, and other codecs

**Relevance:** PipeWire's built-in RTP modules can handle the audio side on Linux without custom code. We can configure PipeWire to receive audio RTP from the phone and expose it as a virtual microphone/speaker.

**What we can use:**
- PipeWire RTP source module for mic audio from phone
- PipeWire RTP sink module for speaker audio to phone
- Configuration files (no code to write)

**Effort saved:** ~30% of Linux audio plumbing

---

### ireader/media-server
| Field | Value |
|-------|-------|
| **URL** | https://github.com/ireader/media-server |
| **Stars** | 3,484 |
| **Language** | C |
| **License** | MIT ✅ |

**Capabilities:**
- ✅ Complete RTSP/RTP/RTMP stack in C
- ✅ H.264/H.265 RTP packetization/depacketization
- ✅ MPEG-TS muxing
- ✅ Well-tested, production-quality code

**What we can extract:**
- RTP packetizer/depacketizer for H.264
- SDP parser/generator
- MPEG-TS muxer (if we use TS container for UDP)
- RTSP server (if needed)

**Effort saved:** ~20% of protocol handling code

---

## Category 5: Unified / All-in-One Projects

### No single all-in-one project exists that does everything AllRelay needs.
The closest combinations are:
1. **RootEncoder** (Android side: screen+camera+audio) + **scrcpy v4l2** (Linux side: virtual webcam)
2. **scrcpy** (does screen+camera+audio via ADB) + custom WiFi transport

---

## Category 6: Magisk Module Templates

### HANA-CI-Build-Project/magisk-module-template
| Field | Value |
|-------|-------|
| **URL** | https://github.com/HANA-CI-Build-Project/magisk-module-template |
| **Stars** | ~10 |
| **Template** | Magisk 20.3+ format (customize.sh + service.sh) |

**Structure:**
```
module/
├── customize.sh      # Installation script
├── service.sh        # Boot service script (runs at boot)
├── system/
│   └── bin/          # Native binaries go here
├── module.prop       # Module metadata
└── META-INF/         # For manual install
```

**What we can extract:**
- Module structure and packaging
- service.sh pattern for starting daemon at boot
- SELinux context setting for native daemons

### SAPTeamDEV/Magisk-Module-Template
- GitHub Actions-powered template
- Automates build and release

### Official Magisk Guide
- https://topjohnwu.github.io/Magisk/guides.html
- Documents service.sh, post-fs-data.sh, native binary deployment

**Key insight:** For a Magisk module approach:
1. Compile native daemon (C/C++) for ARM
2. Place in `system/bin/` or `system/lib/`
3. Use `service.sh` to start daemon at boot
4. Set SELinux contexts with `magiskpolicy`

---

## Category 7: Protocol / Framing Libraries

### uvgRTP (ultravideo/uvgRTP)
| Field | Value |
|-------|-------|
| **URL** | https://github.com/ultravideo/uvgRTP |
| **Stars** | 436 |
| **Language** | C++ |
| **License** | BSD-2-Clause ✅ |

**Capabilities:**
- ✅ Complete RTP/SRTP library
- ✅ H.264, H.265, H.266 payload formats
- ✅ Opus payload support
- ✅ High performance, well-tested
- ✅ Used in academic research

**What we can extract:**
- Full RTP packet construction/deconstruction
- SRTP encryption (if needed)
- H.264/H.265/Opus RTP payload handlers
- Jitter buffer

**Effort saved:** ~25% of protocol layer (if we use it instead of ireader/media-server)

---

### tinydigger/RTPH264Streaming
| Field | Value |
|-------|-------|
| **URL** | https://github.com/tinydigger/RTPH264Streaming |
| **Stars** | 17 |
| **License** | N/A ⚠️ |

**Verdict:** Small reference project. uvgRTP or ireader/media-server are better choices.

---

## Dependency Map

### 1. FORK DIRECTLY
| Repo | Fork For | Notes |
|------|----------|-------|
| **pedroSG94/RootEncoder** | Android app (capture + encoding) | Fork and extend for multi-stream support |
| **HANA-CI-Build-Project/magisk-module-template** | Magisk module packaging | Minimal template, easy to customize |

### 2. EXTRACT SPECIFIC COMPONENTS FROM
| Repo | Extract | Component |
|------|---------|-----------|
| **Genymobile/scrcpy** | v4l2 output code, virtual display creation | Linux virtual webcam + screen capture patterns |
| **ireader/media-server** | RTP packetizer/depacketizer, SDP, MPEG-TS | Protocol layer (C library, MIT) |
| **ultravideo/uvgRTP** | RTP/SRTP library | Alternative to ireader for protocol layer (BSD) |
| **dev47apps/droidcam-linux-client** | v4l2loopback device creation | Linux virtual camera setup |
| **teamclouday/AndroidMic** | Audio capture patterns, virtual audio device | Mic streaming reference |
| **TREEofbusybeaver/phonespeakermic** | Bidirectional audio architecture | Speaker + mic streaming reference |
| **jiangdongguo/androidusbcamera** | USB camera handling (optional) | Future USB camera support |

### 3. USE AS-IS (Configuration Only)
| Tool | For |
|------|-----|
| **PipeWire RTP modules** | Linux audio source/sink (mic + speaker) |
| **v4l2loopback kernel module** | Linux virtual video device |
| **GStreamer** | Media pipeline glue on Linux |

### 4. WRITE FROM SCRATCH
| Component | Reason | Est. Effort |
|-----------|--------|-------------|
| **AllRelay control protocol** | Custom multiplexed protocol for screen+camera+mic+speaker over WiFi | 2 weeks |
| **Android multi-stream manager** | Coordinate screen + camera + audio capture simultaneously | 1 week |
| **Unified Android app UI** | Settings, connection management, permissions | 1-2 weeks |
| **Linux daemon (allrelay-daemon)** | Receive streams, create v4l2 devices, manage PipeWire | 2 weeks |
| **SDP/session negotiation** | Agree on codecs, resolution, ports at connection time | 3 days |
| **Magisk module integration** | Package daemon + auto-start | 3 days |

---

## Estimated Effort Saved by Reusing

| Component | Without Reuse | With Reuse | Savings |
|-----------|--------------|------------|---------|
| Android screen capture | 2-3 weeks | 2-3 days (RootEncoder) | **85%** |
| Android camera capture | 1-2 weeks | 1-2 days (RootEncoder) | **80%** |
| Android audio capture | 1 week | 1-2 days (RootEncoder + references) | **75%** |
| Android H.264/AAC encoding | 2 weeks | 0 days (RootEncoder) | **100%** |
| RTP packet construction | 1-2 weeks | 0 days (uvgRTP or ireader) | **100%** |
| Linux v4l2 virtual camera | 1 week | 1-2 days (scrcpy + droidcam ref) | **70%** |
| Linux audio routing | 1-2 weeks | 2-3 days (PipeWire RTP modules) | **75%** |
| Magisk module packaging | 1 week | 1-2 days (template) | **75%** |
| **TOTAL** | **~12-16 weeks** | **~4-5 weeks** | **~65-70%** |

---

## Recommended Architecture

```
┌─────────────────────────────────────────┐
│           Android Device (AllRelay)     │
│                                         │
│  ┌──────────┐  ┌──────────┐  ┌───────┐ │
│  │ Screen   │  │ Camera   │  │ Audio │ │
│  │ Capture  │  │ Capture  │  │Record │ │
│  │(MediaProj)│ │(Camera2) │  │(AAudio)│ │
│  └────┬─────┘  └────┬─────┘  └───┬───┘ │
│       │              │            │      │
│  ┌────┴──────────────┴────────────┴───┐ │
│  │     RootEncoder Fork               │ │
│  │  (H.264 + Opus encoding)           │ │
│  └───────────────┬────────────────────┘ │
│                  │                       │
│  ┌───────────────┴────────────────────┐ │
│  │     AllRelay Control Layer         │ │
│  │  (Session negotiation, SDP,        │ │
│  │   multiplexed UDP transport)       │ │
│  └───────────────┬────────────────────┘ │
│                  │                       │
└──────────────────┼───────────────────────┘
                   │ WiFi (UDP/RTP)
                   │
┌──────────────────┼───────────────────────┐
│     Linux PC (allrelay-daemon)          │
│                  │                       │
│  ┌───────────────┴────────────────────┐ │
│  │  allrelay-daemon (C/Rust)          │ │
│  │  - RTP receiver                    │ │
│  │  - H.264 depacketizer (uvgRTP)    │ │
│  │  - Opus decoder (libopus)          │ │
│  └──┬────────┬────────┬──────────────┘ │
│     │        │        │                 │
│  ┌──┴──┐  ┌──┴──┐  ┌──┴──────────┐    │
│  │v4l2 │  │v4l2 │  │ PipeWire    │    │
│  │loop │  │loop │  │ RTP Source  │    │
│  │(scr)│  │(cam)│  │ (mic audio) │    │
│  └──┬──┘  └──┬──┘  └──────┬──────┘    │
│     │        │             │            │
│  /dev/vid0 /dev/vid1   virtual mic     │
│  (screen)  (camera)    (from phone)    │
└─────────────────────────────────────────┘
```

---

## Key Risks & Considerations

1. **Simultaneous capture:** Android limits MediaProjection + Camera to the same app. RootEncoder's DisplayBase and CameraBase use different encoder instances — running both simultaneously requires careful resource management (2 MediaCodec instances).

2. **Audio on Android 10+:** Internal audio capture requires MediaProjection. Microphone capture is straightforward via AudioRecord. For speaker functionality (PC→phone), we need AudioTrack playback.

3. **WiFi latency:** UDP/RTP over WiFi will have variable latency. Consider adding FEC (Forward Error Correction) and jitter buffers.

4. **SELinux (root path):** Running a native daemon via Magisk requires proper SELinux policy. The Magisk template handles this.

5. **Android 14+ restrictions:** Foreground service type must be declared (`mediaProjection`, `microphone`). RootEncoder handles this but needs configuration.

6. **scrcpy vs custom:** scrcpy already does most of what we need but uses ADB transport. Forking scrcpy and adding WiFi transport might be faster than building from RootEncoder, but gives less control.
