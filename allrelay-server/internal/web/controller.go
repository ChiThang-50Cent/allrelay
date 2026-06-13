package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/allrelay/allrelay-server/internal/audio"
	"github.com/allrelay/allrelay-server/internal/protocol"
	"github.com/allrelay/allrelay-server/internal/transport"
	"github.com/allrelay/allrelay-server/internal/video"
)

// ServerController manages the actual connection to the phone
// and provides an interface for the web UI to control it
type ServerController struct {
	mu        sync.RWMutex
	connected bool
	host      string
	port      int
	webServer *WebServer

	// Connection
	conn *transport.Connection
	
	// Stream control
	streams map[string]*StreamController
	
	// Cleanup
	cleanup []func()
}

// StreamController manages a single stream
type StreamController struct {
	Name    string
	Port    int
	Active  bool
	Running bool
	stop    func()
	cancel  context.CancelFunc
	done    chan struct{} // closed when goroutine fully exits
	gen     int           // generation counter for stream lifecycle
}

// NewServerController creates a new server controller
func NewServerController(webServer *WebServer) *ServerController {
	sc := &ServerController{
		streams:   make(map[string]*StreamController),
		webServer: webServer,
		cleanup:   make([]func(), 0),
	}

	// Initialize streams
	sc.streams["screen"] = &StreamController{Name: "screen", Port: 5000}
	sc.streams["camera"] = &StreamController{Name: "camera", Port: 5001}
	sc.streams["mic"] = &StreamController{Name: "mic", Port: 5002}
	sc.streams["speaker"] = &StreamController{Name: "speaker", Port: 5003}
	sc.streams["control"] = &StreamController{Name: "control", Port: 5004}

	return sc
}

// Connect connects to a phone and starts streaming.
// If already connected to the same host, disconnects and reconnects
// (handles Go server restart after crash).
func (sc *ServerController) Connect(host string, port int) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	// If already connected to same host, force reconnect
	if sc.connected && sc.host == host {
		slog.Info("Reconnecting to phone", "host", host)
		sc.disconnectLocked()
	}

	slog.Info("Connecting to phone", "host", host, "port", port)

	// Connect speaker + camera. Screen/video intentionally disabled
	// to avoid conflicts with the speaker daemon flow.
	conn, err := transport.Connect(host, uint16(port),
		false, // video (screen) — daemon mode skips screen
		true,  // camera
		false, // mic
		true,  // speaker
		false) // control
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}

	sc.conn = conn
	sc.connected = true
	sc.host = host
	sc.port = port

	// Update web server status
	if sc.webServer != nil {
		phone := &PhoneDevice{
			ID:   host,
			IP:   host,
			Name: fmt.Sprintf("Phone (%s)", host),
		}
		sc.webServer.SetConnectionStatus(true, phone)
	}

	// Start speaker stream by default (PC → phone audio)
	if conn.HasStream(protocol.StreamSpeaker) {
		sc.startSpeakerStream()
	}

	// Start camera stream by default (phone camera → PC → v4l2loopback)
	if conn.HasStream(protocol.StreamCamera) {
		sc.startCameraStream()
	}

	slog.Info("Connected to phone", "host", host)
	return nil
}

// Disconnect disconnects from the phone
func (sc *ServerController) Disconnect() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.disconnectLocked()
}

// disconnectLocked disconnects (caller holds lock)
func (sc *ServerController) disconnectLocked() error {
	if !sc.connected {
		return nil
	}

	slog.Info("Disconnecting from phone", "host", sc.host)

	// Stop all streams
	for _, stream := range sc.streams {
		if stream.stop != nil {
			stream.stop()
		}
		stream.Running = false
		stream.Active = false
	}

	// Run cleanup functions
	for _, fn := range sc.cleanup {
		fn()
	}
	sc.cleanup = nil

	// Close connection
	if sc.conn != nil {
		sc.conn.Close()
		sc.conn = nil
	}

	sc.connected = false
	sc.host = ""
	sc.port = 0

	// Update web server status
	if sc.webServer != nil {
		sc.webServer.SetConnectionStatus(false, nil)
	}

	return nil
}

