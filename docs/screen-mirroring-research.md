# Android Screen Mirroring to Linux/Ubuntu: Comprehensive Technical Analysis

*Generated: 2026-06-09 | Sources: 50+ | Focus: Rooted devices, unified app integration*

---

## Executive Summary

Screen mirroring from Android to Linux involves three major subsystems: **screen capture + encoding** on the device, **network transport**, and **decoding + display** on the host. scrcpy — the gold standard — achieves 35–70ms glass-to-glass latency using `MediaCodec` encoding of a virtual `Surface`, transmitted over an ADB tunnel, decoded with FFmpeg and rendered with SDL2 [1][2]. For a **rooted device**, root access enables significant advantages: bypassing MediaProjection consent dialogs, potentially hiding recording indicators, and using higher-privilege input injection methods (UHID, direct `/dev/input`). However, the core capture mechanism (virtual display → MediaCodec → Surface) remains the same whether rooted or not — scrcpy itself doesn't use root [1].

---

## 1. Screen Capture on Android

### 1.1 How scrcpy Captures the Screen

scrcpy uses a **two-path approach** for screen capture, both visible in the `ScreenCapture.java` source code [3]:

**Path 1: DisplayManager API (preferred)**
```
ServiceManager.getDisplayManager()
    .createVirtualDisplay("scrcpy", width, height, displayId, surface)
```
This creates a virtual display mirroring the specified physical display, rendering into a `Surface` that feeds into MediaCodec [3].

**Path 2: SurfaceControl API (fallback)**
When `DisplayManager` fails (e.g., permission issues), scrcpy falls back to:
```java
IBinder display = SurfaceControl.createDisplay("scrcpy", secure);
SurfaceControl.setDisplaySurface(display, surface);
SurfaceControl.setDisplayProjection(display, 0, deviceRect, displayRect);
SurfaceControl.setDisplayLayerStack(display, layerStack);
```
This uses the hidden `android.view.SurfaceControl` class via reflection [3][4].

**The key insight**: Neither path uses `MediaProjection`. scrcpy runs as the `shell` user via `app_process`, which has sufficient privileges to create virtual displays without the MediaProjection consent dialog [1][5].

### 1.2 The Encoding Pipeline

Once a `Surface` is available, `SurfaceEncoder.java` handles encoding [6]:

1. Creates a `MediaCodec` encoder (H.264 by default)
2. Configures it with the `Surface` as input (`COLOR_FormatSurface`)
3. MediaCodec hardware-encodes the surface content directly — no CPU-side pixel copying
4. Encoded packets are written to the video socket

Key MediaFormat settings from scrcpy's source [6]:
```java
format.setInteger(MediaFormat.KEY_BIT_RATE, bitRate);
format.setInteger(MediaFormat.KEY_FRAME_RATE, 60);  // nominal, actual is variable
format.setInteger(MediaFormat.KEY_COLOR_FORMAT, COLOR_FormatSurface);
format.setInteger(MediaFormat.KEY_I_FRAME_INTERVAL, 10);  // 10 seconds
format.setLong(MediaFormat.KEY_REPEAT_PREVIOUS_FRAME_AFTER, 100_000);  // 100ms
format.setInteger(MediaFormat.KEY_LATENCY, 1);  // Android 8+, output 1 frame as soon as 1 queued
format.setInteger(MediaFormat.KEY_PRIORITY, 0);  // real-time priority (Android 6+)
```

### 1.3 Screen Capture Approaches Comparison for Rooted Devices

| Approach | Root Required? | Consent Dialog? | Notification? | Latency | Reliability |
|----------|---------------|-----------------|---------------|---------|-------------|
| **Virtual Display (scrcpy method)** | No (shell user) | No | No | ★★★★★ | ★★★★★ |
| **SurfaceControl.createDisplay** | No (shell) | No | No | ★★★★★ | ★★★★ |
| **MediaProjection API** | No | **Yes** (each time) | **Yes** (persistent) | ★★★★ | ★★★★★ |
| **Direct framebuffer (/dev/graphics/fb0)** | Yes | No | No | ★★★ | ★ |
| **SurfaceFlinger screenshot** | Yes | No | No | ★★ | ★★ |
| **/dev/graphics/fb0** — why it's bad | — | — | — | — | — |

### 1.4 Direct Framebuffer Access — Avoid It

Reading `/dev/graphics/fb0` with root is a common suggestion but is **effectively broken on modern Android** [7][8]:

