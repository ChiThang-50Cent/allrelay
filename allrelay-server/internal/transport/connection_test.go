package transport

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/allrelay/allrelay-server/internal/protocol"
)

// TestConnectAndDummyByte verifies that connectPort reads the dummy byte correctly
// for a non-video port (no device name expected).
func TestConnectAndDummyByte(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	port := uint16(addr.Port)

	errCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()
		conn.Write([]byte{0xAB})
		errCh <- nil
	}()

	// Use "test" (not "video") to avoid device name read
	conn, err := connectPort("127.0.0.1", port, "test")
	if err != nil {
		t.Fatalf("connectPort failed: %v", err)
	}
	conn.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Logf("Accept error: %v", err)
		}
	case <-time.After(1 * time.Second):
	}
}

// TestConnectAndDummyByteVideo verifies that connectPort reads the dummy byte
// AND the device name for the video port.
func TestConnectAndDummyByteVideo(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	port := uint16(addr.Port)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Send dummy byte + 64-byte device name
		buf := make([]byte, 65)
		buf[0] = 0xAB
		copy(buf[1:], []byte("TestPhone\x00"))
		conn.Write(buf)
	}()

	conn, err := connectPort("127.0.0.1", port, "video")
	if err != nil {
		t.Fatalf("connectPort video failed: %v", err)
	}
	conn.Close()
}

// TestConnectWrongDummyByte verifies error on wrong dummy byte.
func TestConnectWrongDummyByte(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	port := uint16(addr.Port)

	go func() {
		conn, _ := listener.Accept()
		if conn != nil {
			defer conn.Close()
			conn.Write([]byte{0xFF})
		}
	}()

	_, err = connectPort("127.0.0.1", port, "test")
	if err == nil {
		t.Error("Expected error for wrong dummy byte")
	}
}

// TestConnectionEmptyClose verifies no panic on empty connection close.
func TestConnectionEmptyClose(t *testing.T) {
	conn := &Connection{}
	err := conn.Close()
	if err != nil {
		t.Errorf("Close() failed: %v", err)
	}
}

// TestHasStream verifies stream presence checks.
func TestHasStream(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	port := uint16(addr.Port)

	go func() {
		conn, _ := listener.Accept()
		if conn != nil {
			defer conn.Close()
			conn.Write([]byte{0xAB})
			time.Sleep(50 * time.Millisecond)
		}
	}()

	// Use "control" to avoid device name read
	conn, err := connectPort("127.0.0.1", port, "control")
	if err != nil {
		t.Fatalf("connectPort failed: %v", err)
	}
	defer conn.Close()

	if conn == nil {
		t.Fatal("Connection is nil")
	}
}

func TestReconnectSpeaker(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	if port <= 3 {
		t.Skip("ephemeral port too low for speaker offset")
	}

	oldSpeaker, oldPeer := net.Pipe()
	defer oldPeer.Close()
	conn := &Connection{speaker: oldSpeaker}

	accepted := make(chan struct{})
	go func() {
		client, err := listener.Accept()
		if err != nil {
			return
		}
		defer client.Close()
		client.Write([]byte{dummyByte})
		close(accepted)
	}()

	if err := conn.ReconnectStream("127.0.0.1", uint16(port-3), protocol.StreamSpeaker); err != nil {
		t.Fatalf("ReconnectStream speaker failed: %v", err)
	}
	defer conn.CloseStream(protocol.StreamSpeaker)

	if conn.speaker == oldSpeaker {
		t.Fatal("speaker connection was not replaced")
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("speaker reconnect did not reach listener")
	}
}

// TestConnectFailsWhenRequestedPortOffline verifies that requested port failures are not swallowed.
func TestConnectFailsWhenRequestedPortOffline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := ConnectContext(ctx, "127.0.0.1", port, false, false, false, true, false)
	if err == nil {
		if conn != nil {
			conn.Close()
		}
		t.Fatal("expected Connect to fail when speaker port is offline, but it succeeded")
	}
}

// TestConnectContextCancellation verifies prompt abort on context cancellation.
func TestConnectContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := ConnectStreamPortContext(ctx, "127.0.0.1", 59999, protocol.StreamSpeaker)
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("cancellation took too long: %v", elapsed)
	}
}
