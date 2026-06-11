# AllRelay - Current Work

> **Last updated:** 2026-06-11 23:30
> **Current phase:** Phase 4 — Root & Polish ✅ (ALL TASKS DONE) + Speaker path fix 🔄
> **Next milestone:** Tag v0.4.0-alpha, flash Magisk module qua Manager

---

## Status Legend

| Icon | Meaning |
|------|---------|
| ⬜ | Not started |
| 🔄 | In progress |
| ✅ | Done |
| ⏸️ | Blocked |
| ❌ | Cancelled |

---

## Quick Links

| Document | Path |
|----------|------|
| Specs | `SPEC.md` |
| Research | `docs/` |
| Architecture | `docs/architecture-research.md` |
| Open Source Reuse | `docs/open-source-research.md` |

---

## Overall Progress

```
Phase 1: Core Fork           [██████████] 100%  Target: Week 1-2
Phase 1.5: Cleanup           [██████████] 100%  Target: Week 2 (half day)
Phase 2: Multi-Stream        [██████████████████] 100%  Target: Week 3-4
Phase 2.5: Cleanup           [██████████████████] 100%  Target: Week 4 (half day)
Phase 3: Polish              [██████████████████] 100%  Target: Week 5
Phase 4: Root & Polish       [██████████] 100%  Target: Week 6
```

---

## Phase 1: Core Fork (Week 1-2)

> **Goal**: Fork scrcpy, replace ADB transport with Wi-Fi, add stream IDs
> **Deliverable**: Screen mirroring works over Wi-Fi with toggle UI

### Tasks

| # | Task | Status | Assignee | Notes |
|---|------|--------|----------|-------|
| 1.1 | Clone scrcpy, set up build environment | ✅ | | scrcpy 4.0 cloned, SDL3 3.2.8 built, client builds OK |
| 1.2 | Add `stream_id` to packet header (16-byte format) | ✅ | | StreamId.java enum, 16-byte header, 12/12 tests pass |
| 1.3 | Replace ADB socket with TCP/UDP direct transport | ✅ | | WifiConnection.java + --wifi CLI option, 12/12 tests |
| 1.4 | Basic mDNS discovery | ✅ | | MdnsService.java + mdns-discover Go tool |
| 1.5 | Test: screen mirroring over Wi-Fi | ✅ | | End-to-end works! Fixed deadlock + duplicate header bugs. 1080x2640@30fps |
| 1.6 | Kotlin toggle UI skeleton | ✅ | | ToggleActivity.kt + AllRelayService.kt |
| 1.7 | Build system setup (Android + Ubuntu) | ✅ | | scripts/build.sh + test-wifi-transport.sh |

### Dependencies

```
1.1 → 1.2 → 1.3 → 1.5
                → 1.4
1.6 (parallel)
1.7 (parallel)
```

---

## Phase 1.5: Post-Phase 1 Cleanup (Week 2, half day)

> **Goal**: Clean up technical debt, fix rough edges, commit properly before moving on
> **Deliverable**: Clean codebase, proper git history, updated docs

### Tasks

| # | Task | Status | Assignee | Notes |
|---|------|--------|----------|-------|
| 1.5.1 | Fix `--force-adb-forward` auto-enable in Wi-Fi mode | ✅ | | cli.c: added `&& !opts->wifi_mode` guard |
| 1.5.2 | Clean up Server.java Wi-Fi path | ✅ | | Removed mDNS stub, demoted warnings, cleaned comments, removed `e.printStackTrace()` |
| 1.5.3 | Review & reduce log verbosity | ✅ | | Demoted port-listen logs to `Ln.d()`, removed "running" message |
| 1.5.4 | Update test-wifi-transport.sh | ✅ | | Fixed server JAR path, added `test_with_server` to main flow, updated help text |
| 1.5.5 | Add e2e Wi-Fi test script | ✅ | | New `scripts/test-e2e-wifi.sh` - deploy → start → connect → verify frames → cleanup |
| 1.5.6 | Clean up unused imports & dead code | ✅ | | Removed unused imports from WifiConnection, WifiStreamer, Server; removed duplicate IOException |
| 1.5.7 | Document Wi-Fi protocol format | ✅ | | `docs/wifi-protocol.md` - full wire format, byte layout, client/server flow |
| 1.5.8 | Git: commit Phase 1 + tag | ✅ | | 5 clean commits, tagged `v0.1.0-alpha` |