- Modern Android uses Hardware Composer (HWC) which composites multiple gralloc surfaces at scan-out time
- The framebuffer device may show only black, or only the last GPU-rendered layer
- Most Android 8+ devices with HWC 2.0 don't have a meaningful framebuffer device
- No hardware acceleration for capture, high CPU overhead
- Cannot capture secure/protected content

**Verdict**: Do NOT use `/dev/graphics/fb0`. Use the virtual display approach.

### 1.5 Root-Specific Capture Advantages

With root access, you gain:

1. **`service call SurfaceFlinger` commands** — Direct interaction with SurfaceFlinger (e.g., `service call SurfaceFlinger 1013 i32 1` for screen capture), but this is fragile and undocumented [9]
2. **No MediaProjection needed** — scrcpy already achieves this as `shell` user without root
3. **Hide recording indicators** — With root:
   ```bash
   device_config put privacy media_projection_indicators_enabled false default
   ```
   This disables the green recording dot on Android 12+ [10]
4. **`wm screen-capture` control** — Can enable/disable screen capture system-wide [11]
5. **System-level integration** — Run server as system user for maximum privileges

**Key finding**: For screen capture specifically, root provides **minimal additional benefit** over shell-user scrcpy. The main root advantage is for input injection and hiding indicators.

### 1.6 Achievable Latency

scrcpy achieves **35–70ms glass-to-glass** latency over USB, and **80–150ms** over Wi-Fi [1][12][13]. The latency budget:

| Component | Latency |
|-----------|---------|
| Screen capture → encoder input | ~0–5ms (Surface is zero-copy) |
| H.264 encoding (hardware) | ~5–15ms |
| Network transport (TCP/ADB) | ~1–5ms (USB), ~10–50ms (Wi-Fi) |
| FFmpeg decode | ~5–15ms |
| SDL2 render | ~1–5ms |
| **Total** | **~35–70ms (USB), ~80–150ms (Wi-Fi)** |

---

## 2. Input Control (Touch, Keyboard, Mouse)

### 2.1 scrcpy's Input Injection Methods

scrcpy offers **three input injection methods** [14][15]:

#### 2.1.1 SDK Mode (`--keyboard=sdk`, default)
- Uses hidden `InputManager.injectInputEvent()` via reflection [16]
- Injects `KeyEvent` and `MotionEvent` objects at the Android framework level
- Works over both USB and Wi-Fi
- Limited to ASCII and some special characters for keyboard
- Requires "USB debugging (Security Settings)" enabled on some devices

```java
// From scrcpy's InputManager.java wrapper
Method method = InputManager.class.getMethod("injectInputEvent", InputEvent.class, int.class);
return (boolean) method.invoke(manager, inputEvent, INJECT_INPUT_EVENT_MODE_ASYNC);
```

#### 2.1.2 UHID Mode (`--keyboard=uhid` or `-K`)
- Uses the **UHID kernel module** (`/dev/uhid`) to simulate a physical HID keyboard [15]
- Creates a virtual USB HID device at the kernel level
- Works for ALL characters and IME (unlike SDK mode)
- Works over Wi-Fi (unlike AOA)
- Can disable the on-screen keyboard
- Requires configuring keyboard layout on the device once

#### 2.1.3 AOA Mode (`--keyboard=aoa`)
- Uses AOAv2 (Android Open Accessory) protocol
- Simulates physical HID keyboard at USB level
- **Only works over USB** (not Wi-Fi)
- Does not require ADB or the scrcpy server

### 2.2 Root-Specific Input Methods

With root, additional methods become available:

#### 2.2.1 Direct `/dev/input/eventX` Injection
- Write raw input event structs directly to the device node [17][18]
- Requires finding the correct input device (use `getevent -pl`)
- Low-level but very fast
- Multi-touch support by writing `ABS_MT_POSITION_X`, `ABS_MT_POSITION_Y`, `ABS_MT_TRACKING_ID`, `SYN_MT_REPORT`, `SYN_REPORT` sequences

```bash
# Example: inject touch at (500, 500)
sendevent /dev/input/event2 3 57 0      # ABS_MT_TRACKING_ID = 0
sendevent /dev/input/event2 3 53 500    # ABS_MT_POSITION_X = 500
sendevent /dev/input/event2 3 54 500    # ABS_MT_POSITION_Y = 500
sendevent /dev/input/event2 1 330 1     # BTN_TOUCH = 1
sendevent /dev/input/event2 0 0 0       # SYN_REPORT
```

#### 2.2.2 UInput Virtual Device
- Create a virtual input device using the kernel's uinput module [19][20]
- Write to `/dev/uinput` (or `/dev/input/uinput`)
- Register capabilities (EV_KEY, EV_ABS, etc.)
- Then write events to the virtual device
- Advantage: creates a persistent device that Android recognizes
- Multi-touch: use `ABS_MT_*` protocol

