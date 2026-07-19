package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for development
	},
}

// Client represents a WebSocket client
type Client struct {
	hub        *Hub
	conn       *websocket.Conn
	remoteAddr string
	userAgent  string
	send       chan []byte // text messages (JSON status, events)
	sendBin    chan []byte // binary messages (H.264 NAL units for screen)
	writeMu    sync.Mutex  // serializes concurrent WriteMessage calls
	wg         sync.WaitGroup
}

// Hub maintains the set of active clients and broadcasts messages
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
	controlMu  sync.RWMutex

	// Latest decoder setup for a viewer that joins after screen streaming began.
	// Access units are immutable once published, so their slices are safe to retain.
	screenSession      *screenSession
	screenConfigFrames [][]byte
	screenKeyFrame     []byte

	// onControl forwards touch and key events to the active control connection.
	onControl func(data []byte)
}

type screenSession struct {
	Width  uint32 `json:"width"`
	Height uint32 `json:"height"`
}

type screenInit struct {
	Session  *screenSession `json:"session,omitempty"`
	Configs  [][]byte       `json:"configs,omitempty"`
	KeyFrame []byte         `json:"keyFrame,omitempty"`
}

const maxScreenReplayFrameBytes = 2 * 1024 * 1024
const maxScreenReplayConfigFrames = 4

// NewHub creates a new WebSocket hub
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run starts the hub
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			total := len(h.clients)
			h.mu.Unlock()
			slog.Debug("WebSocket client connected", "total", total, "remote_addr", client.remoteAddr)

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				close(client.sendBin)
			}
			total := len(h.clients)
			h.mu.Unlock()
			slog.Debug("WebSocket client disconnected", "total", total, "remote_addr", client.remoteAddr)

		case message := <-h.broadcast:
			// A full client queue removes that client, so this must be an
			// exclusive lock rather than an RLock.
			h.mu.Lock()
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.Unlock()
		}
	}
}

// SetScreenSession records and broadcasts the active screen dimensions. A new
// session invalidates codec configuration and keyframes from the old encoder.
func (h *Hub) SetScreenSession(width, height uint32) {
	session := &screenSession{Width: width, Height: height}
	h.mu.Lock()
	h.screenSession = session
	h.screenConfigFrames = nil
	h.screenKeyFrame = nil
	h.mu.Unlock()
	h.BroadcastEvent("screen_session", session)
}

// ClearScreenReplay removes cached decoder state when screen streaming stops.
func (h *Hub) ClearScreenReplay() {
	h.mu.Lock()
	h.screenSession = nil
	h.screenConfigFrames = nil
	h.screenKeyFrame = nil
	h.mu.Unlock()
}

// BroadcastScreenFrame sends one H.264 access unit and retains the small set of
// decoder inputs needed to start a remote viewer that connects late.
func (h *Hub) BroadcastScreenFrame(data []byte) {
	h.mu.Lock()
	if len(data) > 1 {
		flags := data[0]
		if flags&1 != 0 {
			h.screenConfigFrames = append(h.screenConfigFrames, data)
			if len(h.screenConfigFrames) > maxScreenReplayConfigFrames {
				h.screenConfigFrames = h.screenConfigFrames[len(h.screenConfigFrames)-maxScreenReplayConfigFrames:]
			}
		}
		if flags&2 != 0 && len(data) <= maxScreenReplayFrameBytes {
			h.screenKeyFrame = data
		}
	}
	for client := range h.clients {
		select {
		case client.sendBin <- data:
		default:
			// Client buffer full, drop this frame
		}
	}
	h.mu.Unlock()
}

// SetControlHandler updates the active control forwarding target.
func (h *Hub) SetControlHandler(handler func([]byte)) {
	h.controlMu.Lock()
	h.onControl = handler
	h.controlMu.Unlock()
}

func (h *Hub) forwardControl(data []byte) {
	h.controlMu.RLock()
	handler := h.onControl
	h.controlMu.RUnlock()
	if handler != nil {
		handler(data)
	}
}

func (h *Hub) screenInitMessage() []byte {
	h.mu.RLock()
	init := screenInit{
		Session:  h.screenSession,
		Configs:  append([][]byte(nil), h.screenConfigFrames...),
		KeyFrame: h.screenKeyFrame,
	}
	h.mu.RUnlock()

	message, err := json.Marshal(WSMessage{Type: "screen_init", Data: init})
	if err != nil {
		slog.Error("Failed to marshal cached screen initialization", "error", err)
		return nil
	}
	return message
}

// BroadcastStatus sends status update to all connected clients
func (h *Hub) BroadcastStatus(status *ConnectionStatus) {
	msg := WSMessage{
		Type: "status",
		Data: status,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		slog.Error("Failed to marshal status", "error", err)
		return
	}
	h.broadcast <- data
}

