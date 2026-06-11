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
	"os/signal"
	"syscall"

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

	// Speaker — handled separately (PC sends data, doesn't receive)
	if !*noSpeaker && conn.HasStream(protocol.StreamSpeaker) {
		slog.Info("Speaker stream: connected", "note", "ready for PC→phone audio (Phase 3)")
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
	if !*noHeartbeat {
		hm, err := heartbeat.NewMonitor(heartbeat.DefaultPort)
		if err != nil {
			slog.Warn("Heartbeat monitor unavailable", "error", err)
		} else {
			defer hm.Close()
			// Display status updates in background
			go displayStatus(hm, *noReconnect)
			slog.Info("Heartbeat monitor: listening", "port", heartbeat.DefaultPort)
		}
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

		// Write config (SPS/PPS) before any key frames
		if header.IsConfig() {
			haveConfig = true
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
// Phase 2: logs statistics; Phase 3: routes to PipeWire RTP source.
func makeMicHandler() protocol.StreamHandler {
	var packetCount uint64
	var byteCount uint64

	return func(header *protocol.Header, payload []byte) error {
		packetCount++
		byteCount += uint64(len(payload))

		if header.IsConfig() {
			slog.Debug("mic Opus config", "bytes", len(payload))
			return nil
		}

		// Log every 500 packets (~5 seconds at 10ms frames)
		if packetCount%500 == 0 {
			slog.Debug("mic stream",
				"packets", packetCount,
				"size", len(payload),
				"total_bytes", byteCount)
		}
		return nil
	}
}

// WriteSpeakerPacket writes an Opus-encoded speaker packet to the phone.
// TODO(Phase 3): Implement PC → phone reverse audio via PipeWire RTP sink.
// Currently the speaker connection is established but idle.
func WriteSpeakerPacket(w io.Writer, pts uint64, payload []byte) error {
	_ = w
	_ = pts
	_ = payload
	return nil
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

		_ = noReconnect // TODO(Phase 3): trigger reconnection on heartbeat loss
	}
}