```c
// Conceptual flow:
int fd = open("/dev/uinput", O_WRONLY | O_NONBLOCK);
// Configure device capabilities via ioctl
struct uinput_setup setup = { .name = "Virtual Touch", ... };
ioctl(fd, UI_SET_SETUP, &setup);
// Write events
struct input_event ev = { .type = EV_ABS, .code = ABS_MT_POSITION_X, .value = 500 };
write(fd, &ev, sizeof(ev));
```

### 2.3 Multi-Touch Handling

For multi-touch, the **MT Protocol B** (used by Android) requires [18]:

1. Send `ABS_MT_TRACKING_ID` for each pointer (unique per finger)
2. Send `ABS_MT_POSITION_X` and `ABS_MT_POSITION_Y`
3. Send `ABS_MT_PRESSURE` (optional)
4. Send `SYN_MT_REPORT` after each pointer's data
5. Send `SYN_REPORT` after all pointers

For 5-point multi-touch, you'd send 5 groups of (tracking_id, x, y) + SYN_MT_REPORT, then SYN_REPORT.

**Recommendation for your app**: Use `InputManager.injectInputEvent()` (SDK mode) as the primary method — it handles all complexity including multi-touch. For special cases, UHID is superior. Direct `/dev/input` should be a fallback.

### 2.4 Special Keys (Back, Home, Recent)

scrcpy maps these as follows [21]:
- **Back**: Right-click (mouse) or mapped key
- **Home**: Middle-click (mouse) or mapped key
- **Recent apps**: Custom mapping
- **Volume up/down**: Custom shortcuts (MOD+Up/Down)
- **Power**: Custom shortcut (MOD+P)
- **Screen on/off**: Custom shortcut (MOD+O)

These are all sent as `KeyEvent` with the appropriate Android keycode (KEYCODE_BACK, KEYCODE_HOME, etc.) via `InputManager.injectInputEvent()`.

---

## 3. Encoding for Screen Content

### 3.1 H.264 vs H.265 for Screen Mirroring

| Aspect | H.264 | H.265 (HEVC) |
|--------|-------|--------------|
| **Text readability** | Good | Better (less blocking artifacts) |
| **Sharp edges** | Good | Slightly better |
| **Bandwidth** | Baseline | ~30-50% less for same quality |
| **Encoding speed** | Fast | Slower (may increase latency) |
| **Decoding on PC** | Excellent (hardware everywhere) | Good (most modern GPUs) |
| **Device support** | Universal | Android 5.0+ (most devices) |
| **scrcpy support** | Default | `--video-codec=h265` |

**Recommendation**: H.264 is the safe default. H.265 is better for bandwidth-constrained Wi-Fi but test encoding latency on target devices. scrcpy also supports AV1 (`--video-codec=av1`) but it's rarely hardware-encodable on phones [1][6].

### 3.2 Optimal Encoder Settings for Screen Content

From scrcpy's actual configuration [6]:

```java
// Screen-specific settings
KEY_FRAME_RATE = 60              // Nominal frame rate (actual is variable)
KEY_I_FRAME_INTERVAL = 10        // I-frame every 10 seconds
KEY_REPEAT_PREVIOUS_FRAME_AFTER = 100_000  // Repeat last frame after 100ms
KEY_LATENCY = 1                  // Minimal encoding latency (Android 8+)
KEY_PRIORITY = 0                 // Real-time priority (Android 6+)
KEY_COLOR_FORMAT = COLOR_FormatSurface  // Zero-copy from display
KEY_COLOR_RANGE = COLOR_RANGE_LIMITED  // Android 7+
```

**For screen content specifically**:
- **I-frame interval**: 10s is fine for scrcpy (they accept frame drops). For your app, consider **2–5 seconds** for faster recovery from packet loss
- **Bitrate**: 2–8 Mbps for 1080p60 screen content (text-heavy needs more)
- **CBR vs VBR**: Use **VBR** — screen content varies wildly (static UI vs scrolling)
- **KEY_REPEAT_PREVIOUS_FRAME_AFTER**: Essential — keeps the display alive when screen is static

### 3.3 Different Settings for Screen vs Camera

| Parameter | Screen | Camera |
|-----------|--------|--------|
| I-frame interval | 2–5s | 1–2s |
| Bitrate (1080p) | 4–8 Mbps | 3–6 Mbps |
| Frame rate | 30–60 fps | 30 fps |
| Latency mode | Balanced | Ultra-low |
| Content type | `KEY_CONTENT_TYPE_NOT_SET` | `KEY_CONTENT_TYPE_VIDEO_CALL` |
| KEY_LATENCY | 1 | 1 |
| KEY_REPEAT_FRAME_AFTER | Yes | No |

