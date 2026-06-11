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
	"sync"
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
	}

	// Mic stream handler (stream ID 0x03)
	if !*noMic && conn.HasStream(protocol.StreamMic) {
		slog.Info("Mic stream: connected")
		md.AddStream(protocol.StreamMic, conn.MicStream(),
			makeMicHandler())
	}

	// Speaker — PC captures system audio, encodes Opus, sends to phone
	if !*noSpeaker && conn.HasStream(protocol.StreamSpeaker) {
		slog.Info("Speaker stream: connected")
		go runSpeakerCapture(conn.SpeakerWriter())
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
				// Skip codec ID header — not SPS/PPS
				slog.Debug("camera: skipping codec ID header", "bytes", len(payload))
				return nil
			}
			haveConfig = true
			slog.Debug("camera config hex", "hex", fmt.Sprintf("%x", payload))
			// Convert to Annex B format for GStreamer byte-stream decoder
			annexB := video.ConfigToAnnexB(payload)
			if _, err := pipeline.Write(annexB); err != nil {
				return fmt.Errorf("camera config write: %w", err)
			}
			slog.Debug("camera config fed to pipeline",
				"raw_bytes", len(payload),
				"annexb_bytes", len(annexB))
			return nil
		}

		// Session packets (resolution/rotation changes) are handled by GStreamer
		// via SPS changes in the stream; log but don't feed to pipeline.
		if header.IsSession() {
			slog.Debug("camera session update", "bytes", len(payload))
			return nil
		}

		// If we haven't received config yet, the decoder may fail on the
		// first keyframe. This is normal — config is usually sent first.
		if !haveConfig && header.IsKeyFrame() {
			slog.Warn("camera: keyframe without prior config, decode may fail")
		}

		// Feed frame to GStreamer pipeline
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
// Routes phone mic audio → Ogg muxing → FIFO → gst-launch → pulsesink → null-sink,
// then pw-loopback exposes the null-sink monitor as a virtual microphone source.
//
// Uses a FIFO + delayed gst-launch start to avoid "EOS before finding a chain"
// from oggdemux (phone sends config immediately but audio data starts seconds later).
func makeMicHandler() protocol.StreamHandler {
	fifoPath := fmt.Sprintf("/tmp/allrelay-mic-%d.fifo", os.Getpid())
	os.Remove(fifoPath)
	if err := exec.Command("mkfifo", fifoPath).Run(); err != nil {
		slog.Error("Mic: mkfifo failed", "error", err)
		return func(header *protocol.Header, payload []byte) error { return nil }
	}

	// Start gst-launch reading from FIFO (uses pulsesink → null-sink → pw-loopback)
	cmd := exec.Command("gst-launch-1.0", "-q",
		"filesrc", "location="+fifoPath,
		"!", "oggdemux",
		"!", "opusdec",
		"!", "audioconvert",
		"!", "audioresample",
		"!", "pulsesink", "device=allrelay-mic-sink",
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		slog.Error("Mic: gst-launch start failed", "error", err)
		os.Remove(fifoPath)
		return func(header *protocol.Header, payload []byte) error { return nil }
	}

	// Open FIFO with O_RDWR — never blocks even without reader.
	// This eliminates the race where gst-launch opens read-end and
	// immediately closes it before we write the first byte.
	oggOut, openErr := os.OpenFile(fifoPath, os.O_RDWR, 0)
	if openErr != nil {
		slog.Error("Mic: FIFO open failed", "error", openErr)
		cmd.Process.Kill()
		os.Remove(fifoPath)
		return func(header *protocol.Header, payload []byte) error { return nil }
	}

	slog.Info("Mic: gst-launch pipeline started → allrelay-mic-sink", "pid", cmd.Process.Pid)

	go func() {
		if err := cmd.Wait(); err != nil {
			slog.Warn("Mic: gst-launch exited", "error", err)
		}
		oggOut.Close()
		os.Remove(fifoPath)
	}()

	var (
		mu          sync.Mutex
		serial      uint32 = 12345
		pageSeq     uint32
		granulePos  uint64
		pending     [][]byte // buffered audio packets before pipeline fully starts
		pendingCfg  []byte   // buffered config
		started     bool
		packetCount uint64
		byteCount   uint64
	)

	return func(header *protocol.Header, payload []byte) error {
		mu.Lock()
		defer mu.Unlock()

		packetCount++
		if packetCount <= 3 {
			slog.Info("mic packet received", "num", packetCount, "isConfig", header.IsConfig(), "size", len(payload))
		}
		byteCount += uint64(len(payload))

		if header.IsConfig() {
			pendingCfg = payload
			return nil
		}

		// Buffer audio packets until we have config + a few packets.
		// This prevents oggdemux from getting "EOS before finding a chain"
		// when there's a gap between OpusHead and first audio data.
		if !started {
			pending = append(pending, payload)
			if len(pendingCfg) > 0 && len(pending) >= 25 {
				// Write buffered config + all buffered audio now
				writeOggPage(oggOut, serial, pageSeq, 0, [][]byte{pendingCfg})
				pageSeq++
				tagsPacket := make([]byte, 24)
				copy(tagsPacket, "OpusTags")
				tagsPacket[8] = 8
				copy(tagsPacket[12:], "AllRelay")
				writeOggPage(oggOut, serial, pageSeq, 0, [][]byte{tagsPacket})
				pageSeq++
				for _ = range pending {
					granulePos += 960
				}
				writeOggPage(oggOut, serial, pageSeq, granulePos, pending)
				pageSeq++
				pending = nil
				pendingCfg = nil
				started = true
			}
			return nil
		}

		// Streaming mode: write one Ogg page per packet
		granulePos += 960
		writeOggPage(oggOut, serial, pageSeq, granulePos, [][]byte{payload})
		pageSeq++

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
			continue
		}
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

	pageLen := 27 + len(segTable) + dataLen
	page := make([]byte, pageLen)

	copy(page[0:4], "OggS")
	page[4] = 0
	if pageSeq == 0 {
		page[5] = 2 // BOS
	} else {
		page[5] = 0
	}
	page[6] = byte(granulePos)
	page[7] = byte(granulePos >> 8)
	page[8] = byte(granulePos >> 16)
	page[9] = byte(granulePos >> 24)
	page[10] = byte(granulePos >> 32)
	page[11] = byte(granulePos >> 40)
	page[12] = byte(granulePos >> 48)
	page[13] = byte(granulePos >> 56)
	page[14] = byte(serial)
	page[15] = byte(serial >> 8)
	page[16] = byte(serial >> 16)
	page[17] = byte(serial >> 24)
	page[18] = byte(pageSeq)
	page[19] = byte(pageSeq >> 8)
	page[20] = byte(pageSeq >> 16)
	page[21] = byte(pageSeq >> 24)
	page[26] = byte(len(segTable))
	copy(page[27:27+len(segTable)], segTable)
	off := 27 + len(segTable)
	for _, pkt := range packets {
		copy(page[off:], pkt)
		off += len(pkt)
	}

	crc := oggCRC32(page)
	page[22] = byte(crc)
	page[23] = byte(crc >> 8)
	page[24] = byte(crc >> 16)
	page[25] = byte(crc >> 24)

	w.Write(page)
}

