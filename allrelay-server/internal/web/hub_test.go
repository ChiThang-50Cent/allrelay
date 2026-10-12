package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestScreenInitMessageReplaysDecoderState(t *testing.T) {
	hub := NewHub()
	generation := hub.BeginScreenSession()
	if !hub.SetScreenSession(generation, 1080, 2400) {
		t.Fatal("screen header was rejected")
	}

	config := []byte{1, 0, 0, 1, 0x67, 0x42, 0x00, 0x1f}
	keyframe := []byte{2, 0, 0, 1, 0x65, 0x88, 0x84}
	hub.BroadcastScreenFrame(generation, config)
	hub.BroadcastScreenFrame(generation, keyframe)

	replay := struct {
		Type string     `json:"type"`
		Data screenInit `json:"data"`
	}{}
	if err := json.Unmarshal(hub.screenInitMessage(), &replay); err != nil {
		t.Fatalf("decode screen init: %v", err)
	}
	if replay.Type != "screen_init" {
		t.Fatalf("type = %q, want screen_init", replay.Type)
	}
	if replay.Data.Session == nil || replay.Data.Session.Width != 1080 || replay.Data.Session.Height != 2400 {
		t.Fatalf("session = %#v, want 1080x2400", replay.Data.Session)
	}
	if len(replay.Data.Configs) != 1 || !bytes.Equal(replay.Data.Configs[0], config) {
		t.Fatalf("configs = %x, want %x", replay.Data.Configs, config)
	}
	if !bytes.Equal(replay.Data.KeyFrame, keyframe) {
		t.Fatalf("key frame = %x, want %x", replay.Data.KeyFrame, keyframe)
	}
}

func TestScreenLifecycleWaitsForHeaderAndControlRoute(t *testing.T) {
	hub := NewHub()
	generation := hub.BeginScreenSession()
	var writes [][]byte
	if _, err := hub.RegisterControlRoute(generation, recordControlWrites(&writes)); err != nil {
		t.Fatalf("register control route: %v", err)
	}
	if len(writes) != 0 {
		t.Fatalf("writes before header = %x, want none", writes)
	}
	if !hub.SetScreenSession(generation, 1080, 2400) {
		t.Fatal("screen header was rejected")
	}
	if !equalPackets(writes, [][]byte{{10, 0}}) {
		t.Fatalf("writes after route and header = %x, want 0a00", writes)
	}
}

func TestScreenLifecycleSuppressesDuplicateDisplayOff(t *testing.T) {
	hub := NewHub()
	generation := hub.BeginScreenSession()
	var writes [][]byte
	if _, err := hub.RegisterControlRoute(generation, recordControlWrites(&writes)); err != nil {
		t.Fatalf("register control route: %v", err)
	}
	if !hub.SetScreenSession(generation, 1080, 2400) || !hub.SetScreenSession(generation, 2400, 1080) {
		t.Fatal("screen header was rejected")
	}
	if !equalPackets(writes, [][]byte{{10, 0}}) {
		t.Fatalf("writes = %x, want one 0a00", writes)
	}
}

func TestScreenLifecycleReplacementRouteReplaysDisplayOff(t *testing.T) {
	hub := NewHub()
	generation := hub.BeginScreenSession()
	if !hub.SetScreenSession(generation, 1080, 2400) {
		t.Fatal("screen header was rejected")
	}
	var firstWrites [][]byte
	first, err := hub.RegisterControlRoute(generation, recordControlWrites(&firstWrites))
	if err != nil {
		t.Fatalf("register first route: %v", err)
	}
	var secondWrites [][]byte
	if _, err := hub.RegisterControlRoute(generation, recordControlWrites(&secondWrites)); err != nil {
		t.Fatalf("register replacement route: %v", err)
	}
	hub.ClearControlRoute(first)
	if err := hub.WriteControl([]byte{1, 2, 3}); err != nil {
		t.Fatalf("write through replacement route: %v", err)
	}
	if !equalPackets(firstWrites, [][]byte{{10, 0}}) {
		t.Fatalf("first route writes = %x, want 0a00", firstWrites)
	}
	if !equalPackets(secondWrites, [][]byte{{10, 0}, {1, 2, 3}}) {
		t.Fatalf("replacement route writes = %x, want 0a00 then input", secondWrites)
	}
}

func TestScreenLifecycleReplacementStopRestoresPriorDisplayPower(t *testing.T) {
	hub := NewHub()
	generation := hub.BeginScreenSession()
	var firstWrites [][]byte
	if _, err := hub.RegisterControlRoute(generation, recordControlWrites(&firstWrites)); err != nil {
		t.Fatalf("register first route: %v", err)
	}
	if !hub.SetScreenSession(generation, 1080, 2400) {
		t.Fatal("screen header was rejected")
	}

	var replacementWrites [][]byte
	if _, err := hub.RegisterControlRoute(generation, func(data []byte) error {
		replacementWrites = append(replacementWrites, append([]byte(nil), data...))
		if bytes.Equal(data, []byte{10, 0}) {
			return errNoControlRoute
		}
		return nil
	}); err == nil {
		t.Fatal("replacement route unexpectedly sent display OFF")
	}
	if err := hub.EndScreenSession(generation); err != nil {
		t.Fatalf("end screen session: %v", err)
	}
	if !equalPackets(firstWrites, [][]byte{{10, 0}}) {
		t.Fatalf("first route writes = %x, want 0a00", firstWrites)
	}
	if !equalPackets(replacementWrites, [][]byte{{10, 0}, {10, 1}}) {
		t.Fatalf("replacement route writes = %x, want failed 0a00 then 0a01", replacementWrites)
	}
}

