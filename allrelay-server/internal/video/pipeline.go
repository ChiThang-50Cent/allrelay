// Package video handles video stream processing using GStreamer pipelines.
//
// Video streams (screen H.264, camera H.264) arrive over TCP from the
// Android device. This package spawns GStreamer subprocesses to decode
// and route the video to the appropriate output:
//
//	Camera → v4l2loopback (/dev/video10) via v4l2sink
//	Monitor → SDL2/GL window via glimagesink (Phase 3.2)
package video

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Pipeline manages a GStreamer subprocess that reads H.264 from stdin
// and renders it to a video output sink.
//
// The GStreamer command is executed as a child process. H.264 data is
// written via the Write() method and piped to the process's stdin.
// The pipeline runs until Close() is called or the process exits.
type Pipeline struct {
	name   string
	pipeline string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	done   chan error
	mu     sync.Mutex
}

// NewPipeline creates a GStreamer pipeline with the given description.
//
// The pipeline string is passed directly to gst-launch-1.0. H.264 data
// enters via fdsrc fd=0 (stdin). The caller must ensure the pipeline
// terminates with a valid video sink.
//
// Example camera pipeline:
//
//	fdsrc fd=0 ! h264parse ! avdec_h264 ! videoconvert ! video/x-raw,format=YUY2 ! v4l2sink device=/dev/video10 sync=false
//
// Example monitor pipeline:
//
//	fdsrc fd=0 ! h264parse ! avdec_h264 ! videoconvert ! glimagesink sync=false
func NewPipeline(name, pipeline string) (*Pipeline, error) {
	if name == "" {
		return nil, errors.New("pipeline name required")
	}
	if pipeline == "" {
		return nil, errors.New("pipeline description required")
	}

	p := &Pipeline{
		name:     name,
		pipeline: pipeline,
		done:     make(chan error, 1),
	}

	// gst-launch-1.0 requires each token as a separate argument
	args := append([]string{"-q"}, strings.Fields(pipeline)...)
	if err := p.startCmd("gst-launch-1.0", args); err != nil {
		return nil, fmt.Errorf("start pipeline %q: %w", name, err)
	}

	slog.Info("Pipeline started", "name", name)
	return p, nil
}

// startCmd launches the given command as a subprocess and sets up
// stdin piping and error monitoring.
func (p *Pipeline) startCmd(command string, args []string) error {
	p.cmd = exec.Command(command, args...)

	var err error
	p.stdin, err = p.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}

	// Capture stderr for error diagnostics
	stderr, err := p.cmd.StderrPipe()
	if err != nil {
		p.stdin.Close()
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := p.cmd.Start(); err != nil {
		p.stdin.Close()
		return fmt.Errorf("start %s: %w", command, err)
	}

	// Monitor the process in background
	go func() {
		errBuf := make([]byte, 4096)
		n, _ := stderr.Read(errBuf)
		err := p.cmd.Wait()

		if n > 0 {
			slog.Error("Pipeline stderr",
				"name", p.name,
				"output", string(errBuf[:n]))
		}

		if err != nil {
			slog.Error("Pipeline exited with error",
				"name", p.name,
				"error", err)
		}

		select {
		case p.done <- err:
		default:
		}
	}()

	return nil
}

// Write sends H.264 data to the GStreamer pipeline via stdin.
// Implements io.Writer.
//
// IMPORTANT: does NOT hold p.mu during the actual IO operation.
// This prevents a deadlock where Write blocks on a full pipe
// while Close() tries to acquire the mutex to shut down.
func (p *Pipeline) Write(data []byte) (int, error) {
	p.mu.Lock()
	stdin := p.stdin
	p.mu.Unlock()

	if stdin == nil {
		return 0, errors.New("pipeline closed")
	}

	return stdin.Write(data)
}

