package web

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/allrelay/allrelay-server/internal/audio"
	"github.com/allrelay/allrelay-server/internal/protocol"
	"github.com/allrelay/allrelay-server/internal/transport"
	"github.com/allrelay/allrelay-server/internal/video"
)

const controlWriteTimeout = time.Second

// ServerController manages the actual connection to the phone
// and provides an interface for the web UI to control it
type ServerController struct {
	mu        sync.RWMutex
	connected bool
	host      string
	port      int
	webServer *WebServer

	// Connect lifecycle
	connectGen    int
	connectCancel context.CancelFunc

	// Connection
	conn *transport.Connection

	// Control reconnects mutate a shared transport slot. Keep them separate
	// from server state so a dial never holds mu.
	controlMu               sync.Mutex
	controlConnection       *transport.Connection
	controlScreenGeneration int
	controlHubGeneration    uint64

	// Stream control
	streams map[string]*StreamController

	// Cleanup
	cleanup []func()
}

type StreamController struct {
	Name    string
	Port    int
	Active  bool
	Running bool
	FPS     int
	Bitrate int
	Latency int
	Bytes   int64
	Frames  int64
	cancel  context.CancelFunc
	done    chan struct{} // closed when goroutine fully exits
	gen     int           // generation counter for stream lifecycle
	hubGen  uint64        // Hub lifecycle generation for the active screen stream
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
// If already connected or connecting, disconnects/cancels first.
func (sc *ServerController) Connect(host string, port int) error {
	sc.mu.Lock()
	if sc.connectCancel != nil {
		sc.connectCancel()
		sc.connectCancel = nil
	}
	if sc.connected {
		sc.mu.Unlock()
		if err := sc.Disconnect(); err != nil {
			slog.Warn("Disconnect before reconnect had error", "error", err)
		}
		sc.mu.Lock()
	}

	sc.connectGen++
	gen := sc.connectGen
	ctx, cancel := context.WithCancel(context.Background())
	sc.connectCancel = cancel
	sc.mu.Unlock()

	slog.Info("Connecting to phone", "host", host, "port", port)

	// Dial initial speaker stream (PC → phone audio) outside sc.mu with cancellation.
	conn, err := transport.ConnectContext(ctx, host, uint16(port),
		false, // video (screen)
		false, // camera
		false, // mic
		true,  // speaker
		false) // control
	if err != nil {
		sc.mu.Lock()
		if sc.connectGen == gen {
			sc.connectCancel = nil
		}
		sc.mu.Unlock()
		return fmt.Errorf("connection failed: %w", err)
	}

	sc.mu.Lock()
	if sc.connectGen != gen || ctx.Err() != nil {
		conn.Close()
		sc.mu.Unlock()
		return fmt.Errorf("connection aborted")
	}

	sc.connectCancel = nil
	sc.conn = conn
	sc.connected = true
	sc.host = host
	sc.port = port

	sc.launchSpeakerWorkerLocked(conn)
	sc.mu.Unlock()

	// Update web server status outside sc.mu to avoid lock ordering deadlocks
	if sc.webServer != nil {
		phone := &PhoneDevice{
			ID:        host,
			IP:        host,
			Name:      fmt.Sprintf("Phone (%s)", host),
			Connected: true,
		}
		sc.webServer.SetConnectionStatus(true, phone)
		sc.webServer.publishCanonicalStatus()
	}

	slog.Info("Connected to phone", "host", host)
	return nil
}

// Disconnect disconnects from the phone
func (sc *ServerController) Disconnect() error {
	sc.mu.Lock()
	if sc.connectCancel != nil {
		sc.connectCancel()
		sc.connectCancel = nil
		sc.connectGen++
	}

	if !sc.connected {
		sc.mu.Unlock()
		return nil
	}

	slog.Info("Disconnecting from phone", "host", sc.host)

	// Restore the display while the current control route and transport are still live.
	if screen, ok := sc.streams["screen"]; ok && screen.hubGen != 0 && sc.webServer != nil {
		if err := sc.webServer.Hub().EndScreenSession(screen.hubGen); err != nil {
			slog.Debug("Failed to restore display while disconnecting", "error", err)
		}
		screen.hubGen = 0
	}

	// Capture conn and active stream workers
	conn := sc.conn
	var dones []chan struct{}
	for _, stream := range sc.streams {
		if stream.cancel != nil {
			stream.cancel()
			stream.cancel = nil
		}
		if stream.done != nil {
			dones = append(dones, stream.done)
			stream.done = nil
		}
		stream.Running = false
		stream.Active = false
		stream.gen++ // bump generation so any lingering worker can never match
	}

	cleanups := sc.cleanup
	sc.cleanup = nil

	sc.controlMu.Lock()
	sc.controlConnection = nil
	sc.controlScreenGeneration = 0
	sc.controlHubGeneration = 0
	sc.controlMu.Unlock()

	sc.conn = nil
	sc.connected = false
	sc.host = ""
	sc.port = 0
	sc.mu.Unlock()

	// Update web server status outside sc.mu
	if sc.webServer != nil {
		sc.webServer.SetConnectionStatus(false, nil)
		sc.webServer.publishCanonicalStatus()
	}

	// Close transport connection outside lock to unblock all blocking reads/writes immediately
	if conn != nil {
		conn.Close()
	}

	// Run cleanup functions outside lock
	for _, fn := range cleanups {
		fn()
	}

	// Wait for every active worker to exit outside lock
	for _, done := range dones {
		<-done
	}

	return nil
}

// SyncStreamStatus updates stream statuses from controller into the provided slice.
func (sc *ServerController) SyncStreamStatus(streams []StreamStatus) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	for i, s := range streams {
		if ctrl, ok := sc.streams[s.Name]; ok {
			streams[i].Active = ctrl.Active
			streams[i].FPS = ctrl.FPS
			streams[i].Bitrate = ctrl.Bitrate
			streams[i].Latency = ctrl.Latency
			streams[i].BytesSent = ctrl.Bytes
			streams[i].Frames = ctrl.Frames
		}
	}
}

