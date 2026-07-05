//go:build linux

package discovery

import (
	"bufio"
	"net"
	"os"
	"strings"
)

type routeEntry struct {
	Dst  *net.IPNet
	Iface string
}

// readIPv4Routes parses /proc/net/route. Each line is:
//   Iface Destination Gateway Flags ... Mask MTU Window IRTT
// Destination and Mask are in hex (host byte order) without leading 0x.
func readIPv4Routes() ([]routeEntry, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var routes []routeEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	first := true
	for scanner.Scan() {
		line := scanner.Text()
		if first {
			first = false // header line
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		iface := fields[0]
		dstHex := fields[1]
		maskHex := fields[7]

		dst, mask, ok := parseHexRouteAddr(dstHex, maskHex)
		if !ok {
			continue
		}
		ipnet := &net.IPNet{IP: dst, Mask: mask}
		routes = append(routes, routeEntry{Dst: ipnet, Iface: iface})
	}
	if err := scanner.Err(); err != nil {
		return routes, err
	}
	return routes, nil
}

// parseHexRouteAddr parses the hex destination + mask from /proc/net/route.
// Values are little-endian host order, e.g. "0101000A" == 10.0.1.1.
func parseHexRouteAddr(dstHex, maskHex string) (net.IP, net.IPMask, bool) {
	dstInt, ok1 := parseHex32(dstHex)
	maskInt, ok2 := parseHex32(maskHex)
	if !ok1 || !ok2 {
		return nil, nil, false
	}
	ip := net.IPv4(byte(dstInt), byte(dstInt>>8), byte(dstInt>>16), byte(dstInt>>24))
	mask := net.IPv4Mask(byte(maskInt), byte(maskInt>>8), byte(maskInt>>16), byte(maskInt>>24))
	return ip, mask, true
}

func parseHex32(s string) (uint32, bool) {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		s = s[2:]
	}
	if len(s) != 8 {
		return 0, false
	}
	var v uint32
	for _, c := range s {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= uint32(c - '0')
		case c >= 'a' && c <= 'f':
			v |= uint32(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= uint32(c-'A') + 10
		default:
			return 0, false
		}
	}
	return v, true
}