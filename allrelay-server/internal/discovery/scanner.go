// Package discovery handles UDP-based phone discovery.
//
// Strategy: quick unicast scan of local subnet.
// Broadcast is unreliable (many Wi-Fi routers block client-to-client broadcast).
// Unicast to each IP in the /24 subnet takes ~2-3 seconds and always works.
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"syscall"
	"time"
)

const (
	beaconPort  = 5009
	queryStr    = `{"query":"allrelay"}`
	scanTimeout = 3 * time.Second
	// retransmitCadence spreads query packets across the scan window to beat
	// Wi-Fi power-save / doze drops. First packet at t=0, then at 300ms and 800ms.
	retransmitDelays = 3
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
// Returns discovered phones within ~scanTimeout. On configuration errors
// (no usable subnet, cannot bind socket) it returns an error so callers
// can surface it instead of serving stale cached results.
func (s *Scanner) Scan() ([]Phone, error) {
	// Reset cache every scan so a failed scan never returns stale phones.
	s.mu.Lock()
	s.phones = make(map[string]Phone)
	s.mu.Unlock()

	subnet, localIP := detectSubnet()
	if subnet == nil {
		slog.Warn("discovery: cannot detect local subnet")
		return nil, errors.New("cannot detect local subnet")
	}

	// Bind an ephemeral port (:0) with SO_REUSEADDR. We used to bind
	// beaconPort (5009) which collides with whatever else holds that port
	// (another scan-in-progress, a locally running phone responder, scrcpy
	// server on the host, etc.), causing the 2nd+ Scan to silently fall
	// back to an empty cache — the "must retry several times" symptom.
	// The phone replies to the source port of our query, which Go assigns
	// automatically, so we do not need to listen on 5009.
	lc := net.ListenConfig{Control: setReuseAddr}
	conn, err := lc.ListenPacket(context.Background(), "udp4", "0.0.0.0:0")
	if err != nil {
		slog.Warn("discovery: cannot open UDP socket", "error", err)
		return nil, fmt.Errorf("cannot open UDP socket: %w", err)
	}
	defer conn.Close()
	udpConn, ok := conn.(*net.UDPConn)
	if !ok {
		return nil, errors.New("opened socket is not a UDP connection")
	}

	query := []byte(queryStr)
	results := make(chan Phone, 256)
	var wg sync.WaitGroup

	// Use the real subnet mask (not a hardcoded /24) so /16, /8, VPN and
	// Docker networks are scanned correctly. Fall back to /24 only when the
	// detected mask is nil for some reason.
	mask := subnet.Mask
	if mask == nil {
		mask = net.CIDRMask(24, 32)
	}
	ones, bits := mask.Size()
	if bits == 0 {
		mask = net.CIDRMask(24, 32)
		ones = 24
	}
	// Guard against huge /8 networks that would spawn 16M goroutines.
	if ones < 16 {
		slog.Warn("discovery: subnet suspiciously broad, clamping to /16", "mask", mask.String())
		mask = net.CIDRMask(16, 32)
		ones = 16
	}
	baseIP := subnet.IP.Mask(mask)

	// Enumerate host addresses in the subnet.
	hostCount := (1 << (bits - ones)) - 2 // exclude network + broadcast
	if hostCount > 65534 {
		hostCount = 65534
	}

	start := time.Now()
	for i := 1; i <= hostCount+1; i++ {
		ip := incIP(baseIP, uint32(i))
		if ip.Equal(localIP) {
			continue // skip self
		}

		wg.Add(1)
		go func(target net.IP) {
			defer wg.Done()
			queryAddr := &net.UDPAddr{IP: target, Port: beaconPort}
			// Spread retransmits across the scan window. UDP on Wi-Fi is lossy
			// (power-save, doze, ARP misses), so a single burst at t=0 misses
			// phones that were briefly unreachable. Retransmitting at 0/300/800ms
			// keeps a fresh packet in flight while the collector is still open.
			for _, delay := range []time.Duration{0, 300 * time.Millisecond, 800 * time.Millisecond} {
				if delay > 0 {
					time.Sleep(delay)
				}
				udpConn.WriteToUDP(query, queryAddr)
			}
		}(ip)
	}

	// Close results channel once all senders are done so the collector loop
	// can drain remaining replies then exit on the read deadline.
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect responses until the deadline elapses (or the channel closes).
	buf := make([]byte, 512)
	deadline := time.Now().Add(scanTimeout)
	udpConn.SetReadDeadline(deadline)
	for {
		n, remote, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			break // timeout or socket error
		}
		var beacon Beacon
		if err := json.Unmarshal(buf[:n], &beacon); err != nil {
			continue
		}
		if beacon.Name == "" || beacon.Port == 0 {
			continue // ignore malformed replies
		}
		ip := remote.IP.String()
		s.mu.Lock()
		s.phones[ip] = Phone{Name: beacon.Name, IP: ip, Port: beacon.Port}
		s.mu.Unlock()
	}

	slog.Info("discovery: scan complete",
		"found", len(s.phones),
		"subnet", subnet.String(),
		"mask", mask.String(),
		"hosts", hostCount,
		"took", time.Since(start).Round(time.Millisecond))

	return s.List(), nil
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

// setReuseAddr sets SO_REUSEADDR (and SO_REUSEPORT on platforms that
// support it) so multiple scan sockets can coexist.
func setReuseAddr(network, address string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		if serr != nil {
			return
		}
		// SO_REUSEPORT (15) — best effort, ignored if unsupported.
		_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, 15, 1)
	})
	if err != nil {
		return err
	}
	return serr
}