### Dependencies

```
1.5.1 (standalone)
1.5.2 (standalone)
1.5.3 (standalone)
1.5.4 → 1.5.5
1.5.6 (standalone)
1.5.7 (standalone)
1.5.8 depends on all above
```

### Resolved Issues

| # | Issue | Resolution |
|---|-------|------------|
| I-1 | `--force-adb-forward` in Wi-Fi mode | Fixed: added `&& !opts->wifi_mode` guard in cli.c |
| I-2 | Device name sent before stream meta | Non-issue: original DesktopConnection uses same order (device meta → video header → session meta) |
| I-3 | mDNS skipped in server mode | Removed stub code; mDNS delegated to AllRelayService Kotlin wrapper |
| I-4 | Control/Audio stub warnings | Changed `Ln.w()` to `Ln.i()` with deferred-to-phase info |
| I-5 | test-wifi-transport.sh outdated | Fixed: uses scrcpy-server-allrelay, calls test_with_server(), links to e2e script |
| I-6 | build.sh sdkmanager path | Non-issue: `sdkmanager` is the correct binary name in Android SDK cmdline-tools |

### Known Issues (for Phase 2)

| # | Issue | File | Description |
|---|-------|------|-------------|
| - | No remaining known issues from Phase 1 | - | All 6 issues resolved or confirmed non-issues |

---

## Phase 2: Multi-Stream (Week 3-4)

> **Goal**: Add camera and audio streams alongside screen
> **Deliverable**: All 4 streams work independently

### Tasks

| # | Task | Status | Assignee | Notes |
|---|------|--------|----------|-------|
| 2.1 | `MultiCapture.java` - screen + camera simultaneous | ✅ | | New class orchestrating both |
| 2.2 | Camera stream on port 5001 | ✅ | | Modified `Server.java`, `WifiConnection.java`, `server.c`, `server.h`, `cli.c`, `AllRelayService.kt` |
| 2.3 | `AudioReversePlayback.java` - PC→phone speaker | ✅ | | New: receive Opus → AudioTrack playback |
| 2.4 | Mic stream on port 5002 | ✅ | | Created `WifiAudioEncoder.java`, wired in `Server.java` |
| 2.5 | Speaker stream on port 5003 | ✅ | | Speaker socket in `WifiConnection.java`, reverse audio in `Server.java` |
| 2.6 | Client-side stream routing | ✅ | | Go server: protocol parser, demuxer, transport, main |
| 2.7 | PipeWire config generation | ✅ | | PipeWire mic/speaker configs + setup script + systemd service |

### Dependencies

```
2.1 → 2.2
2.3 → 2.5
2.4 (parallel)
2.6 depends on 2.2, 2.4, 2.5
2.7 (parallel with 2.6)
```

---

## Phase 2.5: Post-Phase 2 Cleanup (Week 4, half day)

> **Goal**: Clean up technical debt, fix documentation, improve code quality before Phase 3
> **Deliverable**: Clean codebase, updated docs, proper git history

### Tasks

