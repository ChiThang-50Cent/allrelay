package web

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/allrelay/allrelay-server/internal/discovery"
)

// ServerConfig holds web server configuration
type ServerConfig struct {
	Port  int
	Host  string
	Debug bool
}

// DefaultConfig returns default web server config
func DefaultConfig() ServerConfig {
	return ServerConfig{
		Port:  8080,
		Host:  "0.0.0.0",
		Debug: false,
	}
}

// PhoneDevice represents a discovered phone
type PhoneDevice struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	IP        string    `json:"ip"`
	Ports     []int     `json:"ports"`
	Connected bool      `json:"connected"`
	LastSeen  time.Time `json:"lastSeen"`
}

// StreamStatus represents the status of a stream
type StreamStatus struct {
	Name      string `json:"name"`
	Port      int    `json:"port"`
	Active    bool   `json:"active"`
	FPS       int    `json:"fps,omitempty"`
	Bitrate   int    `json:"bitrate,omitempty"`
	Latency   int    `json:"latency,omitempty"` // ms
	BytesSent int64  `json:"bytesSent,omitempty"`
	Frames    int64  `json:"frames,omitempty"`
}

// ConnectionStatus represents the full connection status
type ConnectionStatus struct {
	Phone     *PhoneDevice   `json:"phone"`
	Streams   []StreamStatus `json:"streams"`
	Connected bool           `json:"connected"`
}

// WebServer manages the web UI and API
type WebServer struct {
	config      ServerConfig
	phones      map[string]*PhoneDevice
	currentConn *ConnectionStatus
	hub         *Hub
	controller  *ServerController
	scanner     *discovery.Scanner
	mu          sync.RWMutex
	httpServer  *http.Server
}

// NewWebServer creates a new web server instance
func NewWebServer(config ServerConfig) *WebServer {
	hub := NewHub()
	go hub.Run()

	ws := &WebServer{
		config:  config,
		phones:  make(map[string]*PhoneDevice),
		hub:     hub,
		scanner: discovery.NewScanner(),
		currentConn: &ConnectionStatus{
			Streams: []StreamStatus{
				{Name: "screen", Port: 5000},
				{Name: "camera", Port: 5001},
				{Name: "mic", Port: 5002},
				{Name: "speaker", Port: 5003},
			},
		},
	}

	// Create controller
	ws.controller = NewServerController(ws)

	return ws
}

// Start begins serving the web UI
func (ws *WebServer) Start() error {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("/api/phones", ws.handlePhones)
	mux.HandleFunc("/api/phones/scan", ws.handleScanPhones)
	mux.HandleFunc("/api/connect", ws.handleConnect)
	mux.HandleFunc("/api/disconnect", ws.handleDisconnect)
	mux.HandleFunc("/api/status", ws.handleStatus)
	mux.HandleFunc("/api/streams/toggle", ws.handleToggleStream)
	mux.HandleFunc("/api/streams/metrics", ws.handleStreamMetrics)

	// WebSocket endpoint
	mux.HandleFunc("/ws", ws.HandleWebSocket)

	// Remote control page
	mux.HandleFunc("/remote", ws.handleRemote)

	// Static files (CSS, JS, images)
	staticPaths := []string{
		"/usr/share/allrelay/static",
		"allrelay-server/internal/web/static",
		"internal/web/static",
		"../allrelay-server/internal/web/static",
		"static",
	}

	var staticDir string
	for _, path := range staticPaths {
		if _, err := os.Stat(path); err == nil {
			staticDir = path
			break
		}
	}

	if staticDir != "" {
		fs := http.FileServer(http.Dir(staticDir))
		mux.Handle("/static/", http.StripPrefix("/static/", fs))
		slog.Debug("Static files", "dir", staticDir)
	}

	// Main page
	mux.HandleFunc("/", ws.handleIndex)

	addr := fmt.Sprintf("%s:%d", ws.config.Host, ws.config.Port)
	ws.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	slog.Info("Web UI starting", "address", addr)
	return ws.httpServer.ListenAndServe()
}

// Stop gracefully stops the web server
func (ws *WebServer) Stop() error {
	if ws.httpServer != nil {
		return ws.httpServer.Close()
	}
	return nil
}

// GetController returns the server controller
func (ws *WebServer) GetController() *ServerController {
	return ws.controller
}

// Hub returns the WebSocket hub for broadcasting messages
func (ws *WebServer) Hub() *Hub {
	return ws.hub
}
func (ws *WebServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	ws.serveTemplate(w, r, "index.html")
}

func (ws *WebServer) handleRemote(w http.ResponseWriter, r *http.Request) {
	ws.serveTemplate(w, r, "remote.html")
}

func (ws *WebServer) serveTemplate(w http.ResponseWriter, r *http.Request, name string) {
	// Try multiple paths for flexibility
	paths := []string{
		"/usr/share/allrelay/templates/" + name,
		"allrelay-server/internal/web/templates/" + name,
		"internal/web/templates/" + name,
		"../allrelay-server/internal/web/templates/" + name,
		"templates/" + name,
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			slog.Debug("Serving template", "path", path)
			http.ServeFile(w, r, path)
			return
		}
	}

	slog.Error("Template not found", "name", name, "tried", paths)
	http.Error(w, "Template not found", http.StatusInternalServerError)
}

