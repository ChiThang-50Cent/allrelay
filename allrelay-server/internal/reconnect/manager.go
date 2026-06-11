// Package reconnect provides reconnection logic with exponential backoff.
//
// When the connection to the Android device is lost (TCP disconnect,
// stream timeout, heartbeat failure), the reconnection manager attempts
// to re-establish the connection with exponential backoff and jitter.
//
// Reconnection strategy:
//   - Initial delay: 1 second
//   - Max delay: 60 seconds
//   - Backoff multiplier: 2x
//   - Jitter: ±25%
//   - Max attempts: unlimited (retry forever)
package reconnect

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"time"
)

// Default configuration values.
const (
	DefaultInitialDelay = 1 * time.Second
	DefaultMaxDelay     = 60 * time.Second
	DefaultMultiplier   = 2.0
	DefaultJitter       = 0.25 // ±25%
)

// Config holds reconnection parameters.
type Config struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Multiplier   float64
	Jitter       float64 // 0.0 to 1.0
}

// DefaultConfig returns the default reconnection configuration.
func DefaultConfig() Config {
	return Config{
		InitialDelay: DefaultInitialDelay,
		MaxDelay:     DefaultMaxDelay,
		Multiplier:   DefaultMultiplier,
		Jitter:       DefaultJitter,
	}
}

// Manager handles automatic reconnection with exponential backoff.
type Manager struct {
	cfg          Config
	connectFn    func() error    // Function to establish connection
	disconnectFn func()          // Function to clean up connection
	onConnected  func()          // Called after successful reconnect
	attempt      int
}

// NewManager creates a new reconnection manager.
//
// connectFn is called to establish a new connection. Return nil on success.
// disconnectFn is called to clean up the previous connection before retrying.
// onConnected is called after a successful reconnection (e.g., to re-setup streams).
func NewManager(cfg Config, connectFn func() error, disconnectFn func(), onConnected func()) *Manager {
	return &Manager{
		cfg:          cfg,
		connectFn:    connectFn,
		disconnectFn: disconnectFn,
		onConnected:  onConnected,
	}
}

// Run starts the reconnection loop. It blocks until ctx is cancelled.
//
// The loop:
//  1. Calls disconnectFn to clean up any stale connection
//  2. Calculates delay with exponential backoff
//  3. Waits for delay (or context cancellation)
//  4. Attempts reconnection via connectFn
//  5. On success: calls onConnected, resets attempt counter
//  6. On failure: increments attempt counter, loops back
func (m *Manager) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			slog.Info("Reconnection manager stopped")
			return
		default:
		}

		// Clean up before attempting reconnect
		if m.disconnectFn != nil {
			m.disconnectFn()
		}

		// Calculate delay
		delay := m.calculateDelay()
		slog.Info("Reconnecting...",
			"attempt", m.attempt+1,
			"delay", delay.Round(time.Millisecond))

		// Wait with context cancellation
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		// Attempt reconnection
		if err := m.connectFn(); err != nil {
			m.attempt++
			slog.Warn("Reconnection failed",
				"attempt", m.attempt,
				"error", err,
				"next_delay", m.calculateDelay().Round(time.Millisecond))
			continue
		}

		// Success!
		slog.Info("Reconnected successfully", "attempts", m.attempt+1)
		m.attempt = 0

		if m.onConnected != nil {
			m.onConnected()
		}

		// Return — the caller will re-enter the run loop if disconnected again
		return
	}
}

// calculateDelay computes the current backoff delay.
//
//	delay = min(initialDelay * multiplier^attempt, maxDelay)
//	delay = delay * (1 ± jitter)
func (m *Manager) calculateDelay() time.Duration {
	base := float64(m.cfg.InitialDelay) * math.Pow(m.cfg.Multiplier, float64(m.attempt))
	delay := time.Duration(base)

	if delay > m.cfg.MaxDelay {
		delay = m.cfg.MaxDelay
	}

	// Apply jitter: ±jitter%
	if m.cfg.Jitter > 0 {
		jitterRange := float64(delay) * m.cfg.Jitter
		jitter := time.Duration(rand.Float64()*2*jitterRange - jitterRange)
		delay += jitter
	}

	if delay < 0 {
		delay = m.cfg.InitialDelay
	}

	return delay
}

// Reset resets the attempt counter (e.g., after a clean disconnect).
func (m *Manager) Reset() {
	m.attempt = 0
}

// AttemptCount returns the current number of consecutive failed attempts.
func (m *Manager) AttemptCount() int {
	return m.attempt
}

// FormatDelay formats a duration for human-readable log output.
func FormatDelay(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
