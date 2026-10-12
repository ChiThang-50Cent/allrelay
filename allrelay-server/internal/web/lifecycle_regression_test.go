package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/allrelay/allrelay-server/internal/transport"
)

// TestOfflineConnectFailsTruthfully verifies that connecting to a phone with an invalid
// handshake on requested speaker port returns HTTP 502 Bad Gateway and does not report connected.
func TestOfflineConnectFailsTruthfully(t *testing.T) {
	// Controlled listener that rejects the handshake immediately
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer l.Close()

	port := l.Addr().(*net.TCPAddr).Port
	if port < 3 {
		t.Skip("ephemeral port too low for base calculation")
	}
	basePort := port - 3

	// Accept speaker connection on basePort+3 and immediately close without valid dummy byte
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	ws := NewWebServer(DefaultConfig())
	defer ws.Stop()

	body, _ := json.Marshal(map[string]interface{}{
		"ip":   "127.0.0.1",
		"port": basePort,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/connect", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	ws.handleConnect(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected HTTP 502 Bad Gateway, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	status := ws.snapshotStatus()
	if status.Connected {
		t.Fatal("expected status.Connected to be false after failed connect")
	}
	if status.Phone != nil {
		t.Fatalf("expected status.Phone to be nil, got %+v", status.Phone)
	}
}

// A pending handshake must neither report Active nor block status or OFF.
// No media worker starts in this fixture.
func TestPendingStreamCanBeCancelledWithoutStickyActive(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	basePort := listener.Addr().(*net.TCPAddr).Port - 1
	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		_, _ = io.Copy(io.Discard, conn)
	}()

	conn, err := transport.ConnectContext(context.Background(), "127.0.0.1", uint16(basePort), false, false, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWebServer(DefaultConfig())
	defer ws.Stop()
	sc := ws.GetController()
	sc.mu.Lock()
	sc.conn = conn
	sc.connected = true
	sc.host = "127.0.0.1"
	sc.port = basePort
	sc.mu.Unlock()

	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/streams/toggle", bytes.NewBufferString(`{"stream":"camera","active":true}`))
		rec := httptest.NewRecorder()
		ws.handleToggleStream(rec, req)
		response <- rec
	}()
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("camera dial did not reach the handshake")
	}

	snapshots := make(chan ConnectionStatus, 1)
	go func() { snapshots <- ws.snapshotStatus() }()
	select {
	case status := <-snapshots:
		for _, stream := range status.Streams {
			if stream.Name == "camera" && stream.Active {
				t.Fatal("pending handshake reported camera Active")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending handshake blocked status")
	}

	stopped := make(chan error, 1)
	go func() { stopped <- sc.ToggleStream("camera", false) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OFF did not cancel the pending handshake")
	}
	select {
	case rec := <-response:
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("cancelled start returned %d: %s", rec.Code, rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled ON did not return")
	}
	for _, stream := range ws.snapshotStatus().Streams {
		if stream.Name == "camera" && stream.Active {
			t.Fatal("cancelled camera start committed Active")
		}
	}
}

// TestPendingConnectCancelledOnDisconnect verifies that Disconnect cancels an in-flight
// Connect dial promptly and guards against late state commit.
func TestPendingConnectCancelledOnDisconnect(t *testing.T) {
	// Listener that accepts but stalls without replying
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer l.Close()

	port := l.Addr().(*net.TCPAddr).Port
	if port < 3 {
		t.Skip("port too low")
	}
	basePort := port - 3
	accepted := make(chan struct{})

	go func() {
		conn, err := l.Accept()
		if err == nil {
			defer conn.Close()
			close(accepted)
			_, _ = io.Copy(io.Discard, conn)
		}
	}()

	ws := NewWebServer(DefaultConfig())
	defer ws.Stop()

	connectErrCh := make(chan error, 1)
	go func() {
		connectErrCh <- ws.GetController().Connect("127.0.0.1", basePort)
	}()

	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("Connect did not reach the handshake")
	}

	// Disconnect should promptly cancel the pending dial
	if err := ws.GetController().Disconnect(); err != nil {
		t.Fatalf("Disconnect failed: %v", err)
	}

	select {
	case err := <-connectErrCh:
		if err == nil {
			t.Fatal("expected Connect to fail when cancelled by Disconnect")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Connect did not abort promptly after Disconnect")
	}

	status := ws.snapshotStatus()
	if status.Connected {
		t.Fatal("server marked connected after cancelled connect")
	}
}

// TestDisconnectRequiresPOST verifies that GET /api/disconnect returns 405 Method Not Allowed
// and does not mutate connection state.
func TestDisconnectRequiresPOST(t *testing.T) {
	ws := NewWebServer(DefaultConfig())
	defer ws.Stop()

	phone := &PhoneDevice{
		ID:   "192.168.1.50",
		IP:   "192.168.1.50",
		Name: "Test Phone",
	}
	ws.SetConnectionStatus(true, phone)

	// Attempt GET /api/disconnect
	req := httptest.NewRequest(http.MethodGet, "/api/disconnect", nil)
	rec := httptest.NewRecorder()
	ws.handleDisconnect(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected HTTP 405 Method Not Allowed on GET /api/disconnect, got %d", rec.Code)
	}

	snap := ws.snapshotStatus()
	if !snap.Connected {
		t.Fatal("GET /api/disconnect mutated state: connected became false")
	}
	if snap.Phone == nil || snap.Phone.ID != "192.168.1.50" {
		t.Fatalf("GET /api/disconnect mutated phone: %+v", snap.Phone)
	}
}

func TestSupersededPendingStartClosesPreviousHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	basePort := listener.Addr().(*net.TCPAddr).Port - 1
	accepted := make(chan net.Conn, 2)
	go func() {
		for range 2 {
			peer, err := listener.Accept()
			if err != nil {
				return
			}
			accepted <- peer
		}
	}()
	nextPeer := func() net.Conn {
		select {
		case peer := <-accepted:
			t.Cleanup(func() { peer.Close() })
			return peer
		case <-time.After(2 * time.Second):
			t.Fatal("pending start did not reach its handshake")
			return nil
		}
	}

	conn, err := transport.ConnectContext(context.Background(), "127.0.0.1", uint16(basePort), false, false, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWebServer(DefaultConfig())
	defer ws.Stop()
	sc := ws.GetController()
	sc.mu.Lock()
	sc.conn, sc.connected, sc.host, sc.port = conn, true, "127.0.0.1", basePort
	sc.mu.Unlock()

	firstResult := make(chan error, 1)
	go func() { firstResult <- sc.ToggleStream("camera", true) }()
	firstPeer := nextPeer()
	sc.mu.RLock()
	firstCancel := sc.streams["camera"].cancel
	sc.mu.RUnlock()
	defer firstCancel() // also clean up a failed-before superseded dial

	secondResult := make(chan error, 1)
	go func() { secondResult <- sc.ToggleStream("camera", true) }()
	_ = nextPeer()
	select {
	case err := <-firstResult:
		if err == nil {
			t.Fatal("superseded start reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("superseded start kept its handshake pending")
	}
	peerClosed := make(chan error, 1)
	go func() {
		_, err := firstPeer.Read(make([]byte, 1))
		peerClosed <- err
	}()
	select {
	case err := <-peerClosed:
		if err != io.EOF {
			t.Fatalf("superseded handshake did not close cleanly: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("superseded handshake socket remained open")
	}
	if err := sc.ToggleStream("camera", false); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-secondResult:
		if err == nil {
			t.Fatal("cancelled replacement start reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OFF did not cancel the replacement handshake")
	}
	for _, stream := range ws.snapshotStatus().Streams {
		if stream.Name == "camera" && stream.Active {
			t.Fatal("superseded/cancelled start committed Active")
		}
	}
}

func TestLaterOFFCancelsRestartWaitingForPreviousWorker(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	basePort := listener.Addr().(*net.TCPAddr).Port - 1
	conn, err := transport.ConnectContext(context.Background(), "127.0.0.1", uint16(basePort), false, false, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWebServer(DefaultConfig())
	defer ws.Stop()
	sc := ws.GetController()
	oldContext, oldCancel := context.WithCancel(context.Background())
	defer oldCancel()
	oldDone := make(chan struct{})
	released := false
	releaseWorker := func() {
		if !released {
			close(oldDone)
			released = true
		}
	}
	defer releaseWorker()
	sc.mu.Lock()
	sc.conn, sc.connected, sc.host, sc.port = conn, true, "127.0.0.1", basePort
	camera := sc.streams["camera"]
	// A previous worker is stopping, but has not acknowledged completion.
	camera.Running, camera.Active, camera.done, camera.cancel = true, false, oldDone, oldCancel
	sc.mu.Unlock()

	startResult := make(chan error, 1)
	go func() { startResult <- sc.ToggleStream("camera", true) }()
	select {
	case <-oldContext.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("restart did not cancel the previous worker")
	}
	sc.mu.RLock()
	waitGeneration := camera.gen
	sc.mu.RUnlock()
	stopResult := make(chan error, 1)
	go func() { stopResult <- sc.ToggleStream("camera", false) }()

	// Synchronize on the newer OFF taking ownership before releasing oldDone.
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		sc.mu.RLock()
		stopping := camera.gen > waitGeneration
		sc.mu.RUnlock()
		if stopping {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("later OFF did not take lifecycle ownership")
		}
	}
	releaseWorker()
	select {
	case err := <-startResult:
		if err == nil {
			t.Fatal("superseded restart reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restart resumed dialing after the newer OFF")
	}
	select {
	case err := <-stopResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OFF did not finish after worker completion")
	}
	for _, stream := range ws.snapshotStatus().Streams {
		if stream.Name == "camera" && stream.Active {
			t.Fatal("superseded restart committed Active after OFF")
		}
	}
}
