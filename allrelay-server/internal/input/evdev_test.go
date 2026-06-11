package input

import (
	"testing"
)

func TestEvdevAvailable(t *testing.T) {
	avail := IsEvdevAvailable()
	t.Logf("evdev available: %v", avail)
	// Don't require it — user may not be in input group
}

func TestBestCapture(t *testing.T) {
	best, err := NewBestCapture()
	if err != nil {
		t.Logf("BestCapture error (may be expected): %v", err)
		return
	}
	t.Logf("BestCapture created: %T", best)
	best.Close()
}

func TestLinuxKeyMapping(t *testing.T) {
	tests := []struct {
		linuxKey int
		androidKey int
	}{
		{30, 29}, // KEY_A → KEYCODE_A
		{16, 45}, // KEY_Q → KEYCODE_Q
		{28, 66}, // KEY_ENTER → KEYCODE_ENTER
		{1, 4},   // KEY_ESC → KEYCODE_BACK
		{57, 62}, // KEY_SPACE → KEYCODE_SPACE
		{103, 19}, // KEY_UP → KEYCODE_DPAD_UP
		{105, 21}, // KEY_LEFT → KEYCODE_DPAD_LEFT
	}

	for _, tt := range tests {
		result := LinuxToAndroidKeycode(tt.linuxKey)
		if result != tt.androidKey {
			t.Errorf("LinuxToAndroidKeycode(%d) = %d, want %d",
				tt.linuxKey, result, tt.androidKey)
		}
	}

	// Verify unknown key returns 0
	if k := LinuxToAndroidKeycode(999); k != 0 {
		t.Errorf("unknown key should return 0, got %d", k)
	}
}

func TestX11ToAndroidKeyMapping(t *testing.T) {
	tests := []struct {
		x11Key   int
		androidKey int
	}{
		{24, 45},  // Q → KEYCODE_Q
		{38, 29},  // A → KEYCODE_A
		{36, 66},  // Return → KEYCODE_ENTER
		{9, 4},    // Escape → KEYCODE_BACK
		{65, 62},  // Space → KEYCODE_SPACE
		{111, 19}, // Up → KEYCODE_DPAD_UP
		{113, 21}, // Left → KEYCODE_DPAD_LEFT
		{133, 3},  // Super_L → KEYCODE_HOME
	}

	for _, tt := range tests {
		result := X11ToAndroidKeycode(tt.x11Key)
		if result != tt.androidKey {
			t.Errorf("X11ToAndroidKeycode(%d) = %d, want %d",
				tt.x11Key, result, tt.androidKey)
		}
	}

	// Verify unknown key returns 0
	if k := X11ToAndroidKeycode(999); k != 0 {
		t.Errorf("unknown key should return 0, got %d", k)
	}
}
