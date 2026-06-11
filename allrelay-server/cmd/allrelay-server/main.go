// AllRelay Server — Ubuntu-side multi-stream receiver and router.
//
// Connects to an Android phone running the AllRelay server over Wi-Fi,
// receives all streams (screen, camera, mic, speaker), and routes them
// to the appropriate outputs:
//
//	Screen  → SDL2 window display (Phase 3)
//	Camera  → v4l2loopback virtual device (Phase 3)
//	Mic     → PipeWire virtual source
//	Speaker → PipeWire virtual sink → Opus encode → send to phone
//
// Usage:
//
//	allrelay-server --host 192.168.1.100 [flags]
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/allrelay/allrelay-server/internal/audio"
	"github.com/allrelay/allrelay-server/internal/bitrate"
	"github.com/allrelay/allrelay-server/internal/control"
	"github.com/allrelay/allrelay-server/internal/heartbeat"
	"github.com/allrelay/allrelay-server/internal/input"
	"github.com/allrelay/allrelay-server/internal/protocol"
	"github.com/allrelay/allrelay-server/internal/transport"
	"github.com/allrelay/allrelay-server/internal/video"
)

func main() {
	host := flag.String("host", "", "Phone IP address (required)")
	port := flag.Int("port", 5000, "Base TCP port")
	noScreen := flag.Bool("no-screen", false, "Disable screen stream")
	noCamera := flag.Bool("no-camera", false, "Disable camera stream")
	noMic := flag.Bool("no-mic", false, "Disable microphone stream")
	noSpeaker := flag.Bool("no-speaker", false, "Disable speaker stream")
	noControl := flag.Bool("no-control", false, "Disable control channel (input injection)")
	noInput := flag.Bool("no-input", false, "Disable input capture (keyboard/mouse → phone)")
	noHeartbeat := flag.Bool("no-heartbeat", false, "Disable heartbeat/status monitoring")
	noReconnect := flag.Bool("no-reconnect", false, "Disable auto-reconnection")
	noAdaptive := flag.Bool("no-adaptive", false, "Disable adaptive bitrate control")
	verbose := flag.Bool("v", false, "Verbose debug output")
	flag.Parse()

	if *verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	} else {
		slog.SetLogLoggerLevel(slog.LevelInfo)
	}

	if *host == "" {
		fmt.Fprintln(os.Stderr, "Error: --host is required (phone IP address)")
		flag.Usage()
		os.Exit(1)
	}

	slog.Info("AllRelay Server", "version", "v0.1.0")
	slog.Info("Connecting...", "host", *host, "port", *port)

	// Connect to all enabled streams
	conn, err := transport.Connect(*host, uint16(*port),
		!*noScreen, !*noCamera, !*noMic, !*noSpeaker, !*noControl)
	if err != nil {
		slog.Error("Connection failed", "error", err)
		os.Exit(1)
	}
	defer conn.Close()

	slog.Info("Connected!")

	// Create multi-demuxer to manage all streams
	md := protocol.NewMultiDemuxer()

	// Screen stream handler (stream ID 0x01)
	if !*noScreen && conn.HasStream(protocol.StreamScreen) {
		slog.Info("Screen stream: connected")

		screenPipeline, err := video.MonitorPipeline()
		if err != nil {
			slog.Error("Failed to start monitor pipeline", "error", err)
		} else {
			md.AddStream(protocol.StreamScreen, conn.VideoStream(),
				makeScreenHandler(screenPipeline))
		}
	}

	// Camera stream handler (stream ID 0x02)
	if !*noCamera && conn.HasStream(protocol.StreamCamera) {
		cameraDev := video.GetCameraDevice()
		slog.Info("Camera stream: connected", "device", cameraDev)

		// Try to ensure v4l2loopback is loaded (non-fatal — user can fix later)
		if err := video.EnsureV4L2Device(cameraDev); err != nil {
			slog.Warn("v4l2loopback not ready", "error", err)
		}

		cameraPipeline, err := video.CameraPipeline(cameraDev)
		if err != nil {
			slog.Error("Failed to start camera pipeline", "error", err)
		} else {
			md.AddStream(protocol.StreamCamera, conn.CameraStream(),
				makeCameraHandler(cameraPipeline))
		}

		// Also expose camera via PipeWire for browsers (reads from v4l2)
		pwPipe, err := video.PipeWireCameraPipeline(cameraDev)
		if err != nil {
			slog.Warn("Browser camera pipeline failed", "error", err)
		} else {
			slog.Info("Browser camera: PipeWire source created")
			go func() {
				if err := <-pwPipe.Done(); err != nil {
					slog.Warn("Browser camera pipeline exited", "error", err)
				}
			}()
		}
	}

	// Mic stream handler (stream ID 0x03)
	if !*noMic && conn.HasStream(protocol.StreamMic) {
		slog.Info("Mic stream: connected")
		md.AddStream(protocol.StreamMic, conn.MicStream(),
			makeMicHandler())
	}

	// Speaker — PC → phone reverse audio (live system capture)
	if !*noSpeaker && conn.HasStream(protocol.StreamSpeaker) {
		slog.Info("Speaker stream: connected")

		go func() {
			sp, err := audio.SpeakerCapturePipeline()
			if err != nil {
				slog.Error("Speaker capture pipeline failed", "error", err)
				return
			}
			defer sp.Close()

			slog.Info("Speaker: live capture started")
			w := conn.SpeakerWriter()

			var pktCount int
			var pktNum int
			err = readOggPackets(sp, func(payload []byte, granulePos uint64) error {
				pktNum++
				if pktNum == 1 {
					// OpusHead → send as config (needed by decoder)
					slog.Debug("speaker: sending OpusHead config", "len", len(payload))
					return writeSpeakerConfig(w, payload)
				}
				if pktNum == 2 {
					// OpusTags → skip
					slog.Debug("speaker: skipping OpusTags", "len", len(payload))
					return nil
				}
				pktCount++
				if pktCount%100 == 0 {
					slog.Debug("speaker stream", "packets", pktCount)
				}
				return WriteSpeakerPacket(w, granulePos, payload)
			})
			if err != nil {
				slog.Error("Speaker error", "error", err)
			} else {
				slog.Info("Speaker finished", "packets", pktCount)
			}
		}()
	}

	// Set up control channel + input injection
	var ctrl *control.Channel
	var inputCapture interface {
		Events() <-chan input.Event
		Close() error
	}

	if !*noControl && conn.HasStream(protocol.StreamControl) {
		slog.Info("Control channel: connected")
		ctrl = control.NewChannel(conn.ControlConn())
		defer ctrl.Close()

		// Input capture: forward PC keyboard/mouse → Android
		if !*noInput {
			var err error
			inputCapture, err = input.NewBestCapture()
			if err != nil {
				slog.Warn("Input capture unavailable", "error", err)
			} else {
				slog.Info("Input capture: enabled (keyboard + mouse → phone)")
				go forwardInputEvents(inputCapture.Events(), ctrl)
				defer inputCapture.Close()
			}
		}
	}

	slog.Info("Streaming... Press Ctrl+C to stop.")

	// Start heartbeat monitor (UDP port 5005)
	var hm *heartbeat.Monitor
	if !*noHeartbeat {
		var err error
		hm, err = heartbeat.NewMonitor(heartbeat.DefaultPort)
		if err != nil {
			slog.Warn("Heartbeat monitor unavailable", "error", err)
		} else {
			defer hm.Close()
			// Display status updates in background
			go displayStatus(hm, *noReconnect)
			slog.Info("Heartbeat monitor: listening", "port", heartbeat.DefaultPort)
		}
	}

	// Adaptive bitrate controller — adjusts video bitrates based on Wi-Fi quality
	if !*noAdaptive && hm != nil && ctrl != nil {
		go runAdaptiveBitrate(hm, ctrl)
		slog.Info("Adaptive bitrate: enabled")
	}

	// Wait for interrupt or stream error
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigCh:
		slog.Info("Interrupted, shutting down...")
	case err := <-md.Errors():
		slog.Error("Stream error, shutting down...", "error", err)
	}

	md.StopAll()
	slog.Info("Done.")
}

