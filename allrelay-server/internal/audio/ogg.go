package audio

import (
	"encoding/binary"
	"fmt"
	"io"
)

// oggPageHeaderSize is the fixed-size portion of the Ogg page header (27 bytes).
const oggPageHeaderSize = 27

// OggDemuxer reads Ogg pages from an io.Reader and extracts the raw packets.
//
// Usage:
//
//	demux := NewOggDemuxer(reader)
//	for {
//	    packet, err := demux.NextPacket()
//	    if err == io.EOF { break }
//	    // process packet (raw Opus data)
//	}
type OggDemuxer struct {
	r       io.Reader
	packets [][]byte // queue of extracted packets pending delivery
}

// NewOggDemuxer creates a demuxer that reads Ogg pages from r.
func NewOggDemuxer(r io.Reader) *OggDemuxer {
	return &OggDemuxer{r: r}
}

// NextPacket returns the next raw packet from the Ogg stream.
// Returns io.EOF when the stream ends.
func (d *OggDemuxer) NextPacket() ([]byte, error) {
	// Return from existing queue first
	if len(d.packets) > 0 {
		pkt := d.packets[0]
		d.packets = d.packets[1:]
		return pkt, nil
	}

	// Read next Ogg page
	_, packets, err := d.ReadPage()
	if err != nil {
		return nil, err
	}

	if len(packets) == 0 {
		return nil, io.EOF
	}

	// Queue all but the first, return the first
	d.packets = packets[1:]
	return packets[0], nil
}

// ReadPage reads one complete Ogg page and returns both the raw page bytes
// and the extracted packets. This is useful when you need the raw Ogg data
// (e.g., for feeding to a decoder that expects Ogg-wrapped input).
//
// Returns: rawPage (complete Ogg page bytes), packets (extracted), error
func (d *OggDemuxer) ReadPage() ([]byte, [][]byte, error) {
	return d.readPage()
}

// readPage reads one Ogg page and returns both the raw page bytes
// and the extracted packets.
func (d *OggDemuxer) readPage() ([]byte, [][]byte, error) {
	// Read page header
	header := make([]byte, oggPageHeaderSize)
	if _, err := io.ReadFull(d.r, header); err != nil {
		return nil, nil, err
	}

	// Verify sync pattern "OggS"
	if string(header[0:4]) != "OggS" {
		return nil, nil, fmt.Errorf("ogg sync lost: expected OggS, got %02x", header[0:4])
	}

	numSegments := int(header[26])

	// Read segment table
	segTable := make([]byte, numSegments)
	if numSegments > 0 {
		if _, err := io.ReadFull(d.r, segTable); err != nil {
			return nil, nil, fmt.Errorf("ogg segment table: %w", err)
		}
	}

	// Calculate total data length
	totalDataLen := 0
	for _, seg := range segTable {
		totalDataLen += int(seg)
	}

	// Read all packet data
	packetData := make([]byte, totalDataLen)
	if totalDataLen > 0 {
		if _, err := io.ReadFull(d.r, packetData); err != nil {
			return nil, nil, fmt.Errorf("ogg packet data: %w", err)
		}
	}

	// Build raw page bytes: header + segment table + packet data
	rawPage := make([]byte, 0, oggPageHeaderSize+numSegments+totalDataLen)
	rawPage = append(rawPage, header...)
	rawPage = append(rawPage, segTable...)
	rawPage = append(rawPage, packetData...)

	// Split data into packets based on segment table.
	// A packet ends when a segment < 255 is encountered.
	var packets [][]byte
	packetStart := 0
	packetLen := 0
	for _, seg := range segTable {
		packetLen += int(seg)
		if seg < 255 {
			// Packet complete
			pkt := make([]byte, packetLen)
			copy(pkt, packetData[packetStart:packetStart+packetLen])
			packets = append(packets, pkt)
			packetStart += packetLen
			packetLen = 0
		}
	}

	return rawPage, packets, nil
}

// WritePacket writes an AllRelay-wrapped packet: 16-byte header + payload.
//
// Header format:
//
//	Offset  Size  Field
//	0       4     stream_id (big-endian uint32)
//	4       8     pts_and_flags (big-endian uint64)
//	12      4     payload_size (big-endian uint32)
func WritePacket(w io.Writer, streamID uint32, flags uint64, pts uint64, payload []byte) error {
	header := make([]byte, 16)
	binary.BigEndian.PutUint32(header[0:4], streamID)
	binary.BigEndian.PutUint64(header[4:12], pts|flags)
	binary.BigEndian.PutUint32(header[12:16], uint32(len(payload)))

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return fmt.Errorf("write payload: %w", err)
		}
	}
	return nil
}
