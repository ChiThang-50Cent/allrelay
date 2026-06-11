// Package bitrate implements adaptive bitrate control for AllRelay streams.
//
// The controller monitors Wi-Fi quality metrics (RTT, packet loss, jitter)
// from the heartbeat monitor and adjusts per-stream bitrates to maintain
// optimal quality under changing network conditions.
//
// Algorithm (from SPEC.md §10.4):
//
//	IF rtt_ms > 100 OR packet_loss_rate > 0.05:
//	    new_bitrate = current_bitrate * 0.7     // Aggressive reduction
//	ELIF rtt_ms > 50 OR packet_loss_rate > 0.02:
//	    new_bitrate = current_bitrate * 0.85    // Moderate reduction
//	ELIF rtt_ms < 20 AND packet_loss_rate < 0.005 AND jitter_ms < 5:
//	    new_bitrate = current_bitrate * 1.1     // Increase
//	ELSE:
//	    new_bitrate = current_bitrate           // Hold
//
//	new_bitrate = CLAMP(new_bitrate, min_bitrate, max_bitrate)
//
// Per-stream priority allocation:
//
//	Monitor: 50% of budget
//	Camera: 30% of budget
//	Audio: 20% of budget (fixed, rarely needs adjustment)
package bitrate

import (
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/allrelay/allrelay-server/internal/heartbeat"
)

// Default bitrate ranges for each stream (in bits per second).
const (
	// Monitor (screen) bitrate
	MonitorMinBitrate = 1_000_000  // 1 Mbps
	MonitorMaxBitrate = 8_000_000  // 8 Mbps
	MonitorDefBitrate = 4_000_000  // 4 Mbps

	// Camera bitrate
	CameraMinBitrate = 500_000    // 500 Kbps
	CameraMaxBitrate = 5_000_000  // 5 Mbps
	CameraDefBitrate = 2_000_000  // 2 Mbps

	// Audio (mic + speaker) bitrate — fixed, rarely adjusts
	AudioMinBitrate = 16_000   // 16 Kbps
	AudioMaxBitrate = 128_000  // 128 Kbps
	AudioDefBitrate = 64_000   // 64 Kbps

	// Budget allocation ratios
	MonitorBudgetShare = 0.50
	CameraBudgetShare  = 0.30
	AudioBudgetShare   = 0.20

	// Total bandwidth budget
	TotalMinBitrate = 1_500_000   // 1.5 Mbps
	TotalMaxBitrate = 14_000_000  // 14 Mbps
)

// StreamConfig holds the current configuration for a single stream.
type StreamConfig struct {
	StreamID    int    // Control protocol stream ID
	CurrentBPS  int    // Current bitrate in bits/sec
	MinBPS      int    // Minimum bitrate
	MaxBPS      int    // Maximum bitrate
	BudgetShare float64 // Share of total bandwidth budget (0.0-1.0)
}

// QualityMetrics summarizes Wi-Fi link quality for bitrate decisions.
type QualityMetrics struct {
	RTTMs          float64 // Round-trip time in milliseconds
	PacketLossRate float64 // Packet loss rate (0.0 - 1.0)
	JitterMs       float64 // Jitter in milliseconds
	WiFiLinkSpeed  int     // Wi-Fi link speed in Mbps
}

// BitrateChange represents a pending bitrate adjustment for a stream.
type BitrateChange struct {
	StreamID int // Stream to adjust
	OldBPS   int // Previous bitrate
	NewBPS   int // New bitrate
	Reason   string
}

// BitrateSetter is called to apply a bitrate change to a stream.
// The implementation should send a config message via the control channel.
type BitrateSetter func(streamID int, bitrateBPS int) error

// Controller manages adaptive bitrate for all streams.
type Controller struct {
	streams  map[int]*StreamConfig // stream_id → config
	setter   BitrateSetter
	mu       sync.RWMutex

	// Current state
	totalBPS      int       // Total allocated bitrate
	lastAdjust    time.Time // Last adjustment time
	consecutiveUp int       // Consecutive upward adjustments (for dampening)

	// Smoothing
	rttEMA          float64 // Exponential moving average of RTT
	packetLossEMA   float64 // EMA of packet loss
	jitterEMA       float64 // EMA of jitter
	emaAlpha        float64 // Smoothing factor (0.0-1.0)

	// Configuration
	adjustInterval    time.Duration // Minimum time between adjustments
	minTotalBPS       int
	maxTotalBPS       int
}

// Config holds Controller configuration.
type Config struct {
	AdjustInterval time.Duration // Minimum time between adjustments
	MinTotalBPS    int           // Minimum total bandwidth
	MaxTotalBPS    int           // Maximum total bandwidth
	EMASmoothing   float64       // EMA smoothing factor (default 0.3)
}