func streamIDFromName(name string) (uint32, bool) {
	switch name {
	case "screen":
		return protocol.StreamScreen, true
	case "camera":
		return protocol.StreamCamera, true
	case "mic":
		return protocol.StreamMic, true
	case "speaker":
		return protocol.StreamSpeaker, true
	case "control":
		return protocol.StreamControl, true
	default:
		return 0, false
	}
}

// ToggleStream toggles a stream on/off
func (sc *ServerController) ToggleStream(name string, active bool) error {
	streamID, ok := streamIDFromName(name)
	if !ok || name == "control" {
		return fmt.Errorf("unknown stream: %s", name)
	}

	if !active {
		return sc.stopStream(name, streamID)
	}
	return sc.startStream(name, streamID)
}

func (sc *ServerController) stopStream(name string, streamID uint32) error {
	sc.mu.Lock()
	if !sc.connected {
		sc.mu.Unlock()
		return fmt.Errorf("not connected to phone")
	}

	stream, ok := sc.streams[name]
	if !ok {
		sc.mu.Unlock()
		return fmt.Errorf("unknown stream: %s", name)
	}

	cancel := stream.cancel
	stream.cancel = nil
	stream.gen++
	gen := stream.gen
	stream.Active = false

	if !stream.Running && stream.done == nil {
		sc.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if sc.webServer != nil {
			sc.webServer.publishCanonicalStatus()
		}
		return nil
	}

	done := stream.done
	// Keep completion visible until teardown joins, so a concurrent ON also waits.
	conn := sc.conn

	if name == "screen" && stream.hubGen != 0 && sc.webServer != nil {
		_ = sc.webServer.Hub().EndScreenSession(stream.hubGen)
		stream.hubGen = 0
	}

	if cancel != nil {
		cancel()
	}
	if conn != nil {
		conn.CloseStream(streamID)
	}
	sc.mu.Unlock()
	if done != nil {
		<-done
	}

	sc.mu.Lock()
	if stream.gen == gen {
		stream.Running = false
		stream.Active = false
		stream.done = nil
	}
	sc.mu.Unlock()

	if sc.webServer != nil {
		sc.webServer.publishCanonicalStatus()
	}
	return nil
}

func (sc *ServerController) startStream(name string, streamID uint32) error {
	sc.mu.Lock()
	if !sc.connected {
		sc.mu.Unlock()
		return fmt.Errorf("not connected to phone")
	}

	stream, ok := sc.streams[name]
	if !ok {
		sc.mu.Unlock()
		return fmt.Errorf("unknown stream: %s", name)
	}

	if stream.Running && stream.Active {
		sc.mu.Unlock()
		return nil
	}

	// Wait for any previous stopping worker
	if stream.Running || stream.done != nil {
		cancel := stream.cancel
		done := stream.done
		conn := sc.conn
		stream.cancel = nil
		stream.gen++
		waitGen := stream.gen

		if cancel != nil {
			cancel()
		}
		if conn != nil {
			conn.CloseStream(streamID)
		}
		sc.mu.Unlock()
		if done != nil {
			<-done
		}
		sc.mu.Lock()
		if !sc.connected {
			sc.mu.Unlock()
			return fmt.Errorf("not connected to phone")
		}
		stream, ok = sc.streams[name]
		if !ok {
			sc.mu.Unlock()
			return fmt.Errorf("unknown stream: %s", name)
		}
		if sc.conn != conn || stream.gen != waitGen {
			sc.mu.Unlock()
			return fmt.Errorf("%s start aborted: session changed", name)
		}
		stream.done = nil
		stream.Running = false
	}

	conn := sc.conn
	if conn == nil {
		sc.mu.Unlock()
		return fmt.Errorf("not connected to phone")
	}
	host := sc.host
	port := sc.port
	// A pending dial has no worker/done channel yet, but still owns a
	// cancellation context. Release it before replacing that ownership.
	if stream.cancel != nil {
		stream.cancel()
	}
	stream.gen++
	gen := stream.gen

	// Create context for dial & worker lifecycle so stopStream or Disconnect promptly aborts in-flight dial/backoff
	ctx, cancel := context.WithCancel(context.Background())
	stream.cancel = cancel
	// NOTE: stream.Active remains false while dialing.

	// Screen always opens a fresh TCP session per Scrcpy-like lifecycle
	needsDial := (name == "screen") || !conn.HasStream(streamID)
	if name == "screen" {
		conn.CloseStream(streamID)
	}
	sc.mu.Unlock()

	var newSocket net.Conn
	if needsDial {
		var dialErr error
		newSocket, dialErr = transport.ConnectStreamPortContext(ctx, host, uint16(port), streamID)
		if dialErr != nil {
			cancel()
			sc.mu.Lock()
			if sc.conn == conn && stream.gen == gen {
				stream.Active = false
				stream.Running = false
				stream.cancel = nil
			}
			sc.mu.Unlock()
			if sc.webServer != nil {
				sc.webServer.publishCanonicalStatus()
			}
			return fmt.Errorf("%s connect failed: %w", name, dialErr)
		}
	}

	sc.mu.Lock()
	// Generation / state ownership check before install: check context & gen
	if !sc.connected || sc.conn != conn || stream.gen != gen || ctx.Err() != nil {
		cancel()
		if newSocket != nil {
			newSocket.Close()
		}
		if stream.gen == gen {
			stream.Active = false
			stream.Running = false
			stream.cancel = nil
		}
		sc.mu.Unlock()
		return fmt.Errorf("%s start aborted: session changed", name)
	}

	if newSocket != nil {
		if err := conn.InstallStream(streamID, newSocket); err != nil {
			cancel()
			newSocket.Close()
			stream.Active = false
			stream.Running = false
			stream.cancel = nil
			sc.mu.Unlock()
			if sc.webServer != nil {
				sc.webServer.publishCanonicalStatus()
			}
			return err
		}
	}

	stream.Running = true
	stream.Active = true
	done := make(chan struct{})
	stream.done = done

	switch name {
	case "speaker":
		sc.launchSpeakerWorker(ctx, cancel, conn, stream, gen, done)
	case "camera":
		sc.launchCameraWorker(ctx, cancel, conn, stream, gen, done)
	case "mic":
		sc.launchMicWorker(ctx, cancel, conn, stream, gen, done)
	case "screen":
		sc.launchScreenWorker(ctx, cancel, conn, stream, gen, done, host, port)
	}

	sc.mu.Unlock()

	if sc.webServer != nil {
		sc.webServer.publishCanonicalStatus()
	}
	return nil
}

