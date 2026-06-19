// AllRelay Server — Ubuntu-side multi-stream receiver and router.
//
// Connects to an Android phone running the AllRelay server over Wi-Fi,
// receives all streams (screen, camera, mic, speaker), and routes them
// to the appropriate outputs:
//
//	Screen  → SDL2 window display (Phase 3)
//	Camera  → v4l2loopback virtual device (Phase 3)
//	Mic     → PipeWire virtual source
//	Speaker → PipeWire virtual sink → Opus encode → send to phone
//
// Usage:
//
//	allrelay-server --web-port 9090
//	allrelay-server --web-host 127.0.0.1 --web-port 0 --web-url-file /tmp/allrelay.url
//	allrelay-server --host 192.168.1.100
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/allrelay/allrelay-server/internal/web"
)

func main() {
	host := flag.String("host", "", "Phone IP address (direct mode)")
	webHost := flag.String("web-host", "0.0.0.0", "Web UI host/interface")
	webPort := flag.Int("web-port", 9090, "Web UI port (use 0 for auto-select)")
	webURLFile := flag.String("web-url-file", "", "Write actual Web UI URL to this file after startup")
	verbose := flag.Bool("v", false, "Verbose debug output")
	flag.Parse()

	if *verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	} else {
		slog.SetLogLoggerLevel(slog.LevelInfo)
	}

	// Create web server with integrated controller
	webConfig := web.DefaultConfig()
	webConfig.Host = strings.TrimSpace(*webHost)
	webConfig.Port = *webPort
	webConfig.URLFile = strings.TrimSpace(*webURLFile)
	webConfig.Debug = *verbose

	webServer := web.NewWebServer(webConfig)

	// If host specified, connect directly
	if *host != "" {
		slog.Info("Direct mode", "host", *host)
		if err := webServer.GetController().Connect(*host, 5000); err != nil {
			slog.Error("Connection failed", "error", err)
			os.Exit(1)
		}
	}

	// Start web server
	go func() {
		if err := webServer.Start(); err != nil {
			slog.Error("Web server error", "error", err)
		}
	}()

	defer webServer.Stop()

	if *host != "" {
		slog.Info("AllRelay running", "mode", "direct", "host", *host)
	} else if *webPort == 0 {
		slog.Info("AllRelay running", "mode", "web", "url", "dynamic (use allrelay open or check startup log)")
	} else {
		displayHost := *webHost
		if displayHost == "" || displayHost == "0.0.0.0" || displayHost == "::" {
			displayHost = "localhost"
		}
		slog.Info("AllRelay running", "mode", "web", "url", fmt.Sprintf("http://%s:%d", displayHost, *webPort))
	}
	slog.Info("Open browser and connect to your phone!")

	// Wait for interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	slog.Info("Shutting down...")
}
