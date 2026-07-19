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
	FPS     int
	Bitrate int
	Latency int
	Bytes   int64
	Frames  int64
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

	// Only open the speaker TCP stream at connect time (PC → phone audio is
	// the default thing the user wants right away). Camera, mic, screen and
	// control are opened lazily via ReconnectStream when the UI toggles them
	// ON, so we don't pay for streams the user never uses.
	conn, err := transport.Connect(host, uint16(port),
		false, // video (screen)
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
			streams[i].FPS = ctrl.FPS
			streams[i].Bitrate = ctrl.Bitrate
			streams[i].Latency = ctrl.Latency
			streams[i].BytesSent = ctrl.Bytes
			streams[i].Frames = ctrl.Frames
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
			go sc.startMicStreamAsync()
		case "screen":
			sc.startScreenStreamLocked()
		}
	} else if !active && stream.Running {
		// Release lock while waiting for goroutine to stop
		// (avoids deadlock: goroutine needs mu to cleanup)
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
		// Re-read stream state (may have changed during unlock)
		stream, ok = sc.streams[name]
		if ok {
			stream.Running = false
		}
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
			if err == context.Canceled {
				slog.Info("Speaker: stopped by toggle")
			} else {
				slog.Error("Speaker capture error", "error", err)
			}
		}
		sc.mu.Lock()
		if stream.Running && stream.gen == gen {
			stream.Running = false
			stream.Active = false
		}
		sc.mu.Unlock()
	}()
}

func (sc *ServerController) startMicStreamAsync() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	stream, ok := sc.streams["mic"]
	if !ok || !stream.Active || stream.Running {
		return
	}
	sc.startMicStreamLocked()
}

// startMicStreamLocked starts the mic stream (phone → PC virtual microphone)
func (sc *ServerController) startMicStreamLocked() {
	stream := sc.streams["mic"]
	if sc.conn == nil {
		slog.Warn("Mic stream not available (no connection)")
		stream.Active = false
		return
	}
	if !sc.conn.HasStream(protocol.StreamMic) {
		slog.Info("Mic: reconnecting TCP")
		if err := sc.conn.ReconnectStream(sc.host, uint16(sc.port), protocol.StreamMic); err != nil {
			slog.Error("Mic: reconnect failed", "error", err)
			stream.Active = false
			return
		}
	}

	stream.Running = false
	stream.Active = false

	ctx, cancel := context.WithCancel(context.Background())
	stream.cancel = cancel

	slog.Info("Starting mic stream")
	stream.Active = true
	stream.Running = true
	stream.gen++
	gen := stream.gen

	onMetrics := func(fps, bitrate, latency int, bytes, frames int64) {
		if sc.webServer != nil {
			sc.webServer.UpdateStreamMetrics("mic", fps, bitrate, latency, bytes, frames)
		}
	}

	stream.done = make(chan struct{})
	go func() {
		defer close(stream.done)
		reader := sc.conn.MicStream()
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
		if stream.Running && stream.gen == gen {
			stream.Running = false
			stream.Active = false
		}
		sc.conn.CloseStream(protocol.StreamMic)
		sc.mu.Unlock()
	}()
}

// startCameraStream starts the camera stream (phone camera → PC → v4l2loopback)
func (sc *ServerController) startCameraStream() {
	sc.startCameraStreamLocked()
}

// startCameraStreamLocked starts the camera stream (must hold lock)
func (sc *ServerController) startCameraStreamLocked() {
	if sc.conn == nil {
		slog.Warn("Camera stream not available (no connection)")
		return
	}

	// Reconnect camera TCP if the old connection was closed by a previous OFF.
	// This ensures a clean session with a fresh TCP reader — the old demuxer
	// goroutine was unblocked and should have exited after the TCP close.
	if !sc.conn.HasStream(protocol.StreamCamera) {
		slog.Info("Camera: reconnecting TCP")
		if err := sc.conn.ReconnectStream(sc.host, uint16(sc.port), protocol.StreamCamera); err != nil {
			slog.Error("Camera: reconnect failed", "error", err)
			return
		}
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
		if stream.Running && stream.gen == gen {
			stream.Running = false
			stream.Active = false
		}
		// Close camera TCP to force-unblock any orphaned demuxer goroutine
		// that may still be blocked on ReadHeader. Without this, the TCP
		// connection stays ESTAB and the old goroutine competes with the
		// new one on the next camera ON.
		sc.conn.CloseStream(protocol.StreamCamera)
		sc.mu.Unlock()
	}()
}