func (sc *ServerController) launchSpeakerWorkerLocked(conn *transport.Connection) {
	stream := sc.streams["speaker"]
	if conn == nil || !conn.HasStream(protocol.StreamSpeaker) {
		stream.Active = false
		stream.Running = false
		return
	}

	stream.Running = true
	stream.Active = true
	stream.gen++
	gen := stream.gen

	ctx, cancel := context.WithCancel(context.Background())
	stream.cancel = cancel
	done := make(chan struct{})
	stream.done = done

	sc.launchSpeakerWorker(ctx, cancel, conn, stream, gen, done)
}

func (sc *ServerController) launchSpeakerWorker(ctx context.Context, cancel context.CancelFunc, conn *transport.Connection, stream *StreamController, gen int, done chan struct{}) {
	onMetrics := func(fps, bitrate, latency int, bytes, frames int64) {
		sc.UpdateStreamMetrics("speaker", gen, conn, fps, bitrate, latency, bytes, frames)
	}
	writer := conn.SpeakerWriter()
	go func() {
		defer close(done)
		defer cancel()
		if err := runSpeakerCapture(ctx, writer, onMetrics); err != nil {
			if err == context.Canceled {
				slog.Info("Speaker: stopped by toggle")
			} else {
				slog.Error("Speaker capture error", "error", err)
			}
		}

		sc.mu.Lock()
		wasRunning := stream.Running && stream.gen == gen && sc.conn == conn
		if wasRunning {
			conn.CloseStream(protocol.StreamSpeaker)
			stream.Running = false
			stream.Active = false
		}
		sc.mu.Unlock()

		if wasRunning && sc.webServer != nil {
			sc.webServer.publishCanonicalStatus()
		}
	}()
}

func (sc *ServerController) launchMicWorker(ctx context.Context, cancel context.CancelFunc, conn *transport.Connection, stream *StreamController, gen int, done chan struct{}) {
	onMetrics := func(fps, bitrate, latency int, bytes, frames int64) {
		sc.UpdateStreamMetrics("mic", gen, conn, fps, bitrate, latency, bytes, frames)
	}
	reader := conn.MicStream()
	go func() {
		defer close(done)
		defer cancel()
		if err := runMicCapture(ctx, reader, onMetrics); err != nil {
			switch err {
			case context.Canceled:
				slog.Info("Mic: stopped by toggle")
			case io.EOF:
				slog.Info("Mic: stream ended cleanly")
			default:
				slog.Error("Mic capture error", "error", err)
			}
		}

		sc.mu.Lock()
		wasRunning := stream.Running && stream.gen == gen && sc.conn == conn
		if wasRunning {
			conn.CloseStream(protocol.StreamMic)
			stream.Running = false
			stream.Active = false
		}
		sc.mu.Unlock()

		if wasRunning && sc.webServer != nil {
			sc.webServer.publishCanonicalStatus()
		}
	}()
}

func (sc *ServerController) launchCameraWorker(ctx context.Context, cancel context.CancelFunc, conn *transport.Connection, stream *StreamController, gen int, done chan struct{}) {
	reader := conn.CameraStream()
	go func() {
		defer close(done)
		defer cancel()
		if err := runCameraCapture(ctx, reader); err != nil {
			switch err {
			case context.Canceled:
				slog.Info("Camera: stopped by toggle")
			case io.EOF:
				slog.Info("Camera: stream ended cleanly")
			default:
				slog.Error("Camera capture error", "error", err)
			}
		}

		sc.mu.Lock()
		wasRunning := stream.Running && stream.gen == gen && sc.conn == conn
		if wasRunning {
			conn.CloseStream(protocol.StreamCamera)
			stream.Running = false
			stream.Active = false
		}
		sc.mu.Unlock()

		if wasRunning && sc.webServer != nil {
			sc.webServer.publishCanonicalStatus()
		}
	}()
}

func (sc *ServerController) launchScreenWorker(ctx context.Context, cancel context.CancelFunc, conn *transport.Connection, stream *StreamController, gen int, done chan struct{}, host string, port int) {
	hub := sc.webServer.Hub()
	hubGeneration := hub.BeginScreenSession()
	stream.hubGen = hubGeneration
	videoConn := conn.VideoConn()
	sc.prepareScreenControlReconnect(conn, gen, hubGeneration)
	go sc.reconnectScreenControl(ctx, conn, host, port, gen, hubGeneration, hub)

	go func() {
		defer close(done)
		defer cancel()
		if err := runScreenCapture(ctx, videoConn, hub, hubGeneration,
			func(fps, bitrate, latency int, bytes, frames int64) {
				sc.UpdateStreamMetrics("screen", gen, conn, fps, bitrate, latency, bytes, frames)
			},
		); err != nil {
			switch err {
			case context.Canceled:
				slog.Info("Screen: stopped by toggle")
			case io.EOF:
				slog.Info("Screen: stream ended cleanly")
			default:
				slog.Error("Screen capture error", "error", err)
			}
		}
		if err := hub.EndScreenSession(hubGeneration); err != nil {
			slog.Debug("Failed to restore display after screen stop", "error", err)
		}
		sc.closeScreenControl(conn, gen, hubGeneration)
		conn.CloseStream(protocol.StreamScreen)

		sc.mu.Lock()
		wasRunning := stream.Running && stream.gen == gen && stream.hubGen == hubGeneration && sc.conn == conn
		if wasRunning {
			stream.Running = false
			stream.Active = false
			stream.hubGen = 0
		}
		sc.mu.Unlock()

		if wasRunning && sc.webServer != nil {
			sc.webServer.publishCanonicalStatus()
		}
	}()
}