// makeScreenHandler creates a handler for screen video packets.
// Routes H.264 frames to a GStreamer pipeline with display sink.
func makeScreenHandler(pipeline *video.Pipeline) protocol.StreamHandler {
	var frameCount uint64
	var byteCount uint64
	var haveConfig bool

	return func(header *protocol.Header, payload []byte) error {
		frameCount++
		byteCount += uint64(len(payload))

		if header.IsConfig() {
			// Skip the 4-byte codec ID header (not real config)
			if len(payload) <= 4 {
				return nil
			}
			haveConfig = true
			annexB := video.ConfigToAnnexB(payload)
			if _, err := pipeline.Write(annexB); err != nil {
				return fmt.Errorf("screen config write: %w", err)
			}
			slog.Debug("screen config fed to pipeline",
				"raw_bytes", len(payload),
				"annexb_bytes", len(annexB))
			return nil
		}
		if header.IsSession() {
			slog.Debug("screen session", "bytes", len(payload))
			return nil
		}

		if !haveConfig && header.IsKeyFrame() {
			slog.Warn("screen: keyframe without prior config, decode may fail")
		}

		if _, err := pipeline.Write(payload); err != nil {
			return fmt.Errorf("screen frame write: %w", err)
		}

		if frameCount%300 == 0 {
			slog.Debug("screen stream",
				"frame", frameCount,
				"size", len(payload),
				"pts", header.PTS(),
				"total_bytes", byteCount)
		}
		return nil
	}
}

