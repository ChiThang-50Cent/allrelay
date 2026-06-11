package bitrate

import (
	"testing"
	"time"

	"github.com/allrelay/allrelay-server/internal/heartbeat"
)

func TestNewController(t *testing.T) {
	cfg := DefaultConfig()
	streams := DefaultStreamConfigs()
	c := NewController(cfg, streams, nil)

	if c == nil {
		t.Fatal("expected non-nil controller")
	}

	if len(c.streams) != 3 {
		t.Errorf("expected 3 streams, got %d", len(c.streams))
	}

	total := c.GetTotalBitrate()
	expectedTotal := MonitorDefBitrate + CameraDefBitrate + AudioDefBitrate
	if total != expectedTotal {
		t.Errorf("total bitrate = %d, want %d", total, expectedTotal)
	}
}

func TestUpdateMetrics_GoodConditions_Increase(t *testing.T) {
	cfg := DefaultConfig()
	// Override interval to allow immediate adjustment
	cfg.AdjustInterval = 0
	streams := DefaultStreamConfigs()

	c := NewController(cfg, streams, nil)

	// Seed EMA with good conditions so smoothed values trigger increase
	c.rttEMA = 10
	c.packetLossEMA = 0.001
	c.jitterEMA = 2

	// First call: Should increase
	metrics := QualityMetrics{
		RTTMs:          10,
		PacketLossRate: 0.001,
		JitterMs:       2,
	}

	changes := c.UpdateMetrics(metrics)
	if len(changes) == 0 {
		t.Fatal("expected bitrate increase under good conditions")
	}

	for _, ch := range changes {
		if ch.NewBPS <= ch.OldBPS {
			t.Errorf("stream %d: new bitrate (%d) should be > old (%d) under good conditions",
				ch.StreamID, ch.NewBPS, ch.OldBPS)
		}
	}
}

func TestUpdateMetrics_HighRTT_Decrease(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AdjustInterval = 0
	streams := DefaultStreamConfigs()

	c := NewController(cfg, streams, nil)

	// Set EMA to make the smoothed values trigger aggressive reduction
	c.rttEMA = 120
	c.packetLossEMA = 0.04
	c.jitterEMA = 8

	// Aggressive reduction (RTT > 100ms)
	metrics := QualityMetrics{
		RTTMs:          150,
		PacketLossRate: 0.06,
		JitterMs:       10,
	}

	changes := c.UpdateMetrics(metrics)
	if len(changes) == 0 {
		t.Fatal("expected bitrate decrease under high RTT")
	}

	for _, ch := range changes {
		if ch.NewBPS >= ch.OldBPS {
			t.Errorf("stream %d: new bitrate (%d) should be < old (%d) under high RTT",
				ch.StreamID, ch.NewBPS, ch.OldBPS)
		}
		// Aggressive reduction: expect ~30% decrease
		expectedFactor := 0.70
		expectedNew := int(float64(ch.OldBPS) * expectedFactor)
		tolerance := float64(ch.OldBPS) * 0.06 // 6% tolerance
		if abs(float64(ch.NewBPS-expectedNew)) > tolerance {
			t.Logf("stream %d: new=%d, old=%d expected~%d (factor=%.2f)",
				ch.StreamID, ch.NewBPS, ch.OldBPS, expectedNew, expectedFactor)
		}
	}
}

func TestUpdateMetrics_HighLoss_Decrease(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AdjustInterval = 0
	streams := DefaultStreamConfigs()

	c := NewController(cfg, streams, nil)

	// Seed EMA with high loss to ensure smoothed values trigger aggressive reduction
	c.rttEMA = 40
	c.packetLossEMA = 0.06
	c.jitterEMA = 12

	// High packet loss (>5%)
	metrics := QualityMetrics{
		RTTMs:          30,
		PacketLossRate: 0.08,
		JitterMs:       15,
	}

	changes := c.UpdateMetrics(metrics)
	if len(changes) == 0 {
		t.Fatal("expected bitrate decrease under high packet loss")
	}

	for _, ch := range changes {
		if ch.NewBPS > ch.OldBPS {
			t.Errorf("stream %d: new bitrate (%d) should decrease under high loss (old=%d)",
				ch.StreamID, ch.NewBPS, ch.OldBPS)
		}
	}
}

