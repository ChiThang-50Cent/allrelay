package audio

/*
#cgo pkg-config: opus
#include <opus/opus.h>
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"unsafe"
)

type OpusDecoder struct {
	dec      *C.OpusDecoder
	channels int
}

func NewOpusDecoder(sampleRate, channels int) (*OpusDecoder, error) {
	var err C.int
	dec := C.opus_decoder_create(C.opus_int32(sampleRate), C.int(channels), &err)
	if dec == nil || err != C.OPUS_OK {
		return nil, fmt.Errorf("opus_decoder_create failed: %d", int(err))
	}
	return &OpusDecoder{dec: dec, channels: channels}, nil
}

func (d *OpusDecoder) Decode(packet []byte, pcm []int16) (int, error) {
	if d == nil || d.dec == nil {
		return 0, fmt.Errorf("opus decoder closed")
	}
	if len(pcm) == 0 {
		return 0, fmt.Errorf("pcm buffer is empty")
	}

	frameSize := len(pcm) / d.channels
	var dataPtr *C.uchar
	if len(packet) > 0 {
		dataPtr = (*C.uchar)(unsafe.Pointer(&packet[0]))
	}

	n := C.opus_decode(
		d.dec,
		dataPtr,
		C.opus_int32(len(packet)),
		(*C.opus_int16)(unsafe.Pointer(&pcm[0])),
		C.int(frameSize),
		0,
	)
	if n < 0 {
		return 0, fmt.Errorf("opus_decode failed: %d", int(n))
	}
	return int(n), nil
}

func (d *OpusDecoder) Channels() int {
	if d == nil {
		return 0
	}
	return d.channels
}

func (d *OpusDecoder) Close() {
	if d == nil || d.dec == nil {
		return
	}
	C.opus_decoder_destroy(d.dec)
	d.dec = nil
}