// oggCRC32 computes the Ogg CRC32 checksum for a page buffer.
// Ogg uses the non-reflected CRC-32 variant (polynomial 0x04C11DB7, init=0, no final XOR).
func oggCRC32(page []byte) uint32 {
	var crc uint32
	for _, b := range page {
		crc = (crc << 8) ^ crc32Table[byte(crc>>24)^b]
	}
	return crc
}

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

// runSpeakerCapture starts the GStreamer speaker capture pipeline,
// demuxes Ogg pages to extract raw Opus packets, and sends them
// to the phone via the speaker TCP connection with 16-byte AllRelay headers.
//
// The first two packets (OpusHead + OpusTags) are sent with CONFIG flag.
// Subsequent audio packets are sent with PTS for timing.
//
// Packet flow:
//
//	PipeWire → pulsesrc → opusenc → oggmux → fdsink stdout
//	                                              ↓
//	                              Go reads Ogg pages, extracts Opus packets
//	                                              ↓
//	                              Writes 16-byte header + raw Opus to TCP
//	                                              ↓
//	                              Android MediaCodec decodes → AudioTrack plays
func runSpeakerCapture(w io.Writer) {
	pipeline, err := audio.SpeakerCapturePipeline()
	if err != nil {
		slog.Error("Speaker: failed to start capture pipeline", "error", err)
		return
	}
	defer pipeline.Close()

	demux := audio.NewOggDemuxer(pipeline)

	// Read and send OpusHead (codec config)
	opusHead, err := demux.NextPacket()
	if err != nil {
		slog.Error("Speaker: failed to read OpusHead", "error", err)
		return
	}
	if err := audio.WritePacket(w, protocol.StreamSpeaker, protocol.FlagConfig, 0, opusHead); err != nil {
		slog.Error("Speaker: failed to send OpusHead", "error", err)
		return
	}
	slog.Info("Speaker: sent OpusHead config", "bytes", len(opusHead))

	// Read and send OpusTags (comment header)
	opusTags, err := demux.NextPacket()
	if err != nil {
		slog.Warn("Speaker: no OpusTags packet", "error", err)
		return
	}
	if err := audio.WritePacket(w, protocol.StreamSpeaker, protocol.FlagConfig, 0, opusTags); err != nil {
		slog.Error("Speaker: failed to send OpusTags", "error", err)
		return
	}
	slog.Debug("Speaker: sent OpusTags", "bytes", len(opusTags))

	// Main loop: read Opus audio packets and forward to phone
	var frameCount uint64
	var byteCount uint64
	for {
		packet, err := demux.NextPacket()
		if err != nil {
			if err == io.EOF {
				slog.Info("Speaker: capture pipeline ended", "frames", frameCount)
			} else {
				slog.Error("Speaker: read error", "error", err)
			}
			return
		}

		pts := frameCount * 20000 // 20ms per frame
		if err := audio.WritePacket(w, protocol.StreamSpeaker, 0, pts, packet); err != nil {
			slog.Error("Speaker: write error", "error", err)
			return
		}

		frameCount++
		byteCount += uint64(len(packet))

		if frameCount%250 == 0 {
			slog.Debug("Speaker stream",
				"frames", frameCount,
				"total_bytes", byteCount,
				"packet_bytes", len(packet))
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