| # | Task | Status | Priority | Notes |
|---|------|--------|----------|-------|
| 2.5.1 | C client: mark camera/speaker sockets as Phase 3 deferred | ✅ | HIGH | Added TODO(Phase 3) in scrcpy.c, fixed info_socket fallback in server.c |
| 2.5.2 | Update SPEC.md: TCP not UDP/RTP | ✅ | HIGH | Updated transport sections, data flow, port table, GStreamer, heartbeat, latency notes |
| 2.5.3 | Update wifi-protocol.md: add multi-stream info | ✅ | HIGH | Scope → Phase 2, stream IDs, camera example, differences table |
| 2.5.4 | Fix server.h + cli.c port comments | ✅ | MODERATE | Added camera (port+1) and speaker (port+3) to comments |
| 2.5.5 | Go: fix go.mod version (1.24.1 → 1.22) | ✅ | MODERATE | Fixed to go 1.22 |
| 2.5.6 | Go: migrate fmt.Printf → log/slog | ✅ | MODERATE | Structured logging with levels in main.go + connection.go |
| 2.5.7 | Go: fix demuxer error swallowing | ✅ | MODERATE | Added errCh to MultiDemuxer, Errors() channel, select in main |
| 2.5.8 | Go: fix mock-android-server NUL bytes | ✅ | MINOR | Replaced `string(make([]byte,N))` with `strings.Repeat(" ", N)` |
| 2.5.9 | Go: mark WriteSpeakerPacket as Phase 3 stub | ✅ | MINOR | Added TODO(Phase 3) comment |
| 2.5.10 | Go: fix Connection.Close() error handling | ✅ | MINOR | Use errors.Join to combine all errors |
| 2.5.11 | Java: Server.java try-with-resources | ✅ | MINOR | Added finally block to guarantee wifiConn.close() |
| 2.5.12 | Add TODO/FIXME markers on deferred work | ✅ | MINOR | Added TODO(Phase 3) in Kotlin, C client, Go stubs |
| 2.5.13 | Git: commit Phase 2 + tag | ✅ | | Commit 9073327, tag v0.2.0-alpha |

### Dependencies

```
2.5.1-2.5.12 (all independent, can be done in any order)
2.5.13 depends on all above
```

### Deferred to Phase 3

| Item | Reason |
|------|--------|
| Kotlin AllRelayService.kt broken launch | Phase 3/Magisk daemon will replace this |
| C client camera/speaker demuxers | Phase 3 will add decoders |
| Go speaker path implementation | Phase 3 will add v4l2sink/audio output |
| Unit tests for new Java classes | Can add alongside Phase 3 features |
| CLI tests for --wifi, --multistream | Can add alongside Phase 3 features |

---

## Phase 3: Polish (Week 5)

> **Goal**: v4l2loopback, input injection, reconnection
> **Deliverable**: Full feature set working end-to-end

### Tasks

| # | Task | Status | Assignee | Notes |
|---|------|--------|----------|-------|
| 3.1 | Camera → v4l2loopback output | ✅ | | GStreamer pipeline → v4l2sink |
| 3.2 | Monitor → SDL2 window with input | ✅ | | GStreamer display + X11 input capture |
| 3.3 | Input injection (touch, keyboard, clipboard) | ✅ | | TCP control JSON + keymap + forwardInputEvents |
| 3.4 | Reconnection logic | ✅ | | `reconnect/manager.go` - exponential backoff |
| 3.5 | Per-stream toggle (enable/disable independently) | ✅ | | `control/protocol.go` - ToggleStream(), JSON messages |
| 3.6 | Heartbeat & status display | ✅ | | `heartbeat/monitor.go` - UDP :5005, status display |

### Dependencies

```
3.1 (standalone)
3.2 → 3.3
3.4 (standalone)
3.5 depends on Phase 2
3.6 (standalone)
```

---

## Phase 3 Bug Fixes (from manual test on SM-F711B / Android 15)

| # | Bug | Root Cause | Fix | Files |
|---|-----|-----------|-----|-------|
| B1 | GStreamer "syntax error" | `gst-launch-1.0` needs tokens as separate args | `strings.Fields()` split | `video/pipeline.go` |
| B2 | "payload too large" / corrupted stream ID | Raw bytes written without 16-byte AllRelay header | Wrap in `writePacket()`; Go skips 64-byte device name | `WifiStreamer.java`, `connection.go` |
| B3 | Session packet = large payload | Session `height` at bytes 12-16 ≠ `PayloadSize` | `PayloadSize=0` for session packets | `protocol/packet.go` |
| B4 | Connection deadlock | Sequential accept blocked control; device name sent too late | Parallel accept threads + `sendDeviceMetaAsync()` | `WifiConnection.java` |
| B5 | Control "connection refused" | 3s optional timeout closed socket before Go connected | video/control → 30s mandatory; camera/audio/speaker → 3s optional | `WifiConnection.java` |
| B6 | Android ClassNotFoundException | `app_process` caches stale DEX from same file path | Unique filename per deploy | - |

## Phase 4 Bug Fixes (from camera stream testing)