// SyncStreamStatus updates stream statuses from controller into the provided slice.
// This is called by the HTTP handler before returning status.
func (sc *ServerController) SyncStreamStatus(streams []StreamStatus) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	for i, s := range streams {
		if ctrl, ok := sc.streams[s.Name]; ok {
			streams[i].Active = ctrl.Active
		}
	}
}

// ToggleStream toggles a stream on/off
func (sc *ServerController) ToggleStream(name string, active bool) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	stream, ok := sc.streams[name]
	if !ok {
		return fmt.Errorf("unknown stream: %s", name)
	}

	if !sc.connected {
		return fmt.Errorf("not connected to phone")
	}

	slog.Info("Toggling stream", "stream", name, "active", active)
	stream.Active = active

	if active && !stream.Running {
		// Release lock while waiting for old goroutine to stop
		// (avoids deadlock: old goroutine needs mu to cleanup)
		cancel := stream.cancel
		done := stream.done
		sc.mu.Unlock()

		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}

		sc.mu.Lock()
		// Re-read stream state (may have changed)
		stream, ok = sc.streams[name]
		if !ok || stream.Running {
			return nil
		}

		// Start stream
		switch name {
		case "speaker":
			sc.startSpeakerStreamLocked()
		case "camera":
			sc.startCameraStreamLocked()
		case "mic":
			sc.startMicStreamLocked()
		}
	} else if !active && stream.Running {
		// Stop stream
		if stream.stop != nil {
			stream.stop()
		}
		stream.Running = false
	}

	return nil
}

// startSpeakerStream starts the speaker stream (PC → phone)
func (sc *ServerController) startSpeakerStream() {
	sc.startSpeakerStreamLocked()
}

// startSpeakerStreamLocked starts the speaker stream (must hold lock)
func (sc *ServerController) startSpeakerStreamLocked() {
	if sc.conn == nil || !sc.conn.HasStream(protocol.StreamSpeaker) {
		slog.Warn("Speaker stream not available (no connection)")
		return
	}

	stream := sc.streams["speaker"]

	// Cancel old goroutine (caller already waited for it via done channel)
	// Cancel and done are set to new values below
	stream.Running = false
	stream.Active = false

	// Create a cancel context for this stream
	ctx, cancel := context.WithCancel(context.Background())
	stream.cancel = cancel

	slog.Info("Starting speaker stream")
	stream.Active = true
	stream.Running = true
	stream.gen++
	gen := stream.gen

	// Metrics callback
	onMetrics := func(fps, bitrate, latency int, bytes, frames int64) {
		if sc.webServer != nil {
			sc.webServer.UpdateStreamMetrics("speaker", fps, bitrate, latency, bytes, frames)
		}
	}

	// Start speaker capture in background
	stream.done = make(chan struct{})
	go func() {
		defer close(stream.done)
		writer := sc.conn.SpeakerWriter()
		if err := runSpeakerCapture(ctx, writer, onMetrics); err != nil {
			slog.Error("Speaker capture error", "error", err)
		}
		sc.mu.Lock()
		if stream.Running && stream.gen == gen {
			stream.Running = false
			stream.Active = false
			sc.connected = false
		}
		sc.mu.Unlock()
	}()
}

// startMicStreamLocked starts the mic stream (phone → PC)
func (sc *ServerController) startMicStreamLocked() {
	if sc.conn == nil || !sc.conn.HasStream(protocol.StreamMic) {
		slog.Warn("Mic stream not available")
		return
	}

	stream := sc.streams["mic"]
	if stream.Running {
		return
	}

	slog.Info("Starting mic stream")
	stream.Active = true
	stream.Running = true

	// TODO: Implement mic stream handling
	// For now, just mark as running
	slog.Info("Mic stream started (placeholder)")
}

// startCameraStream starts the camera stream (phone camera → PC → v4l2loopback)
func (sc *ServerController) startCameraStream() {
	sc.startCameraStreamLocked()
}