func TestScreenLifecycleRejectsStaleControlAndVideo(t *testing.T) {
	hub := NewHub()
	staleGeneration := hub.BeginScreenSession()
	generation := hub.BeginScreenSession()
	var staleWrites [][]byte
	if route, err := hub.RegisterControlRoute(staleGeneration, recordControlWrites(&staleWrites)); err != nil || route != nil {
		t.Fatalf("stale route = %v, %v; want nil, nil", route, err)
	}
	if hub.SetScreenSession(staleGeneration, 1080, 2400) {
		t.Fatal("stale screen header was accepted")
	}
	if hub.BroadcastScreenFrame(staleGeneration, []byte{2, 0, 0, 1, 0x65}) {
		t.Fatal("stale screen frame was accepted")
	}
	if len(hub.broadcast) != 0 {
		t.Fatalf("stale screen session published %d events", len(hub.broadcast))
	}
	if !hub.SetScreenSession(generation, 2400, 1080) {
		t.Fatal("current screen header was rejected")
	}
	if len(staleWrites) != 0 {
		t.Fatalf("stale control writes = %x, want none", staleWrites)
	}
}

func TestScreenLifecycleEndReleasesStateBeforeBlockedControlWrite(t *testing.T) {
	hub := NewHub()
	generation := hub.BeginScreenSession()
	var writes [][]byte
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	if _, err := hub.RegisterControlRoute(generation, func(data []byte) error {
		writes = append(writes, append([]byte(nil), data...))
		if len(data) == 1 && data[0] == 1 {
			close(writeStarted)
			<-releaseWrite
		}
		return nil
	}); err != nil {
		t.Fatalf("register control route: %v", err)
	}
	if !hub.SetScreenSession(generation, 1080, 2400) {
		t.Fatal("screen header was rejected")
	}

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- hub.WriteControl([]byte{1})
	}()
	<-writeStarted

	endDone := make(chan error, 1)
	go func() {
		endDone <- hub.EndScreenSession(generation)
	}()
	for {
		hub.controlMu.Lock()
		ended := !hub.screenActive
		hub.controlMu.Unlock()
		if ended {
			break
		}
		runtime.Gosched()
	}
	if err := hub.WriteControl([]byte{2}); err == nil {
		t.Fatal("write succeeded after lifecycle state ended")
	}
	select {
	case err := <-endDone:
		t.Fatalf("end returned before blocked write released: %v", err)
	default:
	}

	close(releaseWrite)
	if err := <-writeDone; err != nil {
		t.Fatalf("blocked write: %v", err)
	}
	if err := <-endDone; err != nil {
		t.Fatalf("end screen session: %v", err)
	}
	if !equalPackets(writes, [][]byte{{10, 0}, {1}, {10, 1}}) {
		t.Fatalf("writes = %x, want 0a00 then 01 then 0a01", writes)
	}
}

func TestScreenLifecycleStopRestoresDisplayBeforeDetachingRoute(t *testing.T) {
	hub := NewHub()
	generation := hub.BeginScreenSession()
	var writes [][]byte
	if _, err := hub.RegisterControlRoute(generation, recordControlWrites(&writes)); err != nil {
		t.Fatalf("register control route: %v", err)
	}
	if !hub.SetScreenSession(generation, 1080, 2400) {
		t.Fatal("screen header was rejected")
	}
	if err := hub.EndScreenSession(generation); err != nil {
		t.Fatalf("end screen session: %v", err)
	}
	if err := hub.EndScreenSession(generation); err != nil {
		t.Fatalf("second end screen session: %v", err)
	}
	if !equalPackets(writes, [][]byte{{10, 0}, {10, 1}}) {
		t.Fatalf("writes = %x, want 0a00 then 0a01", writes)
	}
	if err := hub.WriteControl([]byte{1}); err == nil {
		t.Fatal("write succeeded after route was detached")
	}
}

func recordControlWrites(writes *[][]byte) func([]byte) error {
	return func(data []byte) error {
		*writes = append(*writes, append([]byte(nil), data...))
		return nil
	}
}

func equalPackets(got, want [][]byte) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !bytes.Equal(got[i], want[i]) {
			return false
		}
	}
	return true
}