// makeCameraHandler creates a handler for camera video packets.
// Routes H.264 frames to a GStreamer pipeline → v4l2loopback.
func makeCameraHandler(pipeline *video.Pipeline) protocol.StreamHandler {
	var frameCount uint64
	var byteCount uint64
	var haveConfig bool

	return func(header *protocol.Header, payload []byte) error {
		frameCount++
		byteCount += uint64(len(payload))

		// Write config (SPS/PPS) before any key frames.
		// Skip the 4-byte codec ID header (sent via writeVideoHeader)
		// which is not real H.264 config data.
		if header.IsConfig() {
			if len(payload) <= 4 {
				slog.Debug("camera: skipping codec ID header", "bytes", len(payload))
				return nil
			}
			haveConfig = true
			slog.Debug("camera config hex", "hex", fmt.Sprintf("%x", payload))
			annexB := video.ConfigToAnnexB(payload)
			if _, err := pipeline.Write(annexB); err != nil {
				return fmt.Errorf("camera config write: %w", err)
			}
			slog.Debug("camera config fed to pipeline",
				"raw_bytes", len(payload),
				"annexb_bytes", len(annexB))
			return nil
		}

		if header.IsSession() {
			slog.Debug("camera session update", "bytes", len(payload))
			return nil
		}

		if !haveConfig && header.IsKeyFrame() {
			slog.Warn("camera: keyframe without prior config, decode may fail")
		}

		// Feed frame to pipeline
		if _, err := pipeline.Write(payload); err != nil {
			return fmt.Errorf("camera frame write: %w", err)
		}

		if frameCount%150 == 0 {
			slog.Debug("camera stream",
				"frame", frameCount,
				"size", len(payload),
				"pts", header.PTS(),
				"total_bytes", byteCount)
		}
		return nil
	}
}