| # | Bug | Root Cause | Fix | Files |
|---|-----|-----------|-----|-------|
| B7 | Camera accept timeout 3s quá ngắn | Go client connect bị refused khi server còn block trên video accept | Tăng ACCEPT_TIMEOUT_OPTIONAL_MS 3s→10s | `WifiConnection.java` |
| B8 | Go client treo vĩnh viễn khi accept fail | ServerSocket không close khi accept timeout, Go client đọc dummy byte block forever | Close ServerSocket trong catch block của accept thread | `WifiConnection.java` |
| B9 | Video accept block 30s block camera | Sequential join: main thread chờ video accept 30s trước khi camera encoding bắt đầu | Shared deadline 12s cho ALL threads thay vì sequential per-thread timeout | `WifiConnection.java` |
| B10 | Device name không detect trên camera port | Go server chỉ check device name trên port "video", nhưng camera port cũng nhận do `getFirstSocket()` | Peek first byte heuristic (>0x20 = ASCII = device name) trên ALL ports | `connection.go` |
| B11 | Codec ID header gây pipeline crash | 4-byte "h264" codec ID bị parse thành GStreamer config data sai | Skip config packets với payload ≤ 4 bytes | `main.go` |
| B12 | Session width/height decode sai | `writeSessionMeta()` viết width/height vào header bytes 8-15, nhưng Go parser đọc 8-byte flags + 4-byte payload | Detect session layout (bit 31 of bytes 4-7) → parse width/height từ header thay vì payload | `packet.go` |
| B13 | GStreamer v4l2sink not-negotiated | avdec_h264 output colorimetry `1:4:16:3` không match v4l2loopback capabilities | Thay gst-launch → ffmpeg pipeline: `ffmpeg -f h264 -i pipe:0 -pix_fmt yuyv422 -f v4l2 /dev/video10` | `pipeline.go` |
| B14 | JAR cache của app_process | Android DEX cache lưu theo file path → JAR cũ được dùng lại dù file mới | Unique filename với timestamp mỗi lần deploy | Workaround |

---

## Phase 4: Root & Polish (Week 6)

> **Goal**: Magisk module, root features, installation
> **Deliverable**: Installable, documented, tested product

### Tasks

| # | Task | Status | Assignee | Notes |
|---|------|--------|----------|-------|
| 4.1 | Magisk module packaging | ✅ | | `module.prop`, `service.sh`, `customize.sh` |
| 4.2 | AAudio MMAP force-enable (root) | ✅ | | `post-fs-data.sh` |
| 4.3 | Hide recording indicators (root) | ✅ | | `device_config` commands in `service.sh` |
| 4.4 | SELinux policy | ✅ | | `sepolicy/allrelay.te` |
| 4.5 | Installation script (Ubuntu) | ✅ | | `scripts/install.sh` |
| 4.6 | README & documentation | ✅ | | `README.md` |
| 4.7 | Testing on real devices | ✅ | | ADB-tested on SM-F711B (Android 15, Magisk 30.2): post-fs-data, indicators, daemon start/stop, all 5 ports, Go server connection, GStreamer pipeline |
| 4.8 | Adaptive bitrate implementation | ✅ | | `internal/bitrate/` Go module, 13/13 tests pass |

### Dependencies

```
4.1 → 4.2 → 4.3 → 4.4
4.5 (parallel)
4.6 (parallel)
4.7 depends on all above
4.8 (standalone, can defer)
```

---

## Decision Log

| Date | Decision | Rationale |
|------|----------|-----------|
| 2026-06-09 | Fork scrcpy as base | 80%+ code reuse, battle-tested, Apache-2.0 |
| 2026-06-09 | RTP/UDP transport (not TCP) | Lower latency, no head-of-line blocking |
| 2026-06-09 | H.264 for video (not H.265) | Universal HW encoder support, lowest latency |
| 2026-06-09 | Opus 10ms frames for audio | 12.5ms algorithmic delay, best balance |
| 2026-06-09 | PipeWire for Linux audio | Built-in RTP modules, modern, low latency |
| 2026-06-09 | Go for Ubuntu server | Performance, single binary, go-gst bindings |
| 2026-06-09 | Magisk module for root daemon | Persistent, silent, auto-start on boot |