// startCameraStreamLocked starts the camera stream (must hold lock)
func (sc *ServerController) startCameraStreamLocked() {
	if sc.conn == nil || !sc.conn.HasStream(protocol.StreamCamera) {
		slog.Warn("Camera stream not available (no connection)")
		return
	}

	stream := sc.streams["camera"]

	// Old goroutine already cancelled + waited by caller
	stream.Running = false
	stream.Active = false

	// Create a cancel context for this stream
	ctx, cancel := context.WithCancel(context.Background())
	stream.cancel = cancel

	slog.Info("Starting camera stream")
	stream.Active = true
	stream.Running = true
	stream.gen++
	gen := stream.gen

	// Start camera capture in background (reads from phone, writes to v4l2loopback)
	stream.done = make(chan struct{})
	go func() {
		defer close(stream.done)
		reader := sc.conn.CameraStream()
		if err := runCameraCapture(ctx, reader); err != nil {
			slog.Error("Camera capture error", "error", err)
		}
		sc.mu.Lock()
		if stream.Running && stream.gen == gen {
			stream.Running = false
			stream.Active = false
		}
		sc.mu.Unlock()
	}()
}

// IsConnected returns whether we're connected to a phone
func (sc *ServerController) IsConnected() bool {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.connected
}

// GetHost returns the current phone host
func (sc *ServerController) GetHost() string {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.host
}

// GetStreamStatus returns the status of all streams
func (sc *ServerController) GetStreamStatus() []StreamStatus {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	statuses := make([]StreamStatus, 0, len(sc.streams))
	for _, s := range sc.streams {
		statuses = append(statuses, StreamStatus{
			Name:   s.Name,
			Port:   s.Port,
			Active: s.Active,
		})
	}
	return statuses
}

// runSpeakerCapture captures audio from PC and sends to phone
func runSpeakerCapture(ctx context.Context, w io.Writer, onMetrics func(fps, bitrate, latency int, bytes, frames int64)) error {
	pipeline, err := audio.SpeakerCapturePipeline()
	if err != nil {
		return fmt.Errorf("failed to start capture pipeline: %w", err)
	}
	defer pipeline.Close()

	demux := audio.NewOggDemuxer(pipeline)

	// Read and send OpusHead (codec config)
	opusHead, err := demux.NextPacket()
	if err != nil {
		return fmt.Errorf("failed to read OpusHead: %w", err)
	}
	if err := audio.WritePacket(w, protocol.StreamSpeaker, protocol.FlagConfig, 0, opusHead); err != nil {
		return fmt.Errorf("failed to send OpusHead: %w", err)
	}
	slog.Info("Speaker: sent OpusHead config", "bytes", len(opusHead))

	// Read and send OpusTags (comment header)
	opusTags, err := demux.NextPacket()
	if err != nil {
		return fmt.Errorf("no OpusTags packet: %w", err)
	}
	if err := audio.WritePacket(w, protocol.StreamSpeaker, protocol.FlagConfig, 0, opusTags); err != nil {
		return fmt.Errorf("failed to send OpusTags: %w", err)
	}
	slog.Debug("Speaker: sent OpusTags", "bytes", len(opusTags))

	// Monitor context cancellation — kill pipeline to unblock reads
	go func() {
		<-ctx.Done()
		slog.Info("Speaker: context cancelled, closing pipeline")
		pipeline.Close()
	}()

	// Main loop: read Opus audio packets and forward to phone
	var frameCount uint64
	var byteCount uint64
	var lastMetricsTime = time.Now()
	var framesSinceMetrics uint64
	var bytesSinceMetrics uint64

	for {
		packet, err := demux.NextPacket()
		if err != nil {
			// Check if this was a clean cancellation
			if ctx.Err() != nil {
				slog.Info("Speaker: stopped by context", "frames", frameCount)
				return ctx.Err()
			}
			if err == io.EOF {
				slog.Info("Speaker: capture pipeline ended", "frames", frameCount)
			} else {
				slog.Error("Speaker: read error", "error", err)
			}
			return err
		}

		sendStart := time.Now()
		pts := frameCount * 20000 // 20ms per frame
		if err := audio.WritePacket(w, protocol.StreamSpeaker, 0, pts, packet); err != nil {
			slog.Error("Speaker: write error", "error", err)
			return err
		}
		sendLatency := time.Since(sendStart).Microseconds()

		frameCount++
		byteCount += uint64(len(packet))
		framesSinceMetrics++
		bytesSinceMetrics += uint64(len(packet))

		// Update metrics every second
		if time.Since(lastMetricsTime) >= time.Second {
			fps := int(float64(framesSinceMetrics) / time.Since(lastMetricsTime).Seconds())
			bitrate := int(float64(bytesSinceMetrics*8) / time.Since(lastMetricsTime).Seconds())
			// Pipeline latency: Go send time + estimated TCP + AudioTrack buffer
			latencyMs := int(sendLatency/1000) + 5 + 150
			if onMetrics != nil {
				onMetrics(fps, bitrate, latencyMs, int64(byteCount), int64(frameCount))
			}

			framesSinceMetrics = 0
			bytesSinceMetrics = 0
			lastMetricsTime = time.Now()
		}

		if frameCount%250 == 0 {
			slog.Debug("Speaker stream",
				"frames", frameCount,
				"total_bytes", byteCount,
				"packet_bytes", len(packet))
		}
	}
}

