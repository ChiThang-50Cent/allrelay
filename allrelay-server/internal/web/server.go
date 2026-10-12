package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/allrelay/allrelay-server/internal/discovery"
)

// ServerConfig holds web server configuration
type ServerConfig struct {
	Port    int
	Host    string
	Debug   bool
	URLFile string
}

// DefaultConfig returns default web server config
func DefaultConfig() ServerConfig {
	return ServerConfig{
		Port:    8080,
		Host:    "127.0.0.1",
		Debug:   false,
		URLFile: "",
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
	ADB       ADBStatus      `json:"adb"`
}

// WebServer manages the web UI and API
type WebServer struct {
	config           ServerConfig
	phones           map[string]*PhoneDevice
	currentConn      *ConnectionStatus
	hub              *Hub
	controller       *ServerController
	scanner          *discovery.Scanner
	mu               sync.RWMutex
	httpServer       *http.Server
	listener         net.Listener
	adbMonitorCancel context.CancelFunc
	adbMonitorID     uint64
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

func (ws *WebServer) setADBStatus(status ADBStatus) {
	ws.mu.Lock()
	ws.currentConn.ADB = status
	snapshot := ws.snapshotStatusLocked()
	ws.mu.Unlock()

	ws.hub.BroadcastStatus(&snapshot)
}

func (ws *WebServer) resetADBStatus() {
	ws.setADBStatus(ADBStatus{})
}

func (ws *WebServer) requireSameOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if !checkSameOrigin(r) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

// Listen sets up routes and starts the TCP listener.
func (ws *WebServer) Listen() error {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("/api/phones", ws.handlePhones)
	mux.HandleFunc("/api/phones/scan", ws.requireSameOrigin(ws.handleScanPhones))
	mux.HandleFunc("/api/connect", ws.requireSameOrigin(ws.handleConnect))
	mux.HandleFunc("/api/disconnect", ws.requireSameOrigin(ws.handleDisconnect))
	mux.HandleFunc("/api/status", ws.handleStatus)
	mux.HandleFunc("/api/streams/toggle", ws.requireSameOrigin(ws.handleToggleStream))
	mux.HandleFunc("/api/streams/metrics", ws.handleStreamMetrics)
	mux.HandleFunc("/api/adb/connect", ws.requireSameOrigin(ws.handleADBConnect))
	mux.HandleFunc("/api/adb/disconnect", ws.requireSameOrigin(ws.handleADBDisconnect))
	mux.HandleFunc("/api/adb/status", ws.handleADBStatus)

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

	addr := net.JoinHostPort(ws.config.Host, strconv.Itoa(ws.config.Port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	if tcpAddr, ok := listener.Addr().(*net.TCPAddr); ok {
		ws.config.Port = tcpAddr.Port
	}

	ws.listener = listener
	ws.httpServer = &http.Server{
		Addr:    listener.Addr().String(),
		Handler: mux,
	}

	url := ws.URL()
	if ws.config.URLFile != "" {
		if err := os.MkdirAll(filepath.Dir(ws.config.URLFile), 0o755); err != nil {
			listener.Close()
			return err
		}
		if err := os.WriteFile(ws.config.URLFile, []byte(url+"\n"), 0o644); err != nil {
			listener.Close()
			return err
		}
	}

	return nil
}

// Serve accepts incoming connections on the listener.
func (ws *WebServer) Serve() error {
	if ws.listener == nil || ws.httpServer == nil {
		return fmt.Errorf("web server not initialized; call Listen first")
	}
	url := ws.URL()
	slog.Info("Web UI starting", "address", ws.listener.Addr().String(), "url", url)
	return ws.httpServer.Serve(ws.listener)
}

// Start begins serving the web UI (listens then serves).
func (ws *WebServer) Start() error {
	if err := ws.Listen(); err != nil {
		return err
	}
	return ws.Serve()
}

// Stop gracefully stops the web server
func (ws *WebServer) Stop() error {
	ws.stopADBMonitor()
	if ws.controller != nil {
		_ = ws.controller.Disconnect()
	}
	if ws.hub != nil {
		ws.hub.Stop()
	}
	if ws.httpServer != nil {
		return ws.httpServer.Close()
	}
	if ws.listener != nil {
		return ws.listener.Close()
	}
	return nil
}

// URL returns the current local web UI URL.
func (ws *WebServer) URL() string {
	ws.mu.RLock()
	defer ws.mu.RUnlock()

	host := ws.config.Host
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(ws.config.Port)))
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

// handleScanPhones triggers a UDP subnet scan for AllRelay phones.
// Android phones listen for discovery queries on UDP port 5009 and reply
// with their name/base TCP port.
func (ws *WebServer) handleScanPhones(w http.ResponseWriter, r *http.Request) {
	results, err := ws.scanner.Scan()
	if err != nil {
		slog.Warn("scan failed", "error", err)
		http.Error(w, fmt.Sprintf("scan failed: %v", err), http.StatusInternalServerError)
		return
	}

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

	ws.stopADBMonitor()
	// Use controller to actually connect
	if ws.controller != nil {
		if err := ws.controller.Connect(req.IP, req.Port); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	status := ws.queryADBStatus()
	ws.setADBStatus(status)
	if shouldHeartbeatADB(status) {
		ws.startADBMonitor()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "connected"})
}

// handleDisconnect disconnects from current phone
func (ws *WebServer) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ws.stopADBMonitor()
	// Use controller to actually disconnect
	if ws.controller != nil {
		if err := ws.controller.Disconnect(); err != nil {
			slog.Error("Disconnect error", "error", err)
		}
	}
	ws.resetADBStatus()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "disconnected"})
}

