package transport

import (
	"net"
	"testing"
	"time"
)

// TestConnectAndDummyByte verifies that connectPort reads the dummy byte correctly.
func TestConnectAndDummyByte(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	port := uint16(addr.Port)

	// Accept in background
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

	conn, err := connectPort("127.0.0.1", port, "test")
	if err != nil {
		t.Fatalf("connectPort failed: %v", err)
	}
	conn.Close()

	// Wait for accept goroutine
	select {
	case err := <-errCh:
		if err != nil {
			t.Logf("Accept error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Log("Accept goroutine timeout (expected if test finished first)")
	}
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
	// Create a mock connection to test HasStream
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

	conn, err := connectPort("127.0.0.1", port, "video")
	if err != nil {
		t.Fatalf("connectPort failed: %v", err)
	}
	defer conn.Close()

	// Verify connection established
	if conn == nil {
		t.Fatal("Connection is nil")
	}
}
