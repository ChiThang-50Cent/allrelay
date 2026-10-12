// Package transport handles TCP connections to the Android AllRelay server.
//
// The Android server listens on 5 ports:
//
//	base+0 (5000) = video/screen (Android → PC)
//	base+1 (5001) = camera (Android → PC)
//	base+2 (5002) = mic (Android → PC)
//	base+3 (5003) = speaker (PC → Android)
//	base+4 (5004) = control (bidirectional)
//
// Each connection receives a dummy byte (0xAB) from the server
// after establishment, confirming the connection is alive.
package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/allrelay/allrelay-server/internal/protocol"
)

const (
	DefaultBasePort = 5000
	connectTimeout  = 5 * time.Second
	dummyByte       = 0xAB
)

// Connection holds all TCP connections to the Android server.
type Connection struct {
	mu     sync.RWMutex
	host   string
	closed bool

	video   net.Conn
	camera  net.Conn
	mic     net.Conn
	speaker net.Conn
	control net.Conn
}

// Connect establishes TCP connections to the Android server on all requested ports.
// basePort is typically 5000. All connections are made in parallel.
func Connect(host string, basePort uint16, connectVideo, connectCamera, connectMic, connectSpeaker, connectControl bool) (*Connection, error) {
	return ConnectContext(context.Background(), host, basePort, connectVideo, connectCamera, connectMic, connectSpeaker, connectControl)
}

// ConnectContext establishes TCP connections with context cancellation support.
func ConnectContext(ctx context.Context, host string, basePort uint16, connectVideo, connectCamera, connectMic, connectSpeaker, connectControl bool) (*Connection, error) {
	if basePort == 0 {
		basePort = DefaultBasePort
	}

	type result struct {
		name string
		conn net.Conn
		err  error
	}

	var wg sync.WaitGroup
	results := make(chan result, 5)

	launch := func(name string, port uint16) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := connectPortContext(ctx, host, port, name)
			results <- result{name: name, conn: c, err: err}
		}()
	}

	if connectVideo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := connectPortWithRetryContext(ctx, host, basePort+0, "video", 6)
			if err != nil {
				slog.Error("Video connection failed (mandatory)", "error", err)
			}
			results <- result{name: "video", conn: c, err: err}
		}()
	}
	if connectCamera {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := connectPortWithRetryContext(ctx, host, basePort+1, "camera", 8)
			if err != nil {
				slog.Warn("Camera connection failed (optional)", "error", err)
			}
			results <- result{name: "camera", conn: c, err: err}
		}()
	}
	if connectMic {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := connectPortWithRetryContext(ctx, host, basePort+2, "mic", 5)
			if err != nil {
				slog.Warn("Mic connection failed (optional)", "error", err)
			}
			results <- result{name: "mic", conn: c, err: err}
		}()
	}
	if connectSpeaker {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := connectPortWithRetryContext(ctx, host, basePort+3, "speaker", 5)
			if err != nil {
				slog.Warn("Speaker connection failed", "error", err)
			}
			results <- result{name: "speaker", conn: c, err: err}
		}()
	}
	if connectControl {
		launch("control", basePort+4)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	conn := &Connection{host: host}
	var firstErr error
	for r := range results {
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		switch r.name {
		case "video":
			conn.video = r.conn
		case "camera":
			conn.camera = r.conn
		case "mic":
			conn.mic = r.conn
		case "speaker":
			conn.speaker = r.conn
		case "control":
			conn.control = r.conn
		}
	}

	if firstErr != nil {
		conn.Close()
		return nil, firstErr
	}

	return conn, nil
}

// connectPortWithRetry attempts to connect to a port with retries.
// Returns the last error if all attempts fail.
func connectPortWithRetry(host string, port uint16, name string, maxRetries int) (net.Conn, error) {
	return connectPortWithRetryContext(context.Background(), host, port, name, maxRetries)
}

