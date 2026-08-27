package audio

import (
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	VirtualMicSinkName   = "allrelay-mic-sink"
	VirtualMicSourceName = "allrelay-phone-mic"
	virtualMicSinkDesc   = "AllRelay-Mic-Sink"
	virtualMicSourceDesc = "AllRelay-Phone-Mic"
)

var virtualMicMu sync.Mutex

// EnsureVirtualMicDevices creates a PulseAudio/pipewire-pulse sink+source pair
// for the phone microphone if it does not already exist.
//
// Audio flow:
//
//	pacat playback -> null sink -> sink monitor -> remap source
//
// Browsers discover the remap source as a normal microphone.
func EnsureVirtualMicDevices() error {
	virtualMicMu.Lock()
	defer virtualMicMu.Unlock()

	if err := ensurePulseSink(); err != nil {
		return err
	}
	if err := ensurePulseSource(); err != nil {
		return err
	}
	return nil
}

func ensurePulseSink() error {
	exists, moduleID, desc, err := pulseObjectInfo("sinks", VirtualMicSinkName)
	if err != nil {
		return err
	}
	if exists {
		if desc == virtualMicSinkDesc {
			return nil
		}
		slog.Info("Mic: reloading Pulse sink to fix description", "name", VirtualMicSinkName, "old", desc, "new", virtualMicSinkDesc)
		if err := unloadPulseModule(moduleID); err != nil {
			return err
		}
	}

	args := []string{
		"load-module",
		"module-null-sink",
		"sink_name=" + VirtualMicSinkName,
		"sink_properties=device.description=" + virtualMicSinkDesc,
	}
	out, err := exec.Command("pactl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("load virtual mic sink: %w: %s", err, strings.TrimSpace(string(out)))
	}
	slog.Info("Mic: created Pulse sink", "name", VirtualMicSinkName, "module", strings.TrimSpace(string(out)))
	return nil
}

func ensurePulseSource() error {
	exists, moduleID, desc, err := pulseObjectInfo("sources", VirtualMicSourceName)
	if err != nil {
		return err
	}
	if exists {
		if desc == virtualMicSourceDesc {
			return nil
		}
		slog.Info("Mic: reloading Pulse source to fix description", "name", VirtualMicSourceName, "old", desc, "new", virtualMicSourceDesc)
		if err := unloadPulseModule(moduleID); err != nil {
			return err
		}
	}

	args := []string{
		"load-module",
		"module-remap-source",
		"master=" + VirtualMicSinkName + ".monitor",
		"source_name=" + VirtualMicSourceName,
		"source_properties=device.description=" + virtualMicSourceDesc,
	}
	out, err := exec.Command("pactl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("load virtual mic source: %w: %s", err, strings.TrimSpace(string(out)))
	}
	slog.Info("Mic: created Pulse source", "name", VirtualMicSourceName, "module", strings.TrimSpace(string(out)))
	return nil
}

func pulseObjectInfo(kind, name string) (exists bool, moduleID string, desc string, err error) {
	out, err := exec.Command("pactl", "list", kind).Output()
	if err != nil {
		return false, "", "", fmt.Errorf("pactl list %s: %w", kind, err)
	}

	lines := strings.Split(string(out), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line != "Name: "+name {
			continue
		}

		exists = true
		for j := i + 1; j < len(lines); j++ {
			entry := strings.TrimSpace(lines[j])
			if strings.HasPrefix(entry, "Name: ") {
				break
			}
			if strings.HasPrefix(entry, "Description: ") {
				desc = strings.TrimPrefix(entry, "Description: ")
			}
			if strings.HasPrefix(entry, "Owner Module: ") {
				moduleID = strings.TrimPrefix(entry, "Owner Module: ")
			}
		}
		return exists, moduleID, desc, nil
	}

	return false, "", "", nil
}

func unloadPulseModule(moduleID string) error {
	if moduleID == "" || moduleID == "n/a" {
		return nil
	}
	out, err := exec.Command("pactl", "unload-module", moduleID).CombinedOutput()
	if err != nil {
		return fmt.Errorf("unload pulse module %s: %w: %s", moduleID, err, strings.TrimSpace(string(out)))
	}
	return nil
}

type VirtualMicWriter struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan error
	mu    sync.Mutex
}

// StartVirtualMicWriter starts a pacat writer feeding PCM into the virtual mic sink.
func StartVirtualMicWriter(sampleRate, channels int) (*VirtualMicWriter, error) {
	if err := EnsureVirtualMicDevices(); err != nil {
		return nil, err
	}

	args := []string{
		"--playback",
		"--raw",
		"--format=s16le",
		"--rate=" + strconv.Itoa(sampleRate),
		"--channels=" + strconv.Itoa(channels),
		"--device=" + VirtualMicSinkName,
	}
	cmd := exec.Command("pacat", args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pacat stdin: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("pacat stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start pacat: %w", err)
	}

	w := &VirtualMicWriter{
		cmd:   cmd,
		stdin: stdin,
		done:  make(chan error, 1),
	}

	go func() {
		buf := make([]byte, 4096)
		n, _ := stderr.Read(buf)
		err := cmd.Wait()
		if n > 0 {
			slog.Error("Mic pacat stderr", "output", strings.TrimSpace(string(buf[:n])))
		}
		select {
		case w.done <- err:
		default:
		}
		close(w.done)
	}()

	slog.Info("Mic: virtual writer started", "sink", VirtualMicSinkName, "rate", sampleRate, "channels", channels)
	return w, nil
}

func (w *VirtualMicWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	stdin := w.stdin
	w.mu.Unlock()
	if stdin == nil {
		return 0, io.ErrClosedPipe
	}
	return stdin.Write(p)
}

func (w *VirtualMicWriter) Close() error {
	w.mu.Lock()
	stdin := w.stdin
	w.stdin = nil
	w.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if w.cmd == nil || w.cmd.Process == nil {
		return nil
	}

	select {
	case err := <-w.done:
		return err
	case <-time.After(2 * time.Second):
		_ = w.cmd.Process.Kill()
		select {
		case err := <-w.done:
			return err
		default:
			return nil
		}
	}
}
