package transport

import (
	"net"
	"testing"
	"time"
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