---

## 4. Display on Linux Side

### 4.1 scrcpy's Display Architecture

scrcpy uses **SDL2 + FFmpeg** [1][2]:

```
H.264 stream → Demuxer → FFmpeg decoder → SDL_Texture → SDL_Renderer → Window
```

Key architecture points:
- **No buffering by default** — frames are displayed immediately upon decode
- Frame dropping — if rendering is delayed, old frames are dropped, always showing latest
- SDL2 handles window management, input capture, threading
- FFmpeg handles H.264/H.265/AV1 decoding via hardware-accelerated decoders

### 4.2 Display Options for Your App

| Option | Pros | Cons | Latency |
|--------|------|------|---------|
| **SDL2 (like scrcpy)** | Fastest, proven, cross-platform | C/C++ only, SDL dependency | ★★★★★ |
| **GStreamer** | Excellent pipeline model, many sinks | Heavier | ★★★★ |
| **Qt/QML with FFmpeg** | Good UI framework | More complex | ★★★ |
| **GTK with FFmpeg** | Native Linux look | More complex | ★★★ |
| **mpv/libmpv embedded** | Built-in buffering, OSD | Higher latency, less control | ★★★ |
| **VNC/remote** | Remote access | Adds protocol overhead | ★★ |

**Recommendation**: For lowest latency, **SDL2 + FFmpeg** is the proven choice. For a more integrated UI, **GStreamer** with `appsink` + custom rendering is excellent.

### 4.3 Handling Rotation and Resolution

scrcpy handles rotation **server-side** [1][3]:
- The server detects display rotation changes
- Resets and restarts the encoding session
- The client receives new dimensions in a 12-byte session packet header
- The client is unaware of device rotation — it just renders whatever dimensions arrive

For your app, implement:
1. Parse session headers for new dimensions
2. Resize the SDL/GTK window accordingly (or letterbox)
3. Handle aspect ratio preservation
4. For multi-monitor: allow window to span or pin to specific monitor

---

## 5. Protocol Design for Screen Mirroring

### 5.1 scrcpy's Protocol

scrcpy uses a **custom binary protocol** over TCP sockets through an ADB tunnel [1][2]:

- **Video socket**: Device → Client, raw H.264/H.265/AV1 packets with 12-byte frame headers
- **Audio socket**: Device → Client, raw Opus/AAC packets with headers
- **Control socket**: Bidirectional, input events + clipboard

**Wire format for video packets** [1]:
```
[Frame Header - 12 bytes]
  byte 0-7: PTS (u64) with flags in MSB (config packet, key frame)
  byte 8-11: packet size (u32)
[Packet Payload - N bytes]
  Raw MediaCodec output
```

Session packets (12 bytes) precede each capture session (rotation change) with new dimensions.

### 5.2 For Your Unified App

**Recommended architecture**:

```
┌─────────────────────────────────────────────────────┐
│ Android Device (Rooted)                             │
│                                                     │
│  Screen Encoder ──→ Screen Socket ──→ TCP/UDP ──┐   │
│  Camera Encoder ──→ Camera Socket ──→ TCP/UDP ──┤   │
│  Mic Encoder ────→ Audio Socket ───→ TCP/UDP ──┤   │
│  Control Listener ←── Control Socket ←── TCP ───┤   │
│  Speaker (reverse) ←── Speaker Socket ←── TCP ──┘   │
└─────────────────────────────────────────────────────┘
```

**Should screen share transport with camera?** Use **separate sockets** for each stream:
- Independent bitrate/resolution control
- Independent failure handling
- Simpler demuxing
- Independent latency tuning

**Bandwidth budget at 1080p60**:
| Stream | Typical Bitrate |
|--------|----------------|
| Screen | 4–8 Mbps |
| Camera (1080p30) | 3–5 Mbps |
| Mic audio (Opus) | 64–128 Kbps |
| Speaker (reverse, Opus) | 64–128 Kbps |
| Control (touch events) | < 10 Kbps |
| **Total** | **~8–14 Mbps** |

This is well within Wi-Fi 5 (802.11ac) capacity (~50–100 Mbps practical throughput) but may need adaptive bitrate on congested networks.

---

## 6. Root-Specific Advantages (Detailed)

### 6.1 Screen Capture Without Permission Dialog