func (sc *ServerController) prepareScreenControlReconnect(conn *transport.Connection, screenGeneration int, hubGeneration uint64) {
	sc.controlMu.Lock()
	sc.controlConnection = conn
	sc.controlScreenGeneration = screenGeneration
	sc.controlHubGeneration = hubGeneration
	sc.controlMu.Unlock()
}

func (sc *ServerController) reconnectScreenControl(ctx context.Context, conn *transport.Connection, host string, port, screenGeneration int, hubGeneration uint64, hub *Hub) {
	slog.Info("Control: opening fresh TCP session (best-effort)")
	sc.controlMu.Lock()
	if sc.controlConnection != conn || sc.controlScreenGeneration != screenGeneration || sc.controlHubGeneration != hubGeneration {
		sc.controlMu.Unlock()
		return
	}
	conn.CloseStream(protocol.StreamControl)
	sc.controlMu.Unlock()

	controlSocket, err := transport.ConnectStreamPortContext(ctx, host, uint16(port), protocol.StreamControl)
	if err != nil {
		slog.Warn("Control: reconnect skipped", "error", err)
		return
	}

	sc.controlMu.Lock()
	if sc.controlConnection != conn || sc.controlScreenGeneration != screenGeneration || sc.controlHubGeneration != hubGeneration || ctx.Err() != nil {
		controlSocket.Close()
		sc.controlMu.Unlock()
		return
	}
	if err := conn.InstallStream(protocol.StreamControl, controlSocket); err != nil {
		controlSocket.Close()
		sc.controlMu.Unlock()
		return
	}
	controlConn := conn.ControlConn()
	sc.controlMu.Unlock()

	sc.mu.RLock()
	stream := sc.streams["screen"]
	stillCurrent := sc.connected && sc.conn == conn && stream.gen == screenGeneration && stream.hubGen == hubGeneration && stream.Running
	sc.mu.RUnlock()
	if !stillCurrent {
		sc.closeControlConn(conn, controlConn)
		return
	}

	route, err := hub.RegisterControlRoute(hubGeneration, func(data []byte) error {
		if len(data) == 0 {
			return nil
		}
		if err := controlConn.SetWriteDeadline(time.Now().Add(controlWriteTimeout)); err != nil {
			return err
		}
		defer controlConn.SetWriteDeadline(time.Time{})
		_, err := controlConn.Write(data)
		return err
	})
	if err != nil {
		slog.Debug("Control route setup error", "error", err)
	}
	if route == nil {
		sc.closeControlConn(conn, controlConn)
	}
}

func (sc *ServerController) closeScreenControl(conn *transport.Connection, screenGeneration int, hubGeneration uint64) {
	sc.controlMu.Lock()
	if sc.controlConnection == conn && sc.controlScreenGeneration == screenGeneration && sc.controlHubGeneration == hubGeneration {
		sc.controlConnection = nil
		sc.controlScreenGeneration = 0
		sc.controlHubGeneration = 0
		conn.CloseControlConn(conn.ControlConn())
	}
	sc.controlMu.Unlock()
}

