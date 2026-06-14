// Package discovery handles UDP-based phone discovery.
//
// Strategy: quick unicast scan of local subnet.
// Broadcast is unreliable (many Wi-Fi routers block client-to-client broadcast).
// Unicast to each IP in the /24 subnet takes ~2-3 seconds and always works.
package discovery

import (
	"encoding/json"
	"log/slog"
	"net"
	"sync"
	"time"
)

const (
	beaconPort  = 5009
	queryStr    = `{"query":"allrelay"}`
	scanTimeout = 2 * time.Second
)

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

// Scanner discovers phones via unicast UDP scan.
type Scanner struct {
	mu     sync.RWMutex
	phones map[string]Phone
}

// NewScanner creates a new scanner.
func NewScanner() *Scanner {
	return &Scanner{phones: make(map[string]Phone)}
}

// Scan finds phones on the local network via unicast probing.
// Returns discovered phones within ~2 seconds.
func (s *Scanner) Scan() []Phone {
	// Get local subnet
	subnet, localIP := detectSubnet()
	if subnet == nil {
		slog.Warn("discovery: cannot detect local subnet")
		return s.List()
	}

	// Open a single UDP socket for sending queries and receiving responses
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: beaconPort})
	if err != nil {
		slog.Warn("discovery: cannot listen", "port", beaconPort, "error", err)
		return s.List()
	}
	defer conn.Close()

	query := []byte(queryStr)
	results := make(chan Phone, 10)
	var wg sync.WaitGroup

	// Scan all IPs in the /24 subnet concurrently
	start := time.Now()
	mask := net.CIDRMask(24, 32)
	baseIP := subnet.IP.Mask(mask)
	for i := 1; i <= 254; i++ {
		ip := make(net.IP, 4)
		copy(ip, baseIP)
		ip[3] = byte(i)
		if ip.Equal(localIP) {
			continue // skip self
		}

		wg.Add(1)
		go func(target net.IP) {
			defer wg.Done()
			queryAddr := &net.UDPAddr{IP: target, Port: beaconPort}
			// Fire a few packets to improve reliability
			for attempt := 0; attempt < 2; attempt++ {
				conn.WriteToUDP(query, queryAddr)
				time.Sleep(5 * time.Millisecond)
			}
		}(ip)
	}

	// Start response collector
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect responses
	buf := make([]byte, 512)
	deadline := time.Now().Add(scanTimeout)
	conn.SetReadDeadline(deadline)
	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			break // timeout
		}
		var beacon Beacon
		if err := json.Unmarshal(buf[:n], &beacon); err != nil {
			continue
		}
		ip := remote.IP.String()
		s.mu.Lock()
		s.phones[ip] = Phone{Name: beacon.Name, IP: ip, Port: beacon.Port}
		s.mu.Unlock()
	}

	slog.Info("discovery: scan complete",
		"found", len(s.phones),
		"subnet", subnet.String(),
		"took", time.Since(start).Round(time.Millisecond))
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

// detectSubnet finds the local IP and subnet for the primary network interface.
func detectSubnet() (*net.IPNet, net.IP) {
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
				continue
			}
			// Skip Docker bridges and virtual interfaces
			if iface.Name == "docker0" || iface.Name[:3] == "br-" {
				continue
			}
			return ipnet, ipnet.IP.To4()
		}
	}
	return nil, nil
}