**Yes, root can capture without MediaProjection consent**. scrcpy already does this as `shell` user [1][5]:
- `shell` UID (2000) has `CAPTURE_VIDEO_OUTPUT` and related permissions
- Virtual display creation via `DisplayManager` or `SurfaceControl` doesn't trigger MediaProjection
- With root, you can run as `system` user for even more privileges

### 6.2 Hide Screen Recording Indicator

With root on Android 12+ [10]:
```bash
# Disable media projection indicators
device_config put privacy media_projection_indicators_enabled false default

# Disable camera/microphone indicators
device_config put privacy camera_indicators_enabled false default
device_config put privacy microphone_indicators_enabled false default
```

**Note**: This requires `shell` or `root` access and may need a reboot. It affects the entire system, not just your app.

### 6.3 Direct SurfaceFlinger Access

With root, you can interact with SurfaceFlinger directly [9]:
```bash
# Take a screenshot
service call SurfaceFlinger 1013 i32 1

# List layers
dumpsys SurfaceFlinger

# Control display power
service call SurfaceFlinger 1008 i32 0 i32 2
```

However, for continuous screen capture, the virtual display approach is still superior.

---

## 7. scrcpy Architecture Deep Dive

### 7.1 Server Execution Model

scrcpy server is a Java application executed via `app_process` [1][5]:

```bash
adb push scrcpy-server /data/local/tmp/scrcpy-server.jar
adb shell CLASSPATH=/data/local/tmp/scrcpy-server.jar \
    app_process / com.genymobile.scrcpy.Server 4.0 [options...]
```

- Runs as `shell` user
- Has access to Android framework via reflection
- Server is a JAR (unsigned APK) containing `classes.dex`
- Executed as `com.genymobile.scrcpy.Server.main()`
- Removed from device immediately after execution (unlinked while still running)

### 7.2 Java Classes Used

Key classes in scrcpy server [3][4][6][16]:

| Class | Purpose |
|-------|---------|
| `ScreenCapture` | Manages virtual display creation and lifecycle |
| `SurfaceEncoder` | Creates MediaCodec encoder, encodes Surface to packets |
| `Controller` | Receives control messages, injects input events |
| `InputManager` (wrapper) | Reflection wrapper for hidden `InputManager.injectInputEvent()` |
| `SurfaceControl` (wrapper) | Reflection wrapper for hidden `SurfaceControl` methods |
| `ServiceManager` (wrapper) | Access to system services (DisplayManager, etc.) |
| `VirtualDisplay` | Created via DisplayManager API |
| `MediaCodec` | Hardware H.264/H.265 encoding |
| `AudioRecord` | Audio capture (with REMOTE_SUBMIX source) |

### 7.3 Server Components (Threading Model)

```
Main Thread
  ├── Video Thread (screen capture + encoding)
  ├── Audio Thread(s) (capture → encode → send)
  ├── Control Thread (receive input events from client)
  ├── Device Message Thread (send clipboard, etc. to client)
  └── Display Monitor Thread (watch for rotation changes)
```

Each socket (video, audio, control) has dedicated read/write threads on both client and server sides [1].

### 7.4 Screen Rotation Handling

1. `DisplayMonitor` watches for display property changes (rotation, size)
2. On change, `CaptureControl.reset()` is called
3. Virtual display is destroyed and recreated with new dimensions
4. MediaCodec encoding session is reset (new `Surface` → encoder pipeline)
5. New session packet with updated dimensions is sent to client
6. Client automatically adjusts to new frame dimensions

### 7.5 Wire Format Details

**Video session packet** (sent on rotation change) [1]:
```
Byte 0: 0x80 (session flag)
Byte 1-3: padding
Byte 4-7: client resized flag (for flex displays)
Byte 8-11: video width (u32 big-endian)
Byte 12-15: video height (u32 big-endian)
```

**Video media packet** (each encoded frame) [1]:
```
Byte 0-7: PTS (u64) with flags in 2 MSBs:
  bit 63: media packet flag
  bit 62: config packet flag  
  bit 61: key frame flag
  bits 60-0: PTS value
Byte 8-11: packet payload size (u32)
Byte 12+: raw MediaCodec output data
```

---

## 8. Integration with Unified App

### 8.1 Running All 4 Streams Simultaneously

**Can you use multiple MediaCodec instances?** Yes, with caveats [22][23]:

- Most Android devices support **2–3 simultaneous hardware encoder instances**
- Screen encoder + camera encoder = 2 instances (usually OK)
- Audio encoder is separate (AudioRecord + MediaCodec for Opus/AAC)
- GPU utilization is the limiting factor, not instance count

**Recommended thread model for unified app**:

