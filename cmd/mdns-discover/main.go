// Package main implements a legacy mDNS discovery helper for AllRelay.
//
// The main product flow now uses the web dashboard's UDP subnet scan.
// This helper remains optional for environments where mDNS is useful.
//
// Usage:
//
//	mdns-discover              # List all AllRelay devices
//	mdns-discover --once       # Find first device and exit
//	mdns-discover --json       # Output as JSON
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/grandcat/zeroconf"
)

const (
	ServiceType   = "_allrelay._tcp"
	ServiceDomain = "local"
	LookupTimeout = 5 * time.Second
)

// Device represents a discovered AllRelay device.
type Device struct {
	Name         string   `json:"name"`
	Hostname     string   `json:"hostname"`
	IP           string   `json:"ip"`
	Port         int      `json:"port"`
	Version      string   `json:"version"`
	DeviceModel  string   `json:"device_model"`
	Streams      []string `json:"streams"`
	Manufacturer string   `json:"manufacturer"`
	DiscoveredAt string   `json:"discovered_at"`
}

func main() {
	once := flag.Bool("once", false, "Find first device and exit")
	jsonOutput := flag.Bool("json", false, "Output as JSON")
	timeout := flag.Duration("timeout", LookupTimeout, "Discovery timeout")
	verbose := flag.Bool("v", false, "Verbose output")
	flag.Parse()

	if *verbose {
		fmt.Fprintf(os.Stderr, "Searching for AllRelay devices on the network...\n")
		fmt.Fprintf(os.Stderr, "Service type: %s.%s\n\n", ServiceType, ServiceDomain)
	}

	devices, err := discoverDevices(*timeout, *verbose)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(devices) == 0 {
		if *verbose {
			fmt.Fprintf(os.Stderr, "No AllRelay devices found.\n")
		}
		if *once {
			os.Exit(1)
		}
		return
	}

	if *jsonOutput {
		printJSON(devices)
	} else {
		printTable(devices)
	}

	if *once {
		// Print the first device's address in a format suitable for --tunnel-host
		fmt.Printf("%s:%d\n", devices[0].IP, devices[0].Port)
	}
}

func discoverDevices(timeout time.Duration, verbose bool) ([]Device, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create resolver: %w", err)
	}

	entries := make(chan *zeroconf.ServiceEntry)
	devices := make([]Device, 0)
	done := make(chan struct{})

	go func(results <-chan *zeroconf.ServiceEntry) {
		for entry := range results {
			device := parseEntry(entry)
			if verbose {
				fmt.Fprintf(os.Stderr, "Found: %s (%s:%d)\n",
					device.Name, device.IP, device.Port)
			}
			devices = append(devices, device)
		}
		close(done)
	}(entries)

	ctx, cancel := contextWithTimeout(timeout)
	defer cancel()

	err = resolver.Browse(ctx, ServiceType, ServiceDomain, entries)
	if err != nil {
		return nil, fmt.Errorf("browse failed: %w", err)
	}

	<-done
	return devices, nil
}

func parseEntry(entry *zeroconf.ServiceEntry) Device {
	device := Device{
		Name:         entry.Instance,
		Hostname:     entry.HostName,
		Port:         entry.Port,
		DiscoveredAt: time.Now().Format(time.RFC3339),
	}

	// Get IP address (prefer IPv4)
	for _, addr := range entry.AddrIPv4 {
		device.IP = addr.String()
		break
	}
	if device.IP == "" {
		for _, addr := range entry.AddrIPv6 {
			device.IP = addr.String()
			break
		}
	}

	// Parse TXT records
	for _, txt := range entry.Text {
		parts := strings.SplitN(txt, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := parts[0], parts[1]
		switch key {
		case "version":
			device.Version = value
		case "device":
			device.DeviceModel = value
		case "streams":
			device.Streams = strings.Split(value, ",")
		case "manufacturer":
			device.Manufacturer = value
		case "port":
			// Override port from TXT record if present
			var port int
			if _, err := fmt.Sscanf(value, "%d", &port); err == nil && port > 0 {
				device.Port = port
			}
		}
	}

	return device
}

func printTable(devices []Device) {
	fmt.Printf("%-30s %-18s %-6s %-20s %s\n",
		"NAME", "IP", "PORT", "MODEL", "STREAMS")
	fmt.Printf("%s\n", strings.Repeat("-", 90))

	for _, d := range devices {
		name := d.Name
		if len(name) > 28 {
			name = name[:25] + "..."
		}
		streams := strings.Join(d.Streams, ",")
		if len(streams) > 18 {
			streams = streams[:15] + "..."
		}
		fmt.Printf("%-30s %-18s %-6d %-20s %s\n",
			name, d.IP, d.Port, d.DeviceModel, streams)
	}
}

func printJSON(devices []Device) {
	data, err := json.MarshalIndent(devices, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "JSON error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

func contextWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	// Use a simple channel-based context for compatibility
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return ctx, cancel
}

func init() {
	// Handle SIGINT/SIGTERM for clean shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		os.Exit(0)
	}()
}
