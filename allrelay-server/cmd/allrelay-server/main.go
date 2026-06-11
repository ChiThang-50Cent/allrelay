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

	"github.com/allrelay/allrelay-server/internal/protocol"
	"github.com/allrelay/allrelay-server/internal/transport"
)

func main() {
	host := flag.String("host", "", "Phone IP address (required)")
	port := flag.Int("port", 5000, "Base TCP port")
	noScreen := flag.Bool("no-screen", false, "Disable screen stream")
	noCamera := flag.Bool("no-camera", false, "Disable camera stream")
	noMic := flag.Bool("no-mic", false, "Disable microphone stream")
	noSpeaker := flag.Bool("no-speaker", false, "Disable speaker stream")
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
		!*noScreen, !*noCamera, !*noMic, !*noSpeaker, false) // control deferred to Phase 3
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
		md.AddStream(protocol.StreamScreen, conn.VideoStream(),
			makeScreenHandler())
	}

	// Camera stream handler (stream ID 0x02)
	if !*noCamera && conn.HasStream(protocol.StreamCamera) {
		slog.Info("Camera stream: connected")
		md.AddStream(protocol.StreamCamera, conn.CameraStream(),
			makeCameraHandler())
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

	slog.Info("Streaming... Press Ctrl+C to stop.")

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
// Phase 2: logs statistics; Phase 3: routes to SDL2 window.
func makeScreenHandler() protocol.StreamHandler {
	var frameCount uint64
	var byteCount uint64

	return func(header *protocol.Header, payload []byte) error {
		frameCount++
		byteCount += uint64(len(payload))

		if header.IsConfig() {
			slog.Debug("screen config", "bytes", len(payload))
			return nil
		}
		if header.IsSession() {
			slog.Debug("screen session", "bytes", len(payload))
			return nil
		}

		// Log every 300 frames (~10 seconds at 30fps)
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
// Phase 2: logs statistics; Phase 3: routes to v4l2loopback.
func makeCameraHandler() protocol.StreamHandler {
	var frameCount uint64

	return func(header *protocol.Header, payload []byte) error {
		frameCount++

		if header.IsConfig() {
			slog.Debug("camera config", "bytes", len(payload))
			return nil
		}
		if header.IsSession() {
			slog.Debug("camera session", "bytes", len(payload))
			return nil
		}

		if frameCount%150 == 0 {
			slog.Debug("camera stream",
				"frame", frameCount,
				"size", len(payload),
				"pts", header.PTS())
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
