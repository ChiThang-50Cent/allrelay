package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/allrelay/allrelay-server/internal/audio"
	"github.com/allrelay/allrelay-server/internal/protocol"
	"github.com/allrelay/allrelay-server/internal/transport"
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
	gen     int // generation counter for stream lifecycle
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

	// Connect only speaker. Android server takes ~15s to timeout video
	// accept, then starts ONLY the speaker stream (no screen = no conflicts).
	conn, err := transport.Connect(host, uint16(port),
		false, // video — not needed, causes screen stream conflicts
		false, // camera
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
		// Start stream
		switch name {
		case "speaker":
			sc.startSpeakerStreamLocked()
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

	// Stop any previous speaker goroutine
	if stream.cancel != nil {
		stream.cancel()
	}
	stream.Running = false
	stream.Active = false

	if stream.Running {
		return
	}

	// Create a cancel context for this stream
	ctx, cancel := context.WithCancel(context.Background())
	stream.cancel = cancel

	slog.Info("Starting speaker stream")
	stream.Active = true
	stream.Running = true

	// Start speaker capture in background
	stream.gen++
	gen := stream.gen
	go func() {
		writer := sc.conn.SpeakerWriter()
		if err := runSpeakerCapture(ctx, writer); err != nil {
			slog.Error("Speaker capture error", "error", err)
		}
		sc.mu.Lock()
		if stream.Running && stream.gen == gen {
			// Only update if this is still the current stream generation
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
func runSpeakerCapture(ctx context.Context, w io.Writer) error {
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
			return err
		}

		pts := frameCount * 20000 // 20ms per frame
		if err := audio.WritePacket(w, protocol.StreamSpeaker, 0, pts, packet); err != nil {
			slog.Error("Speaker: write error", "error", err)
			return err
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
