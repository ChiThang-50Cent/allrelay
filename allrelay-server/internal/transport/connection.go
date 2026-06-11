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
	"strings"
	"time"

	"github.com/allrelay/allrelay-server/internal/protocol"
)

const (
	// Default ports
	DefaultBasePort = 5000

	// Connection timeout
	connectTimeout = 5 * time.Second

	// Dummy byte sent by server on connect
	dummyByte = 0xAB

	// Device name field length (matches DEVICE_NAME_FIELD_LENGTH in WifiConnection.java)
	deviceNameLen = 64
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
// basePort is typically 5000.
func Connect(host string, basePort uint16, connectVideo, connectCamera, connectMic, connectSpeaker, connectControl bool) (*Connection, error) {
	if basePort == 0 {
		basePort = DefaultBasePort
	}

	conn := &Connection{host: host}

	var err error

	if connectVideo {
		conn.video, err = connectPort(host, basePort+0, "video")
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("video: %w", err)
		}
	}

	if connectCamera {
		// Camera port may take a moment to open if the server is still
		// initializing. Retry on connection refused.
		conn.camera = connectPortWithRetry(host, basePort+1, "camera", 8)
		if conn.camera == nil {
			slog.Warn("Camera connection failed (optional)")
		}
	}

	if connectMic {
		conn.mic, err = connectPort(host, basePort+2, "mic")
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("mic: %w", err)
		}
	}

	if connectSpeaker {
		conn.speaker, err = connectPort(host, basePort+3, "speaker")
		if err != nil {
			// Speaker is optional
			slog.Warn("Speaker connection failed", "error", err)
		}
	}

	if connectControl {
		conn.control, err = connectPort(host, basePort+4, "control")
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("control: %w", err)
		}
	}

	return conn, nil
}

// connectPortWithRetry attempts to connect to a port with retries on
// connection refused. Returns nil if all attempts fail.
func connectPortWithRetry(host string, port uint16, name string, maxRetries int) net.Conn {
	for i := 0; i < maxRetries; i++ {
		conn, err := connectPort(host, port, name)
		if err == nil {
			return conn
		}
		// Only retry on connection refused
		if !isConnRefused(err) {
			slog.Warn("Camera connection failed", "error", err)
			return nil
		}
		if i < maxRetries-1 {
			slog.Debug("Camera port not ready, retrying...", "attempt", i+1)
			time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
		}
	}
	return nil
}

// isConnRefused checks if the error is a "connection refused" error.
func isConnRefused(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "connectex: No connection could be made")
}

// connectPort connects to a single port, reads the dummy byte and device name,
// and returns the connection.
func connectPort(host string, port uint16, name string) (net.Conn, error) {
	addr := fmt.Sprintf("%s:%d", host, port)

	dialer := net.Dialer{Timeout: connectTimeout}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", name, err)
	}

	// Enable TCP_NODELAY for low latency
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true)
		tcpConn.SetReadBuffer(256 * 1024)
	}

	// Set a deadline for reading the dummy byte.
	// The Android server may accept connections sequentially with timeouts,
	// so a later port (e.g., control) may not send the dummy byte until
	// earlier ports (camera/mic/speaker) time out.
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))

	// Read the dummy byte sent by the server
	dummy := make([]byte, 1)
	if _, err := io.ReadFull(conn, dummy); err != nil {
		conn.Close()
		return nil, fmt.Errorf("read dummy byte (%s): %w", name, err)
	}

	if dummy[0] != dummyByte {
		conn.Close()
		return nil, fmt.Errorf("unexpected dummy byte (%s): 0x%02x", name, dummy[0])
	}

	// The device sends a 64-byte device name on the first connected socket
	// via getFirstSocket(). Detect and consume it before reading AllRelay
	// headers. We peek at the first byte: AllRelay headers always start
	// with 0x00 (stream_id high byte), device names start with ASCII (>0x20).
	firstByte := make([]byte, 1)
	if _, err := io.ReadFull(conn, firstByte); err != nil {
		// No device name present — this is either a test mock or
		// a port that doesn't receive the device name. Return
		// the connection as-is; the caller handles the situation.
		if err == io.EOF {
			return conn, nil
		}
		conn.Close()
		return nil, fmt.Errorf("peek after dummy (%s): %w", name, err)
	}

	if firstByte[0] >= 0x20 {
		// Device name detected — read remaining 63 bytes
		rest := make([]byte, deviceNameLen-1)
		if _, err := io.ReadFull(conn, rest); err != nil {
			conn.Close()
			return nil, fmt.Errorf("read device name (%s): %w", name, err)
		}
		devName := string(append(firstByte, rest...))
		if idx := strings.IndexByte(devName, 0); idx >= 0 {
			devName = devName[:idx]
		}
		slog.Info("Device name", "name", devName, "port", name)
	} else {
		// No device name — this is packet data. We consumed 1 byte.
		// Wrap conn to prepend it for the packet reader.
		return &connWithPrepend{Conn: conn, prepend: firstByte}, nil
	}

	// Clear deadline after successful read
	conn.SetReadDeadline(time.Time{})

	return conn, nil
}

// Close closes all connections.
// Returns combined errors from all connections that failed to close.
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

// VideoStream returns the video (screen) connection reader + stream ID.
func (c *Connection) VideoStream() io.Reader {
	return c.video
}

// CameraStream returns the camera connection reader + stream ID.
func (c *Connection) CameraStream() io.Reader {
	return c.camera
}

// MicStream returns the mic connection reader + stream ID.
func (c *Connection) MicStream() io.Reader {
	return c.mic
}

// SpeakerWriter returns the speaker connection writer (PC → phone).
func (c *Connection) SpeakerWriter() io.Writer {
	return c.speaker
}

// ControlConn returns the control connection for bidirectional communication.
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

// connWithPrepend wraps a net.Conn and prepends bytes to the first read.
// Used when we've peeked at the first byte of a stream and need to
// feed it back to the packet reader.
type connWithPrepend struct {
	net.Conn
	prepend []byte
	prepended bool
}

func (c *connWithPrepend) Read(b []byte) (int, error) {
	if !c.prepended && len(c.prepend) > 0 {
		n := copy(b, c.prepend)
		c.prepend = c.prepend[n:]
		if len(c.prepend) == 0 {
			c.prepended = true
		}
		return n, nil
	}
	return c.Conn.Read(b)
}
