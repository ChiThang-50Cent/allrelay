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
	hub  *Hub
	conn *websocket.Conn
	send chan []byte  // text messages (JSON status, events)
	sendBin chan []byte  // binary messages (H.264 NAL units for screen)
}

// Hub maintains the set of active clients and broadcasts messages
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex

	// OnControl is called when a client sends a control message (touch, key, etc.)
	OnControl func(data []byte)
}

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
			h.mu.Unlock()
			slog.Debug("WebSocket client connected", "total", len(h.clients))

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				close(client.sendBin)
			}
			h.mu.Unlock()
			slog.Debug("WebSocket client disconnected", "total", len(h.clients))

		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

// BroadcastBinary sends raw binary data (e.g., H.264 NAL units) to all clients.
func (h *Hub) BroadcastBinary(data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for client := range h.clients {
		select {
		case client.sendBin <- data:
		default:
			// Client buffer full, drop this frame
		}
	}
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
		hub:     ws.hub,
		conn:    conn,
		send:    make(chan []byte, 256),
		sendBin: make(chan []byte, 64),
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
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Error("WebSocket read error", "error", err)
			}
			break
		}

		// Handle incoming messages
		var msg WSMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			slog.Error("Invalid WebSocket message", "error", err)
			continue
		}

		// Process message based on type
		switch msg.Type {
		case "ping":
			c.send <- []byte(`{"type":"pong"}`)
		case "control":
			// Forward control message to Android via the hub callback
			if c.hub.OnControl != nil {
				data, _ := json.Marshal(msg.Data)
				c.hub.OnControl(data)
			}
		}
	}
}

// writePump writes messages to the WebSocket connection
func (c *Client) writePump() {
	defer c.conn.Close()

	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case data, ok := <-c.sendBin:
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				return
			}
		}
	}
}