---

## Open Questions

| # | Question | Status | Resolution |
|---|----------|--------|------------|
| Q1 | Should we use GStreamer or custom C for video decode on Ubuntu? | ⬜ | Pending: benchmark both |
| Q2 | How to handle Android 14 foreground service restrictions? | ⬜ | Pending: root daemon may bypass |
| Q3 | Support multiple phones simultaneously in v1? | ⬜ | Deferred to v2 |
| Q4 | SRTP encryption - v1 or v2? | ⬜ | Deferred to v2 |
| Q5 | Should monitor window support remote desktop (mouse/keyboard) if not mirroring? | ⬜ | Pending: scope decision |

---

## Blockers

| # | Blocker | Impact | Mitigation | Status |
|---|---------|--------|------------|--------|
| B-SPK | PipeWire audio graph suspend | Speaker live capture không hoạt động (graph ở QUANT=0, RATE=0). Dùng IEC958 digital output không có receiver vật lý → ALSA/PipeWire không active graph. pipewire-pulse auto-connect cũng broken. | ĐÃ CÓ WORKAROUND: restart pipewire (`systemctl --user restart pipewire pipewire-pulse`) → graph active trở lại. Default sink đã set là `allrelay-speaker-sink` (null-sink), @DEFAULT_MONITOR@ capture hoạt động. Đã kiểm tra: pulsesrc device=@DEFAULT_MONITOR@ hoạt động sau restart. | 🟡 (có workaround) |

---

## Notes

- **Primary test device**: Rooted Android 14+ with Magisk
- **Primary test PC**: Ubuntu 24.04 LTS
- **Wi-Fi requirement**: 5GHz (802.11ac/ax) for best results
- **scrcpy upstream**: Track upstream changes, merge periodically
- **Post-phase cleanup**: After each phase's end-to-end test, run a cleanup pass (Phase 1.5, 2.5, etc.) before moving on. This keeps the codebase clean and git history readable.

---

## Changelog