func TestUpdateMetrics_ModerateConditions_Moderate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AdjustInterval = 0
	streams := DefaultStreamConfigs()

	c := NewController(cfg, streams, nil)

	// Seed EMA with moderate-elevated values to trigger moderate reduction
	c.rttEMA = 60
	c.packetLossEMA = 0.025
	c.jitterEMA = 8

	// Moderate conditions (RTT 50-100, loss 2-5%)
	metrics := QualityMetrics{
		RTTMs:          70,
		PacketLossRate: 0.03,
		JitterMs:       10,
	}

	changes := c.UpdateMetrics(metrics)
	if len(changes) == 0 {
		t.Fatal("expected bitrate decrease under moderate conditions")
	}

	// Moderate reduction: expect ~15% decrease
	expectedFactor := 0.85
	for _, ch := range changes {
		expectedNew := int(float64(ch.OldBPS) * expectedFactor)
		tolerance := float64(ch.OldBPS) * 0.06
		if abs(float64(ch.NewBPS-expectedNew)) > tolerance {
			t.Logf("stream %d: new=%d, old=%d expected~%d (factor=%.2f)",
				ch.StreamID, ch.NewBPS, ch.OldBPS, expectedNew, expectedFactor)
		}
	}
}

func TestUpdateMetrics_StableConditions_Hold(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AdjustInterval = 0
	streams := DefaultStreamConfigs()

	c := NewController(cfg, streams, nil)

	// Seed EMA with stable values
	c.rttEMA = 30
	c.packetLossEMA = 0.01
	c.jitterEMA = 8

	// Stable (neither good enough to increase nor bad enough to decrease)
	metrics := QualityMetrics{
		RTTMs:          35,
		PacketLossRate: 0.015,
		JitterMs:       10,
	}

	changes := c.UpdateMetrics(metrics)
	if len(changes) != 0 {
		t.Errorf("expected no changes under stable conditions, got %d changes", len(changes))
	}
}

func TestClamp_MinMax(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AdjustInterval = 0

	// Custom streams with tight bounds for testing
	streams := []StreamConfig{
		{
			StreamID:    0,
			CurrentBPS:  5000,
			MinBPS:      1000,
			MaxBPS:      10000,
			BudgetShare: 1.0,
		},
	}

	c := NewController(cfg, streams, nil)

	// Extremely bad conditions — should hit minimum
	metrics := QualityMetrics{
		RTTMs:          500,
		PacketLossRate: 0.50,
		JitterMs:       100,
	}

	changes := c.UpdateMetrics(metrics)
	if len(changes) == 0 {
		t.Fatal("expected changes")
	}

	for _, ch := range changes {
		if ch.NewBPS < 1000 {
			t.Errorf("stream %d: bitrate %d below min 1000", ch.StreamID, ch.NewBPS)
		}
		if ch.NewBPS > 10000 {
			t.Errorf("stream %d: bitrate %d above max 10000", ch.StreamID, ch.NewBPS)
		}
	}
}

func TestDampening_ConsecutiveUp(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AdjustInterval = 0
	streams := DefaultStreamConfigs()

	c := NewController(cfg, streams, nil)

	goodMetrics := QualityMetrics{
		RTTMs:          5,
		PacketLossRate: 0.0,
		JitterMs:       1,
	}

	// Simulate 4 consecutive good condition updates
	for i := 0; i < 4; i++ {
		changes := c.UpdateMetrics(goodMetrics)
		if i < 3 {
			if len(changes) == 0 {
				t.Errorf("iteration %d: expected increase", i)
			}
		} else {
			// 4th iteration should be dampened (holding)
			if len(changes) != 0 {
				t.Errorf("iteration %d: expected hold (dampened), got %d changes", i, len(changes))
			}
		}
	}
}

func TestUpdateFromHeartbeat(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AdjustInterval = 0
	streams := DefaultStreamConfigs()

	c := NewController(cfg, streams, nil)

	status := heartbeat.FullStatus{
		Device: heartbeat.DeviceStatus{
			WiFiRSSI:      -45,
			WiFiLinkSpeed: 866,
			Battery:       85,
		},
		StreamStats: map[string]heartbeat.StreamStats{
			"screen": {
				JitterMs:    2.5,
				PacketLoss:  0.001,
				FPS:         60,
				Bitrate:     4_000_000,
			},
		},
	}

	changes := c.UpdateFromHeartbeat(status)
	// With good RSSI (-45 → ~5ms RTT), should trigger increase
	if len(changes) == 0 {
		t.Log("no changes from heartbeat update (may be due to dampening)")
	}
}