// makeMicHandler creates a handler for microphone Opus packets.
// Routes phone mic audio directly to a PipeWire Audio/Source/Virtual node
// so meeting apps can capture it. Raw Opus packets are wrapped in Ogg pages
// and fed to gst-launch for decoding and PipeWire publishing.
func makeMicHandler() protocol.StreamHandler {
	// GStreamer pipeline: Ogg Opus → decode → PipeWire Audio/Source
	cmd := exec.Command("gst-launch-1.0", "-q",
		"fdsrc", "fd=0",
		"!", "oggdemux",
		"!", "opusdec",
		"!", "audioconvert",
		"!", "audioresample",
		"!", "audio/x-raw,rate=48000,channels=1",
		"!", "pipewiresink", "mode=provide",
		"stream-properties=p,media.class=Audio/Source/Virtual,node.name=allrelay-mic,node.description=AllRelay_Phone_Mic",
	)
	cmd.Stderr = os.Stderr
	oggIn, err := cmd.StdinPipe()
	if err != nil {
		slog.Error("Mic: failed to create stdin pipe", "error", err)
		return func(header *protocol.Header, payload []byte) error { return nil }
	}
	if err := cmd.Start(); err != nil {
		slog.Error("Mic: failed to start gst-launch", "error", err)
		return func(header *protocol.Header, payload []byte) error { return nil }
	}
	slog.Info("Mic: gst-launch pipeline started → allrelay-mic (PipeWire Audio/Source)")

	// Watch for exit in background
	go func() {
		if err := cmd.Wait(); err != nil {
			slog.Warn("Mic: gst-launch exited", "error", err)
		}
	}()

	var packetCount uint64
	var byteCount uint64
	var serial uint32 = 12345
	var pageSeq uint32
	var granulePos uint64
	var wroteTags bool
	var audioBuf [][]byte    // buffered audio packets for batching into Ogg pages
	var audioBufGranule uint64 // granule position for the first packet in buffer

	// flushAudio writes buffered audio packets as one Ogg page.
	flushAudio := func() {
		if len(audioBuf) == 0 {
			return
		}
		writeOggPage(oggIn, serial, pageSeq, audioBufGranule, audioBuf)
		pageSeq++
		audioBufGranule = granulePos // granulePos already points past batched packets
		audioBuf = audioBuf[:0]
	}

	return func(header *protocol.Header, payload []byte) error {
		packetCount++
		byteCount += uint64(len(payload))

		if header.IsConfig() {
			slog.Debug("mic Opus config", "bytes", len(payload))
			// Write OpusHead as first Ogg page
			writeOggPage(oggIn, serial, pageSeq, granulePos, [][]byte{payload})
			pageSeq++
			// Write OpusTags as second Ogg page (required by oggdemux)
			// Minimal vendor string "AllRelay"
			opusTags := []byte("OpusTags")
			vendorLen := uint32(8) // "AllRelay"
			vendor := []byte("AllRelay")
			tagsPacket := make([]byte, 8+4+8+4) // "OpusTags" + vendor_length + vendor + user_comment_list_length
			copy(tagsPacket, opusTags)
			tagsPacket[8] = byte(vendorLen)
			tagsPacket[9] = byte(vendorLen >> 8)
			tagsPacket[10] = byte(vendorLen >> 16)
			tagsPacket[11] = byte(vendorLen >> 24)
			copy(tagsPacket[12:], vendor)
			// user_comment_list_length = 0 (bytes 20-23 are already zero)
			writeOggPage(oggIn, serial, pageSeq, granulePos, [][]byte{tagsPacket})
			pageSeq++
			_ = wroteTags
			return nil
		}

		// Buffer audio packet for batch writing
		if len(audioBuf) == 0 {
			audioBufGranule = granulePos
		}
		audioBuf = append(audioBuf, payload)
		granulePos += 960 // advance past this packet

		// Flush every 25 packets (~500ms), matching oggmux behavior
		if len(audioBuf) >= 25 {
			flushAudio()
		}

		if packetCount%500 == 0 {
			slog.Debug("mic stream",
				"packets", packetCount,
				"size", len(payload),
				"total_bytes", byteCount)
		}
		return nil
	}
}

