package video

import (
	"fmt"
	"os"
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