| Date | Change |
|------|--------|
| 2026-06-11 | Speaker path: IMPLEMENTED ✅. Added audio/ogg.go (OggDemuxer + WritePacket), updated pipeline stereo+low-latency, wired runSpeakerCapture() in main.go. WriteSpeakerPacket() stub → full implementation. GStreamer pulsesrc→opusenc→oggmux→fdsink → Go reads Ogg pages, extracts Opus, sends 16-byte header+payload to phone TCP port 5003. |
| 2026-06-11 | PipeWire speaker capture blocked: IEC958 digital output has no physical receiver → graph stays suspended. Attempted: pipewiresrc, pulsesrc, pw-record, pw-cat, FIFO — all fail. Need native PipeWire client (cgo). See Blockers. |
| 2026-06-11 | Mic pipeline: replaced pulsesink→null-sink with pipewiresink mode=provide → Audio/Source/Virtual \"allrelay-mic\". Fixed Ogg CRC32 (non-reflected polynomial), OpusTags page, batch 25 packets/page. |
| 2026-06-11 | Camera pipewire: BrowserCameraPipeline via v4l2src from /dev/video10 → NV12 → pipewiresink mode=provide \"allrelay-camera-pw\". Requires v4l2loopback exclusive_caps=1. |
| 2026-06-11 | Speaker live capture works (pulsesrc @DEFAULT_MONITOR@) ONLY after pipewire restart. CBR opusenc frame-size=20 needed for Android MediaCodec. |
| 2026-06-11 | AudioReversePlayback: fixed CHANNEL_IN_STEREO→CHANNEL_OUT_STEREO, config packet handling, first-payload alignment. |
| 2026-06-11 | Phase 4 COMPLETE ✅ - All 8/8 tasks done, ADB-tested on SM-F711B. Fixed wifi_mode=true bug in daemon/service.sh |
| 2026-06-11 | Mic handler rewrite: FIFO + filesrc + pulsesink approach (reliable). Buffer 25 packets before pipeline start. Ogg CRC32 fixed (non-reflected polynomial). PulseAudio null-sink + remap-source for Edge detection. |
| 2026-06-11 | Camera: ffmpeg YUYV 1920x1080 via v4l2loopback with exclusive_caps=1. Edge detects as "AllRelay Cam". |
| 2026-06-11 | Edge audio fix: PulseAudio module-remap-source creates proper Audio/Source for Edge. Default source/sink set. |
| 2026-06-11 | Speaker still blocked: PipeWire IEC958 graph suspend. PulseAudio null-sink created for Edge output. |
| 2026-06-11 | Phase 4 STARTED 🔄 - Magisk module, root features, install script, docs, adaptive bitrate |
| 2026-06-11 | Phase 3.5 CLEANUP ✅ - removed debug logs, updated docs, fixed TODOs, tag v0.3.0-alpha |
| 2026-06-11 | Phase 3 COMPLETE ✅ All 6/6 tasks. MANUAL TESTED on SM-F711B (Android 15). Fixed 6 critical bugs (see Bug Fixes section) |
| 2026-06-11 | Phase 3 STARTED 🔄 |
| 2026-06-11 | Tasks 3.2-3.6: Monitor display, X11 input capture, control protocol, reconnection, heartbeat, per-stream toggle |
| 2026-06-11 | Task 3.1 ✅ Camera → v4l2loopback: GStreamer pipeline manager, v4l2 helper, AnnexB converter, camera pipeline wired in main.go |
| 2026-06-11 | Phase 2.5 COMPLETE ✅ All 13/13 tasks done. Git commit 9073327, tag v0.2.0-alpha |
| 2026-06-11 | Phase 2.5 cleanup: 12/13 tasks done - code review, docs updated, logging migrated, error handling fixed |
| 2026-06-11 | Phase 2.5 cleanup plan created: 13 tasks, 25 items identified from code review |
| 2026-06-09 | Initial creation from SPEC.md |
| 2026-06-09 | Task 1.1 done: scrcpy cloned, SDL3 built, client compiles |
| 2026-06-09 | Task 1.2 done: stream_id in packet header (16-byte format) |
| 2026-06-09 | Task 1.3 done: Wi-Fi direct transport (WifiConnection + --wifi CLI) |
| 2026-06-09 | Task 1.4 done: mDNS discovery (MdnsService + mdns-discover tool) |
| 2026-06-09 | Task 1.5 partial: Wi-Fi server listening, all ports connected |
| 2026-06-09 | Task 1.6 done: Kotlin toggle UI (ToggleActivity + AllRelayService) |
| 2026-06-09 | Task 1.7 done: Build scripts (build.sh, test-wifi-transport.sh) |
| 2026-06-10 | Task 1.5 progress: Wi-Fi server works, APK build issue fixed |
| 2026-06-11 | Task 1.5 DONE: End-to-end Wi-Fi screen mirroring confirmed (SM-F711B, 1080x2640). Fixed 2 bugs: deadlock (missing dummy byte) and duplicate video header |
| 2026-06-11 | Added Phase 1.5 cleanup plan: 8 tasks, 6 known issues to fix before Phase 2 |
| 2026-06-11 | Phase 2 started: Tasks 2.1 (MultiCapture.java) + 2.2 (camera port) done. Created MultiCapture orchestrator, extended WifiConnection + C client for camera port 5001, added --multistream CLI option, updated AllRelayService.kt |
| 2026-06-11 | Tasks 2.3 (AudioReversePlayback), 2.4 (Mic stream), 2.5 (Speaker stream) done. Created WifiAudioEncoder, AudioReversePlayback, extended WifiConnection with speaker socket port 5003, wired audio in Server.java Wi-Fi path, updated C client for speaker port |
| 2026-06-11 | Tasks 2.6 (Go server) + 2.7 (PipeWire) done. Created Go allrelay-server with protocol parser, multi-demuxer, TCP transport; PipeWire configs for mic/speaker; systemd service; setup-pipewire.sh script. Phase 2 COMPLETE ✅ |
| 2026-06-11 | Phase 2 FULLY TESTED: ✅ Go 20/20 tests pass | ✅ C client compiles + 12/12 tests pass | ✅ Java server compiles + tests pass + APK built | ✅ Go server binary builds | All 3 layers verified! |