// startScreenStreamLocked starts the screen stream (must hold lock)
func (sc *ServerController) startScreenStreamLocked() {
	if sc.conn == nil {
		slog.Warn("Screen stream not available (no connection)")
		return
	}

	// Scrcpy-like lifecycle: every Screen ON starts a fresh TCP session.
	// Never reuse old screen/control sockets across toggles.
	hub := sc.webServer.Hub()
	hub.ClearScreenReplay()
	sc.conn.CloseStream(protocol.StreamScreen)
	sc.conn.CloseStream(protocol.StreamControl)

	slog.Info("Screen: opening fresh TCP session")
	if err := sc.conn.ReconnectStream(sc.host, uint16(sc.port), protocol.StreamScreen); err != nil {
		slog.Error("Screen: reconnect failed", "error", err)
		return
	}

	stream := sc.streams["screen"]
	stream.Running = false
	stream.Active = false

	ctx, cancel := context.WithCancel(context.Background())
	stream.cancel = cancel

	slog.Info("Starting screen stream")
	stream.Active = true
	stream.Running = true
	stream.gen++
	gen := stream.gen
	videoConn := sc.conn.VideoConn()

	// Wire control forwarding best-effort. Control must never block screen.
	hub.SetControlHandler(nil)
	setControlForwarding := func(controlConn net.Conn) {
		if controlConn == nil {
			return
		}
		hub.SetControlHandler(func(data []byte) {
			if len(data) == 0 {
				return
			}
			if _, err := controlConn.Write(data); err != nil {
				slog.Debug("Control write error", "error", err)
			}
		})
	}
	go func(expectedGen int) {
		slog.Info("Control: opening fresh TCP session (best-effort)")
		sc.conn.CloseStream(protocol.StreamControl)
		if err := sc.conn.ReconnectStream(sc.host, uint16(sc.port), protocol.StreamControl); err != nil {
			slog.Warn("Control: reconnect skipped", "error", err)
			return
		}
		sc.mu.RLock()
		stillCurrent := sc.streams["screen"].gen == expectedGen && sc.streams["screen"].Running
		sc.mu.RUnlock()
		if !stillCurrent {
			sc.conn.CloseStream(protocol.StreamControl)
			return
		}
		setControlForwarding(sc.conn.ControlConn())
	}(gen)

	stream.done = make(chan struct{})
	go func() {
		defer close(stream.done)
		defer func() {
			// Clear control forwarding and stale decoder data when screen stops.
			hub.SetControlHandler(nil)
			hub.ClearScreenReplay()
		}()
		if err := runScreenCapture(ctx, videoConn, hub,
			func(fps, bitrate, latency int, bytes, frames int64) {
				sc.mu.Lock()
				if s, ok := sc.streams["screen"]; ok && s.gen == gen {
					s.FPS = fps
					s.Bitrate = bitrate
					s.Latency = latency
					s.Bytes = bytes
					s.Frames = frames
				}
				sc.mu.Unlock()
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
		sc.mu.Lock()
		if stream.Running && stream.gen == gen {
			stream.Running = false
			stream.Active = false
		}
		sc.conn.CloseStream(protocol.StreamScreen)
		sc.conn.CloseStream(protocol.StreamControl)
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

// runSpeakerCapture captures audio from PC and sends to phone.
//
// The pipeline now captures raw PCM and encodes to Opus in Go, avoiding the
// Ogg mux/demux latency and giving direct control over frame size and bitrate.
func runSpeakerCapture(ctx context.Context, w io.Writer, onMetrics func(fps, bitrate, latency int, bytes, frames int64)) error {
	const (
		sampleRate = 48000
		channels   = 2
		bitrate    = 96000
		frameMs    = 5
		// queueFrames caps sender-side buffering between the capture/encode
		// loop and the TCP send goroutine. When the phone or Wi-Fi stalls the
		// write, the queue fills and the capture loop drops the oldest encoded
		// frame instead of blocking. This caps sender latency at
		// queueFrames*frameMs and prevents PulseAudio capture backlog from
		// accumulating (the latency-drifts-up-until-toggle symptom).
		queueFrames = 4 // 4 * 5ms = 20ms max sender buffer
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
		sendSlowThreshold        = 20 * time.Millisecond // send-side tracking bucket
		loopSlowThreshold        = 20 * time.Millisecond // loop-side tracking bucket
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

	var sendMaxMs atomic.Int64    // milliseconds
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
			latencyMs := int(lastSendLatencyUs.Load()/1000) + 5 + 60
			if onMetrics != nil {
				onMetrics(fps, bitrate, latencyMs, int64(sentB), int64(sentF))
			}

			// Backlog diagnostics: report per-loop + send timing once per second.
			// frameMs=5ms is the realtime budget per capture iteration.
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
	slog.Info("Camera: preamble bytes", "n", n, "err", peekErr, "hex", fmt.Sprintf("%x", peek[:n]))

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
func runScreenCapture(ctx context.Context, reader io.Reader, hub *Hub,
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
				hub.SetScreenSession(header.SessionWidth, header.SessionHeight)
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
		hub.BroadcastScreenFrame(msg)

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