```
┌─ Android Device Daemon ─────────────────────────────┐
│                                                      │
│  Thread 1: Screen Capture + Encode                   │
│    VirtualDisplay → MediaCodec (H.264) → Socket     │
│                                                      │
│  Thread 2: Camera Capture + Encode                   │
│    Camera2 API → MediaCodec (H.264) → Socket        │
│                                                      │
│  Thread 3: Mic Capture + Encode                      │
│    AudioRecord → MediaCodec (Opus) → Socket          │
│                                                      │
│  Thread 4: Speaker Capture (reverse)                 │
│    AudioRecord (remote submix) → Opus → Socket       │
│                                                      │
│  Thread 5: Control Listener                          │
│    Socket → InputManager.injectInputEvent()          │
│                                                      │
│  Thread 6: Device State Monitor                      │
│    Display changes, battery, network quality         │
│                                                      │
└──────────────────────────────────────────────────────┘
```

### 8.2 Resource Conflicts

| Resource | Screen | Camera | Conflict? |
|----------|--------|--------|-----------|
| MediaCodec hardware encoder | Yes | Yes | Possible — different encoder instances, shared GPU |
| Camera hardware | No | Yes | No conflict |
| Display/Surface | Yes | No | No conflict |
| CPU | Moderate | Low | Low risk |
| Memory | Low | Moderate | Low risk |
| Network | High | High | **Main bottleneck** |

**Mitigation strategies**:
1. Use different encoder names (e.g., `c2.qti.avc.encoder` for screen, `c2.qti.hevc.encoder` for camera)
2. Lower camera resolution when screen mirroring is active
3. Implement priority-based bitrate allocation
4. Detect and warn about hardware encoder limits

### 8.3 Unified Server Design

```
allrelay-server.jar
├── ScreenModule (ScreenCapture + SurfaceEncoder)
├── CameraModule (CameraCapture + CameraEncoder) 
├── AudioModule (AudioRecord + AudioEncoder)
├── SpeakerModule (RemoteSubmix + SpeakerEncoder)
├── ControlModule (Controller + InputManager)
├── NetworkModule (Socket management, connection pooling)
└── ConfigModule (Options parsing, negotiation)
```

Use **one ADB tunnel with multiple sockets** (like scrcpy), or **one TCP connection per stream** for simplicity.

---

## 9. Bandwidth Considerations

### 9.1 Bandwidth Requirements

| Quality Setting | Screen 1080p60 | Screen 720p30 | Camera 1080p30 |
|----------------|----------------|----------------|-----------------|
| Low quality | 2 Mbps | 1 Mbps | 1.5 Mbps |
| Medium quality | 4 Mbps | 2 Mbps | 3 Mbps |
| High quality | 8 Mbps | 4 Mbps | 5 Mbps |
| Ultra quality | 15 Mbps | 8 Mbps | 8 Mbps |

### 9.2 Adaptive Bitrate for Wi-Fi

Implement a simple adaptive bitrate algorithm [24]:

```python
# Pseudocode for adaptive bitrate
class AdaptiveBitrate:
    def __init__(self):
        self.current_bitrate = 4_000_000  # 4 Mbps
        self.min_bitrate = 500_000         # 500 Kbps
        self.max_bitrate = 15_000_000      # 15 Mbps
        self.rtt_threshold = 50  # ms
        self.loss_threshold = 0.02  # 2%
    
    def update(self, rtt_ms, packet_loss_rate, queue_depth):
        if rtt_ms > self.rtt_threshold or packet_loss_rate > self.loss_threshold:
            self.current_bitrate *= 0.8  # Reduce by 20%
        elif rtt_ms < self.rtt_threshold * 0.5 and queue_depth < 2:
            self.current_bitrate *= 1.1  # Increase by 10%
        self.current_bitrate = clamp(self.current_bitrate, self.min_bitrate, self.max_bitrate)
```

### 9.3 Wi-Fi Congestion with All 4 Streams

When running screen + camera + mic + speaker simultaneously:

1. **Prioritize streams**: Screen > Camera > Audio > Speaker
2. **Lower camera quality** when screen mirroring is active
3. **Use UDP for video** (with FEC) if latency matters more than reliability
4. **TCP for control and audio** (reliability matters)
5. **Reduce resolution before bitrate** — 720p at high quality beats 1080p at low quality for readability
6. **Consider Wi-Fi 6 (802.11ax)** for OFDMA — better handling of multiple streams

---

## 10. Reference Projects