// writeOggPage writes an Ogg page containing the given packets.
// This is a minimal Ogg muxer for wrapping raw Opus packets.
func writeOggPage(w io.Writer, serial uint32, pageSeq uint32, granulePos uint64, packets [][]byte) {
	// Calculate total packet data size and segment table
	var dataLen int
	var segTable []byte
	for _, pkt := range packets {
		pktLen := len(pkt)
		if pktLen == 0 {
			segTable = append(segTable, 0)
			dataLen += 0
			continue
		}
		// Split packet into 255-byte segments
		for remaining := pktLen; remaining > 0; {
			if remaining >= 255 {
				segTable = append(segTable, 255)
				remaining -= 255
			} else {
				segTable = append(segTable, byte(remaining))
				remaining = 0
			}
		}
		dataLen += pktLen
	}

	// Build Ogg page header (27 bytes) + segment table + data
	pageLen := 27 + len(segTable) + dataLen
	page := make([]byte, pageLen)

	// Magic: "OggS"
	copy(page[0:4], "OggS")
	page[4] = 0 // version
	// header_type: 0 = normal, 2 = first page (BOS)
	if pageSeq == 0 {
		page[5] = 2 // BOS
	} else {
		page[5] = 0
	}
	// granule_position (8 bytes, little-endian)
	page[6] = byte(granulePos)
	page[7] = byte(granulePos >> 8)
	page[8] = byte(granulePos >> 16)
	page[9] = byte(granulePos >> 24)
	page[10] = byte(granulePos >> 32)
	page[11] = byte(granulePos >> 40)
	page[12] = byte(granulePos >> 48)
	page[13] = byte(granulePos >> 56)
	// serial (4 bytes, little-endian)
	page[14] = byte(serial)
	page[15] = byte(serial >> 8)
	page[16] = byte(serial >> 16)
	page[17] = byte(serial >> 24)
	// page_sequence (4 bytes, little-endian)
	page[18] = byte(pageSeq)
	page[19] = byte(pageSeq >> 8)
	page[20] = byte(pageSeq >> 16)
	page[21] = byte(pageSeq >> 24)
	// checksum (4 bytes) — set to 0 initially, compute after building page
	page[22] = 0
	page[23] = 0
	page[24] = 0
	page[25] = 0
	// num_segments (1 byte)
	page[26] = byte(len(segTable))
	// segment table
	copy(page[27:27+len(segTable)], segTable)
	// packet data
	off := 27 + len(segTable)
	for _, pkt := range packets {
		copy(page[off:], pkt)
		off += len(pkt)
	}

	// Compute CRC32 checksum over the entire page (with checksum field zeroed)
	// Ogg CRC uses the same polynomial as zlib/CRC-32 (IEEE 802.3)
	crc := oggCRC32(page)
	page[22] = byte(crc)
	page[23] = byte(crc >> 8)
	page[24] = byte(crc >> 16)
	page[25] = byte(crc >> 24)

	w.Write(page)
}

// oggCRC32 computes the Ogg CRC32 checksum for a page buffer.
// Ogg uses the non-reflected CRC-32 variant (polynomial 0x04C11DB7, init=0, no final XOR),
// which differs from the common PKZIP/zlib CRC-32.
func oggCRC32(page []byte) uint32 {
	var crc uint32 = 0
	for _, b := range page {
		crc = (crc << 8) ^ crc32Table[byte(crc>>24)^b]
	}
	return crc
}

// crc32Table is the CRC-32 lookup table for the non-reflected polynomial 0x04C11DB7.
var crc32Table [256]uint32

func init() {
	for i := range crc32Table {
		crc := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
		crc32Table[i] = crc
	}
}

// WriteSpeakerPacket writes an Opus-encoded speaker packet to the phone.
// The packet is formatted with the 16-byte AllRelay header:
//   [stream_id(4)] [pts+flags(8)] [payload_size(4)] [payload...]
func WriteSpeakerPacket(w io.Writer, pts uint64, payload []byte) error {
	return writeSpeakerPacket(w, pts, payload, false)
}

// writeSpeakerConfig sends a speaker config packet (with CONFIG flag set).
// Used for OpusHead before sending audio frames.
func writeSpeakerConfig(w io.Writer, payload []byte) error {
	return writeSpeakerPacket(w, 0, payload, true)
}

func writeSpeakerPacket(w io.Writer, pts uint64, payload []byte, config bool) error {
	header := make([]byte, 16)
	// stream_id (4 bytes, big-endian)
	header[0] = byte(protocol.StreamSpeaker >> 24)
	header[1] = byte(protocol.StreamSpeaker >> 16)
	header[2] = byte(protocol.StreamSpeaker >> 8)
	header[3] = byte(protocol.StreamSpeaker)
	// pts+flags (8 bytes, big-endian)
	var ptsAndFlags uint64
	if config {
		ptsAndFlags = 1 << 62 // PACKET_FLAG_CONFIG
	} else {
		ptsAndFlags = pts
	}
	header[4] = byte(ptsAndFlags >> 56)
	header[5] = byte(ptsAndFlags >> 48)
	header[6] = byte(ptsAndFlags >> 40)
	header[7] = byte(ptsAndFlags >> 32)
	header[8] = byte(ptsAndFlags >> 24)
	header[9] = byte(ptsAndFlags >> 16)
	header[10] = byte(ptsAndFlags >> 8)
	header[11] = byte(ptsAndFlags)
	// payload_size (4 bytes, big-endian)
	ps := uint32(len(payload))
	header[12] = byte(ps >> 24)
	header[13] = byte(ps >> 16)
	header[14] = byte(ps >> 8)
	header[15] = byte(ps)
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("speaker header write: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("speaker payload write: %w", err)
	}
	return nil
}