// Close terminates the GStreamer pipeline.
// It closes stdin (signaling EOF to the subprocess) and waits
// for the process to exit gracefully. If the process does not
// exit within 2 seconds (e.g. stuck on v4l2 output), it is killed.
func (p *Pipeline) Close() error {
	p.mu.Lock()
	if p.stdin == nil {
		p.mu.Unlock()
		return nil
	}

	slog.Info("Stopping pipeline", "name", p.name)

	// Close stdin to signal end-of-stream to the subprocess.
	// This also unblocks any Write() call stuck on a full pipe.
	stdin := p.stdin
	p.stdin = nil
	p.mu.Unlock()

	stdin.Close()

	// Wait for process to exit (with kill timeout)
	if p.cmd != nil && p.cmd.Process != nil {
		done := make(chan error, 1)
		go func() {
			done <- p.cmd.Wait()
		}()
		select {
		case err := <-done:
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					slog.Debug("Pipeline terminated",
						"name", p.name,
						"exit_code", exitErr.ExitCode())
					return nil
				}
				return fmt.Errorf("wait pipeline %q: %w", p.name, err)
			}
		case <-time.After(2 * time.Second):
			slog.Warn("Pipeline stuck, sending SIGKILL", "name", p.name)
			p.cmd.Process.Kill()
			<-done
		}
	}

	return nil
}

// Done returns a channel that receives an error if the pipeline
// exits unexpectedly. The caller can select on this to detect failures.
func (p *Pipeline) Done() <-chan error {
	return p.done
}

// CameraPipeline creates a pipeline that decodes H.264 from stdin and
// writes decoded YUYV frames to a v4l2loopback device (/dev/video10).
//
// This works with Chrome/Zoom/Meet (V4L2-based) and Firefox (falls back to V4L2).
// PipeWire alternative exists in PipeWireCameraPipeline() for browsers that
// support it natively, but V4L2 is universally compatible.
//
// device is the v4l2loopback device path, e.g. "/dev/video10".
func CameraPipeline(device string) (*Pipeline, error) {
	// ffmpeg: H.264 stdin → decode → scale → YUYV → v4l2 output
	// Scale to 640x480 for browser compatibility (Chrome/Edge reject huge frames).
	args := []string{
		"-loglevel", "error",
		"-f", "h264",
		"-i", "pipe:0",
		"-vf", "scale=640:480:force_original_aspect_ratio=decrease,pad=640:480:(ow-iw)/2:(oh-ih)/2",
		"-pix_fmt", "yuyv422",
		"-f", "v4l2",
		device,
	}
	return NewCmdPipeline("camera", "ffmpeg", args)
}

// PipeWireCameraPipeline creates a PipeWire video source from v4l2loopback
// so browsers see it via the camera portal.
// Reads decoded frames from the v4l2loopback device (fed by ffmpeg) and
// re-publishes through PipeWire with NV12 conversion for browser compat.
func PipeWireCameraPipeline(device string) (*Pipeline, error) {
	args := []string{
		"-q",
		"v4l2src", "device=" + device,
		"!", "videoconvert",
		"!", "video/x-raw,format=NV12,framerate=30/1",
		"!", "pipewiresink", "mode=provide",
		"stream-properties=p,media.class=Video/Source,media.role=Camera,node.name=allrelay-camera-pw,node.description=AllRelay_Camera",
	}
	return NewCmdPipeline("camera-pipewire", "gst-launch-1.0", args)
}

// NewCmdPipeline creates a pipeline that runs an arbitrary command
// and pipes data to its stdin.
func NewCmdPipeline(name, command string, args []string) (*Pipeline, error) {
	if name == "" {
		return nil, errors.New("pipeline name required")
	}

	p := &Pipeline{
		name: name,
		done: make(chan error, 1),
	}

	if err := p.startCmd(command, args); err != nil {
		return nil, fmt.Errorf("start pipeline %q: %w", name, err)
	}

	slog.Info("Pipeline started", "name", name, "cmd", command)
	return p, nil
}

// MonitorPipeline creates a pipeline that displays decoded video
// in a window using the best available video sink.
//
// The pipeline: H.264 stdin → decode → convert → autovideosink
// autovideosink selects the best available sink (glimagesink,
// xvimagesink, etc.).
func MonitorPipeline() (*Pipeline, error) {
	pipeline := "fdsrc fd=0 ! h264parse ! avdec_h264 ! videoconvert ! autovideosink sync=false"
	return NewPipeline("monitor", pipeline)
}
