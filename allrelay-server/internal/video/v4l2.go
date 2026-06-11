package video

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
)

// Default V4L2 device for the AllRelay virtual camera.
const DefaultCameraDevice = "/dev/video10"

// EnsureV4L2Device checks if a v4l2loopback device exists and loads the
// kernel module if needed.
//
// device is the path to check (e.g. "/dev/video10").
// Returns nil if the device is usable.
func EnsureV4L2Device(device string) error {
	if _, err := os.Stat(device); err == nil {
		slog.Info("V4L2 device ready", "device", device)
		return nil
	}

	// Device doesn't exist — try loading v4l2loopback
	slog.Info("V4L2 device not found, attempting to load v4l2loopback...",
		"device", device)

	// Extract video number from path
	var videoNr int
	if _, scanErr := fmt.Sscanf(device, "/dev/video%d", &videoNr); scanErr != nil {
		return fmt.Errorf("invalid v4l2 device path %q: %w", device, scanErr)
	}

	label := "AllRelay Camera"
	modprobeArgs := []string{
		"v4l2loopback",
		fmt.Sprintf("devices=1"),
		fmt.Sprintf("video_nr=%d", videoNr),
		fmt.Sprintf("card_label=%s", label),
		"exclusive_caps=1",
	}

	cmd := exec.Command("sudo", append([]string{"modprobe"}, modprobeArgs...)...)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo modprobe v4l2loopback: %w — may need to run manually:\n"+
			"  sudo modprobe v4l2loopback devices=1 video_nr=%d card_label=\"%s\" exclusive_caps=1",
			err, videoNr, label)
	}

	// Verify it exists now
	if _, err := os.Stat(device); err != nil {
		return fmt.Errorf("v4l2loopback loaded but %s still not found: %w", device, err)
	}

	slog.Info("V4L2 device created", "device", device)
	return nil
}

// IsV4L2DeviceReady checks if the v4l2 device exists and is accessible.
func IsV4L2DeviceReady(device string) bool {
	info, err := os.Stat(device)
	if err != nil {
		return false
	}
	// Device files are character devices with mode including read/write
	return info.Mode()&os.ModeCharDevice != 0
}

// CameraReady checks if the default camera device is accessible.
func CameraReady() bool {
	return IsV4L2DeviceReady(DefaultCameraDevice)
}

// GetCameraDevice returns the configured v4l2 device path.
// Checks the ALLRELAY_CAMERA_DEVICE env var, falls back to /dev/video10.
func GetCameraDevice() string {
	if d := os.Getenv("ALLRELAY_CAMERA_DEVICE"); d != "" {
		return d
	}
	return DefaultCameraDevice
}

// LoadModule attempts to load v4l2loopback and returns an error
// with the exact command to run if it fails (no password prompting).
func LoadModule() error {
	// Try loading without sudo — will fail if not root, which is expected.
	// The caller (or user) needs sudo.
	if err := EnsureV4L2Device(DefaultCameraDevice); err != nil {
		return errors.New("v4l2loopback module not loaded. Run:\n" +
			"  sudo modprobe v4l2loopback devices=1 video_nr=10 card_label=\"AllRelay Camera\" exclusive_caps=1")
	}
	return nil
}
