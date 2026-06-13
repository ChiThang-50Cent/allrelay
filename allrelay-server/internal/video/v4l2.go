package video

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
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

// ReloadV4L2Loopback reloads the v4l2loopback kernel module for a clean camera session.
// Browsers (Zoom/Meet) enumerate cameras on page load, so the device must be
// freshly created and capturing when the browser opens.
//
// Uses sudo with password from ALLRELAY_SUDO_PASSWORD env var if set.
func ReloadV4L2Loopback(device string) error {
	var videoNr int
	if _, err := fmt.Sscanf(device, "/dev/video%d", &videoNr); err != nil {
		return fmt.Errorf("invalid device path: %w", err)
	}

	sudoPass := os.Getenv("ALLRELAY_SUDO_PASSWORD")

	// Remove existing module
	rmCmd := exec.Command("sudo", "-S", "modprobe", "-r", "v4l2loopback")
	if sudoPass != "" {
		rmCmd.Stdin = strings.NewReader(sudoPass + "\n")
	}
	rmCmd.Run() // ignore errors (module might not be loaded)

	// Small sleep for device cleanup
	time.Sleep(500 * time.Millisecond)

	// Reload with clean settings
	label := "AllRelay Cam"
	args := []string{
		"-S", "modprobe", "v4l2loopback",
		fmt.Sprintf("video_nr=%d", videoNr),
		fmt.Sprintf("card_label=%s", label),
		"exclusive_caps=1",
		"max_buffers=4",
	}
	cmd := exec.Command("sudo", args...)
	if sudoPass != "" {
		cmd.Stdin = strings.NewReader(sudoPass + "\n")
	}

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("modprobe failed: %w (output: %s)", err, string(out))
	}

	slog.Info("Camera: v4l2loopback reloaded",
		"device", device,
		"label", label,
		"exclusive_caps", true,
		"max_buffers", 4)
	return nil
}
