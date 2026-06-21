package audio

/*
#cgo pkg-config: opus
#include <opus/opus.h>
#include <stdlib.h>

static int opus_set_bitrate(OpusEncoder *enc, opus_int32 bitrate) {
    return opus_encoder_ctl(enc, OPUS_SET_BITRATE(bitrate));
}
*/
import "C"

import (
	"encoding/binary"
	"fmt"
	"unsafe"
)

// OpusHeadPacket returns an OpusHead packet suitable for configuring an Opus decoder.
func OpusHeadPacket(sampleRate, channels int, preSkip uint16) []byte {
	head := make([]byte, 19)
	copy(head[0:8], "OpusHead")
	head[8] = 1 // version
	head[9] = byte(channels)
	binary.LittleEndian.PutUint16(head[10:12], preSkip)
	binary.LittleEndian.PutUint32(head[12:16], uint32(sampleRate))
	binary.LittleEndian.PutUint16(head[16:18], 0) // output gain
	head[18] = 0                                  // channel mapping family 0 (mono/stereo)
	return head
}

// OpusTagsPacket returns a minimal OpusTags comment packet.
func OpusTagsPacket(vendor string) []byte {
	vendorBytes := []byte(vendor)
	tags := make([]byte, 8+4+len(vendorBytes)+4)
	offset := 0
	copy(tags[offset:offset+8], "OpusTags")
	offset += 8
	binary.LittleEndian.PutUint32(tags[offset:offset+4], uint32(len(vendorBytes)))
	offset += 4
	copy(tags[offset:offset+len(vendorBytes)], vendorBytes)
	offset += len(vendorBytes)
	binary.LittleEndian.PutUint32(tags[offset:offset+4], 0) // user comment count
	return tags
}

type OpusEncoder struct {
	enc      *C.OpusEncoder
	channels int
}

func NewOpusEncoder(sampleRate, channels, bitrate int) (*OpusEncoder, error) {
	var err C.int
	enc := C.opus_encoder_create(C.opus_int32(sampleRate), C.int(channels), 2051, &err)
	if enc == nil || err != C.OPUS_OK {
		return nil, fmt.Errorf("opus_encoder_create failed: %d", int(err))
	}

	if ret := C.opus_set_bitrate(enc, C.opus_int32(bitrate)); ret != C.OPUS_OK {
		C.opus_encoder_destroy(enc)
		return nil, fmt.Errorf("opus_set_bitrate failed: %d", int(ret))
	}

	return &OpusEncoder{enc: enc, channels: channels}, nil
}

func (e *OpusEncoder) Encode(pcm []int16) ([]byte, error) {
	if e == nil || e.enc == nil {
		return nil, fmt.Errorf("opus encoder closed")
	}
	if len(pcm) == 0 {
		return nil, fmt.Errorf("pcm buffer is empty")
	}

	frameSize := len(pcm) / e.channels
	maxDataBytes := 1275 // Max bytes for a single Opus frame
	out := make([]byte, maxDataBytes)

	var pcmPtr *C.opus_int16
	if len(pcm) > 0 {
		pcmPtr = (*C.opus_int16)(unsafe.Pointer(&pcm[0]))
	}

	n := C.opus_encode(
		e.enc,
		pcmPtr,
		C.int(frameSize),
		(*C.uchar)(unsafe.Pointer(&out[0])),
		C.opus_int32(maxDataBytes),
	)
	if n < 0 {
		return nil, fmt.Errorf("opus_encode failed: %d", int(n))
	}
	return out[:n], nil
}

func (e *OpusEncoder) Close() {
	if e == nil || e.enc == nil {
		return
	}
	C.opus_encoder_destroy(e.enc)
	e.enc = nil
}
