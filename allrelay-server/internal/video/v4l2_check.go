package video

import (
	"os/exec"
	"strings"
)

// CheckV4L2Capture returns true if the v4l2 device supports Video Capture
// (i.e., exclusive_caps=1 is active).
func CheckV4L2Capture(device string) bool {
	out, err := exec.Command("v4l2-ctl", "-d", device, "--info").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "Video Capture")
}
