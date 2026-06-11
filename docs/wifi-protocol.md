# AllRelay Wi-Fi Protocol

> Protocol version: 1  
> Date: 2026-06-11  
> Scope: Phase 2 (multi-stream over raw TCP with 16-byte header)

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

### Phase 3: Codec Header

```
   │  4 bytes: codec ID (big-endian) │  writeVideoHeader()
   │ <────────────────────────────── │
   │                                 │
```

The server writes a 4-byte codec identifier to each stream socket, **before**
any data packets. This tells the client which decoder to use.

| Codec    | ID (hex)    | Bytes (ASCII) |
|----------|-------------|----------------|
| H.264    | 0x68323634  | "h264"        |
| H.265    | 0x68323635  | "h265"        |
| AV1      | 0x00617631  | "\0av1"       |
| Opus     | 0x6F707573  | "opus"        |
| FLAC     | 0x664C6143  | "fLaC"        |
| RAW      | 0x00726177  | "\0raw"       |

### Phase 4: Session Metadata (video streams: screen + camera)

```
   │  16 bytes: session header       │  writeSessionMeta()
   │ <────────────────────────────── │
   │                                 │
```

Layout (big-endian):

| Offset | Size | Field                       |
|--------|------|-----------------------------|
| 0      | 4    | `stream_id`                 |
| 4      | 8    | `pts + flags`               |
| 12     | 4    | `packet_size` (= 0 for meta)|

For session packets:
- `stream_id`: enum value (SCREEN=0x00000001, CAMERA=0x00000002, MIC=0x00000003, SPEAKER=0x00000004)
- `flags`: `PACKET_FLAG_SESSION` (bit 63) set, bit 0 = client_resize flag
- The width and height follow after this 16-byte header in the session payload

### Phase 5: Data Packets

```
   │  per-frame:                     │
   │  16-byte header + payload       │  writePacket()
   │ <────────────────────────────── │
   │                                 │
```

Each frame is wrapped in a 16-byte header followed by the encoded data:

| Offset | Size | Field                       |
|--------|------|-----------------------------|
| 0      | 4    | `stream_id`                 |
| 4      | 8    | `pts + flags`               |
| 12     | 4    | `packet_size`               |

Flags:
- Bit 63 (`PACKET_FLAG_SESSION`): session metadata (only for session packets)
- Bit 62 (`PACKET_FLAG_CONFIG`): codec configuration packet
- Bit 61 (`PACKET_FLAG_KEY_FRAME`): key frame

---

## Complete Wire Format

```
Byte offset  Content
───────────  ──────────────────────────────────────
0            Dummy byte (0xAB)
1-64         Device name (UTF-8, null-padded to 64)
65-68        Codec ID (4 bytes, big-endian)
69-84        Session header (16 bytes, video only)
85+          Data packets (16-byte header + payload)
```

### Example (H.264 screen mirroring, port 5000)

```
Offset  Hex                                          ASCII
──────  ───────────────────────────────────────────  ─────
00      AB                                           ·       [dummy byte]
01      53 4D 2D 46 37 31 31 42 00 00 00 00 00 ...  SM-F711B [device name, 64B]
41      68 32 36 34                                  h264    [codec: H.264]
45      00 00 00 01 80 00 00 00 WW WW HH HH         ····.... [session: stream=1, W×H, flags=0x80...]
55      SS SS SS SS PP PP PP PP PP PP PP PP LL LL    ········ [packet: stream+PTS+len, 16B header]
65      <H.264 encoded frame data>                           [payload]
```

### Example (camera, port 5001)

```
Offset  Hex                                          ASCII
──────  ───────────────────────────────────────────  ─────
00      AB                                           ·       [dummy byte]
41      68 32 36 34                                  h264    [codec: H.264]
45      00 00 00 02 80 00 00 00 WW WW HH HH         ··...... [session: stream=CAMERA(2)]
55      SS SS SS SS PP PP PP PP PP PP PP PP LL LL    ········ [packet: stream+PTS+len, 16B header]
65      <H.264 camera frame data>                            [payload]
```

---

## Client-Side Processing

1. **`connect_and_read_byte()`** — TCP connect + read 1 dummy byte per socket
2. **`device_read_info()`** — read 64 bytes from first socket → device name
3. **`sc_demuxer_recv_codec_id()`** — read 4 bytes → codec selector
4. **`sc_demuxer_recv_header()`** — read 16 bytes → check if session packet
5. **`sc_demuxer_parse_packet()`** — read payload → decode frame

---

## Differences from ADB Tunnel Mode

| Aspect             | ADB Tunnel                    | Wi-Fi Direct               |
|--------------------|-------------------------------|----------------------------|
| Transport          | LocalSocket (Unix domain)     | TCP socket                 |
| Ports              | Single socket (multiplexed)   | Per-stream ports (5000+)   |
| Connection health  | Dummy byte on first socket    | Dummy byte on every socket |
| Server lifecycle   | Started by client via ADB     | Must be pre-started        |
| Device discovery   | ADB device list               | mDNS (_allrelay._tcp)      |
| Control channel    | Bidirectional LocalSocket     | TCP port 5004 (Phase 3)    |
| Header format      | Original scrcpy (12-byte)     | AllRelay 16-byte header    |
| Camera stream      | Not supported                 | TCP port 5001              |
| Speaker stream     | Not supported                 | TCP port 5003 (reverse)    |

---

## See Also

- [SPEC.md](../SPEC.md) — Full technical specification
- [WifiConnection.java](../scrcpy/server/src/main/java/com/genymobile/scrcpy/device/WifiConnection.java) — Server-side implementation
- [server.c](../scrcpy/app/src/server.c) (sc_server_connect_wifi) — Client-side implementation
- [demuxer.c](../scrcpy/app/src/demuxer.c) — Packet demuxer