### 10.1 scrcpy (Primary Reference)
- **GitHub**: https://github.com/Genymobile/scrcpy (143K+ stars)
- **Architecture**: Java server (app_process) + C client (SDL2 + FFmpeg)
- **License**: Apache 2.0
- **Key insight**: Shell user has enough permissions for screen capture + input injection
- **Limitation**: Designed for single-device mirroring, not a multi-stream daemon

### 10.2 droidVNC-NG
- **GitHub**: https://github.com/bk138/droidVNC-NG
- VNC server using MediaProjection + MediaCodec
- No root required (uses Android 7+ APIs)
- Good reference for VNC-based approach

### 10.3 ScreenStream
- **GitHub**: https://github.com/dkrivoruchko/ScreenStream
- Streams screen via HTTP/MJPEG/WebRTC
- Uses MediaProjection
- Good for web-based viewing

### 10.4 sji-android-screen-capture
- **GitHub**: https://github.com/sjitech/sji-android-screen-capture
- HTML5 browser-based viewer
- No root required
- Project stopped but good reference

### 10.5 UInput Virtual Device Reference
- **Blog**: https://brunodmt.github.io/rust/2018/11/03/android-virtual-input-with-rust.html
- Creating virtual touch input via uinput in Rust on Android
- Cross-compiled to ARM for Android

---

## 11. Implementation Recommendations

### 11.1 Recommended Architecture

```
┌──────────────────────┐          ┌──────────────────────┐
│   Android Device     │          │   Linux PC           │
│   (Rooted)           │          │                      │
│                      │  TCP/    │                      │
│  allrelay-daemon     │  Wi-Fi   │  allrelay-client     │
│  ├── ScreenCapture   │─────────→│  ├── ScreenDecoder   │
│  ├── CameraCapture   │─────────→│  ├── CameraDecoder   │
│  ├── MicCapture      │─────────→│  ├── AudioPlayer     │
│  ├── SpeakerCapture  │←─────────│  ├── SpeakerCapture  │
│  ├── ControlServer   │←─────────│  ├── InputCapture    │
│  └── AdaptiveBR      │          │  └── AdaptiveBR      │
└──────────────────────┘          └──────────────────────┘
```

### 11.2 Key Technical Decisions

1. **Screen Capture**: Use scrcpy's approach (VirtualDisplay → MediaCodec) — no root needed, but root allows indicator hiding
2. **Input Injection**: Primary: `InputManager.injectInputEvent()` via reflection. Secondary: UHID for physical keyboard simulation
3. **Encoding**: H.264 default, H.265 option for bandwidth-constrained scenarios
4. **Transport**: Separate TCP sockets per stream, with optional UDP for screen video
5. **Display**: SDL2 + FFmpeg for lowest latency, or GStreamer for pipeline flexibility
6. **Adaptive Bitrate**: Monitor RTT and packet loss, adjust encoder bitrate dynamically
7. **Server**: Java daemon run via `app_process` (like scrcpy), persistent for multi-stream use

### 11.3 Root-Specific Enhancements to Implement

1. **Hide recording indicators**: `device_config put privacy media_projection_indicators_enabled false`
2. **Run as system user** for maximum permissions
3. **Direct input injection** via `/dev/input` as fallback
4. **Capture FLAG_SECURE content** (root can override this)
5. **Auto-grant permissions** for MediaProjection (if needed for certain devices)
6. **Disable screen timeout** during mirroring: `settings put system screen_off_timeout 2147483647`

### 11.4 Minimum Viable Implementation Steps

1. **Phase 1**: Port scrcpy's server-side screen capture code to your daemon
2. **Phase 2**: Add camera capture (Camera2 API → MediaCodec)
3. **Phase 3**: Add audio capture (AudioRecord → Opus) and playback (reverse channel)
4. **Phase 4**: Add input injection (InputManager + UHID)
5. **Phase 5**: Implement adaptive bitrate and quality negotiation
6. **Phase 6**: Add root-specific features (indicator hiding, FLAG_SECURE override)

---

## Sources