// DefaultConfig returns the default controller configuration.
func DefaultConfig() Config {
	return Config{
		AdjustInterval: 2 * time.Second,
		MinTotalBPS:    TotalMinBitrate,
		MaxTotalBPS:    TotalMaxBitrate,
		EMASmoothing:   0.3,
	}
}

// DefaultStreamConfigs returns the default stream configurations.
func DefaultStreamConfigs() []StreamConfig {
	return []StreamConfig{
		{
			StreamID:    0, // Monitor (control.StreamMonitor)
			CurrentBPS:  MonitorDefBitrate,
			MinBPS:      MonitorMinBitrate,
			MaxBPS:      MonitorMaxBitrate,
			BudgetShare: MonitorBudgetShare,
		},
		{
			StreamID:    1, // Camera (control.StreamCamera)
			CurrentBPS:  CameraDefBitrate,
			MinBPS:      CameraMinBitrate,
			MaxBPS:      CameraMaxBitrate,
			BudgetShare: CameraBudgetShare,
		},
		{
			StreamID:    3, // Speaker (control.StreamSpeaker) — audio is shared
			CurrentBPS:  AudioDefBitrate,
			MinBPS:      AudioMinBitrate,
			MaxBPS:      AudioMaxBitrate,
			BudgetShare: AudioBudgetShare,
		},
	}
}

// NewController creates a new adaptive bitrate controller.
func NewController(cfg Config, streams []StreamConfig, setter BitrateSetter) *Controller {
	c := &Controller{
		streams:       make(map[int]*StreamConfig),
		setter:        setter,
		adjustInterval: cfg.AdjustInterval,
		minTotalBPS:   cfg.MinTotalBPS,
		maxTotalBPS:   cfg.MaxTotalBPS,
		emaAlpha:      cfg.EMASmoothing,
	}

	for i := range streams {
		s := streams[i] // copy
		c.streams[s.StreamID] = &s
		c.totalBPS += s.CurrentBPS
	}

	return c
}

// UpdateMetrics feeds new Wi-Fi quality metrics into the controller.
// Returns any bitrate changes that should be applied.
func (c *Controller) UpdateMetrics(metrics QualityMetrics) []BitrateChange {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Update EMA values for smoothing
	c.updateEMA(metrics)

	// Check if enough time has passed since last adjustment
	if time.Since(c.lastAdjust) < c.adjustInterval {
		return nil
	}

	// Use smoothed values for decision
	rtt := c.rttEMA
	loss := c.packetLossEMA
	jitter := c.jitterEMA

	// Calculate multi-factor for the overall budget
	var factor float64
	var reason string

	switch {
	case rtt > 100 || loss > 0.05:
		factor = 0.70
		reason = fmt.Sprintf("high RTT (%.0fms) or loss (%.1f%%)", rtt, loss*100)
	case rtt > 50 || loss > 0.02:
		factor = 0.85
		reason = fmt.Sprintf("elevated RTT (%.0fms) or loss (%.1f%%)", rtt, loss*100)
	case rtt < 20 && loss < 0.005 && jitter < 5:
		// Dampen upward adjustments to avoid oscillation
		if c.consecutiveUp >= 3 {
			factor = 1.0
			reason = "holding (dampened)"
		} else {
			factor = 1.10
			reason = fmt.Sprintf("good conditions RTT=%.0fms loss=%.1f%% jitter=%.1fms", rtt, loss*100, jitter)
		}
	default:
		factor = 1.0
		reason = "stable conditions"
	}

	if factor == 1.0 {
		c.consecutiveUp = 0
		return nil
	}

	if factor > 1.0 {
		c.consecutiveUp++
	} else {
		c.consecutiveUp = 0
	}

	// Apply factor to each stream's current bitrate individually
	changes := c.adjustAllStreams(factor, reason)

	if len(changes) == 0 {
		return nil
	}

	// Recompute total from stream states
	total := 0
	for _, s := range c.streams {
		total += s.CurrentBPS
	}
	c.totalBPS = total
	c.lastAdjust = time.Now()

	return changes
}

