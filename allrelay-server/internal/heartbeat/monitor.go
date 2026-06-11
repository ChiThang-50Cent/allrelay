// Package heartbeat implements UDP heartbeat monitoring between the
// Ubuntu server and the Android device.
//
// The Android device sends status updates via UDP on port 5005 every
// second. The heartbeat monitor tracks connection health and device
// statistics (battery, CPU, Wi-Fi signal).
//
// If no heartbeat is received within the timeout period, the connection
// is considered lost and reconnection is triggered.
//
// Message format (from SPEC.md §5.6):
//
//	{
//	  "ts": 1717920000000,
//	  "stream_stats": {...},
//	  "device": {
//	    "battery": 78,
//	    "cpu_usage": 25.3,
//	    "wifi_rssi": -45,
//	    "wifi_link_speed": 866
//	  }
//	}
package heartbeat

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Default heartbeat configuration.
const (
	DefaultPort       = 5005
	DefaultTimeout    = 5 * time.Second
	DefaultBufferSize = 2048
)

// DeviceStatus holds device health metrics from the heartbeat.
type DeviceStatus struct {
	Timestamp     int64   `json:"ts"`
	Battery       int     `json:"battery"`
	CPUUsage      float64 `json:"cpu_usage"`
	WiFiRSSI      int     `json:"wifi_rssi"`
	WiFiLinkSpeed int     `json:"wifi_link_speed"`
	CPUTemp       float64 `json:"cpu_temp,omitempty"`
}

// StreamStats holds per-stream statistics.
type StreamStats struct {
	FPS           int     `json:"fps,omitempty"`
	Bitrate       int64   `json:"bitrate,omitempty"`
	DroppedFrames int     `json:"dropped_frames,omitempty"`
	JitterMs      float64 `json:"jitter_ms,omitempty"`
	PacketLoss    float64 `json:"packet_loss,omitempty"`
	BufferOverruns int    `json:"buffer_overruns,omitempty"`
}

// FullStatus combines device and stream status.
type FullStatus struct {
	Device       DeviceStatus            `json:"device"`
	StreamStats  map[string]StreamStats  `json:"stream_stats"`
	LastSeen     time.Time               `json:"-"`
}

// Monitor tracks UDP heartbeat messages from the Android device.
type Monitor struct {
	conn     *net.UDPConn
	status   FullStatus
	lastBeat time.Time
	timeout  time.Duration
	done     chan struct{}
	updateCh chan FullStatus
	mu       sync.RWMutex
}

// NewMonitor creates a new heartbeat monitor listening on the given UDP port.
func NewMonitor(port int) (*Monitor, error) {
	addr := &net.UDPAddr{
		IP:   net.IPv4zero,
		Port: port,
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen UDP :%d: %w", port, err)
	}

	// Set read buffer
	conn.SetReadBuffer(DefaultBufferSize)

	m := &Monitor{
		conn:     conn,
		timeout:  DefaultTimeout,
		done:     make(chan struct{}),
		updateCh: make(chan FullStatus, 16),
	}

	go m.receive()

	slog.Info("Heartbeat monitor started", "port", port)
	return m, nil
}

// receive reads UDP heartbeat messages in a loop.
func (m *Monitor) receive() {
	defer close(m.updateCh)

	buf := make([]byte, DefaultBufferSize)

	for {
		select {
		case <-m.done:
			return
		default:
		}

		// Set deadline for each read
		m.conn.SetReadDeadline(time.Now().Add(1 * time.Second))

		n, _, err := m.conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			if err.Error() == "use of closed network connection" {
				return
			}
			slog.Error("heartbeat read error", "error", err)
			continue
		}

		// Parse JSON status
		var raw struct {
			TS          int64  `json:"ts"`
			Device      struct {
				Battery       int     `json:"battery"`
				CPUUsage      float64 `json:"cpu_usage"`
				WiFiRSSI      int     `json:"wifi_rssi"`
				WiFiLinkSpeed int     `json:"wifi_link_speed"`
				CPUTemp       float64 `json:"cpu_temp"`
			} `json:"device"`
			StreamStats map[string]StreamStats `json:"stream_stats"`
		}

		if err := json.Unmarshal(buf[:n], &raw); err != nil {
			slog.Debug("heartbeat: invalid JSON", "error", err)
			continue
		}

		now := time.Now()
		status := FullStatus{
			Device: DeviceStatus{
				Timestamp:     raw.TS,
				Battery:       raw.Device.Battery,
				CPUUsage:      raw.Device.CPUUsage,
				WiFiRSSI:      raw.Device.WiFiRSSI,
				WiFiLinkSpeed: raw.Device.WiFiLinkSpeed,
				CPUTemp:       raw.Device.CPUTemp,
			},
			StreamStats: raw.StreamStats,
			LastSeen:    now,
		}

		m.mu.Lock()
		m.status = status
		m.lastBeat = now
		m.mu.Unlock()

		// Send update to channel (non-blocking)
		select {
		case m.updateCh <- status:
		default:
		}

		slog.Debug("heartbeat received",
			"battery", status.Device.Battery,
			"rssi", status.Device.WiFiRSSI,
			"cpu", status.Device.CPUUsage)
	}
}

// Status returns the latest device status.
func (m *Monitor) Status() FullStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

// LastBeat returns the time of the last received heartbeat.
func (m *Monitor) LastBeat() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastBeat
}

// IsAlive returns true if a heartbeat has been received within the timeout period.
func (m *Monitor) IsAlive() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.lastBeat.IsZero() {
		return true // No heartbeat yet, not necessarily dead
	}
	return time.Since(m.lastBeat) < m.timeout
}

// Updates returns a channel of status updates for display/logging.
func (m *Monitor) Updates() <-chan FullStatus {
	return m.updateCh
}

// SetTimeout sets the heartbeat timeout duration.
func (m *Monitor) SetTimeout(d time.Duration) {
	m.timeout = d
}

// Close stops the heartbeat monitor.
func (m *Monitor) Close() error {
	select {
	case <-m.done:
		return nil
	default:
		close(m.done)
	}
	if m.conn != nil {
		return m.conn.Close()
	}
	return nil
}

// FormatStatus returns a human-readable status string.
func FormatStatus(s FullStatus) string {
	batteryIcon := batteryIcon(s.Device.Battery)
	wifiIcon := wifiIcon(s.Device.WiFiRSSI)

	return fmt.Sprintf(
		"%s %d%% | %s %d dBm | CPU %.1f%%",
		batteryIcon, s.Device.Battery,
		wifiIcon, s.Device.WiFiRSSI,
		s.Device.CPUUsage,
	)
}

// batteryIcon returns an emoji based on battery percentage.
func batteryIcon(pct int) string {
	switch {
	case pct >= 90:
		return "🔋"
	case pct >= 60:
		return "🔋"
	case pct >= 30:
		return "🪫"
	default:
		return "🪫"
	}
}

// wifiIcon returns an emoji based on Wi-Fi RSSI.
func wifiIcon(rssi int) string {
	switch {
	case rssi >= -50:
		return "📶" // Excellent
	case rssi >= -60:
		return "📶" // Good
	case rssi >= -70:
		return "📶" // Fair
	default:
		return "📡" // Weak
	}
}