[1] [scrcpy for developers](https://scrcpyapp.org/en/guides/develop/) — Official developer documentation covering server/client architecture, protocol, encoding
[2] [scrcpy develop.md (GitHub)](https://raw.githubusercontent.com/Genymobile/scrcpy/master/doc/develop.md) — Canonical developer documentation
[3] [ScreenCapture.java](https://raw.githubusercontent.com/Genymobile/scrcpy/master/server/src/main/java/com/genymobile/scrcpy/video/ScreenCapture.java) — Virtual display creation and screen capture implementation
[4] [SurfaceControl.java wrapper](https://raw.githubusercontent.com/Genymobile/scrcpy/master/server/src/main/java/com/genymobile/scrcpy/wrappers/SurfaceControl.java) — Reflection wrapper for hidden SurfaceControl APIs
[5] [Introducing scrcpy (rom1v blog)](https://blog.rom1v.com/2018/03/introducing-scrcpy/) — Original technical deep-dive on scrcpy's design
[6] [SurfaceEncoder.java](https://raw.githubusercontent.com/Genymobile/scrcpy/master/server/src/main/java/com/genymobile/scrcpy/video/SurfaceEncoder.java) — MediaCodec encoder configuration and encoding loop
[7] [Android read fb0 black screen (StackOverflow)](https://stackoverflow.com/questions/17304611/android-read-fb0-always-give-me-blackscreen) — Why framebuffer doesn't work on modern Android
[8] [Why is FrameBuffer missing on some Android devices](https://android.stackexchange.com/questions/232608/why-is-the-framebuffer-missing-on-some-android-devices) — HWC replacing framebuffer
[9] [SurfaceFlinger screen capture (Google Groups)](https://groups.google.com/g/android-platform/c/N1r82hPgof4) — Direct SurfaceFlinger screenshot approach
[10] [Screen recording indicator disable (droidVNC-NG issue)](https://github.com/bk138/droidVNC-NG/issues/324) — `device_config put privacy media_projection_indicators_enabled false`
[11] [Prevent screenshot (Android SE)](https://android.stackexchange.com/questions/159814/preventing-android-os-from-screenshot-taking) — `wm screen-capture` command
[12] [scrcpy latency benchmark (GitHub issue)](https://github.com/Genymobile/scrcpy/issues/4746) — Measured 45-55ms USB latency
[13] [scrcpy latency guide (Medium)](https://medium.com/@zouyu1121/say-goodbye-to-lag-and-ads-the-ultimate-guide-to-scrcpy-the-best-open-source-android-mirroring-3e3d1840296a) — 35-70ms glass-to-glass typical
[14] [scrcpy keyboard modes](https://raw.githubusercontent.com/Genymobile/scrcpy/master/doc/keyboard.md) — SDK, UHID, and AOA keyboard modes
[15] [scrcpy UHID keyboard (XDA)](https://www.xda-developers.com/scrcpy-update-keyboard-mouse-passthrough/) — HID keyboard/mouse passthrough
[16] [InputManager.java wrapper](https://raw.githubusercontent.com/Genymobile/scrcpy/master/server/src/main/java/com/genymobile/scrcpy/wrappers/InputManager.java) — Reflection wrapper for InputManager.injectInputEvent()
[17] [Redroid multi-touch /dev/input](https://github.com/remote-android/redroid-doc/issues/628) — Multi-touch via /dev/input
[18] [Android touch input pipeline (DIFAI)](https://difai-project.org/android_touch_sensing.html) — Touch event flow through Android stack
[19] [UInput kernel module docs](https://kernel.org/doc/html/v4.12/input/uinput.html) — Linux uinput documentation
[20] [Android virtual input with uinput (Rust)](https://brunodmt.github.io/rust/2018/11/03/android-virtual-input-with-rust.html) — Creating virtual touch device on Android
[21] [scrcpy control.md](https://raw.githubusercontent.com/Genymobile/scrcpy/master/doc/control.md) — Input control protocol documentation
[22] [Multiple MediaCodec instances (StackOverflow)](https://stackoverflow.com/questions/24602662/multiple-instances-of-mediacodec-used-as-video-encoder-in-android) — Simultaneous encoding sessions
[23] [MediaCodec simultaneous decoding (GitHub)](https://github.com/androidx/media/issues/1825) — Multiple encoder/decoder support
[24] [Screen mirroring bandwidth needs (AirBeam)](https://www.airbeam.tv/what-network-bandwidth-does-android-screen-mirroring-actually-need/) — 5-25 Mbps typical range
[25] [Screen Content Coding in AV1 (Visionular)](https://visionular.ai/av1-screen-content-coding/) — Screen content optimization for video codecs
[26] [Android Input Architecture (newandroidbook)](https://newandroidbook.com/Book/Input.html) — Comprehensive Android input subsystem documentation
[27] [Hardware Composer HAL (AOSP)](https://source.android.com/docs/core/graphics/hwc) — HWC architecture
[28] [MediaProjection API (Android)](https://developer.android.com/media/grow/media-projection) — Official MediaProjection documentation
[29] [Privacy indicators (AOSP)](https://source.android.com/docs/core/permissions/privacy-indicators) — Screen recording indicator system
[30] [droidVNC-NG](https://github.com/bk138/droidVNC-NG) — Alternative VNC-based screen mirroring
