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

	if err := p.start(); err != nil {
		return nil, fmt.Errorf("start pipeline %q: %w", name, err)
	}

	slog.Info("Pipeline started", "name", name)
	return p, nil
}

// start launches the gst-launch-1.0 subprocess.
func (p *Pipeline) start() error {
	// gst-launch-1.0 requires each token of the pipeline as a separate argument.
	// For example: gst-launch-1.0 -q fdsrc fd=0 ! h264parse ! fakesink
	// Not: gst-launch-1.0 -q "fdsrc fd=0 ! h264parse ! fakesink"
	args := []string{"-q"}
	args = append(args, strings.Fields(p.pipeline)...)
	p.cmd = exec.Command("gst-launch-1.0", args...)

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
		return fmt.Errorf("start gst-launch-1.0: %w", err)
	}

	// Monitor the process in background
	go func() {
		// Read stderr for error messages
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
func (p *Pipeline) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stdin == nil {
		return 0, errors.New("pipeline closed")
	}

	return p.stdin.Write(data)
}

// Close terminates the GStreamer pipeline.
// It closes stdin (signaling EOF to gst-launch-1.0) and waits
// for the process to exit gracefully.
func (p *Pipeline) Close() error {
	p.mu.Lock()
	if p.stdin == nil {
		p.mu.Unlock()
		return nil
	}

	slog.Info("Stopping pipeline", "name", p.name)

	// Close stdin to signal end-of-stream to GStreamer
	stdin := p.stdin
	p.stdin = nil
	p.mu.Unlock()

	stdin.Close()

	// Wait for process to exit (or kill it)
	if p.cmd != nil && p.cmd.Process != nil {
		// TODO: add timeout + kill if stuck
		err := p.cmd.Wait()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				// Non-zero exit is expected when we close stdin
				slog.Debug("Pipeline terminated",
					"name", p.name,
					"exit_code", exitErr.ExitCode())
				return nil
			}
			return fmt.Errorf("wait pipeline %q: %w", p.name, err)
		}
	}

	return nil
}

// Done returns a channel that receives an error if the pipeline
// exits unexpectedly. The caller can select on this to detect failures.
func (p *Pipeline) Done() <-chan error {
	return p.done
}

// CameraPipeline creates a pipeline that routes decoded video to a
// v4l2loopback device, making it available as a virtual webcam.
//
// The pipeline: H.264 stdin → decode → convert to YUY2 → v4l2sink
//
// device is the v4l2loopback device path, e.g. "/dev/video10".
func CameraPipeline(device string) (*Pipeline, error) {
	pipeline := fmt.Sprintf(
		"fdsrc fd=0 ! h264parse ! avdec_h264 ! videoconvert ! video/x-raw,format=YUY2 ! v4l2sink device=%s sync=false",
		device,
	)
	return NewPipeline("camera", pipeline)
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
