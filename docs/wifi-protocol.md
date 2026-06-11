# AllRelay Wi-Fi Protocol

> Protocol version: 1  
> Date: 2026-06-11  
> Scope: Phase 3 (multi-stream over raw TCP with 16-byte header, GStreamer pipelines, input capture)

---

## Overview

AllRelay uses raw TCP sockets for Wi-Fi transport, replacing scrcpy's ADB tunnel.
Each stream uses a separate TCP port, allowing independent connections for video,
audio, and control.

### Port Allocation

| Stream  | Port Offset | Port (default) | Stream ID | Direction     |
|---------|------------|----------------|-----------|---------------|
| Video   | +0         | 5000           | 0x00000001 (SCREEN) | Android → PC  |
| Camera  | +1         | 5001           | 0x00000002 (CAMERA) | Android → PC  |
| Mic     | +2         | 5002           | 0x00000003 (MIC)    | Android → PC  |
| Speaker | +3         | 5003           | 0x00000004 (SPEAKER)| PC → Android  |
| Control | +4         | 5004           | —          | Bidirectional |

---

## Connection Sequence

### Phase 1: Handshake (per socket)

```
┌──────┐                         ┌────────┐
│ PC   │                         │ Phone  │
│client│                         │ server │
└──┬───┘                         └───┬────┘
   │  TCP connect                    │
   │ ──────────────────────────────> │
   │                                 │ accept()
   │                                 │ write dummy byte (0xAB, 1 byte)
   │ <────────────────────────────── │
   │  read 1 byte (connection check) │
   │                                 │
```

Each socket does a TCP connect + 1-byte health check. The server sends `0xAB`
immediately after accepting the connection. If the dummy byte arrives, the
connection is alive. If not, the client retries (up to 100 attempts, 200ms apart).

### Phase 2: Device Metadata

```
   │                                 │ sendDeviceMeta()
   │  64 bytes: device name (UTF-8)  │
   │ <────────────────────────────── │
   │                                 │
```

The server writes exactly 64 bytes to the **first** connected socket (typically
video). The bytes are the UTF-8 device name (e.g. "SM-F711B"), null-padded to 64.

The client reads these 64 bytes via `device_read_info()` before starting any
stream processing.

### Phase 3: AllRelay Packets (16-byte header)

After the device name, ALL data is wrapped in the AllRelay 16-byte packet format.
This includes codec configuration, session metadata, and media frames.

**Codec configuration** (`writeVideoHeader` / `writeAudioHeader`):

```
   │  16-byte header + 4-byte codec ID  │
   │ <───────────────────────────────── │
```

The codec ID (e.g., `h264`, `opus`) is sent as a config packet:
- `stream_id`: the stream identifier
- `flags`: `PACKET_FLAG_CONFIG` (bit 62)
- `packet_size`: 4 (the codec ID length)
- Payload: 4-byte big-endian codec ID

**Session metadata** (video resolution/rotation changes):

Layout within the 16-byte header:

| Offset | Size | Field                       |
|--------|------|-----------------------------|
| 0      | 4    | `stream_id`                 |
| 4      | 4    | `flags` (session bit 63 in MSB) |
| 8      | 4    | `width`                     |
| 12     | 4    | `height`                    |

> **Note:** Session packets have a different header layout than media packets.
> Bytes 8-11 contain `width` and bytes 12-15 contain `height` — NOT `pts_and_flags`
> and `payload_size`. The payload size for session packets is always 0.

**Media frames**:

| Offset | Size | Field                       |
|--------|------|-----------------------------|
| 0      | 4    | `stream_id` (big-endian u32) |
| 4      | 8    | `pts + flags` (big-endian u64) |
| 12     | 4    | `packet_size` (big-endian u32) |

Flags (in `pts_and_flags`):
- Bit 63 (`PACKET_FLAG_SESSION`): session metadata
- Bit 62 (`PACKET_FLAG_CONFIG`): codec configuration
- Bit 61 (`PACKET_FLAG_KEY_FRAME`): key frame

---

## Complete Wire Format (per socket)

### Video port (5000) — Screen stream

```
Byte offset  Content
───────────  ──────────────────────────────────────
0            Dummy byte (0xAB)
1-64         Device name (UTF-8, null-padded to 64)
65+          AllRelay 16-byte header packets (config → session → frames)
```

