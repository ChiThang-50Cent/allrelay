package protocol

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

// StreamHandler receives packets for a specific stream.
// The handler is called with the parsed header and payload bytes.
// Return an error to stop processing for this stream.
type StreamHandler func(header *Header, payload []byte) error

// Demuxer reads packets from a single reader and routes them
// to the appropriate handler based on stream_id.
//
// This is the core multi-stream routing component — it reads
// the merged stream from a TCP connection and dispatches each
// packet to the correct handler goroutine.
type Demuxer struct {
	reader   io.Reader
	handlers map[uint32]StreamHandler
	done     chan struct{}
	mu       sync.RWMutex
}

// NewDemuxer creates a new packet demuxer.
func NewDemuxer(reader io.Reader) *Demuxer {
	return &Demuxer{
		reader:   reader,
		handlers: make(map[uint32]StreamHandler),
		done:     make(chan struct{}),
	}
}

// RegisterHandler registers a handler for a specific stream ID.
// Only one handler per stream ID is allowed.
func (d *Demuxer) RegisterHandler(streamID uint32, handler StreamHandler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.handlers[streamID] = handler
}

// Run starts the demux loop. It blocks until an error occurs or Stop() is called.
// Run reads packets in a loop and dispatches each to the appropriate handler
// in a separate goroutine to avoid head-of-line blocking between streams.
func (d *Demuxer) Run() error {
	for {
		select {
		case <-d.done:
			return nil
		default:
		}

		header, err := ReadHeader(d.reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("demux read: %w", err)
		}

		payload, err := ReadPayload(d.reader, header.PayloadSize)
		if err != nil {
			return fmt.Errorf("demux payload (stream=%s): %w",
				StreamName(header.StreamID), err)
		}

		d.mu.RLock()
		handler, ok := d.handlers[header.StreamID]
		d.mu.RUnlock()

		if !ok {
			// No handler for this stream — silently drop
			continue
		}

		// Dispatch to handler (synchronously for now; each stream
		// typically has its own connection/goroutine)
		if err := handler(header, payload); err != nil {
			return fmt.Errorf("handler error (stream=%s): %w",
				StreamName(header.StreamID), err)
		}
	}
}

// Stop signals the demuxer to stop processing.
func (d *Demuxer) Stop() {
	select {
	case <-d.done:
	default:
		close(d.done)
	}
}

// MultiDemuxer manages multiple stream connections, each with its own Demuxer.
// Each stream (screen, camera, mic, speaker) arrives on a separate TCP port.
// Errors from any stream demuxer are propagated via the Errors() channel.
type MultiDemuxer struct {
	demuxers map[uint32]*Demuxer
	errCh    chan error
	mu       sync.Mutex
}

// NewMultiDemuxer creates a new multi-stream demuxer manager.
func NewMultiDemuxer() *MultiDemuxer {
	return &MultiDemuxer{
		demuxers: make(map[uint32]*Demuxer),
		errCh:    make(chan error, 4), // buffered for each stream
	}
}

// Errors returns a channel that receives demux errors from any stream.
// The caller should select on this channel to detect stream failures.
func (m *MultiDemuxer) Errors() <-chan error {
	return m.errCh
}

// AddStream adds a stream with its reader and handler.
// It starts a goroutine to run the demuxer for this stream.
func (m *MultiDemuxer) AddStream(streamID uint32, reader io.Reader, handler StreamHandler) {
	demuxer := NewDemuxer(reader)
	demuxer.RegisterHandler(streamID, handler)

	m.mu.Lock()
	m.demuxers[streamID] = demuxer
	m.mu.Unlock()

	go func() {
		if err := demuxer.Run(); err != nil {
			// Propagate error to caller
			select {
			case m.errCh <- fmt.Errorf("[%s] demux error: %w", StreamName(streamID), err):
			default:
			}
		}
	}()
}

// StopAll stops all demuxers.
func (m *MultiDemuxer) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.demuxers {
		d.Stop()
	}
}
