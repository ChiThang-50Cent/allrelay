//go:build !linux

package discovery

import "net"

// readIPv4Routes is a no-op on non-Linux platforms; detectSubnet falls back
// to the simple interface enumeration path.
func readIPv4Routes() ([]routeEntry, error) {
	return nil, nil
}

// stub the routeEntry type so the file builds in isolation.
var _ = func() routeEntry { return routeEntry{Dst: (*net.IPNet)(nil)} }