// UpdateFromHeartbeat extracts quality metrics from a heartbeat status update.
func (c *Controller) UpdateFromHeartbeat(status heartbeat.FullStatus) []BitrateChange {
	// Extract metrics from heartbeat
	metrics := QualityMetrics{
		WiFiLinkSpeed: status.Device.WiFiLinkSpeed,
	}

	// Try to extract RTT, loss, jitter from stream stats
	// The monitor stream usually has the most detailed stats
	if screenStats, ok := status.StreamStats["screen"]; ok {
		metrics.JitterMs = screenStats.JitterMs
		metrics.PacketLossRate = screenStats.PacketLoss
	}

	// Estimate RTT from Wi-Fi RSSI (rough approximation)
	// RSSI -40 dBm → ~2ms, -60 → ~5ms, -80 → ~15ms
	if status.Device.WiFiRSSI != 0 {
		rssi := float64(status.Device.WiFiRSSI)
		// Linear interpolation: 2ms at -30, 15ms at -85
		metrics.RTTMs = 2.0 + (math.Abs(rssi)-30)*13.0/55.0
	}

	return c.UpdateMetrics(metrics)
}

// updateEMA updates the exponential moving averages.
func (c *Controller) updateEMA(metrics QualityMetrics) {
	alpha := c.emaAlpha
	c.rttEMA = alpha*metrics.RTTMs + (1-alpha)*c.rttEMA
	c.packetLossEMA = alpha*metrics.PacketLossRate + (1-alpha)*c.packetLossEMA
	c.jitterEMA = alpha*metrics.JitterMs + (1-alpha)*c.jitterEMA
}

// adjustAllStreams applies a multiplicative factor to each stream's current bitrate.
// Each stream is individually clamped to its min/max range.
func (c *Controller) adjustAllStreams(factor float64, reason string) []BitrateChange {
	var changes []BitrateChange

	for _, s := range c.streams {
		targetBPS := int(float64(s.CurrentBPS) * factor)
		targetBPS = c.clamp(targetBPS, s.MinBPS, s.MaxBPS)

		// Audio streams have a higher floor — don't drop below 32 Kbps
		if s.BudgetShare <= AudioBudgetShare && targetBPS < 32_000 {
			targetBPS = 32_000
		}

		if targetBPS == s.CurrentBPS {
			continue
		}

		change := BitrateChange{
			StreamID: s.StreamID,
			OldBPS:   s.CurrentBPS,
			NewBPS:   targetBPS,
			Reason:   reason,
		}

		s.CurrentBPS = targetBPS
		changes = append(changes, change)
	}

	return changes
}

// ApplyChanges sends bitrate changes to the Android device via the setter callback.
func (c *Controller) ApplyChanges(changes []BitrateChange) {
	for _, change := range changes {
		dir := "↓"
		if change.NewBPS > change.OldBPS {
			dir = "↑"
		}

		slog.Info("Bitrate adjusted",
			"stream", change.StreamID,
			"old", formatBitrate(change.OldBPS),
			"new", formatBitrate(change.NewBPS),
			"dir", dir,
			"reason", change.Reason,
		)

		if c.setter != nil {
			if err := c.setter(change.StreamID, change.NewBPS); err != nil {
				slog.Warn("Failed to set bitrate",
					"stream", change.StreamID,
					"bitrate", change.NewBPS,
					"error", err)
			}
		}
	}
}

// GetStreamBitrate returns the current bitrate for a stream.
func (c *Controller) GetStreamBitrate(streamID int) (int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	s, ok := c.streams[streamID]
	if !ok {
		return 0, false
	}
	return s.CurrentBPS, true
}

// GetTotalBitrate returns the current total allocated bitrate.
func (c *Controller) GetTotalBitrate() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.totalBPS
}

// clamp constrains a value between min and max.
func (c *Controller) clamp(val, min, max int) int {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}

// formatBitrate formats a bitrate in bps to a human-readable string.
func formatBitrate(bps int) string {
	if bps >= 1_000_000 {
		return fmt.Sprintf("%.1f Mbps", float64(bps)/1_000_000)
	}
	if bps >= 1_000 {
		return fmt.Sprintf("%d Kbps", bps/1_000)
	}
	return fmt.Sprintf("%d bps", bps)
}

// MetricsFromHeartbeat extracts quality metrics from a heartbeat status update.
// This is the public version used by callers.
func MetricsFromHeartbeat(status heartbeat.FullStatus) QualityMetrics {
	metrics := QualityMetrics{
		WiFiLinkSpeed: status.Device.WiFiLinkSpeed,
	}

	if screenStats, ok := status.StreamStats["screen"]; ok {
		metrics.JitterMs = screenStats.JitterMs
		metrics.PacketLossRate = screenStats.PacketLoss
	}

	if status.Device.WiFiRSSI != 0 {
		rssi := float64(status.Device.WiFiRSSI)
		metrics.RTTMs = 2.0 + (math.Abs(rssi)-30)*13.0/55.0
	}

	return metrics
}
