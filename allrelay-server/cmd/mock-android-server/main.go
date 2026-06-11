// mock-android-server: Simulates an AllRelay Android server for end-to-end testing.
//
// Starts TCP listeners on ports 15000-15004, sends dummy data with proper
// 16-byte headers on each connection, mimicking the real Android server behavior.
//
// Usage: go run ./cmd/mock-android-server/
// Then in another terminal: ./bin/allrelay-server --host 127.0.0.1 --port 15000
package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"
)

const basePort = 15000
const dummyByte = 0xAB

// Packet header helpers (matching the real protocol)
const (
	HeaderSize    = 16
	FlagKeyFrame  = uint64(1 << 61)
	FlagConfig    = uint64(1 << 62)
)

func makeHeader(streamID uint32, pts uint64, flags uint64, payloadSize uint32) []byte {
	buf := make([]byte, HeaderSize)
	binary.BigEndian.PutUint32(buf[0:4], streamID)
	binary.BigEndian.PutUint64(buf[4:12], pts|flags)
	binary.BigEndian.PutUint32(buf[12:16], payloadSize)
	return buf
}

func handleConnection(conn net.Conn, streamID uint32, name string) {
	defer conn.Close()

	// Send dummy byte (like Android server does)
	conn.Write([]byte{dummyByte})

	fmt.Printf("[%s] client connected from %s\n", name, conn.RemoteAddr())

	// Send stream config (codec info)
	configPayload := []byte(fmt.Sprintf("CONFIG %s codec=opus/h264", name))
	header := makeHeader(streamID, 0, FlagConfig, uint32(len(configPayload)))
	conn.Write(header)
	conn.Write(configPayload)

	// Send regular data packets
	frameCount := 0
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		frameCount++
		var payload string

		switch streamID {
		case 0x01: // screen
			payload = fmt.Sprintf("SCREEN_FRAME_%d_%s", frameCount, strings.Repeat(" ", 200))
		case 0x02: // camera
			payload = fmt.Sprintf("CAMERA_FRAME_%d_%s", frameCount, strings.Repeat(" ", 100))
		case 0x03: // mic
			payload = fmt.Sprintf("MIC_PACKET_%d_%s", frameCount, strings.Repeat(" ", 40))
		default:
			payload = fmt.Sprintf("DATA_%d", frameCount)
		}

		flags := uint64(0)
		if frameCount == 1 {
			flags = FlagKeyFrame // first frame is keyframe
		}

		header := makeHeader(streamID, uint64(frameCount*33333), flags, uint32(len(payload)))
		conn.Write(header)
		conn.Write([]byte(payload))

		// Stop after 10 frames per stream
		if frameCount >= 10 {
			break
		}
	}
}

func startServer(offset uint16, streamID uint32, name string) {
	port := basePort + offset
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		fmt.Printf("[%s] Failed to listen on %d: %v\n", name, port, err)
		return
	}
	defer listener.Close()
	fmt.Printf("[%s] listening on port %d\n", name, port)

	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Printf("[%s] Accept error: %v\n", name, err)
			return
		}
		go handleConnection(conn, streamID, name)
	}
}

func main() {
	fmt.Println("=== AllRelay Mock Android Server ===")
	fmt.Printf("Base port: %d\n", basePort)
	fmt.Println()

	// Start all 4 stream servers
	go startServer(0, 0x00000001, "screen")
	go startServer(1, 0x00000002, "camera")
	go startServer(2, 0x00000003, "mic")
	go startServer(3, 0x00000004, "speaker")

	fmt.Println("All streams listening. Press Ctrl+C to stop.")
	fmt.Println()
	fmt.Println("Test with:")
	fmt.Printf("  ./bin/allrelay-server --host 127.0.0.1 --port %d\n", basePort)

	// Run forever
	select {}
}
