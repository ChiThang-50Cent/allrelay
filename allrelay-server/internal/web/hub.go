package web

import (
	"encoding/json"
	"errors"
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
	controlMu  sync.Mutex
	// Serializes automatic display writes across route replacement and stop
	// without holding controlMu during a blocked route write.
	displayMu sync.Mutex
	// Latest decoder setup for a viewer that joins after screen streaming began.
	// Access units are immutable once published, so their slices are safe to retain.
	screenSession      *screenSession
	screenConfigFrames [][]byte
	screenKeyFrame     []byte

	// Screen lifecycle state is synchronized with route replacement, while
	// displayMu lets stop release this lock before a blocked route write ends.
	screenGeneration  uint64
	screenActive      bool
	screenHeaderReady bool
	displayOffDesired bool
	// displayPowerOff records a successful automatic display OFF write across
	// route replacements. It is cleared only after the active route restores
	// display power successfully.
	displayPowerOff bool
	controlRoute    *controlRoute
}

type controlRoute struct {
	generation     uint64
	write          func([]byte) error
	writeMu        sync.Mutex
	closed         bool
	displayOffSent bool
}

var errNoControlRoute = errors.New("no active control route")

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

// BeginScreenSession starts a new generation-scoped screen lifecycle.
func (h *Hub) BeginScreenSession() uint64 {
	h.displayMu.Lock()
	defer h.displayMu.Unlock()
	h.controlMu.Lock()
	if h.screenActive {
		route := h.endScreenSessionLocked()
		if err := h.restoreDisplay(route); err != nil {
			slog.Debug("Failed to restore display while replacing screen session", "error", err)
		}
		route.close()
	}
	h.screenGeneration++
	h.screenActive = true
	h.screenHeaderReady = false
	h.displayOffDesired = true
	h.controlRoute = nil
	generation := h.screenGeneration
	h.mu.Lock()
	h.screenSession = nil
	h.screenConfigFrames = nil
	h.screenKeyFrame = nil
	h.mu.Unlock()
	h.controlMu.Unlock()
	return generation
}

// SetScreenSession records the current generation's video header. Only the
// active generation may update replay state or trigger display power changes.
func (h *Hub) SetScreenSession(generation uint64, width, height uint32) bool {
	session := &screenSession{Width: width, Height: height}
	h.displayMu.Lock()
	defer h.displayMu.Unlock()
	h.controlMu.Lock()
	if !h.screenActive || generation != h.screenGeneration {
		h.controlMu.Unlock()
		return false
	}
	h.mu.Lock()
	h.screenSession = session
	h.screenConfigFrames = nil
	h.screenKeyFrame = nil
	h.mu.Unlock()
	h.screenHeaderReady = true

	// Publish while the generation remains locked. A replacement cannot start
	// between accepting this header and queuing its session event.
	h.BroadcastEvent("screen_session", session)
	route := h.controlRoute
	var err error
	if route != nil && h.displayOffDesired {
		err = route.sendDisplayOff()
		if err == nil {
			h.displayPowerOff = true
		}
	}
	h.controlMu.Unlock()

	if err != nil {
		slog.Debug("Failed to turn off display after screen header", "error", err)
	}
	return true
}

// EndScreenSession ends a lifecycle once. It disables later automatic display
// OFF writes, restores display power through the active route, then detaches it.
func (h *Hub) EndScreenSession(generation uint64) error {
	h.displayMu.Lock()
	defer h.displayMu.Unlock()
	h.controlMu.Lock()
	if !h.screenActive || generation != h.screenGeneration {
		h.controlMu.Unlock()
		return nil
	}
	route := h.endScreenSessionLocked()
	h.mu.Lock()
	h.screenSession = nil
	h.screenConfigFrames = nil
	h.screenKeyFrame = nil
	h.mu.Unlock()
	h.controlMu.Unlock()

	err := h.restoreDisplay(route)
	route.close()
	return err
}

func (h *Hub) endScreenSessionLocked() *controlRoute {
	h.screenActive = false
	h.screenHeaderReady = false
	h.displayOffDesired = false
	route := h.controlRoute
	h.controlRoute = nil
	return route
}

// restoreDisplay restores the display through the route that was active when
// the lifecycle ended. The caller holds displayMu, and this deliberately does
// not consult the route's displayOffSent flag: an earlier route may have sent
// the successful OFF.
func (h *Hub) restoreDisplay(route *controlRoute) error {
	if !h.displayPowerOff {
		return nil
	}
	if route == nil {
		return errNoControlRoute
	}
	if err := route.sendDisplayOn(); err != nil {
		return err
	}
	h.displayPowerOff = false
	return nil
}

// RegisterControlRoute installs a generation-scoped control writer. Replacing
// a live route retires the prior writer and replays display OFF before an end
// operation may observe the new route.
func (h *Hub) RegisterControlRoute(generation uint64, write func([]byte) error) (*controlRoute, error) {
	h.displayMu.Lock()
	defer h.displayMu.Unlock()
	h.controlMu.Lock()
	defer h.controlMu.Unlock()
	if !h.screenActive || generation != h.screenGeneration {
		return nil, nil
	}
	if previous := h.controlRoute; previous != nil {
		previous.close()
	}
	route := &controlRoute{generation: generation, write: write}
	h.controlRoute = route
	if !h.screenHeaderReady || !h.displayOffDesired {
		return route, nil
	}
	if err := route.sendDisplayOff(); err != nil {
		return route, err
	}
	h.displayPowerOff = true
	return route, nil
}

func (h *Hub) ClearControlRoute(route *controlRoute) {
	h.displayMu.Lock()
	defer h.displayMu.Unlock()
	h.controlMu.Lock()
	shouldClose := h.controlRoute == route
	if shouldClose {
		h.controlRoute = nil
	}
	h.controlMu.Unlock()
	if shouldClose {
		route.close()
	}
}

// WriteControl forwards ordinary control input through the active route.
func (h *Hub) WriteControl(data []byte) error {
	h.controlMu.Lock()
	route := h.controlRoute
	h.controlMu.Unlock()
	if route == nil {
		return errNoControlRoute
	}
	return route.writeControl(data)
}

func (r *controlRoute) writeControl(data []byte) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if r.closed {
		return errNoControlRoute
	}
	return r.write(data)
}

func (r *controlRoute) sendDisplayOff() error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if r.closed {
		return errNoControlRoute
	}
	if r.displayOffSent {
		return nil
	}
	if err := r.write([]byte{10, 0}); err != nil {
		return err
	}
	r.displayOffSent = true
	return nil
}

func (r *controlRoute) sendDisplayOn() error {
	if r == nil {
		return errNoControlRoute
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if r.closed {
		return errNoControlRoute
	}
	return r.write([]byte{10, 1})
}

func (r *controlRoute) close() {
	if r == nil {
		return
	}
	r.writeMu.Lock()
	r.closed = true
	r.writeMu.Unlock()
}

// BroadcastScreenFrame sends one H.264 access unit and retains the small set of
// decoder inputs needed to start a remote viewer that connects late.
func (h *Hub) BroadcastScreenFrame(generation uint64, data []byte) bool {
	h.controlMu.Lock()
	if !h.screenActive || generation != h.screenGeneration {
		h.controlMu.Unlock()
		return false
	}
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
	h.controlMu.Unlock()
	return true
}

func (h *Hub) forwardControl(data []byte) {
	if err := h.WriteControl(data); err != nil && !errors.Is(err, errNoControlRoute) {
		slog.Debug("Control write error", "error", err)
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