// snapshotStatus returns a detached copy of ConnectionStatus.
// Controller is authoritative for stream state and metrics.
func (ws *WebServer) snapshotStatus() ConnectionStatus {
	var streams []StreamStatus
	if ws.controller != nil {
		streams = ws.controller.GetStreamStatus()
	}

	ws.mu.Lock()
	defer ws.mu.Unlock()

	if len(streams) > 0 {
		ws.currentConn.Streams = streams
	}

	return ws.snapshotStatusLocked()
}

func (ws *WebServer) snapshotStatusLocked() ConnectionStatus {
	status := ConnectionStatus{
		Connected: ws.currentConn.Connected,
		ADB:       ws.currentConn.ADB,
	}
	if ws.currentConn.Phone != nil {
		phone := *ws.currentConn.Phone
		phone.Connected = ws.currentConn.Connected
		status.Phone = &phone
	}
	status.Streams = make([]StreamStatus, len(ws.currentConn.Streams))
	copy(status.Streams, ws.currentConn.Streams)
	return status
}

// publishCanonicalStatus takes a snapshot and broadcasts it via WebSocket.
func (ws *WebServer) publishCanonicalStatus() {
	snapshot := ws.snapshotStatus()
	ws.hub.BroadcastStatus(&snapshot)
}

// SetStreamActive updates active state for a specific stream and broadcasts canonical snapshot.
func (ws *WebServer) SetStreamActive(name string, active bool) {
	ws.publishCanonicalStatus()
}

// handleStatus returns current connection status
func (ws *WebServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := ws.snapshotStatus()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// handleStreamMetrics returns detailed stream metrics matching handleStatus.
func (ws *WebServer) handleStreamMetrics(w http.ResponseWriter, r *http.Request) {
	status := ws.snapshotStatus()

	metrics := make(map[string]interface{}, len(status.Streams))
	for _, s := range status.Streams {
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
	if ws.controller != nil {
		ws.controller.mu.Lock()
		if s, ok := ws.controller.streams[name]; ok {
			s.FPS = fps
			s.Bitrate = bitrate
			s.Latency = latency
			s.Bytes = bytesSent
			s.Frames = frames
		}
		ws.controller.mu.Unlock()
	}
	ws.publishCanonicalStatus()
}

// SetConnectionStatus updates the connection status
func (ws *WebServer) SetConnectionStatus(connected bool, phone *PhoneDevice) {
	ws.mu.Lock()
	ws.currentConn.Connected = connected
	if phone != nil {
		phoneCopy := *phone
		phoneCopy.Connected = connected
		ws.currentConn.Phone = &phoneCopy
		if p, ok := ws.phones[phone.ID]; ok {
			p.Connected = connected
		}
	} else {
		if ws.currentConn.Phone != nil {
			ws.currentConn.Phone.Connected = false
		}
		ws.currentConn.Phone = nil
	}
	snapshot := ws.snapshotStatusLocked()
	ws.mu.Unlock()

	// Broadcast status change
	ws.hub.BroadcastStatus(&snapshot)
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
