// Package protocol implements the AllRelay multi-stream packet format.
//
// Each packet has a 16-byte header followed by a variable-length payload:
//
//	Offset  Size  Field
//	0       4     stream_id (big-endian uint32)
//	4       8     pts_and_flags (big-endian uint64)
//	12      4     payload_size (big-endian uint32)
//
// Flag bits in pts_and_flags:
//
//	Bit 63: session packet (not media)
//	Bit 62: config packet (codec configuration)
//	Bit 61: key frame
//	Bits 52-0: PTS value (microseconds)
package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

// HeaderSize is the fixed size of the AllRelay packet header.
const HeaderSize = 16

// Stream IDs as defined in the AllRelay protocol.
const (
	StreamScreen  uint32 = 0x00000001
	StreamCamera  uint32 = 0x00000002
	StreamMic     uint32 = 0x00000003
	StreamSpeaker uint32 = 0x00000004
)

// Flag bits in pts_and_flags.
const (
	FlagSession uint64 = 1 << 63
	FlagConfig  uint64 = 1 << 62
	FlagKeyFrame uint64 = 1 << 61
	PTSMask     uint64 = (1 << 53) - 1 // lower 53 bits
)

// StreamName returns a human-readable name for a stream ID.
func StreamName(id uint32) string {
	switch id {
	case StreamScreen:
		return "screen"
	case StreamCamera:
		return "camera"
	case StreamMic:
		return "mic"
	case StreamSpeaker:
		return "speaker"
	default:
		return fmt.Sprintf("unknown(0x%08x)", id)
	}
}

// Header represents a parsed 16-byte AllRelay packet header.
type Header struct {
	StreamID    uint32 // stream identifier
	PTSAndFlags uint64 // raw pts_and_flags field
	PayloadSize uint32 // payload size in bytes
}

// PTS extracts the presentation timestamp in microseconds.
func (h *Header) PTS() uint64 {
	return h.PTSAndFlags & PTSMask
}

// IsSession returns true if this is a session (resolution change) packet.
func (h *Header) IsSession() bool {
	return h.PTSAndFlags&FlagSession != 0
}

// IsConfig returns true if this is a codec config packet.
func (h *Header) IsConfig() bool {
	return h.PTSAndFlags&FlagConfig != 0
}

// IsKeyFrame returns true if this packet contains a key frame.
func (h *Header) IsKeyFrame() bool {
	return h.PTSAndFlags&FlagKeyFrame != 0
}

// MediaType returns a string describing the packet type.
func (h *Header) MediaType() string {
	if h.IsSession() {
		return "session"
	}
	if h.IsConfig() {
		return "config"
	}
	if h.IsKeyFrame() {
		return "keyframe"
	}
	return "media"
}

// ReadHeader reads and parses a 16-byte AllRelay header from the reader.
// Returns io.EOF if the stream ends cleanly.
func ReadHeader(r io.Reader) (*Header, error) {
	buf := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, buf); err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("read header: %w", err)
	}

	return &Header{
		StreamID:    binary.BigEndian.Uint32(buf[0:4]),
		PTSAndFlags: binary.BigEndian.Uint64(buf[4:12]),
		PayloadSize: binary.BigEndian.Uint32(buf[12:16]),
	}, nil
}

// ReadPayload reads exactly payloadSize bytes from the reader.
func ReadPayload(r io.Reader, size uint32) ([]byte, error) {
	if size == 0 {
		return nil, nil
	}
	if size > 16*1024*1024 { // 16 MB sanity limit
		return nil, fmt.Errorf("payload too large: %d bytes", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}
	return buf, nil
}
