// Package discovery handles UDP-based phone discovery.
//
// Protocol:
//   1. PC broadcasts a "who is there" query to 255.255.255.255:5009
//   2. Phone receives query, responds with unicast JSON to source IP:5009
//   3. PC collects responses
//
// This approach avoids broadcast issues (many routers block client→server
// broadcast on Wi-Fi). Server-side (PC) broadcast is more reliable.
package discovery

import (
	"encoding/json"
	"log/slog"
	"net"
	"sync"
	"time"
)

// BeaconPort is the UDP port for discovery.
const BeaconPort = 5009

// Phone represents a discovered AllRelay phone.
type Phone struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

// Beacon is the UDP message format.
type Beacon struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

// Scanner discovers AllRelay phones via UDP query/response.
type Scanner struct {
	mu      sync.RWMutex
	phones  map[string]Phone
	timeout time.Duration
}

// NewScanner creates a new discovery scanner.
func NewScanner() *Scanner {
	return &Scanner{
		phones:  make(map[string]Phone),
		timeout: 3 * time.Second,
	}
}

// Scan sends a broadcast query and waits for responses.
func (s *Scanner) Scan() []Phone {
	addr := &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: BeaconPort}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		slog.Warn("discovery: cannot listen", "port", BeaconPort, "error", err)
		return s.List()
	}
	defer conn.Close()

	// Send broadcast query to all local subnets
	// 255.255.255.255 is often blocked by routers, so we target
	// each local interface's subnet broadcast address (e.g., 192.168.1.255).
	query := []byte(`{"query":"allrelay"}`)
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil || ipnet.IP.IsLoopback() {
				continue
			}
			// Calculate broadcast: IP | ^mask
			broadcastIP := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				broadcastIP[i] = ipnet.IP.To4()[i] | ^ipnet.Mask[i]
			}
			broadcast := &net.UDPAddr{IP: broadcastIP, Port: BeaconPort}
			slog.Debug("discovery: broadcasting to", "iface", iface.Name, "broadcast", broadcastIP.String())
			for i := 0; i < 3; i++ {
				conn.WriteToUDP(query, broadcast)
				time.Sleep(200 * time.Millisecond)
			}
		}
	}

	// Listen for responses
	conn.SetReadDeadline(time.Now().Add(s.timeout))
	buf := make([]byte, 512)
	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
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
		slog.Debug("discovery: found", "name", beacon.Name, "ip", ip, "port", beacon.Port)
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