func TestCheckSameOrigin(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		origin string
		allow  bool
	}{
		{
			name:   "no origin allowed (tray or non-browser client)",
			host:   "localhost:8080",
			origin: "",
			allow:  true,
		},
		{
			name:   "same origin exact match",
			host:   "localhost:8080",
			origin: "http://localhost:8080",
			allow:  true,
		},
		{
			name:   "same origin 127.0.0.1 exact match",
			host:   "127.0.0.1:9090",
			origin: "http://127.0.0.1:9090",
			allow:  true,
		},
		{
			name:   "case insensitive host match",
			host:   "LocalHost:8080",
			origin: "http://localhost:8080",
			allow:  true,
		},
		{
			name:   "foreign origin rejected",
			host:   "localhost:8080",
			origin: "http://attacker.com:8080",
			allow:  false,
		},
		{
			name:   "foreign origin with different port rejected",
			host:   "localhost:8080",
			origin: "http://localhost:9999",
			allow:  false,
		},
		{
			name:   "malformed origin rejected",
			host:   "localhost:8080",
			origin: "://malformed-url",
			allow:  false,
		},
		{
			name:   "ftp scheme rejected",
			host:   "localhost:8080",
			origin: "ftp://localhost:8080",
			allow:  false,
		},
		{
			name:   "userinfo rejected",
			host:   "localhost:8080",
			origin: "http://user:pass@localhost:8080",
			allow:  false,
		},
		{
			name:   "origin with path rejected",
			host:   "localhost:8080",
			origin: "http://localhost:8080/somepath",
			allow:  false,
		},
		{
			name:   "origin with query rejected",
			host:   "localhost:8080",
			origin: "http://localhost:8080?param=1",
			allow:  false,
		},
		{
			name:   "default http port match when host omits 80",
			host:   "localhost",
			origin: "http://localhost:80",
			allow:  true,
		},
		{
			name:   "default http port match when origin omits 80",
			host:   "localhost:80",
			origin: "http://localhost",
			allow:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/ws", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			got := checkSameOrigin(req)
			if got != tc.allow {
				t.Errorf("checkSameOrigin(host=%q, origin=%q) = %v; want %v", tc.host, tc.origin, got, tc.allow)
			}
		})
	}
}

func TestWebSocketAdmissionAndTeardownLifecycle(t *testing.T) {
	ws := NewWebServer(DefaultConfig())
	defer ws.Stop()

	server := httptest.NewServer(http.HandlerFunc(ws.HandleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	// 1. Foreign origin client is refused
	dialerForeign := websocket.Dialer{}
	foreignHeader := http.Header{}
	foreignHeader.Set("Origin", "http://evil.com")
	_, resp, err := dialerForeign.Dial(wsURL, foreignHeader)
	if err == nil {
		t.Fatal("expected foreign origin WebSocket connection to fail")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for foreign origin, got %d", resp.StatusCode)
	}

	// 2. Same-origin or no-origin client succeeds
	dialerNoOrigin := websocket.Dialer{}
	conn, _, err := dialerNoOrigin.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("expected no-origin client to connect successfully: %v", err)
	}

	// Read initial status
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read initial status: %v", err)
	}
	if !strings.Contains(string(msg), `"status"`) {
		t.Fatalf("unexpected message: %s", string(msg))
	}

	// Send ping and receive pong
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	_, pongMsg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if !strings.Contains(string(pongMsg), `"pong"`) {
		t.Fatalf("unexpected pong: %s", string(pongMsg))
	}

	// Closing client connection tears down cleanly and removes client under hub.mu even without broadcasts
	if err := conn.Close(); err != nil {
		t.Fatalf("close conn: %v", err)
	}

	removed := false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ws.hub.mu.RLock()
		n := len(ws.hub.clients)
		ws.hub.mu.RUnlock()
		if n == 0 {
			removed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !removed {
		t.Fatal("client was not removed from hub after connection close")
	}
}

func TestClientTeardownIdempotent(t *testing.T) {
	hub := NewHub()
	defer hub.Stop()

	client := &Client{
		hub:     hub,
		send:    make(chan []byte, 2),
		sendBin: make(chan []byte, 2),
		closed:  make(chan struct{}),
	}

	// Test enqueueText and enqueueBin after closed channel
	close(client.closed)
	if client.enqueueText([]byte("msg")) {
		t.Fatal("enqueueText succeeded on closed client")
	}
	if client.enqueueBin([]byte("data")) {
		t.Fatal("enqueueBin succeeded on closed client")
	}
}

func TestHubBroadcastOverflowDisconnection(t *testing.T) {
	hub := NewHub()
	defer hub.Stop()
	go hub.Run()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		client := &Client{
			hub:        hub,
			conn:       conn,
			remoteAddr: r.RemoteAddr,
			send:       make(chan []byte, 1),
			sendBin:    make(chan []byte, 1),
			closed:     make(chan struct{}),
		}
		hub.register <- client
		// Fill send buffer
		client.send <- []byte("msg1")
		go client.readPump()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Wait for client to be registered in hub
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		hub.mu.RLock()
		n := len(hub.clients)
		hub.mu.RUnlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Broadcast another message which will overflow this client
	hub.broadcast <- []byte("msg2")

	// Wait for client to be removed from hub.clients
	removed := false
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		hub.mu.RLock()
		n := len(hub.clients)
		hub.mu.RUnlock()
		if n == 0 {
			removed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !removed {
		t.Fatal("client was not disconnected and removed from hub after queue overflow")
	}
}
