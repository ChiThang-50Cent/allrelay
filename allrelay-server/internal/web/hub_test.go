package web

import (
	"bytes"
	"encoding/json"
	"runtime"
	"testing"
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
