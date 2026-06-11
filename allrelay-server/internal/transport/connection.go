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
		conn.camera, err = connectPort(host, basePort+1, "camera")
		if err != nil {
			// Camera is optional
			slog.Warn("Camera connection failed", "error", err)
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

// connectPort connects to a single port, reads the dummy byte, and returns the connection.
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
	default:
		return false
	}
}