func (sc *ServerController) closeControlConn(conn *transport.Connection, controlConn net.Conn) {
	sc.controlMu.Lock()
	conn.CloseControlConn(controlConn)
	sc.controlMu.Unlock()
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

// UpdateStreamMetrics updates metrics for a specific stream in the controller.
// It is guarded by generation and connection ownership.
func (sc *ServerController) UpdateStreamMetrics(name string, gen int, conn *transport.Connection, fps, bitrate, latency int, bytesSent, frames int64) {
	sc.mu.Lock()
	stream, ok := sc.streams[name]
	if !ok || sc.conn != conn || stream.gen != gen || !stream.Running {
		sc.mu.Unlock()
		return
	}
	stream.FPS = fps
	stream.Bitrate = bitrate
	stream.Latency = latency
	stream.Bytes = bytesSent
	stream.Frames = frames
	sc.mu.Unlock()

	if sc.webServer != nil {
		sc.webServer.publishCanonicalStatus()
	}
}

// GetStreamStatus returns the authoritative status of all streams in fixed order.
func (sc *ServerController) GetStreamStatus() []StreamStatus {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	names := []string{"screen", "camera", "mic", "speaker"}
	statuses := make([]StreamStatus, len(names))
	for i, name := range names {
		if s, ok := sc.streams[name]; ok {
			statuses[i] = StreamStatus{
				Name:      s.Name,
				Port:      s.Port,
				Active:    s.Active,
				FPS:       s.FPS,
				Bitrate:   s.Bitrate,
				Latency:   s.Latency,
				BytesSent: s.Bytes,
				Frames:    s.Frames,
			}
		}
	}
	return statuses
}

// runSpeakerCapture captures audio from PC and sends to phone.
//
// The pipeline now captures raw PCM and encodes to Opus in Go, avoiding the
// Ogg mux/demux latency and giving direct control over frame size and bitrate.
func runSpeakerCapture(ctx context.Context, w io.Writer, onMetrics func(fps, bitrate, latency int, bytes, frames int64)) error {
	const (
		sampleRate = 48000
		channels   = 2
		bitrate    = 96000
		frameMs    = 20
		// queueFrames caps sender-side buffering between the capture/encode
		// loop and the TCP send goroutine. When the phone or Wi-Fi stalls the
		// write, the queue fills and the capture loop drops the oldest encoded
		// frame instead of blocking. This caps sender latency at
		// queueFrames*frameMs and prevents PulseAudio capture backlog from
		// accumulating (the latency-drifts-up-until-toggle symptom).
		queueFrames = 5 // 5 * 20ms = 100ms max sender buffer
	)
	frameSamples := sampleRate * frameMs / 1000 * channels // int16 samples per frame
	pcmBytes := frameSamples * 2                           // s16le

	pipeline, err := audio.SpeakerPCMCapturePipeline()
	if err != nil {
		return fmt.Errorf("failed to start capture pipeline: %w", err)
	}
	defer pipeline.Close()

	enc, err := audio.NewOpusEncoder(sampleRate, channels, bitrate)
	if err != nil {
		pipeline.Close()
		return fmt.Errorf("failed to create opus encoder: %w", err)
	}
	defer enc.Close()

	// Send OpusHead and OpusTags config packets (same format the Android decoder expects)
	opusHead := audio.OpusHeadPacket(sampleRate, channels, 0)
	if err := audio.WritePacket(w, protocol.StreamSpeaker, protocol.FlagConfig, 0, opusHead); err != nil {
		return fmt.Errorf("failed to send OpusHead: %w", err)
	}
	slog.Info("Speaker: sent OpusHead config", "bytes", len(opusHead))

	opusTags := audio.OpusTagsPacket("AllRelay")
	if err := audio.WritePacket(w, protocol.StreamSpeaker, protocol.FlagConfig, 0, opusTags); err != nil {
		return fmt.Errorf("failed to send OpusTags: %w", err)
	}
	slog.Debug("Speaker: sent OpusTags", "bytes", len(opusTags))

	// Monitor context cancellation — kill pipeline to unblock reads.
	go func() {
		<-ctx.Done()
		slog.Info("Speaker: context cancelled, closing pipeline")
		pipeline.Close()
	}()

	const (
		sendSlowThreshold        = 20 * time.Millisecond  // send-side tracking bucket
		loopSlowThreshold        = 20 * time.Millisecond  // loop-side tracking bucket
		sendStallNoticeThreshold = 100 * time.Millisecond // log INFO when a send stall this large is absorbed
	)

	// Decouple capture/encode from TCP send via a bounded, drop-oldest queue.
	// The send goroutine blocks on WritePacket when the phone/Wi-Fi stalls;
	// the capture loop never blocks on send, so PulseAudio capture never
	// accumulates a stale backlog. When the queue is full the oldest encoded
	// frame is discarded, capping sender latency at queueFrames*frameMs.
	type opusPacket struct {
		pts  uint64
		data []byte
	}
	queue := make(chan opusPacket, queueFrames)

	var sendMaxMs atomic.Int64 // milliseconds
	var slowSendCount atomic.Uint64
	var droppedFrames atomic.Uint64
	var sentFrames atomic.Uint64
	var sentBytes atomic.Uint64
	var lastSendLatencyUs atomic.Int64

	// sendFatal is closed by the send goroutine on an unrecoverable write
	// error so the capture loop stops feeding a dead connection.
	sendFatal := make(chan struct{})

	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		for pkt := range queue {
			sendStart := time.Now()
			if err := audio.WritePacket(w, protocol.StreamSpeaker, 0, pkt.pts, pkt.data); err != nil {
				slog.Error("Speaker: write error", "error", err)
				close(sendFatal)
				// Drain remaining queued frames then exit.
				for range queue {
				}
				return
			}
			sendDur := time.Since(sendStart)
			lastSendLatencyUs.Store(sendDur.Microseconds())
			if ms := sendDur.Milliseconds(); ms > sendMaxMs.Load() {
				sendMaxMs.Store(ms)
			}
			if sendDur >= sendSlowThreshold {
				slowSendCount.Add(1)
			}
			sentFrames.Add(1)
			sentBytes.Add(uint64(len(pkt.data)))
		}
	}()

	// If the send goroutine dies (TCP write error), stop capture too so we
	// don't keep feeding a dead connection.
	go func() {
		<-sendFatal
		slog.Info("Speaker: send goroutine failed, closing capture pipeline")
		pipeline.Close()
	}()

	// Capture + encode loop.
	pcmBuf := make([]int16, frameSamples)
	rawBuf := make([]byte, pcmBytes)
	var frameCount uint64
	var lastMetricsTime = time.Now()
	var framesSinceMetrics uint64
	var bytesSinceMetrics uint64

	// Loop-side timing (read+encode+enqueue). Send-side timing is tracked
	// atomically by the send goroutine above.
	var loopMaxMs time.Duration
	var loopCount uint64
	var slowLoopCount uint64
	iterStart := time.Now()

	var loopErr error
	for {
		_, err := io.ReadFull(pipeline, rawBuf)
		if err != nil {
			if ctx.Err() != nil {
				slog.Info("Speaker: stopped by context", "frames", frameCount, "dropped", droppedFrames.Load())
				loopErr = ctx.Err()
				break
			}
			if err == io.EOF {
				slog.Info("Speaker: capture pipeline ended", "frames", frameCount)
			} else {
				slog.Error("Speaker: read error", "error", err)
			}
			loopErr = err
			break
		}

		// Convert s16le bytes to int16 samples (native endian is little on x86/ARM)
		for i := 0; i < frameSamples; i++ {
			pcmBuf[i] = int16(rawBuf[i*2]) | int16(rawBuf[i*2+1])<<8
		}

		packet, err := enc.Encode(pcmBuf)
		if err != nil {
			slog.Error("Speaker: encode error", "error", err)
			loopErr = err
			break
		}

		frameCount++
		pts := frameCount * uint64(frameMs*1000) // microseconds
		pkt := opusPacket{pts: pts, data: packet}

		// Drop-oldest enqueue: if the send queue is full (TCP write stalled),
		// discard the oldest queued frame to make room. This bounds sender
		// latency instead of letting it drift upward.
		select {
		case queue <- pkt:
		default:
			select {
			case <-queue:
				droppedFrames.Add(1)
				queue <- pkt
			default:
				// Extremely unlikely: queue drained between checks; try once more.
				select {
				case queue <- pkt:
				default:
					droppedFrames.Add(1)
				}
			}
		}

		bytesSinceMetrics += uint64(len(packet))
		framesSinceMetrics++

		loopDur := time.Since(iterStart)
		loopCount++
		if loopDur > loopMaxMs {
			loopMaxMs = loopDur
		}
		if loopDur >= loopSlowThreshold {
			slowLoopCount++
		}

		// Update metrics every second.
		if time.Since(lastMetricsTime) >= time.Second {
			metricsElapsed := time.Since(lastMetricsTime)
			fps := int(float64(framesSinceMetrics) / metricsElapsed.Seconds())
			bitrate := int(float64(bytesSinceMetrics*8) / metricsElapsed.Seconds())
			sentF := sentFrames.Load()
			sentB := sentBytes.Load()
			latencyMs := int(lastSendLatencyUs.Load()/1000) + frameMs + 60
			if onMetrics != nil {
				onMetrics(fps, bitrate, latencyMs, int64(sentB), int64(sentF))
			}

			// Backlog diagnostics: report per-loop + send timing once per second.
			// frameMs=20ms is the realtime budget per capture iteration.
			backlogMs := 0
			if loopMaxMs > time.Duration(frameMs)*time.Millisecond {
				backlogMs = int(loopMaxMs/(time.Millisecond) - frameMs)
			}
			sendMax := sendMaxMs.Load()
			slowSend := slowSendCount.Load()
			dropped := droppedFrames.Swap(0)
			slog.Debug("Speaker timing",
				"loop_max_ms", loopMaxMs.Milliseconds(),
				"send_max_ms", sendMax,
				"backlog_ms", backlogMs,
				"slow_send", slowSend,
				"slow_loop", slowLoopCount,
				"dropped", dropped,
				"iters", loopCount)
			// Only warn when the capture loop genuinely fell behind realtime
			// (backlog > 0): that is the condition the drop-oldest queue cannot
			// fully absorb and which could still affect latency. TCP send stalls
			// that the queue already absorbed via drops are expected behavior
			// and only warrant a heavy-stall notice at sendStallNoticeThreshold.
			if backlogMs > 0 {
				slog.Warn("Speaker: capture loop fell behind realtime",
					"loop_max_ms", loopMaxMs.Milliseconds(),
					"backlog_ms", backlogMs,
					"slow_loop", slowLoopCount)
			} else if sendMax >= sendStallNoticeThreshold.Milliseconds() {
				slog.Info("Speaker: TCP send stall absorbed by drop-oldest queue",
					"send_max_ms", sendMax,
					"slow_send", slowSend,
					"dropped", dropped)
			}
			sendMaxMs.Store(0)
			slowSendCount.Store(0)
			loopMaxMs = 0
			slowLoopCount = 0

			framesSinceMetrics = 0
			bytesSinceMetrics = 0
			lastMetricsTime = time.Now()
		}

		if frameCount%250 == 0 {
			slog.Debug("Speaker stream",
				"frames", frameCount,
				"sent", sentFrames.Load(),
				"dropped", droppedFrames.Load(),
				"packet_bytes", len(packet))
		}

		iterStart = time.Now()
	}

	// Stop the send goroutine and wait for it to finish so deferred Close()
	// calls don't race with in-flight writes.
	close(queue)
	<-sendDone
	return loopErr
}

