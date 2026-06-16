package protocol

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func TestReadHeader(t *testing.T) {
	// Build a valid 16-byte header
	var buf bytes.Buffer

	// stream_id = 0x01 (screen)
	binary.Write(&buf, binary.BigEndian, uint32(0x00000001))
	// pts_and_flags: PTS=12345, key_frame flag set
	ptsAndFlags := uint64(12345) | FlagKeyFrame
	binary.Write(&buf, binary.BigEndian, ptsAndFlags)
	// payload_size = 1024
	binary.Write(&buf, binary.BigEndian, uint32(1024))

	header, err := ReadHeader(&buf)
	if err != nil {
		t.Fatalf("ReadHeader failed: %v", err)
	}

	if header.StreamID != 0x00000001 {
		t.Errorf("StreamID = 0x%08x, want 0x00000001", header.StreamID)
	}
	if header.PTS() != 12345 {
		t.Errorf("PTS = %d, want 12345", header.PTS())
	}
	if !header.IsKeyFrame() {
		t.Error("IsKeyFrame = false, want true")
	}
	if header.IsConfig() {
		t.Error("IsConfig = true, want false")
	}
	if header.IsSession() {
		t.Error("IsSession = true, want false")
	}
	if header.PayloadSize != 1024 {
		t.Errorf("PayloadSize = %d, want 1024", header.PayloadSize)
	}
	if header.MediaType() != "keyframe" {
		t.Errorf("MediaType = %s, want keyframe", header.MediaType())
	}
}

func TestReadHeader_Session(t *testing.T) {
	var buf bytes.Buffer
	binary.Write(&buf, binary.BigEndian, uint32(StreamScreen))
	binary.Write(&buf, binary.BigEndian, uint32(FlagSession>>32))
	binary.Write(&buf, binary.BigEndian, uint32(1080))
	binary.Write(&buf, binary.BigEndian, uint32(2400))

	header, err := ReadHeader(&buf)
	if err != nil {
		t.Fatalf("ReadHeader failed: %v", err)
	}
	if !header.IsSession() {
		t.Error("IsSession = false, want true")
	}
	if header.MediaType() != "session" {
		t.Errorf("MediaType = %s, want session", header.MediaType())
	}
	if header.SessionWidth != 1080 || header.SessionHeight != 2400 {
		t.Fatalf("session size = %dx%d, want 1080x2400", header.SessionWidth, header.SessionHeight)
	}
	if header.PayloadSize != 0 {
		t.Fatalf("PayloadSize = %d, want 0 for session packet", header.PayloadSize)
	}
}

func TestReadHeader_Config(t *testing.T) {
	var buf bytes.Buffer
	binary.Write(&buf, binary.BigEndian, uint32(0x01))
	binary.Write(&buf, binary.BigEndian, uint64(FlagConfig))
	binary.Write(&buf, binary.BigEndian, uint32(256))

	header, err := ReadHeader(&buf)
	if err != nil {
		t.Fatalf("ReadHeader failed: %v", err)
	}
	if !header.IsConfig() {
		t.Error("IsConfig = false, want true")
	}
	if header.MediaType() != "config" {
		t.Errorf("MediaType = %s, want config", header.MediaType())
	}
}

func TestReadHeader_AllStreamIDs(t *testing.T) {
	tests := []struct {
		id   uint32
		name string
	}{
		{StreamScreen, "screen"},
		{StreamCamera, "camera"},
		{StreamMic, "mic"},
		{StreamSpeaker, "speaker"},
		{StreamControl, "control"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			binary.Write(&buf, binary.BigEndian, tt.id)
			binary.Write(&buf, binary.BigEndian, uint64(0))
			binary.Write(&buf, binary.BigEndian, uint32(0))

			header, err := ReadHeader(&buf)
			if err != nil {
				t.Fatalf("ReadHeader(%s) failed: %v", tt.name, err)
			}
			if header.StreamID != tt.id {
				t.Errorf("StreamID = 0x%08x, want 0x%08x", header.StreamID, tt.id)
			}
			if StreamName(header.StreamID) != tt.name {
				t.Errorf("StreamName = %s, want %s", StreamName(header.StreamID), tt.name)
			}
		})
	}
}

func TestReadHeader_MediaPacket(t *testing.T) {
	var buf bytes.Buffer
	binary.Write(&buf, binary.BigEndian, uint32(0x02))   // camera
	binary.Write(&buf, binary.BigEndian, uint64(999999)) // plain PTS
	binary.Write(&buf, binary.BigEndian, uint32(5000))

	header, err := ReadHeader(&buf)
	if err != nil {
		t.Fatalf("ReadHeader failed: %v", err)
	}
	if header.PTS() != 999999 {
		t.Errorf("PTS = %d, want 999999", header.PTS())
	}
	if header.MediaType() != "media" {
		t.Errorf("MediaType = %s, want media", header.MediaType())
	}
}

func TestReadHeader_ShortRead(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0x00, 0x00, 0x00}) // only 3 bytes

	_, err := ReadHeader(&buf)
	if err == nil {
		t.Error("Expected error on short read, got nil")
	}
}

func TestReadPayload(t *testing.T) {
	payload := []byte("hello AllRelay payload test data here")
	reader := bytes.NewReader(payload)

	data, err := ReadPayload(reader, uint32(len(payload)))
	if err != nil {
		t.Fatalf("ReadPayload failed: %v", err)
	}
	if string(data) != string(payload) {
		t.Errorf("ReadPayload = %q, want %q", string(data), string(payload))
	}
}

func TestReadPayload_ZeroSize(t *testing.T) {
	reader := bytes.NewReader([]byte{})
	data, err := ReadPayload(reader, 0)
	if err != nil {
		t.Fatalf("ReadPayload(0) failed: %v", err)
	}
	if data != nil {
		t.Errorf("ReadPayload(0) should return nil, got %v", data)
	}
}