// connectPortWithRetryContext connects with retries and aborts promptly on context cancellation.
func connectPortWithRetryContext(ctx context.Context, host string, port uint16, name string, maxRetries int) (net.Conn, error) {
	var lastErr error
	for i := range maxRetries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		conn, err := connectPortContext(ctx, host, port, name)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if i < maxRetries-1 {
			slog.Debug(name+" port not ready, retrying...", "attempt", i+1, "error", err)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(i+1) * 500 * time.Millisecond):
			}
		}
	}
	slog.Warn(name+" connection failed after retries", "attempts", maxRetries, "error", lastErr)
	if lastErr == nil {
		lastErr = fmt.Errorf("%s connection failed: zero retries", name)
	}
	return nil, lastErr
}

// connectPort connects to a single port, reads the dummy byte, and returns the connection.
func connectPort(host string, port uint16, name string) (net.Conn, error) {
	return connectPortContext(context.Background(), host, port, name)
}

// connectPortContext connects to a single port with context cancellation.
func connectPortContext(ctx context.Context, host string, port uint16, name string) (net.Conn, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(int(port)))

	dialer := net.Dialer{Timeout: connectTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", name, err)
	}

	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true)
		if name == "speaker" {
			tcpConn.SetWriteBuffer(8 * 1024)
		} else {
			tcpConn.SetReadBuffer(256 * 1024)
		}
	}

	stopDone := make(chan struct{})
	defer close(stopDone)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stopDone:
		}
	}()

	conn.SetReadDeadline(time.Now().Add(15 * time.Second))

	dummy := make([]byte, 1)
	if _, err := io.ReadFull(conn, dummy); err != nil {
		conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("read dummy byte (%s): %w", name, err)
	}

	if dummy[0] != dummyByte {
		conn.Close()
		return nil, fmt.Errorf("unexpected dummy byte (%s): 0x%02x", name, dummy[0])
	}

	conn.SetReadDeadline(time.Time{})

	// Only video port receives device name after dummy byte.
	// Other ports return immediately to avoid blocking startup.
	if name != "video" {
		return conn, nil
	}

	return conn, nil
}

// Close closes all connections.
func (c *Connection) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	var conns []net.Conn
	if c.video != nil {
		conns = append(conns, c.video)
		c.video = nil
	}
	if c.camera != nil {
		conns = append(conns, c.camera)
		c.camera = nil
	}
	if c.mic != nil {
		conns = append(conns, c.mic)
		c.mic = nil
	}
	if c.speaker != nil {
		conns = append(conns, c.speaker)
		c.speaker = nil
	}
	if c.control != nil {
		conns = append(conns, c.control)
		c.control = nil
	}
	c.mu.Unlock()

	var errs []error
	for _, conn := range conns {
		if err := conn.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// VideoStream returns the video (screen) connection reader.
func (c *Connection) VideoStream() io.Reader {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.video
}

// VideoConn returns the raw video (screen) TCP connection.
func (c *Connection) VideoConn() net.Conn {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.video
}

// CameraStream returns the camera connection reader.
func (c *Connection) CameraStream() io.Reader {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.camera
}

// MicStream returns the mic connection reader.
func (c *Connection) MicStream() io.Reader {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mic
}

// SpeakerWriter returns the speaker connection writer (PC → phone).
func (c *Connection) SpeakerWriter() io.Writer {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.speaker
}

// ControlConn returns the control connection.
func (c *Connection) ControlConn() net.Conn {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.control
}

// CloseControlConn closes conn and only clears the active control slot when it
// still refers to that exact connection. This lets a stale reconnect clean up
// its own socket without disrupting a newer control route.
func (c *Connection) CloseControlConn(conn net.Conn) {
	if c == nil || conn == nil {
		return
	}
	c.mu.Lock()
	if c.control == conn {
		c.control = nil
	}
	c.mu.Unlock()
	conn.Close()
}

// HasStream returns true if the given stream connection was established.
func (c *Connection) HasStream(id uint32) bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	switch id {
	case protocol.StreamScreen:
		return c.video != nil
	case protocol.StreamCamera:
		return c.camera != nil
	case protocol.StreamMic:
		return c.mic != nil
	case protocol.StreamSpeaker:
		return c.speaker != nil
	case protocol.StreamControl:
		return c.control != nil
	default:
		return false
	}
}