// handlePhones returns list of discovered phones
func (ws *WebServer) handlePhones(w http.ResponseWriter, r *http.Request) {
	ws.mu.RLock()
	defer ws.mu.RUnlock()

	phones := make([]*PhoneDevice, 0, len(ws.phones))
	for _, p := range ws.phones {
		phones = append(phones, p)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(phones)
}

// handleScanPhones triggers an mDNS network scan for AllRelay phones.
// Android phones advertise as _allrelay._tcp on port 5000.
func (ws *WebServer) handleScanPhones(w http.ResponseWriter, r *http.Request) {
	results := ws.scanner.Scan()

	phones := make([]PhoneDevice, 0, len(results))
	ws.mu.Lock()
	for _, p := range results {
		id := p.IP + ":" + itoa(p.Port)
		device := PhoneDevice{
			ID:       id,
			Name:     p.Name,
			IP:       p.IP,
			Ports:    []int{p.Port},
			LastSeen: time.Now(),
		}
		// Preserve connected status
		if existing, ok := ws.phones[id]; ok {
			device.Connected = existing.Connected
		}
		ws.phones[id] = &device
		phones = append(phones, device)
	}
	ws.mu.Unlock()

	if phones == nil {
		phones = []PhoneDevice{} // return [] not null
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(phones)
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}

// handleConnect connects to a phone
func (ws *WebServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		IP   string `json:"ip"`
		Port int    `json:"port"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Use controller to actually connect
	if ws.controller != nil {
		if err := ws.controller.Connect(req.IP, req.Port); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "connected"})
}

// handleDisconnect disconnects from current phone
func (ws *WebServer) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	// Use controller to actually disconnect
	if ws.controller != nil {
		if err := ws.controller.Disconnect(); err != nil {
			slog.Error("Disconnect error", "error", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "disconnected"})
}

// handleStatus returns current connection status
func (ws *WebServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	// Sync stream states from controller if available
	if ws.controller != nil {
		ws.controller.SyncStreamStatus(ws.currentConn.Streams)
	}

	ws.mu.RLock()
	defer ws.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ws.currentConn)
}

// handleToggleStream toggles a stream on/off
func (ws *WebServer) handleToggleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Stream string `json:"stream"`
		Active bool   `json:"active"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Use controller to toggle stream
	if ws.controller != nil {
		if err := ws.controller.ToggleStream(req.Stream, req.Active); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	// Update local state
	ws.mu.Lock()
	var updatedStream StreamStatus
	for i, s := range ws.currentConn.Streams {
		if s.Name == req.Stream {
			ws.currentConn.Streams[i].Active = req.Active
			updatedStream = ws.currentConn.Streams[i]
			break
		}
	}
	ws.mu.Unlock()

	// Broadcast update to all WebSocket clients
	ws.hub.BroadcastStreamUpdate(updatedStream)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// handleStreamMetrics returns detailed stream metrics
func (ws *WebServer) handleStreamMetrics(w http.ResponseWriter, r *http.Request) {
	ws.mu.RLock()
	defer ws.mu.RUnlock()

	metrics := make(map[string]interface{})
	for _, s := range ws.currentConn.Streams {
		metrics[s.Name] = map[string]interface{}{
			"active":  s.Active,
			"fps":     s.FPS,
			"bitrate": s.Bitrate,
			"latency": s.Latency,
			"bytes":   s.BytesSent,
			"frames":  s.Frames,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

// UpdateStreamMetrics updates metrics for a specific stream
func (ws *WebServer) UpdateStreamMetrics(name string, fps, bitrate, latency int, bytesSent, frames int64) {
	ws.mu.Lock()
	for i, s := range ws.currentConn.Streams {
		if s.Name == name {
			ws.currentConn.Streams[i].FPS = fps
			ws.currentConn.Streams[i].Bitrate = bitrate
			ws.currentConn.Streams[i].Latency = latency
			ws.currentConn.Streams[i].BytesSent = bytesSent
			ws.currentConn.Streams[i].Frames = frames
			break
		}
	}
	ws.mu.Unlock()

	// Broadcast updated metrics
	ws.hub.BroadcastStatus(ws.currentConn)
}

// SetConnectionStatus updates the connection status
func (ws *WebServer) SetConnectionStatus(connected bool, phone *PhoneDevice) {
	ws.mu.Lock()
	ws.currentConn.Connected = connected
	ws.currentConn.Phone = phone
	ws.mu.Unlock()

	// Broadcast status change
	ws.hub.BroadcastStatus(ws.currentConn)
}

// AddPhone adds a phone to the discovered list
func (ws *WebServer) AddPhone(phone *PhoneDevice) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.phones[phone.ID] = phone
}

// RemovePhone removes a phone from the discovered list
func (ws *WebServer) RemovePhone(id string) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	delete(ws.phones, id)
}