func TestReadPayload_TooLarge(t *testing.T) {
	reader := bytes.NewReader(make([]byte, 100))
	_, err := ReadPayload(reader, 17*1024*1024) // > 16MB
	if err == nil {
		t.Error("Expected error for oversized payload, got nil")
	}
}

func TestReadPayload_EOF(t *testing.T) {
	reader := bytes.NewReader([]byte{0x01}) // only 1 byte, but requesting 10
	_, err := ReadPayload(reader, 10)
	if err == nil {
		t.Error("Expected error for insufficient data, got nil")
	}
}

// TestDemuxer tests the multi-stream packet routing
func TestDemuxer(t *testing.T) {
	// Build a buffer with mixed stream packets
	var buf bytes.Buffer

	// Packet 1: screen, keyframe, size 4
	binary.Write(&buf, binary.BigEndian, uint32(0x01))
	binary.Write(&buf, binary.BigEndian, uint64(1000)|FlagKeyFrame)
	binary.Write(&buf, binary.BigEndian, uint32(4))
	buf.Write([]byte("SCRN"))

	// Packet 2: camera, config, size 9
	binary.Write(&buf, binary.BigEndian, uint32(0x02))
	binary.Write(&buf, binary.BigEndian, uint64(FlagConfig))
	binary.Write(&buf, binary.BigEndian, uint32(9))
	buf.Write([]byte("CAMCONFIG"))

	// Packet 3: screen, media, size 6
	binary.Write(&buf, binary.BigEndian, uint32(0x01))
	binary.Write(&buf, binary.BigEndian, uint64(2000))
	binary.Write(&buf, binary.BigEndian, uint32(6))
	buf.Write([]byte("SCREEN"))

	// Packet 4: mic, media, size 5
	binary.Write(&buf, binary.BigEndian, uint32(0x03))
	binary.Write(&buf, binary.BigEndian, uint64(3000))
	binary.Write(&buf, binary.BigEndian, uint32(5))
	buf.Write([]byte("MICRO"))

	// Collect received data per stream
	screenData := []string{}
	cameraData := []string{}
	micData := []string{}

	demuxer := NewDemuxer(&buf)

	demuxer.RegisterHandler(0x01, func(h *Header, payload []byte) error {
		screenData = append(screenData, string(payload))
		return nil
	})
	demuxer.RegisterHandler(0x02, func(h *Header, payload []byte) error {
		cameraData = append(cameraData, string(payload))
		return nil
	})
	demuxer.RegisterHandler(0x03, func(h *Header, payload []byte) error {
		micData = append(micData, string(payload))
		return nil
	})

	err := demuxer.Run()
	if err != nil && err != io.EOF {
		t.Fatalf("Demuxer.Run failed: %v", err)
	}

	if len(screenData) != 2 {
		t.Errorf("screen packets: got %d, want 2", len(screenData))
	}
	if len(cameraData) != 1 {
		t.Errorf("camera packets: got %d, want 1", len(cameraData))
	}
	if len(micData) != 1 {
		t.Errorf("mic packets: got %d, want 1", len(micData))
	}

	if len(screenData) >= 1 && screenData[0] != "SCRN" {
		t.Errorf("screen[0] = %q, want %q", screenData[0], "SCRN")
	}
	if len(screenData) >= 2 && screenData[1] != "SCREEN" {
		t.Errorf("screen[1] = %q, want %q", screenData[1], "SCREEN")
	}
	if len(cameraData) >= 1 && cameraData[0] != "CAMCONFIG" {
		t.Errorf("camera[0] = %q, want %q", cameraData[0], "CAMCONFIG")
	}
	if len(micData) >= 1 && micData[0] != "MICRO" {
		t.Errorf("mic[0] = %q, want %q", micData[0], "MICRO")
	}
}

func TestDemuxer_UnknownStream(t *testing.T) {
	var buf bytes.Buffer
	// Packet with stream ID that has no handler
	binary.Write(&buf, binary.BigEndian, uint32(0xDEAD))
	binary.Write(&buf, binary.BigEndian, uint64(0))
	binary.Write(&buf, binary.BigEndian, uint32(4))
	buf.Write([]byte("DEAD"))

	demuxer := NewDemuxer(&buf)
	// No handler registered for 0xDEAD — should silently drop

	err := demuxer.Run()
	if err != nil && err != io.EOF {
		t.Fatalf("Demuxer.Run with unknown stream failed: %v", err)
	}
	// Should not crash or error
}

func TestDemuxer_Stop(t *testing.T) {
	var buf bytes.Buffer
	// Add one packet so demuxer starts processing
	binary.Write(&buf, binary.BigEndian, uint32(0x01))
	binary.Write(&buf, binary.BigEndian, uint64(0))
	binary.Write(&buf, binary.BigEndian, uint32(4))
	buf.Write([]byte("DATA"))

	demuxer := NewDemuxer(&buf)
	received := false
	demuxer.RegisterHandler(0x01, func(h *Header, payload []byte) error {
		received = true
		demuxer.Stop() // stop after first packet
		return nil
	})

	err := demuxer.Run()
	if err != nil {
		t.Fatalf("Demuxer.Run failed: %v", err)
	}
	if !received {
		t.Error("Handler was not called")
	}
}

func TestHeaderSize(t *testing.T) {
	if HeaderSize != 16 {
		t.Errorf("HeaderSize = %d, want 16", HeaderSize)
	}
}

func TestStreamName_Unknown(t *testing.T) {
	name := StreamName(0xBADF00D)
	if name == "" {
		t.Error("StreamName returned empty for unknown ID")
	}
}
