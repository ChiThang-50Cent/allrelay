// Package discovery handles UDP beacon-based phone discovery.
// Android phones broadcast a UDP packet with their info every 5 seconds.
// The PC listens for these broadcasts to discover phones.
package discovery

import (
	"encoding/json"
	"log/slog"
	"net"
	"sync"
	"time"
)

// BeaconPort is the UDP port for phone discovery broadcasts.
const BeaconPort = 5009

// Phone represents a discovered AllRelay phone.
type Phone struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

// Beacon is the UDP message sent by the phone.
type Beacon struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

// Scanner discovers AllRelay phones via UDP beacon.
type Scanner struct {
	mu      sync.RWMutex
	phones  map[string]Phone // keyed by IP
	conn    *net.UDPConn
	timeout time.Duration
}

// NewScanner creates a new UDP beacon scanner.
func NewScanner() *Scanner {
	return &Scanner{
		phones:  make(map[string]Phone),
		timeout: 3 * time.Second,
	}
}

// Scan listens for UDP beacons for timeout duration.
func (s *Scanner) Scan() []Phone {
	addr := &net.UDPAddr{Port: BeaconPort}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		slog.Warn("beacon: cannot listen", "port", BeaconPort, "error", err)
		return s.List()
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(s.timeout))

	buf := make([]byte, 512)
	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			break // timeout or error
		}

		var beacon Beacon
		if err := json.Unmarshal(buf[:n], &beacon); err != nil {
			continue
		}

		ip := remote.IP.String()
		s.mu.Lock()
		s.phones[ip] = Phone{
			Name: beacon.Name,
			IP:   ip,
			Port: beacon.Port,
		}
		s.mu.Unlock()
		slog.Debug("beacon: discovered", "name", beacon.Name, "ip", ip, "port", beacon.Port)
	}

	return s.List()
}

// List returns all discovered phones.
func (s *Scanner) List() []Phone {
	s.mu.RLock()
	defer s.mu.RUnlock()
	phones := make([]Phone, 0, len(s.phones))
	for _, p := range s.phones {
		phones = append(phones, p)
	}
	return phones
}