// BroadcastStreamUpdate sends stream update to all connected clients
func (h *Hub) BroadcastStreamUpdate(stream StreamStatus) {
	msg := WSMessage{
		Type: "stream_update",
		Data: stream,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		slog.Error("Failed to marshal stream update", "error", err)
		return
	}
	h.broadcast <- data
}

// BroadcastEvent sends a generic event to all connected clients
func (h *Hub) BroadcastEvent(eventType string, data interface{}) {
	msg := WSMessage{
		Type: eventType,
		Data: data,
	}
	jsonData, err := json.Marshal(msg)
	if err != nil {
		slog.Error("Failed to marshal event", "error", err)
		return
	}
	h.broadcast <- jsonData
}

// WSMessage represents a WebSocket message
type WSMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// HandleWebSocket handles WebSocket upgrade requests
func (ws *WebServer) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("WebSocket upgrade failed", "error", err)
		return
	}

	client := &Client{
		hub:        ws.hub,
		conn:       conn,
		remoteAddr: r.RemoteAddr,
		userAgent:  r.UserAgent(),
		send:       make(chan []byte, 256),
		sendBin:    make(chan []byte, 64),
	}

	ws.hub.register <- client

	// Send initial status
	ws.mu.RLock()
	status := ws.currentConn
	ws.mu.RUnlock()

	msg := WSMessage{Type: "status", Data: status}
	data, _ := json.Marshal(msg)
	client.send <- data

	go client.writePump()
	go client.readPump()
}

// readPump reads messages from the WebSocket connection
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	for {
		messageType, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Error("WebSocket read error", "error", err)
			}
			break
		}

		if messageType == websocket.BinaryMessage {
			c.hub.forwardControl(message)
			continue
		}

		// Handle incoming text/JSON messages
		var msg WSMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			slog.Error("Invalid WebSocket message", "error", err)
			continue
		}

		// Process message based on type
		switch msg.Type {
		case "ping":
			c.send <- []byte(`{"type":"pong"}`)
		case "screen_init":
			// The client requests this after applying its initial status update, so
			// its screen canvas is ready before replayed config/keyframe data arrives.
			if init := c.hub.screenInitMessage(); init != nil {
				c.send <- init
			}
		case "control":
			// Backward-compatible fallback for older clients.
			data, _ := json.Marshal(msg.Data)
			c.hub.forwardControl(data)
		case "client_log":
			c.logClientEvent(msg.Data)
		}
	}
}

func (c *Client) logClientEvent(data interface{}) {
	payload, ok := data.(map[string]interface{})
	if !ok {
		slog.Warn("Invalid client log payload", "remote_addr", c.remoteAddr)
		return
	}

	level, _ := payload["level"].(string)
	event, _ := payload["event"].(string)
	page, _ := payload["page"].(string)
	fields, _ := payload["fields"].(map[string]interface{})
	if event == "" {
		event = "client_log"
	}

	args := []any{
		"event", event,
		"page", page,
		"remote_addr", c.remoteAddr,
		"user_agent", c.userAgent,
	}
	if len(fields) > 0 {
		args = append(args, "fields", fields)
	}

	switch level {
	case "debug":
		slog.Debug("Remote client event", args...)
	case "warn":
		slog.Warn("Remote client event", args...)
	case "error":
		slog.Error("Remote client event", args...)
	default:
		slog.Info("Remote client event", args...)
	}
}

// writePump writes messages to the WebSocket connection.
//
// Text (status/events) and binary (H.264 screen frames) are written by two
// independent goroutines so that a slow text write can never block binary
// frame delivery — otherwise periodic BroadcastStatus messages could stall
// screen frames and cause the remote viewer to freeze on the "connecting"
// state while other streams (e.g. speaker) are active.
func (c *Client) writePump() {
	c.wg.Add(2)
	go c.writeTextPump()
	go c.writeBinaryPump()
	c.wg.Wait()
	c.conn.Close()
}

// writeTextPump writes text (JSON) messages from c.send.
func (c *Client) writeTextPump() {
	defer c.wg.Done()
	for message := range c.send {
		c.writeMu.Lock()
		err := c.conn.WriteMessage(websocket.TextMessage, message)
		c.writeMu.Unlock()
		if err != nil {
			return
		}
	}
	c.writeMu.Lock()
	c.conn.WriteMessage(websocket.CloseMessage, []byte{})
	c.writeMu.Unlock()
}

// writeBinaryPump writes binary (H.264) messages from c.sendBin.
func (c *Client) writeBinaryPump() {
	defer c.wg.Done()
	for data := range c.sendBin {
		c.writeMu.Lock()
		err := c.conn.WriteMessage(websocket.BinaryMessage, data)
		c.writeMu.Unlock()
		if err != nil {
			return
		}
	}
	c.writeMu.Lock()
	c.conn.WriteMessage(websocket.CloseMessage, []byte{})
	c.writeMu.Unlock()
}