// CloseStream closes and clears a single stream connection.
// This unblocks any goroutine blocked on reads from this stream.
func (c *Connection) CloseStream(id uint32) {
	if c == nil {
		return
	}
	var toClose net.Conn
	c.mu.Lock()
	switch id {
	case protocol.StreamScreen:
		if c.video != nil {
			slog.Info("Closing screen TCP connection")
			toClose = c.video
			c.video = nil
		}
	case protocol.StreamCamera:
		if c.camera != nil {
			slog.Info("Closing camera TCP connection")
			toClose = c.camera
			c.camera = nil
		}
	case protocol.StreamMic:
		if c.mic != nil {
			slog.Info("Closing mic TCP connection")
			toClose = c.mic
			c.mic = nil
		}
	case protocol.StreamSpeaker:
		if c.speaker != nil {
			slog.Info("Closing speaker TCP connection")
			toClose = c.speaker
			c.speaker = nil
		}
	case protocol.StreamControl:
		if c.control != nil {
			slog.Info("Closing control TCP connection")
			toClose = c.control
			c.control = nil
		}
	}
	c.mu.Unlock()
	if toClose != nil {
		toClose.Close()
	}
}

// InstallStream installs a dialed socket into the given stream slot, closing any previous socket.
func (c *Connection) InstallStream(id uint32, streamConn net.Conn) error {
	if c == nil {
		if streamConn != nil {
			streamConn.Close()
		}
		return fmt.Errorf("connection is nil")
	}
	var oldConn net.Conn
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		if streamConn != nil {
			streamConn.Close()
		}
		return fmt.Errorf("connection is closed")
	}
	switch id {
	case protocol.StreamScreen:
		oldConn = c.video
		c.video = streamConn
	case protocol.StreamCamera:
		oldConn = c.camera
		c.camera = streamConn
	case protocol.StreamMic:
		oldConn = c.mic
		c.mic = streamConn
	case protocol.StreamSpeaker:
		oldConn = c.speaker
		c.speaker = streamConn
	case protocol.StreamControl:
		oldConn = c.control
		c.control = streamConn
	default:
		c.mu.Unlock()
		if streamConn != nil {
			streamConn.Close()
		}
		return fmt.Errorf("unknown stream id: %d", id)
	}
	c.mu.Unlock()

	if oldConn != nil && oldConn != streamConn {
		oldConn.Close()
	}
	return nil
}

// ConnectStreamPort connects to a single stream port with retries.
func ConnectStreamPort(host string, basePort uint16, id uint32) (net.Conn, error) {
	return ConnectStreamPortContext(context.Background(), host, basePort, id)
}

// ConnectStreamPortContext connects to a single stream port with context cancellation.
func ConnectStreamPortContext(ctx context.Context, host string, basePort uint16, id uint32) (net.Conn, error) {
	var port uint16
	var name string
	var retries int

	switch id {
	case protocol.StreamScreen:
		port = basePort + 0
		name = "video"
		retries = 8
	case protocol.StreamCamera:
		port = basePort + 1
		name = "camera"
		retries = 8
	case protocol.StreamMic:
		port = basePort + 2
		name = "mic"
		retries = 5
	case protocol.StreamSpeaker:
		port = basePort + 3
		name = "speaker"
		retries = 5
	case protocol.StreamControl:
		port = basePort + 4
		name = "control"
		retries = 8
	default:
		return nil, fmt.Errorf("unknown stream id: %d", id)
	}

	return connectPortWithRetryContext(ctx, host, port, name, retries)
}

// ReconnectStream re-establishes a single stream connection to the phone.
// host is the phone IP, basePort is the base port (e.g. 5000).
// The per-stream port offset is added automatically.
func (c *Connection) ReconnectStream(host string, basePort uint16, id uint32) error {
	return c.ReconnectStreamContext(context.Background(), host, basePort, id)
}

// ReconnectStreamContext re-establishes a single stream connection with context cancellation.
func (c *Connection) ReconnectStreamContext(ctx context.Context, host string, basePort uint16, id uint32) error {
	if c == nil {
		return fmt.Errorf("connection is nil")
	}
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return fmt.Errorf("connection is closed")
	}
	c.mu.RUnlock()

	c.CloseStream(id)

	newConn, err := ConnectStreamPortContext(ctx, host, basePort, id)
	if err != nil {
		return fmt.Errorf("reconnect stream %s failed: %w", protocol.StreamName(id), err)
	}
	return c.InstallStream(id, newConn)
}