// readOggPackets reads Ogg Opus pages from r and extracts raw Opus packets.
// Each extracted packet is sent to the callback fn. Stops at EOF or error.
func readOggPackets(r io.Reader, fn func(payload []byte, granulePos uint64) error) error {
	header := make([]byte, 27)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		if string(header[0:4]) != "OggS" {
			return fmt.Errorf("not an Ogg page: %x", header[0:4])
		}
		granulePos := uint64(header[6]) | uint64(header[7])<<8 |
			uint64(header[8])<<16 | uint64(header[9])<<24 |
			uint64(header[10])<<32 | uint64(header[11])<<40 |
			uint64(header[12])<<48 | uint64(header[13])<<56
		numSegments := int(header[26])

		segTable := make([]byte, numSegments)
		if _, err := io.ReadFull(r, segTable); err != nil {
			return err
		}

		var pkt []byte
		for _, segLen := range segTable {
			chunk := make([]byte, segLen)
			if segLen > 0 {
				if _, err := io.ReadFull(r, chunk); err != nil {
					return err
				}
			}
			pkt = append(pkt, chunk...)
			if segLen < 255 {
				if len(pkt) > 0 {
					if err := fn(pkt, granulePos); err != nil {
						return err
					}
					pkt = nil
				}
			}
		}
	}
}

// forwardInputEvents reads captured input events from the input capturer
// and sends them to the Android device via the control channel.
//
// Keyboard events: Linux/X11 keycode → Android keycode → control channel
// Mouse events: left click → touch down/up, right click → Back
func forwardInputEvents(events <-chan input.Event, ctrl *control.Channel) {
	for event := range events {
		switch event.Type {
		case input.EventKeyDown:
			if err := ctrl.SendKey("down", event.Keycode); err != nil {
				slog.Debug("key down send failed", "keycode", event.Keycode, "error", err)
			}
		case input.EventKeyUp:
			if err := ctrl.SendKey("up", event.Keycode); err != nil {
				slog.Debug("key up send failed", "keycode", event.Keycode, "error", err)
			}
		case input.EventTouchDown:
			if err := ctrl.SendTouch("down", event.X, event.Y); err != nil {
				slog.Debug("touch down send failed", "error", err)
			}
		case input.EventTouchMove:
			if err := ctrl.SendTouch("move", event.X, event.Y); err != nil {
				slog.Debug("touch move send failed", "error", err)
			}
		case input.EventTouchUp:
			if err := ctrl.SendTouch("up", event.X, event.Y); err != nil {
				slog.Debug("touch up send failed", "error", err)
			}
		}
	}
	slog.Info("Input forwarder stopped")
}

// displayStatus monitors heartbeat updates and logs device status.
// When noReconnect is false and heartbeat is lost, it triggers reconnection.
func displayStatus(hm *heartbeat.Monitor, noReconnect bool) {
	for status := range hm.Updates() {
		slog.Info("Device status",
			"battery", status.Device.Battery,
			"wifi_rssi", status.Device.WiFiRSSI,
			"cpu", fmt.Sprintf("%.1f%%", status.Device.CPUUsage),
			"wifi_speed", fmt.Sprintf("%d Mbps", status.Device.WiFiLinkSpeed))

		_ = noReconnect // TODO(Phase 4): trigger reconnection on heartbeat loss
	}
}

// runAdaptiveBitrate runs the adaptive bitrate control loop.
// It monitors heartbeat status updates and adjusts video bitrates
// based on Wi-Fi quality metrics (RTT, packet loss, jitter).
func runAdaptiveBitrate(hm *heartbeat.Monitor, ctrl *control.Channel) {
	// Create the bitrate setter callback
	setter := func(streamID int, bitrateBPS int) error {
		return ctrl.Send(control.Message{
			Type:     control.TypeConfig,
			StreamID: streamID,
			Bitrate:  bitrateBPS,
		})
	}

	cfg := bitrate.DefaultConfig()
	streams := bitrate.DefaultStreamConfigs()
	controller := bitrate.NewController(cfg, streams, setter)

	for status := range hm.Updates() {
		changes := controller.UpdateFromHeartbeat(status)
		if len(changes) > 0 {
			controller.ApplyChanges(changes)
		}
	}

	slog.Info("Adaptive bitrate controller stopped")
}