> **Note:** Device name is only sent on the video port. Camera, mic, speaker,
> and control ports skip directly to the 16-byte header packets after the
> dummy byte.

### Example — Screen stream (H.264) on port 5000

```
Byte offset  Hex                                             ASCII
───────────  ──────────────────────────────────────────────  ──────
00           AB                                              ·        [dummy byte]
01           53 4D 2D 46 37 31 31 42 00 00 00 00 00 00 ...  SM-F711B  [device name, 64B]
65           00 00 00 01 40 00 00 00 00 00 00 00 00 00 00 04  ··..@····  [config: stream=SCREEN, config flag, size=4]
81           68 32 36 34                                     h264      [codec ID: H.264]
85           00 00 00 01 80 00 00 00 WW WW HH HH             ··..····  [session: stream=SCREEN, session flag, W×H]
101          SS SS SS SS PP PP PP PP PP PP PP PP LL LL        ········  [frame: stream+PTS+len, 16B header]
117          <H.264 encoded frame data>                                [payload]
```

### Camera port (5001)

```
Byte offset  Hex                                             ASCII
───────────  ──────────────────────────────────────────────  ──────
00           AB                                              ·        [dummy byte]
01           00 00 00 02 40 00 00 00 00 00 00 00 00 00 00 04  ··..@····  [config: stream=CAMERA, config flag, size=4]
17           68 32 36 34                                     h264      [codec ID: H.264]
21           SS SS SS SS PP PP PP PP PP PP PP PP LL LL        ········  [frame: stream+PTS+len, 16B header]
37           <H.264 camera frame data>                                 [payload]
```

---

## Client-Side Processing (Go server)

1. **`connectPort()`** — TCP connect + read 1 dummy byte per socket
2. **Video port only**: read 64 bytes device name
3. **Create demuxer per stream** — reads 16-byte AllRelay headers
4. **Session packets**: detected by bit 63, `PayloadSize` set to 0
5. **Config packets**: bit 62, fed as Annex B to GStreamer pipeline
6. **Media frames**: decoded via GStreamer pipeline

### GStreamer Pipeline

```
fdsrc fd=0 ! h264parse ! avdec_h264 ! videoconvert ! autovideosink sync=false
```

H.264 data (config + frames) is piped to stdin of a `gst-launch-1.0`
subprocess. Config data is converted to Annex B byte-stream format before
feeding to the decoder.

## Android Server Connection Accept (Phase 3)

Connections are accepted in **parallel threads**, avoiding the sequential
blocking issue from earlier versions:

- **Video + Control**: mandatory, use `ACCEPT_TIMEOUT_MS` (30s)
- **Camera + Audio + Speaker**: optional, use `ACCEPT_TIMEOUT_OPTIONAL_MS` (3s)
- **Device name**: sent immediately from the video accept thread (not after all accepts)

This prevents a deadlock where the PC client waits for the device name while
the Android server waits for optional stream connections.

---

## Differences from ADB Tunnel Mode

| Aspect             | ADB Tunnel                    | Wi-Fi Direct               |
|--------------------|-------------------------------|----------------------------|
| Transport          | LocalSocket (Unix domain)     | TCP socket                 |
| Ports              | Single socket (multiplexed)   | Per-stream ports (5000+)   |
| Connection health  | Dummy byte on first socket    | Dummy byte on every socket |
| Server lifecycle   | Started by client via ADB     | Must be pre-started        |
| Device discovery   | ADB device list               | mDNS (_allrelay._tcp)      |
| Control channel    | Bidirectional LocalSocket     | TCP port 5004 |
| Header format      | Original scrcpy (12-byte)     | AllRelay 16-byte header    |
| Camera stream      | Not supported                 | TCP port 5001              |
| Speaker stream     | Not supported                 | TCP port 5003 (reverse)    |

---

## See Also

- [SPEC.md](../SPEC.md) — Full technical specification
- [WifiConnection.java](../scrcpy/server/src/main/java/com/genymobile/scrcpy/device/WifiConnection.java) — Server-side implementation
- [server.c](../scrcpy/app/src/server.c) (sc_server_connect_wifi) — Client-side implementation
- [demuxer.c](../scrcpy/app/src/demuxer.c) — Packet demuxer