type opusConfig struct {
	SampleRate int
	Channels   int
	PreSkip    int
}

func parseOpusHead(payload []byte) (*opusConfig, error) {
	if len(payload) < 19 || string(payload[:8]) != "OpusHead" {
		return nil, fmt.Errorf("invalid OpusHead payload (%d bytes)", len(payload))
	}
	channels := int(payload[9])
	if channels <= 0 {
		return nil, fmt.Errorf("invalid Opus channels: %d", channels)
	}
	sampleRate := int(binary.LittleEndian.Uint32(payload[12:16]))
	if sampleRate == 0 {
		sampleRate = 48000
	}
	return &opusConfig{
		SampleRate: sampleRate,
		Channels:   channels,
		PreSkip:    int(binary.LittleEndian.Uint16(payload[10:12])),
	}, nil
}

// runMicCapture reads Opus mic packets from the phone, decodes them to PCM,
// and feeds a PulseAudio/pipewire-pulse virtual microphone source.
//
// Unlike camera, mic must stay active even during silence or delayed first audio.
// So we do NOT apply an idle read timeout here. The stream ends only on:
//   - explicit toggle OFF (ctx cancel → close TCP to unblock reader)
//   - real TCP disconnect / EOF
//   - fatal decoder/output error
func runMicCapture(ctx context.Context, reader io.Reader, onMetrics func(fps, bitrate, latency int, bytes, frames int64)) error {
	var micConn net.Conn
	if conn, ok := reader.(net.Conn); ok {
		micConn = conn
	}

	var (
		decoder         *audio.OpusDecoder
		output          *audio.VirtualMicWriter
		pcm             []int16
		pcmBytes        []byte
		frameCount      uint64
		byteCount       uint64
		framesWindow    uint64
		bytesWindow     uint64
		lastMetricsTime = time.Now()
		started         bool
	)
	defer func() {
		if decoder != nil {
			decoder.Close()
		}
		if output != nil {
			_ = output.Close()
		}
	}()

	demuxer := protocol.NewDemuxer(reader)
	fatalErrCh := make(chan error, 1)
	demuxer.RegisterHandler(protocol.StreamMic, func(header *protocol.Header, payload []byte) error {
		if len(payload) == 0 {
			return nil
		}

		// Android sends an initial codec identifier packet ("opus"). Ignore it.
		if len(payload) == 4 && string(payload) == "opus" {
			slog.Info("Mic: codec", "name", string(payload))
			return nil
		}

		if header.IsConfig() {
			cfg, err := parseOpusHead(payload)
			if err != nil {
				select {
				case fatalErrCh <- fmt.Errorf("parse OpusHead: %w", err):
				default:
				}
				demuxer.Stop()
				return nil
			}

			if decoder != nil {
				decoder.Close()
			}
			decoder, err = audio.NewOpusDecoder(cfg.SampleRate, cfg.Channels)
			if err != nil {
				select {
				case fatalErrCh <- fmt.Errorf("create opus decoder: %w", err):
				default:
				}
				demuxer.Stop()
				return nil
			}

			if output != nil {
				_ = output.Close()
			}
			output, err = audio.StartVirtualMicWriter(cfg.SampleRate, cfg.Channels)
			if err != nil {
				select {
				case fatalErrCh <- fmt.Errorf("start virtual mic: %w", err):
				default:
				}
				demuxer.Stop()
				return nil
			}

			pcm = make([]int16, 5760*cfg.Channels)
			pcmBytes = make([]byte, len(pcm)*2)
			slog.Info("Mic: configured", "rate", cfg.SampleRate, "channels", cfg.Channels, "preskip", cfg.PreSkip)
			return nil
		}

		if decoder == nil || output == nil {
			slog.Debug("Mic: dropping packet before config", "bytes", len(payload))
			return nil
		}

		samplesPerChannel, err := decoder.Decode(payload, pcm)
		if err != nil {
			select {
			case fatalErrCh <- fmt.Errorf("decode opus: %w", err):
			default:
			}
			demuxer.Stop()
			return nil
		}

		totalSamples := samplesPerChannel * decoder.Channels()
		for i, sample := range pcm[:totalSamples] {
			binary.LittleEndian.PutUint16(pcmBytes[i*2:], uint16(sample))
		}
		if _, err := output.Write(pcmBytes[:totalSamples*2]); err != nil {
			select {
			case fatalErrCh <- fmt.Errorf("write virtual mic: %w", err):
			default:
			}
			demuxer.Stop()
			return nil
		}

		if !started {
			slog.Info("Mic: received first frame", "bytes", len(payload), "samples", samplesPerChannel)
			started = true
		}

		frameCount++
		byteCount += uint64(len(payload))
		framesWindow++
		bytesWindow += uint64(len(payload))

		if time.Since(lastMetricsTime) >= time.Second {
			fps := int(float64(framesWindow) / time.Since(lastMetricsTime).Seconds())
			bitrate := int(float64(bytesWindow*8) / time.Since(lastMetricsTime).Seconds())
			if onMetrics != nil {
				onMetrics(fps, bitrate, 40, int64(byteCount), int64(frameCount))
			}
			framesWindow = 0
			bytesWindow = 0
			lastMetricsTime = time.Now()
		}

		if frameCount%250 == 0 {
			slog.Debug("Mic stream", "frames", frameCount, "total_bytes", byteCount, "packet_bytes", len(payload))
		}

		return nil
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- demuxer.Run()
	}()

	// Explicitly close TCP on toggle OFF so a blocked demux read wakes up immediately.
	if micConn != nil {
		go func() {
			<-ctx.Done()
			slog.Info("Mic: context cancelled, closing TCP reader")
			_ = micConn.Close()
		}()
	}

	select {
	case <-ctx.Done():
		slog.Info("Mic: context cancelled, stopping")
		demuxer.Stop()
		return ctx.Err()
	case err := <-fatalErrCh:
		slog.Info("Mic: fatal handler error", "frames", frameCount, "bytes", byteCount, "error", err)
		return err
	case err := <-errCh:
		slog.Info("Mic: demuxer ended", "frames", frameCount, "bytes", byteCount, "error", err)
		return err
	}
}

// runCameraCapture reads H.264 camera packets from the phone and feeds
// them to an FFmpeg pipeline that decodes and writes to a v4l2loopback device (/dev/video10).
//
// Android camera daemon sends AllRelay protocol packets (16-byte header + payload).
// We use the demuxer to strip headers and forward only H.264 NAL units to FFmpeg.
type readDeadlineReader struct {
	conn    net.Conn
	timeout time.Duration
}

func (r *readDeadlineReader) Read(p []byte) (int, error) {
	if err := r.conn.SetReadDeadline(time.Now().Add(r.timeout)); err != nil {
		return 0, err
	}
	n, err := r.conn.Read(p)
	if err == nil {
		_ = r.conn.SetReadDeadline(time.Time{})
	}
	return n, err
}

func isNetTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func runCameraCapture(ctx context.Context, reader io.Reader) error {
	var camConn net.Conn
	if conn, ok := reader.(net.Conn); ok {
		camConn = conn
	}

	// Explicitly close TCP on toggle OFF/cancel so a blocked preamble or demux read wakes up immediately.
	if camConn != nil {
		go func() {
			<-ctx.Done()
			slog.Info("Camera: context cancelled, closing TCP reader")
			_ = camConn.Close()
		}()
	}

	device := video.GetCameraDevice()

	// Do NOT apply an idle read timeout for the camera (same policy as screen).
	// The Android H.264 encoder can legitimately pause for several seconds
	// (GOP buffering, GC pause, Wi-Fi power-save). A 5s deadline kept
	// tearing down healthy streams — the so-called "camera hay bị ngắt ngang"
	// symptom. The stream ends on real conditions: context cancellation, a
	// natural demuxer EOF, or a TCP RST/FIN (socket closed on either side).

	// Ensure v4l2loopback device exists (module should be loaded at boot)
	if err := video.EnsureV4L2Device(device); err != nil {
		slog.Warn("Camera: v4l2 device check failed", "error", err)
	}

	// Set output format BEFORE opening pipeline.
	// Required for exclusive_caps=1 v4l2loopback: device initially
	// reports CAPTURE-only; setting output format triggers OUTPUT mode switch.
	if err := video.SetupV4L2Output(device, 640, 480, "YUYV"); err != nil {
		slog.Warn("Camera: v4l2 format setup failed, pipeline may still work", "error", err)
	}

	slog.Info("Camera: opening v4l2 pipeline", "device", device)

	pipeline, err := video.CameraPipeline(device)
	if err != nil {
		return fmt.Errorf("camera pipeline: %w", err)
	}
	defer pipeline.Close()

	pipelineErrCh := make(chan error, 1)
	go func() {
		if err := <-pipeline.Done(); err != nil {
			pipelineErrCh <- err
		}
		close(pipelineErrCh)
	}()

	var frameCount uint64
	var byteCount uint64
	var started bool

	// Debug: peek first bytes to diagnose protocol issues
	peek := make([]byte, 32)
	n, peekErr := io.ReadFull(reader, peek)
	if peekErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("camera read preamble: %w", peekErr)
	}
	slog.Info("Camera: preamble bytes", "n", n, "hex", fmt.Sprintf("%x", peek[:n]))

	// Reconstruct reader with peek bytes prepended
	combinedReader := io.MultiReader(bytes.NewReader(peek[:n]), reader)

	demuxer := protocol.NewDemuxer(combinedReader)
	fatalErrCh := make(chan error, 1)
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

		// Write H.264 NAL units to ffmpeg pipeline.
		// If ffmpeg exits unexpectedly, stop the demuxer so the stream fully tears down.
		if _, err := pipeline.Write(payload); err != nil {
			select {
			case fatalErrCh <- fmt.Errorf("pipeline write: %w", err):
			default:
			}
			demuxer.Stop()
			return nil
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
		if camConn != nil {
			_ = camConn.Close()
		}
		return ctx.Err()
	case err := <-pipelineErrCh:
		if err != nil {
			slog.Info("Camera: pipeline ended", "frames", frameCount, "bytes", byteCount, "error", err)
			demuxer.Stop()
			return err
		}
		return nil
	case err := <-fatalErrCh:
		slog.Info("Camera: fatal handler error", "frames", frameCount, "bytes", byteCount, "error", err)
		return err
	case err := <-errCh:
		if isNetTimeout(err) {
			slog.Info("Camera: stream idle timeout", "frames", frameCount, "bytes", byteCount)
			return io.EOF
		}
		slog.Info("Camera: demuxer ended", "frames", frameCount, "bytes", byteCount, "error", err)
		return err
	}
}