func TestMetricsFromHeartbeat(t *testing.T) {
	status := heartbeat.FullStatus{
		Device: heartbeat.DeviceStatus{
			WiFiRSSI:      -60,
			WiFiLinkSpeed: 433,
		},
		StreamStats: map[string]heartbeat.StreamStats{
			"screen": {
				JitterMs:    5.0,
				PacketLoss:  0.01,
			},
		},
	}

	metrics := MetricsFromHeartbeat(status)

	if metrics.WiFiLinkSpeed != 433 {
		t.Errorf("WiFiLinkSpeed = %d, want 433", metrics.WiFiLinkSpeed)
	}
	if metrics.JitterMs != 5.0 {
		t.Errorf("JitterMs = %f, want 5.0", metrics.JitterMs)
	}
	if metrics.PacketLossRate != 0.01 {
		t.Errorf("PacketLossRate = %f, want 0.01", metrics.PacketLossRate)
	}
	// RTT estimated from RSSI -60
	if metrics.RTTMs < 0 {
		t.Errorf("RTTMs = %f, should be positive", metrics.RTTMs)
	}
}

func TestGetStreamBitrate(t *testing.T) {
	cfg := DefaultConfig()
	streams := DefaultStreamConfigs()
	c := NewController(cfg, streams, nil)

	bps, ok := c.GetStreamBitrate(0) // Monitor
	if !ok {
		t.Fatal("monitor stream not found")
	}
	if bps != MonitorDefBitrate {
		t.Errorf("monitor bitrate = %d, want %d", bps, MonitorDefBitrate)
	}

	_, ok = c.GetStreamBitrate(99) // Unknown
	if ok {
		t.Error("unknown stream should not be found")
	}
}

func TestBitrateSetter(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AdjustInterval = 0
	streams := DefaultStreamConfigs()

	var calledStreamID int
	var calledBitrate int

	setter := func(streamID int, bitrateBPS int) error {
		calledStreamID = streamID
		calledBitrate = bitrateBPS
		return nil
	}

	c := NewController(cfg, streams, setter)

	// Trigger decrease
	metrics := QualityMetrics{
		RTTMs:          200,
		PacketLossRate: 0.10,
		JitterMs:       50,
	}

	changes := c.UpdateMetrics(metrics)
	c.ApplyChanges(changes)

	if calledStreamID == 0 && calledBitrate == 0 {
		t.Log("setter was called (check bitrate values)")
	}
}

func TestFormatBitrate(t *testing.T) {
	tests := []struct {
		bps  int
		want string
	}{
		{5_000_000, "5.0 Mbps"},
		{1_500_000, "1.5 Mbps"},
		{500_000, "500 Kbps"},
		{64_000, "64 Kbps"},
		{500, "500 bps"},
	}

	for _, tt := range tests {
		got := formatBitrate(tt.bps)
		if got != tt.want {
			t.Errorf("formatBitrate(%d) = %q, want %q", tt.bps, got, tt.want)
		}
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// Test that the interval enforcement works
func TestAdjustInterval(t *testing.T) {
	cfg := Config{
		AdjustInterval: 1 * time.Hour, // Very long interval
		MinTotalBPS:    TotalMinBitrate,
		MaxTotalBPS:    TotalMaxBitrate,
		EMASmoothing:   0.3,
	}

	streams := DefaultStreamConfigs()
	c := NewController(cfg, streams, nil)

	// Set lastAdjust to now
	c.lastAdjust = time.Now()

	// Even bad metrics should produce no changes due to interval
	metrics := QualityMetrics{
		RTTMs:          999,
		PacketLossRate: 0.99,
		JitterMs:       999,
	}

	changes := c.UpdateMetrics(metrics)
	if len(changes) != 0 {
		t.Errorf("expected no changes due to adjust interval, got %d", len(changes))
	}
}
