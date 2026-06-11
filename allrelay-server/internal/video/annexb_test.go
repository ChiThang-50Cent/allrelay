package video

import (
	"bytes"
	"testing"
)

func TestIsAnnexB(t *testing.T) {
	tests := []struct {
		name  string
		data  []byte
		expect bool
	}{
		{"4-byte start code", []byte{0x00, 0x00, 0x00, 0x01, 0x67}, true},
		{"3-byte start code", []byte{0x00, 0x00, 0x01, 0x68}, true},
		{"no start code", []byte{0x67, 0x42, 0x00, 0x01}, false},
		{"partial match", []byte{0x00, 0x00, 0x02, 0x01}, false},
		{"too short", []byte{0x00, 0x00}, false},
		{"empty", []byte{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAnnexB(tt.data); got != tt.expect {
				t.Errorf("IsAnnexB(%x) = %v, want %v", tt.data, got, tt.expect)
			}
		})
	}
}

func TestToAnnexB(t *testing.T) {
	// Already Annex B -> returns same data
	annexB := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00}
	result := ToAnnexB(annexB)
	if !bytes.Equal(result, annexB) {
		t.Errorf("ToAnnexB on annexB data should return same slice")
	}

	// Raw NAL -> prepends 4-byte start code
	raw := []byte{0x67, 0x42, 0x00}
	result = ToAnnexB(raw)
	expected := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00}
	if !bytes.Equal(result, expected) {
		t.Errorf("ToAnnexB on raw NAL = %x, want %x", result, expected)
	}
}

func TestSplitNALs_AnnexB(t *testing.T) {
	// SPS + PPS in Annex B format
	data := []byte{
		0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x0a, // SPS
		0x00, 0x00, 0x00, 0x01, 0x68, 0xce, 0x38, 0x80, // PPS
	}

	nals := SplitNALs(data)
	if len(nals) != 2 {
		t.Fatalf("expected 2 NALs, got %d", len(nals))
	}

	// First should include 4-byte start code + SPS
	if !bytes.HasPrefix(nals[0], annexBStartCode4) {
		t.Errorf("NAL 0 missing start code: %x", nals[0])
	}

	// Second should include 4-byte start code + PPS
	if !bytes.HasPrefix(nals[1], annexBStartCode4) {
		t.Errorf("NAL 1 missing start code: %x", nals[1])
	}
}

func TestSplitNALs_AVC(t *testing.T) {
	// SPS + PPS in AVC format (4-byte length prefix)
	data := []byte{
		0x00, 0x00, 0x00, 0x04, // length = 4
		0x67, 0x42, 0x00, 0x0a, // SPS
		0x00, 0x00, 0x00, 0x04, // length = 4
		0x68, 0xce, 0x38, 0x80, // PPS
	}

	nals := SplitNALs(data)
	if len(nals) != 2 {
		t.Fatalf("expected 2 NALs, got %d", len(nals))
	}

	if !bytes.Equal(nals[0], []byte{0x67, 0x42, 0x00, 0x0a}) {
		t.Errorf("NAL 0 = %x", nals[0])
	}
	if !bytes.Equal(nals[1], []byte{0x68, 0xce, 0x38, 0x80}) {
		t.Errorf("NAL 1 = %x", nals[1])
	}
}

func TestConfigToAnnexB_AlreadyAnnexB(t *testing.T) {
	config := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42}
	result := ConfigToAnnexB(config)
	if !bytes.Equal(result, config) {
		t.Errorf("already Annex B should be unchanged")
	}
}

func TestConfigToAnnexB_AVCFormat(t *testing.T) {
	// AVC format: 4-byte length + SPS + 4-byte length + PPS
	config := []byte{
		0x00, 0x00, 0x00, 0x04,
		0x67, 0x42, 0x00, 0x0a,
		0x00, 0x00, 0x00, 0x04,
		0x68, 0xce, 0x38, 0x80,
	}

	result := ConfigToAnnexB(config)
	// Should convert to:
	// 00 00 00 01 67 42 00 0a 00 00 00 01 68 ce 38 80
	expected := []byte{
		0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x0a,
		0x00, 0x00, 0x00, 0x01, 0x68, 0xce, 0x38, 0x80,
	}
	if !bytes.Equal(result, expected) {
		t.Errorf("ConfigToAnnexB AVC:\n got  %x\n want %x", result, expected)
	}
}

func TestConfigToAnnexB_RawNAL(t *testing.T) {
	// Single raw NAL (no start code, no length prefix)
	config := []byte{0x67, 0x42, 0x00, 0x0a}

	result := ConfigToAnnexB(config)
	expected := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x0a}
	if !bytes.Equal(result, expected) {
		t.Errorf("ConfigToAnnexB raw: got %x, want %x", result, expected)
	}
}