// runScreenCapture reads H.264 NAL units from the TCP reader via the demuxer
// and broadcasts them as binary WebSocket messages to all connected clients.
func runScreenCapture(ctx context.Context, reader io.Reader, hub *Hub, generation uint64,
	onMetrics func(fps, bitrate, latency int, bytes, frames int64)) error {

	// Do NOT apply an idle read timeout for screen mirroring.
	// A static phone screen may legitimately produce no new H.264 packets for
	// several seconds, and timing out here kills an otherwise healthy session.
	// Instead, explicitly close the TCP reader on toggle OFF/disconnect so any
	// blocking demux read wakes up immediately.
	if conn, ok := reader.(net.Conn); ok {
		go func() {
			<-ctx.Done()
			slog.Info("Screen: context cancelled, closing TCP reader")
			_ = conn.Close()
		}()
	}

	// Debug: peek first bytes
	peek := make([]byte, 32)
	n, peekErr := io.ReadFull(reader, peek)
	slog.Info("Screen: preamble bytes", "n", n, "err", peekErr, "hex", fmt.Sprintf("%x", peek[:n]))

	combinedReader := io.MultiReader(bytes.NewReader(peek[:n]), reader)

	demuxer := protocol.NewDemuxer(combinedReader)
	errCh := make(chan error, 1)
	go func() {
		errCh <- demuxer.Run()
	}()

	var frameCount uint64
	var byteCount uint64
	var started bool
	startTime := time.Now()
	lastMetricsTime := startTime
	lastFrameCount := uint64(0)
	lastByteCount := uint64(0)

	demuxer.RegisterHandler(protocol.StreamScreen, func(header *protocol.Header, payload []byte) error {
		// Session meta (size/orientation change) has zero payload.
		// Forward it to the browser so the popup can resize/reset the decoder
		// before frames for the new orientation arrive.
		if len(payload) == 0 {
			if header.SessionWidth > 0 && header.SessionHeight > 0 {
				hub.SetScreenSession(generation, header.SessionWidth, header.SessionHeight)
			}
			return nil
		}

		// Only forward Annex B H.264 NAL units
		if !video.IsAnnexB(payload) {
			return nil
		}

		// Broadcast H.264 access unit to WebSocket clients.
		// Prefix 1 byte of flags so the browser knows whether this packet is
		// codec config and/or a key frame.
		flags := byte(0)
		if header.IsConfig() {
			flags |= 1 << 0
		}
		if header.IsKeyFrame() {
			flags |= 1 << 1
		}
		msg := make([]byte, 1+len(payload))
		msg[0] = flags
		copy(msg[1:], payload)
		hub.BroadcastScreenFrame(generation, msg)

		frameCount++
		byteCount += uint64(len(payload))

		if !started {
			slog.Info("Screen: received first frame", "bytes", len(payload))
			started = true
		}

		// Periodic metrics
		now := time.Now()
		if now.Sub(lastMetricsTime) >= time.Second {
			elapsed := now.Sub(lastMetricsTime).Seconds()
			fps := int(float64(frameCount-lastFrameCount) / elapsed)
			bitrate := int(float64(byteCount-lastByteCount) * 8 / elapsed)
			latency := 0 // screen latency metric not measured correctly yet

			slog.Debug("Screen stream",
				"frames", frameCount,
				"total_bytes", byteCount,
				"fps", fps,
				"bitrate", bitrate)

			onMetrics(fps, bitrate, latency, int64(byteCount), int64(frameCount))

			lastMetricsTime = now
			lastFrameCount = frameCount
			lastByteCount = byteCount
		}

		return nil
	})

	// Wait for stream to end
	select {
	case <-ctx.Done():
		demuxer.Stop()
		return ctx.Err()
	case err := <-errCh:
		if isNetTimeout(err) {
			slog.Info("Screen: stream idle timeout", "frames", frameCount, "bytes", byteCount)
			return io.EOF
		}
		slog.Info("Screen: demuxer ended", "frames", frameCount, "bytes", byteCount, "error", err)
		return err
	}
}