// runCameraCapture reads H.264 camera packets from the phone and feeds
// them to an FFmpeg pipeline that decodes and writes to a v4l2loopback device (/dev/video10).
//
// Android camera daemon sends AllRelay protocol packets (16-byte header + payload).
// We use the demuxer to strip headers and forward only H.264 NAL units to FFmpeg.
func runCameraCapture(ctx context.Context, reader io.Reader) error {
	// PipeWire camera source — no device path needed, no v4l2loopback, no sudo.
	// Browsers see it via xdg-desktop-portal.
	device := video.GetCameraDevice()

	slog.Info("Camera: opening PipeWire pipeline")

	pipeline, err := video.CameraPipeline(device)
	if err != nil {
		return fmt.Errorf("camera pipeline: %w", err)
	}
	defer pipeline.Close()

	var frameCount uint64
	var byteCount uint64
	var started bool

	// Debug: peek first bytes to diagnose protocol issues
	peek := make([]byte, 32)
	n, peekErr := io.ReadFull(reader, peek)
	slog.Info("Camera: preamble bytes", "n", n, "err", peekErr, "hex", fmt.Sprintf("%x", peek[:n]))

	// Reconstruct reader with peek bytes prepended
	combinedReader := io.MultiReader(bytes.NewReader(peek[:n]), reader)

	demuxer := protocol.NewDemuxer(combinedReader)
	demuxer.RegisterHandler(protocol.StreamCamera, func(header *protocol.Header, payload []byte) error {
		// Skip zero-payload packets (session meta, etc.)
		if len(payload) == 0 {
			return nil
		}

		// Only forward actual H.264 NAL units (start with Annex B start code).
		// The first non-config packet is a codec ID ("h264") which we skip.
		// Config packets contain SPS/PPS — h264parse needs these!
		if !video.IsAnnexB(payload) {
			slog.Debug("Camera: skipping non-H.264 packet", "len", len(payload),
				"prefix", fmt.Sprintf("%x", payload[:min(len(payload), 4)]))
			return nil
		}

		// Write H.264 NAL units to GStreamer pipeline
		if _, err := pipeline.Write(payload); err != nil {
			return fmt.Errorf("pipeline write: %w", err)
		}

		if !started {
			slog.Info("Camera: received first frame", "bytes", len(payload))
			started = true
		}

		frameCount++
		byteCount += uint64(len(payload))

		// Log periodic stats
		if frameCount%150 == 0 {
			slog.Debug("Camera stream",
				"frames", frameCount,
				"total_bytes", byteCount,
				"last_packet", len(payload))
		}

		return nil
	})

	slog.Info("Camera: demuxer started")

	// Run demuxer in background, listen for context cancellation
	errCh := make(chan error, 1)
	go func() {
		errCh <- demuxer.Run()
	}()

	select {
	case <-ctx.Done():
		slog.Info("Camera: context cancelled, stopping")
		demuxer.Stop()
		return ctx.Err()
	case err := <-errCh:
		slog.Info("Camera: demuxer ended", "frames", frameCount, "bytes", byteCount, "error", err)
		return err
	}
}
