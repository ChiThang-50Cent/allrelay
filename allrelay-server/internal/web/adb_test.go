package web

import "testing"

func TestParseADBDeviceState(t *testing.T) {
	output := "List of devices attached\n192.168.1.24:5555\tunauthorized\n[fd00::24]:5555\tdevice product:phone\n"

	if state := parseADBDeviceState(output, "192.168.1.24", 5555); state != "unauthorized" {
		t.Fatalf("IPv4 state = %q, want unauthorized", state)
	}
	if state := parseADBDeviceState(output, "fd00::24", 5555); state != "device" {
		t.Fatalf("IPv6 state = %q, want device", state)
	}
	if state := parseADBDeviceState(output, "192.168.1.25", 5555); state != "disconnected" {
		t.Fatalf("absent device state = %q, want disconnected", state)
	}
}

func TestHeartbeatRequiresConnectedDevice(t *testing.T) {
	tests := []struct {
		name   string
		status ADBStatus
		want   bool
	}{
		{name: "connected device", status: ADBStatus{HostState: "device", PhoneEnabled: true}, want: true},
		{name: "unauthorized host", status: ADBStatus{HostState: "unauthorized", PhoneEnabled: true}, want: false},
		{name: "disabled phone", status: ADBStatus{HostState: "device", PhoneEnabled: false}, want: false},
		{name: "disconnected host", status: ADBStatus{HostState: "disconnected", PhoneEnabled: true}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldHeartbeatADB(tt.status); got != tt.want {
				t.Fatalf("shouldHeartbeatADB(%+v) = %t, want %t", tt.status, got, tt.want)
			}
		})
	}
}

func TestADBMonitorOwnsOnlyItsCurrentPhone(t *testing.T) {
	ws := &WebServer{
		currentConn:      &ConnectionStatus{Phone: &PhoneDevice{IP: "192.168.1.24"}},
		adbMonitorCancel: func() {},
		adbMonitorID:     7,
	}

	if !ws.adbMonitorOwnsPhone(7, "192.168.1.24") {
		t.Fatal("current monitor should own the current phone")
	}
	if ws.adbMonitorOwnsPhone(6, "192.168.1.24") {
		t.Fatal("superseded monitor must not own the phone")
	}
	if ws.adbMonitorOwnsPhone(7, "192.168.1.25") {
		t.Fatal("monitor must not own a replaced phone")
	}

	status := ADBStatus{IP: "192.168.1.24", PhoneEnabled: true}
	if ws.setADBStatusForMonitor(6, "192.168.1.24", status) {
		t.Fatal("superseded monitor must not publish cached status")
	}
	if ws.currentConn.ADB != (ADBStatus{}) {
		t.Fatalf("superseded monitor published status: %+v", ws.currentConn.ADB)
	}
	if !ws.setADBStatusForMonitor(7, "192.168.1.24", status) {
		t.Fatal("current monitor should publish cached status")
	}
}

func TestBuildADBStatusPreservesCachedLease(t *testing.T) {
	ws := &WebServer{}
	status := ws.buildADBStatus("192.168.1.24", phoneADBStatus{
		OK:              true,
		Enabled:         true,
		Listening:       true,
		Port:            5555,
		AutoDisableAtMs: 1_234_567,
		Message:         "ADB TCP listening on port 5555",
	})

	if !status.PhoneReachable || !status.PhoneEnabled || !status.PhoneListening {
		t.Fatalf("phone cache flags = %+v, want reachable enabled listener", status)
	}
	if status.AutoDisableAtMs != 1_234_567 {
		t.Fatalf("lease = %d, want 1234567", status.AutoDisableAtMs)
	}
	if status.Port != 5555 || status.Message != "ADB TCP listening on port 5555" {
		t.Fatalf("cached protocol fields = %+v", status)
	}
}