// incIP returns base + n as a 4-byte IPv4.
func incIP(base net.IP, n uint32) net.IP {
	out := make(net.IP, 4)
	copy(out, base)
	// Add with carry over the 32-bit address.
	b := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	b += n
	out[0] = byte(b >> 24)
	out[1] = byte(b >> 16)
	out[2] = byte(b >> 8)
	out[3] = byte(b)
	return out
}

// detectSubnet finds the local IP and subnet for the primary network interface.
// It prefers the interface that owns the IPv4 default route, falling back to
// the first up, non-loopback, non-link-local interface. Docker bridges and
// virtual "br-*" interfaces are skipped.
func detectSubnet() (*net.IPNet, net.IP) {
	if ipnet, ip := detectDefaultRouteSubnet(); ipnet != nil {
		return ipnet, ip
	}

	interfaces, _ := net.Interfaces()
	var fallback *net.IPNet
	var fallbackIP net.IP
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
			// Skip Docker bridges and virtual interfaces.
			if iface.Name == "docker0" || (len(iface.Name) >= 3 && iface.Name[:3] == "br-") {
				continue
			}
			if ipnet.IP.IsPrivate() {
				return ipnet, ipnet.IP.To4()
			}
			if fallback == nil {
				fallback = ipnet
				fallbackIP = ipnet.IP.To4()
			}
		}
	}
	return fallback, fallbackIP
}

// detectDefaultRouteSubnet inspects the kernel IPv4 routing table and returns
// the subnet of the interface used for the default route (0.0.0.0/0). This
// avoids picking the wrong interface on multi-homed hosts (Wi-Fi + VPN +
// Docker bridges, etc.).
func detectDefaultRouteSubnet() (*net.IPNet, net.IP) {
	routes, err := readIPv4Routes()
	if err != nil {
		return nil, nil
	}
	var defaultIface string
	for _, r := range routes {
		if r.Dst == nil || r.Dst.IP.IsUnspecified() {
			defaultIface = r.Iface
			break
		}
	}
	if defaultIface == "" {
		return nil, nil
	}

	iface, err := net.InterfaceByName(defaultIface)
	if err != nil || iface == nil {
		return nil, nil
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, nil
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil && !ipnet.IP.IsLoopback() && !ipnet.IP.IsLinkLocalUnicast() {
			return ipnet, ipnet.IP.To4()
		}
	}
	return nil, nil
}