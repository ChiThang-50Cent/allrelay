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
//	allrelay-server --web --web-port 9090
//	allrelay-server --host 192.168.1.100
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/allrelay/allrelay-server/internal/web"
)

func main() {
	host := flag.String("host", "", "Phone IP address (direct mode)")
	webPort := flag.Int("web-port", 9090, "Web UI port")
	verbose := flag.Bool("v", false, "Verbose debug output")
	flag.Parse()

	if *verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	} else {
		slog.SetLogLoggerLevel(slog.LevelInfo)
	}

	// Create web server with integrated controller
	webConfig := web.DefaultConfig()
	webConfig.Port = *webPort
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
		slog.Info("Starting AllRelay Web UI", "port", *webPort)
		if err := webServer.Start(); err != nil {
			slog.Error("Web server error", "error", err)
		}
	}()

	defer webServer.Stop()

	if *host != "" {
		slog.Info("AllRelay running", "mode", "direct", "host", *host)
	} else {
		slog.Info("AllRelay running", "mode", "web", "url", fmt.Sprintf("http://localhost:%d", *webPort))
	}
	slog.Info("Open browser and connect to your phone!")

	// Wait for interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	slog.Info("Shutting down...")
}
