package transport_test

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/allrelay/allrelay-server/internal/protocol"
	"github.com/allrelay/allrelay-server/internal/transport"
)

// TestE2EMultiStream is a full end-to-end integration test:
// 1. Starts mock Android servers on 4 ports
// 2. Connects the AllRelay Go client
// 3. Sends tagged test packets on each stream
// 4. Verifies all packets are received and routed correctly
func TestE2EMultiStream(t *testing.T) {
	const basePort uint16 = 15500

	// Start mock servers
	listeners := make([]net.Listener, 4)
	streamNames := []string{"screen", "camera", "mic", "speaker"}
	streamIDs := []uint32{0x01, 0x02, 0x03, 0x04}

	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		port := basePort + uint16(i)
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("Listen %s: %v", streamNames[i], err)
		}
		listeners[i] = listener

		wg.Add(1)
		go func(idx int, name string, streamID uint32) {
			defer wg.Done()
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close()

			// Send dummy byte
			conn.Write([]byte{0xAB})

			// Video port (basePort+0) also sends 64-byte device name
			if idx == 0 {
				deviceName := make([]byte, 64)
				copy(deviceName, []byte("TestDevice\x00"))
				conn.Write(deviceName)
			}

			// Send a tagged test packet
			payload := fmt.Sprintf("HELLO_FROM_%s", name)
			header := make([]byte, 16)
			binary.BigEndian.PutUint32(header[0:4], streamID)
			binary.BigEndian.PutUint64(header[4:12], uint64(idx*1000))
			binary.BigEndian.PutUint32(header[12:16], uint32(len(payload)))
			conn.Write(header)
			conn.Write([]byte(payload))

			// Keep connection alive briefly
			time.Sleep(200 * time.Millisecond)
		}(i, streamNames[i], streamIDs[i])
	}

	// Give servers time to start
	time.Sleep(50 * time.Millisecond)

	// Connect client
	conn, err := transport.Connect("127.0.0.1", basePort, true, true, true, true, false)
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer conn.Close()

	// Verify all streams connected
	for _, id := range streamIDs {
		if !conn.HasStream(id) {
			t.Errorf("Stream %s not connected", protocol.StreamName(id))
		}
	}

	// Collect received data
	received := make(map[uint32]string)
	var mu sync.Mutex
	done := make(chan struct{})

	// Screen handler
	go func() {
		demuxer := protocol.NewDemuxer(conn.VideoStream())
		demuxer.RegisterHandler(0x01, func(h *protocol.Header, payload []byte) error {
			mu.Lock()
			received[0x01] = string(payload)
			mu.Unlock()
			if len(received) >= 3 { // expect 3 streams (screen+camera+mic, speaker is PC→phone)
				close(done)
			}
			return nil
		})
		demuxer.Run()
	}()

	// Camera handler
	go func() {
		demuxer := protocol.NewDemuxer(conn.CameraStream())
		demuxer.RegisterHandler(0x02, func(h *protocol.Header, payload []byte) error {
			mu.Lock()
			received[0x02] = string(payload)
			mu.Unlock()
			if len(received) >= 3 {
				select {
				case <-done:
				default:
					close(done)
				}
			}
			return nil
		})
		demuxer.Run()
	}()

	// Mic handler
	go func() {
		demuxer := protocol.NewDemuxer(conn.MicStream())
		demuxer.RegisterHandler(0x03, func(h *protocol.Header, payload []byte) error {
			mu.Lock()
			received[0x03] = string(payload)
			mu.Unlock()
			if len(received) >= 3 {
				select {
				case <-done:
				default:
					close(done)
				}
			}
			return nil
		})
		demuxer.Run()
	}()

	// Wait for all streams to deliver data (with timeout)
	select {
	case <-done:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for stream data")
	}

	// Verify each stream received the expected data
	mu.Lock()
	defer mu.Unlock()

	expected := map[uint32]string{
		0x01: "HELLO_FROM_screen",
		0x02: "HELLO_FROM_camera",
		0x03: "HELLO_FROM_mic",
	}

	for id, exp := range expected {
		got, ok := received[id]
		if !ok {
			t.Errorf("Stream %s: no data received", protocol.StreamName(id))
		} else if got != exp {
			t.Errorf("Stream %s: got %q, want %q", protocol.StreamName(id), got, exp)
		}
	}

	t.Logf("E2E multi-stream test passed: %d streams verified", len(received))

	// Wait for mock servers to finish
	wg.Wait()
}
