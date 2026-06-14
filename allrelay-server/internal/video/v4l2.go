package video

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// Default V4L2 device for the AllRelay virtual camera.
const DefaultCameraDevice = "/dev/video10"

// EnsureV4L2Device checks if a v4l2loopback device exists.
// If not, returns instructions for one-time setup.
// Does NOT call sudo — that's for the install script, not runtime.
func EnsureV4L2Device(device string) error {
	if _, err := os.Stat(device); err == nil {
		return nil
	}

	var videoNr int
	fmt.Sscanf(device, "/dev/video%d", &videoNr)

	return fmt.Errorf(
		"v4l2loopback device %s not found. One-time setup:\n"+
			"  sudo modprobe v4l2loopback video_nr=%d card_label=\"AllRelay Cam\" exclusive_caps=1\n"+
			"  # To persist across reboots:\n"+
			"  echo 'v4l2loopback' | sudo tee /etc/modules-load.d/allrelay.conf\n"+
			"  echo 'options v4l2loopback video_nr=%d card_label=\"AllRelay Cam\" exclusive_caps=1' | sudo tee /etc/modprobe.d/allrelay.conf",
		device, videoNr, videoNr,
	)
}

// SetupV4L2Output sets the output format on the v4l2loopback device.
// This must be called BEFORE ffmpeg/gstreamer opens the device for writing.
//
// Important: with exclusive_caps=1, v4l2loopback can keep the last good
// output format between sessions if keep_format=1 is enabled. This makes
// camera OFF→ON restarts reliable without requiring sudo/module reloads.
func SetupV4L2Output(device string, width, height int, pixelformat string) error {
	// Persist the last good format across writer restarts.
	if err := setV4L2Ctrl(device, "keep_format=1"); err != nil {
		slog.Warn("v4l2: failed to enable keep_format", "error", err)
	}

	if current, err := getV4L2Format(device, "--get-fmt-video-out"); err == nil {
		if v4l2FormatMatches(current, width, height, pixelformat) {
			slog.Debug("v4l2: output format already configured", "device", device)
			return nil
		}
		slog.Info("v4l2: output format differs, reconfiguring", "device", device)
	}

	slog.Info("v4l2: setting output format",
		"device", device,
		"width", width,
		"height", height,
		"format", pixelformat)

	cmd := exec.Command("v4l2-ctl",
		"-d", device,
		"--set-fmt-video-out",
		fmt.Sprintf("width=%d,height=%d,pixelformat=%s", width, height, pixelformat),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("v4l2-ctl set output format: %w\noutput: %s", err, string(out))
	}

	current, err := getV4L2Format(device, "--get-fmt-video-out")
	if err != nil {
		return fmt.Errorf("v4l2-ctl verify output format: %w", err)
	}
	if !v4l2FormatMatches(current, width, height, pixelformat) {
		return fmt.Errorf("v4l2 output format mismatch after setup: %s", strings.TrimSpace(current))
	}
	return nil
}

// getV4L2Format runs v4l2-ctl to get a format string, returns it or error.
func getV4L2Format(device, flag string) (string, error) {
	cmd := exec.Command("v4l2-ctl", "-d", device, flag)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func setV4L2Ctrl(device, ctrl string) error {
	cmd := exec.Command("v4l2-ctl", "-d", device, "--set-ctrl", ctrl)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("set ctrl %s: %w\noutput: %s", ctrl, err, string(out))
	}
	return nil
}

func v4l2FormatMatches(output string, width, height int, pixelformat string) bool {
	return strings.Contains(output, fmt.Sprintf("Width/Height      : %d/%d", width, height)) &&
		strings.Contains(output, fmt.Sprintf("Pixel Format      : '%s'", pixelformat))
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

// LoadModule checks if v4l2loopback is available and returns instructions
// if the module needs to be installed.
func LoadModule() error {
	if err := EnsureV4L2Device(DefaultCameraDevice); err != nil {
		return err
	}
	return nil
}
