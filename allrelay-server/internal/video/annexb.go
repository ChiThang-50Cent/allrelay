package video

import (
	"encoding/binary"
)

// annexBStartCode4 is the 4-byte Annex B start code prefix.
var annexBStartCode4 = []byte{0x00, 0x00, 0x00, 0x01}

// annexBStartCode3 is the 3-byte Annex B start code prefix.
var annexBStartCode3 = []byte{0x00, 0x00, 0x01}

// IsAnnexB checks whether data starts with an Annex B start code.
// Returns true if the first bytes are 0x00 0x00 0x01 or 0x00 0x00 0x00 0x01.
func IsAnnexB(data []byte) bool {
	if len(data) < 3 {
		return false
	}
	if data[0] == 0 && data[1] == 0 && data[2] == 1 {
		return true
	}
	if len(data) >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1 {
		return true
	}
	return false
}

// ToAnnexB converts a raw NAL unit to Annex B bytestream format
// by prepending a 4-byte start code (0x00 0x00 0x00 0x01).
//
// If the data already has an Annex B start code, it is returned as-is.
// Otherwise, a new slice is allocated with the start code prepended.
func ToAnnexB(nal []byte) []byte {
	if IsAnnexB(nal) {
		return nal
	}
	out := make([]byte, 4+len(nal))
	copy(out, annexBStartCode4)
	copy(out[4:], nal)
	return out
}

// SplitNALs splits a buffer containing multiple concatenated NAL units
// (typically codec config: SPS + PPS) into individual NAL units.
//
// It looks for NAL start codes in the stream. If found, splits on them.
// If no start codes are found, assumes each NAL is prefixed by a 4-byte
// big-endian length (AVC format).
func SplitNALs(data []byte) [][]byte {
	if len(data) < 4 {
		return [][]byte{data}
	}

	var nals [][]byte
	pos := 0

	// Check if the data starts with an Annex B start code
	if IsAnnexB(data) {
		// Annex B format: split on start codes
		for pos < len(data) {
			start := pos

			// Skip the start code
			if pos+4 <= len(data) &&
				data[pos] == 0 && data[pos+1] == 0 && data[pos+2] == 0 && data[pos+3] == 1 {
				pos += 4
			} else if pos+3 <= len(data) &&
				data[pos] == 0 && data[pos+1] == 0 && data[pos+2] == 1 {
				pos += 3
			}

			// Find next start code
			nextStart := -1
			for i := pos; i < len(data)-3; i++ {
				if data[i] == 0 && data[i+1] == 0 {
					if data[i+2] == 1 {
						nextStart = i
						break
					}
					if i+3 < len(data) && data[i+2] == 0 && data[i+3] == 1 {
						nextStart = i
						break
					}
				}
			}

			var end int
			if nextStart == -1 {
				end = len(data)
			} else {
				end = nextStart
			}

			if end > start {
				nals = append(nals, data[start:end])
			}
			pos = end
		}
	} else {
		// AVC format: split by 4-byte length prefix
		for pos < len(data) {
			if pos+4 > len(data) {
				break
			}
			size := binary.BigEndian.Uint32(data[pos : pos+4])
			pos += 4
			if pos+int(size) > len(data) {
				break
			}
			nals = append(nals, data[pos:pos+int(size)])
			pos += int(size)
		}
	}

	if len(nals) == 0 {
		return [][]byte{data}
	}
	return nals
}

// ConfigToAnnexB converts codec config data (SPS + PPS, typically from
// MediaCodec's csd-0/csd-1 buffers) to Annex B format suitable for
// feeding to a byte-stream decoder like GStreamer's h264parse.
//
// The input is either:
//   - Already Annex B (start codes present) → returned as-is
//   - AVC format (4-byte length prefix before each NAL) → converted to Annex B
//   - Concatenated NALs without start codes → start codes prepended
func ConfigToAnnexB(config []byte) []byte {
	if len(config) == 0 {
		return config
	}

	// Already Annex B? Return as-is
	if IsAnnexB(config) {
		return config
	}

	// Try to split into individual NALs and add start codes
	nals := SplitNALs(config)
	if len(nals) == 1 && &nals[0][0] == &config[0] {
		// Single NAL, not Annex B — just prepend start code
		return ToAnnexB(config)
	}

	// Multiple NALs: add start codes and concatenate
	var totalLen int
	for _, nal := range nals {
		totalLen += 4 + len(nal) // 4-byte start code + NAL
	}

	out := make([]byte, 0, totalLen)
	for _, nal := range nals {
		out = append(out, annexBStartCode4...)
		out = append(out, nal...)
	}
	return out
}
