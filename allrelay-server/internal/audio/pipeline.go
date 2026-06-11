// Package audio handles audio stream processing using GStreamer pipelines.
//
// Audio streams flow in both directions:
//   - Mic: Phone → Go (Opus) → decode → PipeWire sink
//   - Speaker: PipeWire capture → encode (Opus) → Go → Phone
//
// This package manages the GStreamer subprocess for speaker capture.
package audio

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
)

// CapturePipeline manages a GStreamer subprocess that captures system audio,
// encodes it to Opus (Ogg-wrapped), and outputs on stdout.
//
// Data is read via the Read() method from the pipeline's stdout.
// The pipeline runs until Close() is called or the process exits.
type CapturePipeline struct {
	name    string
	cmd     *exec.Cmd
	stdout  io.ReadCloser
	done    chan error
}

// SpeakerCapturePipeline creates a pipeline that captures system audio
// from the default audio monitor, encodes to Opus, muxes to Ogg,
// and outputs on stdout.
//
// The caller reads Ogg pages from the returned CapturePipeline and extracts
// raw Opus packets to send to the phone via the speaker stream.
func SpeakerCapturePipeline() (*CapturePipeline, error) {
	// Use pulsesrc to capture system audio from the default monitor.
	// @DEFAULT_MONITOR@ resolves via pipewire-pulse to the current default
	// sink's monitor (e.g., alsa_output.xxx.monitor).
	args := []string{
		"-q",
		"pulsesrc", "device=@DEFAULT_MONITOR@",
		"!", "audio/x-raw,rate=48000,channels=1",
		"!", "opusenc", "bitrate=64000", "bitrate-type=cbr", "frame-size=20",
		"!", "oggmux", "max-delay=0", "max-page-delay=2000000",
		"!", "fdsink", "fd=1",
	}
	return NewCapturePipeline("speaker-capture", "gst-launch-1.0", args)
}

// NewCapturePipeline creates a capture pipeline that reads from the
// subprocess's stdout.
func NewCapturePipeline(name, command string, args []string) (*CapturePipeline, error) {
	if name == "" {
		return nil, errors.New("capture pipeline name required")
	}

	cmd := exec.Command(command, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", command, err)
	}

	p := &CapturePipeline{
		name:   name,
		cmd:    cmd,
		stdout: stdout,
		done:   make(chan error, 1),
	}

	// Monitor the process in background
	go func() {
		errBuf := make([]byte, 4096)
		n, _ := stderr.Read(errBuf)
		err := cmd.Wait()

		if n > 0 {
			slog.Error("Capture pipeline stderr",
				"name", name,
				"output", string(errBuf[:n]))
		}

		if err != nil {
			slog.Error("Capture pipeline exited with error",
				"name", name,
				"error", err)
		} else {
			slog.Debug("Capture pipeline exited normally", "name", name)
		}

		select {
		case p.done <- err:
		default:
		}
	}()

	slog.Info("Capture pipeline started", "name", name, "cmd", command)
	return p, nil
}

// Read reads data from the capture pipeline's stdout.
// Implements io.Reader.
func (p *CapturePipeline) Read(data []byte) (int, error) {
	if p.stdout == nil {
		return 0, errors.New("capture pipeline closed")
	}
	return p.stdout.Read(data)
}

// Close terminates the capture pipeline.
func (p *CapturePipeline) Close() error {
	if p.stdout == nil {
		return nil
	}

	slog.Info("Stopping capture pipeline", "name", p.name)

	p.stdout.Close()
	p.stdout = nil

	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
		p.cmd.Wait()
	}

	return nil
}

// Done returns a channel that receives an error if the pipeline
// exits unexpectedly.
func (p *CapturePipeline) Done() <-chan error {
	return p.done
}
