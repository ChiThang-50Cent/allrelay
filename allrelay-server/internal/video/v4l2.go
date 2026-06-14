package video

import (
	"fmt"
	"os"
	"os/exec"
	"log/slog"
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
// With exclusive_caps=1, the device initially reports as CAPTURE-only;
// setting the output format triggers the mode switch to OUTPUT.
//
// If the format is already set (from a previous run or crash), this is a no-op
// and returns nil (trying to reset would fail with VIDIOC_G_FMT).
func SetupV4L2Output(device string, width, height int, pixelformat string) error {
	// Check if format is already configured correctly
	if fmt, err := getV4L2Format(device, "--get-fmt-video-out"); err == nil {
		slog.Debug("v4l2: output format already set", "format", fmt)
		return nil
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
		// VIDIOC_G_FMT failure means device is in a bad state.
		// Try reloading v4l2loopback (needs sudo — only works if passwordless sudo).
		slog.Warn("v4l2: format setup failed, trying module reload", "error", err, "output", string(out))
		if reloadErr := reloadV4L2Module(); reloadErr != nil {
			slog.Warn("v4l2: module reload failed (needs sudo?)", "error", reloadErr)
			return fmt.Errorf("v4l2-ctl set output format: %w\noutput: %s", err, string(out))
		}
		// Retry after reload
		cmd = exec.Command("v4l2-ctl",
			"-d", device,
			"--set-fmt-video-out",
			fmt.Sprintf("width=%d,height=%d,pixelformat=%s", width, height, pixelformat),
		)
		out, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("v4l2-ctl set output format (after reload): %w\noutput: %s", err, string(out))
		}
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

// reloadV4L2Module attempts to reload the v4l2loopback module.
// Uses sudo rmmod + modprobe; only works with passwordless sudo.
func reloadV4L2Module() error {
	if err := exec.Command("sudo", "rmmod", "v4l2loopback").Run(); err != nil {
		slog.Debug("v4l2: rmmod skipped (may not be loaded)", "error", err)
	}
	cmd := exec.Command("sudo", "modprobe", "v4l2loopback",
		"video_nr=10",
		"card_label=AllRelay Cam",
		"exclusive_caps=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("modprobe: %w\noutput: %s", err, string(out))
	}
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

// LoadModule checks if v4l2loopback is available and returns instructions
// if the module needs to be installed.
func LoadModule() error {
	if err := EnsureV4L2Device(DefaultCameraDevice); err != nil {
		return err
	}
	return nil
}
