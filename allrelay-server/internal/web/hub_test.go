package web

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestScreenInitMessageReplaysDecoderState(t *testing.T) {
	hub := NewHub()
	hub.SetScreenSession(1080, 2400)

	config := []byte{1, 0, 0, 1, 0x67, 0x42, 0x00, 0x1f}
	keyframe := []byte{2, 0, 0, 1, 0x65, 0x88, 0x84}
	hub.BroadcastScreenFrame(config)
	hub.BroadcastScreenFrame(keyframe)

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

func TestControlHandlerCanBeReplacedAndCleared(t *testing.T) {
	hub := NewHub()
	var received []byte
	hub.SetControlHandler(func(data []byte) {
		received = append([]byte(nil), data...)
	})
	hub.forwardControl([]byte{1, 2, 3})
	if !bytes.Equal(received, []byte{1, 2, 3}) {
		t.Fatalf("received = %x, want 010203", received)
	}

	hub.SetControlHandler(nil)
	hub.forwardControl([]byte{4})
	if !bytes.Equal(received, []byte{1, 2, 3}) {
		t.Fatalf("cleared handler changed received data: %x", received)
	}
}

func TestScreenSessionAndClearDiscardOldReplay(t *testing.T) {
	hub := NewHub()
	hub.SetScreenSession(1080, 2400)
	hub.BroadcastScreenFrame([]byte{1, 0, 0, 1, 0x67})
	hub.BroadcastScreenFrame([]byte{2, 0, 0, 1, 0x65})

	hub.SetScreenSession(2400, 1080)
	var afterSession struct {
		Data screenInit `json:"data"`
	}
	if err := json.Unmarshal(hub.screenInitMessage(), &afterSession); err != nil {
		t.Fatalf("decode new session: %v", err)
	}
	if len(afterSession.Data.Configs) != 0 || len(afterSession.Data.KeyFrame) != 0 {
		t.Fatalf("new session retained stale frame data: %#v", afterSession.Data)
	}

	hub.ClearScreenReplay()
	var afterClear struct {
		Data screenInit `json:"data"`
	}
	if err := json.Unmarshal(hub.screenInitMessage(), &afterClear); err != nil {
		t.Fatalf("decode cleared replay: %v", err)
	}
	if afterClear.Data.Session != nil || len(afterClear.Data.Configs) != 0 || len(afterClear.Data.KeyFrame) != 0 {
		t.Fatalf("cleared replay = %#v, want empty", afterClear.Data)
	}
}
