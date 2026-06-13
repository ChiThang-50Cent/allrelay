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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
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
	host string

	video   net.Conn
	camera  net.Conn
	mic     net.Conn
	speaker net.Conn
	control net.Conn
}

// Connect establishes TCP connections to the Android server on all requested ports.
// basePort is typically 5000. All connections are made in parallel.
func Connect(host string, basePort uint16, connectVideo, connectCamera, connectMic, connectSpeaker, connectControl bool) (*Connection, error) {
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
			c, err := connectPort(host, port, name)
			results <- result{name: name, conn: c, err: err}
		}()
	}

	if connectVideo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := connectPortWithRetry(host, basePort+0, "video", 6)
			if c == nil {
				slog.Error("Video connection failed (mandatory)")
			}
			results <- result{name: "video", conn: c, err: nil}
		}()
	}
	if connectCamera {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := connectPortWithRetry(host, basePort+1, "camera", 8)
			if c == nil {
				slog.Warn("Camera connection failed (optional)")
			}
			results <- result{name: "camera", conn: c, err: nil}
		}()
	}
	if connectMic {
		launch("mic", basePort+2)
	}
	if connectSpeaker {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := connectPortWithRetry(host, basePort+3, "speaker", 5)
			if c == nil {
				slog.Warn("Speaker connection failed (optional)")
			}
			results <- result{name: "speaker", conn: c, err: nil}
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

	// Drain unused ports to prevent Android server streams from blocking.
	for name, c := range map[string]net.Conn{
		"video":  conn.video,
		"camera": conn.camera,
		"mic":    conn.mic,
	} {
		if c != nil {
			go func(n string, conn net.Conn) {
				bytes, _ := io.Copy(io.Discard, conn)
				slog.Debug("Drain finished", "port", n, "bytes", bytes)
			}(name, c)
		}
	}

	return conn, nil
}

// connectPortWithRetry attempts to connect to a port with retries.
// Returns nil if all attempts fail.
func connectPortWithRetry(host string, port uint16, name string, maxRetries int) net.Conn {
	for i := 0; i < maxRetries; i++ {
		conn, err := connectPort(host, port, name)
		if err == nil {
			return conn
		}
		if i < maxRetries-1 {
			slog.Debug(name+" port not ready, retrying...", "attempt", i+1, "error", err)
			time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
		}
	}
	slog.Warn(name+" connection failed after retries", "attempts", maxRetries)
	return nil
}

// connectPort connects to a single port, reads the dummy byte,
// and returns the connection.
func connectPort(host string, port uint16, name string) (net.Conn, error) {
	addr := fmt.Sprintf("%s:%d", host, port)

	dialer := net.Dialer{Timeout: connectTimeout}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", name, err)
	}

	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true)
		tcpConn.SetReadBuffer(256 * 1024)
	}

	conn.SetReadDeadline(time.Now().Add(15 * time.Second))

	dummy := make([]byte, 1)
	if _, err := io.ReadFull(conn, dummy); err != nil {
		conn.Close()
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

	// The video connection also receives a 64-byte device name.
	// We don't need it for speaker-only mode, so skip reading it.
	return conn, nil
}

// Close closes all connections.
func (c *Connection) Close() error {
	var errs []error
	if c.video != nil {
		errs = append(errs, c.video.Close())
	}
	if c.camera != nil {
		errs = append(errs, c.camera.Close())
	}
	if c.mic != nil {
		errs = append(errs, c.mic.Close())
	}
	if c.speaker != nil {
		errs = append(errs, c.speaker.Close())
	}
	if c.control != nil {
		errs = append(errs, c.control.Close())
	}
	return errors.Join(errs...)
}

// VideoStream returns the video (screen) connection reader.
func (c *Connection) VideoStream() io.Reader {
	return c.video
}

// CameraStream returns the camera connection reader.
func (c *Connection) CameraStream() io.Reader {
	return c.camera
}

// MicStream returns the mic connection reader.
func (c *Connection) MicStream() io.Reader {
	return c.mic
}

// SpeakerWriter returns the speaker connection writer (PC → phone).
func (c *Connection) SpeakerWriter() io.Writer {
	return c.speaker
}

// ControlConn returns the control connection.
func (c *Connection) ControlConn() net.Conn {
	return c.control
}

// HasStream returns true if the given stream connection was established.
func (c *Connection) HasStream(id uint32) bool {
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
