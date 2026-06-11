// Package control implements the TCP control channel for communicating
// with the Android AllRelay server on port 5004.
//
// The control channel uses a JSON-based protocol for:
//   - Stream toggle (enable/disable streams)
//   - Input injection (touch, keyboard, clipboard)
//   - Configuration changes (resolution, bitrate, camera selection)
//   - Status queries
//   - Clipboard sync
//
// Message format follows the SPEC.md §5.5 Control Messages specification.
package control

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Message types sent over the control channel.
const (
	TypeHello        = "hello"
	TypeHelloAck     = "hello_ack"
	TypeToggle       = "toggle"
	TypeConfig       = "config"
	TypeTouch        = "touch"
	TypeKey          = "key"
	TypeClipboard    = "clipboard"
	TypeVolume       = "volume"
	TypeStatusReq    = "status_request"
	TypeStatus       = "status"
	TypeIFrameReq    = "iframe_request"
	TypePing         = "ping"
	TypePong         = "pong"
)

// Stream IDs for toggle/config messages.
const (
	StreamMonitor = iota
	StreamCamera
	StreamMicrophone
	StreamSpeaker
)

// Message represents a JSON control message exchanged over TCP.
type Message struct {
	Type      string `json:"type"`
	StreamID  int    `json:"stream_id,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
	Action    string `json:"action,omitempty"`    // touch: down, move, up; key: down, up
	PointerID int    `json:"pointer_id,omitempty"`
	X         float64 `json:"x,omitempty"`
	Y         float64 `json:"y,omitempty"`
	Pressure  float64 `json:"pressure,omitempty"`
	Keycode   int    `json:"keycode,omitempty"`
	MetaState int    `json:"meta_state,omitempty"`
	Repeat    int    `json:"repeat,omitempty"`
	Text      string `json:"text,omitempty"`       // clipboard
	Stream    string `json:"stream,omitempty"`     // volume: media, ring, alarm
	Level     int    `json:"level,omitempty"`      // volume level 0-100
	Resolution string `json:"resolution,omitempty"` // config: "1920x1080"
	FPS       int    `json:"fps,omitempty"`         // config
	Bitrate   int    `json:"bitrate,omitempty"`     // config: bps
}

// Channel manages the TCP control connection to the Android server.
type Channel struct {
	conn   net.Conn
	enc    *json.Encoder
	dec    *json.Decoder
	mu     sync.Mutex
	recvCh chan Message
	done   chan struct{}
}

// NewChannel creates a new control channel over an existing TCP connection.
// The conn should already be established to the Android server on port 5004.
func NewChannel(conn net.Conn) *Channel {
	ch := &Channel{
		conn:   conn,
		enc:    json.NewEncoder(conn),
		dec:    json.NewDecoder(conn),
		recvCh: make(chan Message, 64),
		done:   make(chan struct{}),
	}

	// Start receiver goroutine
	go ch.receive()

	return ch
}

// receive reads JSON messages from the connection in a background goroutine.
func (ch *Channel) receive() {
	defer close(ch.recvCh)

	for {
		select {
		case <-ch.done:
			return
		default:
		}

		var msg Message
		if err := ch.dec.Decode(&msg); err != nil {
			if err != io.EOF {
				slog.Error("control channel read error", "error", err)
			}
			return
		}

		select {
		case ch.recvCh <- msg:
		default:
			slog.Warn("control channel receive buffer full, dropping message")
		}
	}
}

// Send transmits a JSON message to the Android server.
func (ch *Channel) Send(msg Message) error {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	if ch.conn == nil {
		return fmt.Errorf("control channel not connected")
	}

	// Set deadline to avoid blocking forever
	ch.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))

	if err := ch.enc.Encode(msg); err != nil {
		return fmt.Errorf("send control message: %w", err)
	}

	slog.Debug("control message sent", "type", msg.Type, "stream_id", msg.StreamID)
	return nil
}

// Receive returns the channel for receiving incoming messages from Android.
func (ch *Channel) Receive() <-chan Message {
	return ch.recvCh
}

// Close shuts down the control channel.
func (ch *Channel) Close() error {
	select {
	case <-ch.done:
		return nil
	default:
		close(ch.done)
	}

	if ch.conn != nil {
		return ch.conn.Close()
	}
	return nil
}

// Convenience methods for common control operations

// SendKey sends a key event (down or up) to the Android device.
func (ch *Channel) SendKey(action string, keycode int) error {
	return ch.Send(Message{
		Type:    TypeKey,
		Action:  action,
		Keycode: keycode,
	})
}

// SendTouch sends a touch event to the Android device.
func (ch *Channel) SendTouch(action string, x, y float64) error {
	return ch.Send(Message{
		Type:      TypeTouch,
		Action:    action,
		PointerID: 0,
		X:         x,
		Y:         y,
		Pressure:  1.0,
	})
}

// ToggleStream enables or disables a stream on the Android device.
func (ch *Channel) ToggleStream(streamID int, enabled bool) error {
	return ch.Send(Message{
		Type:     TypeToggle,
		StreamID: streamID,
		Enabled:  &enabled,
	})
}

// RequestIFrame requests an I-frame (keyframe) for a stream.
func (ch *Channel) RequestIFrame(streamID int) error {
	return ch.Send(Message{
		Type:     TypeIFrameReq,
		StreamID: streamID,
	})
}

// SendClipboard sends clipboard text to the Android device.
func (ch *Channel) SendClipboard(text string) error {
	return ch.Send(Message{
		Type: TypeClipboard,
		Text: text,
	})
}

// RequestStatus requests device status (battery, Wi-Fi, CPU).
func (ch *Channel) RequestStatus() error {
	return ch.Send(Message{
		Type: TypeStatusReq,
	})
}

// Hello sends the initial handshake message.
func (ch *Channel) Hello(version, deviceName, model string, screenWidth, screenHeight int) error {
	return ch.Send(Message{
		Type: TypeHello,
	})
	// TODO(Phase 4): Send full hello with device info
}

// IsConnected returns true if the control channel is active.
func (ch *Channel) IsConnected() bool {
	select {
	case <-ch.done:
		return false
	default:
		return true
	}
}
