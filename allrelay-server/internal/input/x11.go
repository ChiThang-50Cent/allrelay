// Package input captures user input events and maps them to Android events.
//
// Input events (keyboard, mouse, touch) are captured from the host system
// and translated into Android-compatible control messages for injection
// via the TCP control channel.
//
// X11 capture uses the `xinput test-xi2 --root` subprocess to monitor
// keyboard and mouse/touch events globally.
package input

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// EventType represents the type of an input event.
type EventType int

const (
	EventNone    EventType = iota
	EventKeyDown
	EventKeyUp
	EventTouchDown
	EventTouchMove
	EventTouchUp
	EventScroll
)

// String returns a human-readable name for the event type.
func (e EventType) String() string {
	switch e {
	case EventKeyDown:
		return "KeyDown"
	case EventKeyUp:
		return "KeyUp"
	case EventTouchDown:
		return "TouchDown"
	case EventTouchMove:
		return "TouchMove"
	case EventTouchUp:
		return "TouchUp"
	case EventScroll:
		return "Scroll"
	default:
		return "None"
	}
}

// Event represents a captured input event ready for injection.
type Event struct {
	Type    EventType
	Keycode int     // Android KeyEvent keycode (for keyboard)
	X       float64 // Normalized X coordinate [0..1] (for touch)
	Y       float64 // Normalized Y coordinate [0..1] (for touch)
	ScrollD float64 // Scroll delta
}

// X11Capture captures X11 input events via the xinput subprocess.
// It monitors all X11 input (keyboard and pointer) and emits parsed events.
type X11Capture struct {
	cmd    *exec.Cmd
	events chan Event
	done   chan struct{}
	mu     sync.Mutex
}

// NewX11Capture creates a new X11 input capturer.
// It spawns `xinput test-xi2 --root` and begins parsing its output.
func NewX11Capture() (*X11Capture, error) {
	c := &X11Capture{
		events: make(chan Event, 256),
		done:   make(chan struct{}),
	}

	if err := c.start(); err != nil {
		return nil, fmt.Errorf("x11 capture: %w", err)
	}

	return c, nil
}

// start launches the xinput subprocess and begins parsing.
func (c *X11Capture) start() error {
	c.cmd = exec.Command("xinput", "test-xi2", "--root")

	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := c.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("start xinput: %w", err)
	}

	// Monitor stderr
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			slog.Warn("xinput stderr", "line", scanner.Text())
		}
	}()

	// Parse events from stdout
	go c.parse(stdout)

	slog.Info("X11 input capture started")
	return nil
}

// parse reads and parses xinput test-xi2 output.
//
// Example output:
//
//	EVENT type 2 (KeyPress)
//	    device: 3 (3)
//	    detail: 38
//	    flags: root
//	    ...
//
//	EVENT type 3 (KeyRelease)
//	    device: 3 (3)
//	    detail: 38
//	    ...
//
//	EVENT type 15 (RawButtonPress)
//	    device: 2 (12)
//	    detail: 1
//	    ...
//
//	EVENT type 17 (RawMotion)
//	    device: 2 (12)
//	    detail: 0
//	    valuators:
//	          0: 1234.56 (Rel X)
//	          1: 567.89 (Rel Y)
//	    ...
func (c *X11Capture) parse(r io.Reader) {
	defer close(c.events)

	scanner := bufio.NewScanner(r)
	var currentType int
	var currentDetail int
	var relX, relY float64
	inMotion := false
	gotValuator0 := false
	gotValuator1 := false

	for scanner.Scan() {
		select {
		case <-c.done:
			return
		default:
		}

		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, "EVENT type ") {
			// New event begins
			parts := strings.SplitN(line, "(", 2)
			if len(parts) >= 2 {
				typeStr := strings.TrimPrefix(parts[0], "EVENT type ")
				typeStr = strings.TrimSpace(typeStr)
				if t, err := strconv.Atoi(typeStr); err == nil {
					currentType = t
					currentDetail = 0
					relX = 0
					relY = 0
					inMotion = false
					gotValuator0 = false
					gotValuator1 = false
				}
			}
			continue
		}

		if strings.HasPrefix(line, "detail:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				if d, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
					currentDetail = d
				}
			}
			continue
		}

		if strings.HasPrefix(line, "valuators:") {
			inMotion = true
			continue
		}

		if inMotion && strings.Contains(line, ":") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				valIdx, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
				valStr := strings.TrimSpace(parts[1])
				// Remove the label in parentheses
				if parenIdx := strings.Index(valStr, "("); parenIdx >= 0 {
					valStr = strings.TrimSpace(valStr[:parenIdx])
				}
				val, err2 := strconv.ParseFloat(valStr, 64)

				if err1 == nil && err2 == nil {
					if valIdx == 0 {
						relX = val
						gotValuator0 = true
					} else if valIdx == 1 {
						relY = val
						gotValuator1 = true
					}
				}
			}
			continue
		}

		// Empty line or end of event block
		if line == "" || strings.HasPrefix(line, "flags:") ||
			strings.HasPrefix(line, "modifiers:") ||
			strings.HasPrefix(line, "group:") ||
			strings.HasPrefix(line, "windows:") {
			continue
		}

		// If we're in a motion event and have both valuators, emit touch event
		if inMotion && gotValuator0 && gotValuator1 {
			c.emitMotion(currentType, currentDetail, relX, relY)
			inMotion = false
		}

		// After valuators, next EVENT line signals the end of this event
		// Emit the button/key event
		c.emitButtonKey(currentType, currentDetail)
	}

	if err := scanner.Err(); err != nil {
		slog.Error("xinput scanning error", "error", err)
	}
}

// emitButtonKey emits a key or button event based on the xinput event type and detail.
func (c *X11Capture) emitButtonKey(eventType, detail int) {
	switch eventType {
	case 2: // KeyPress
		androidKey := X11ToAndroidKeycode(detail)
		if androidKey != 0 {
			select {
			case c.events <- Event{Type: EventKeyDown, Keycode: androidKey}:
			default:
			}
		}
	case 3: // KeyRelease
		androidKey := X11ToAndroidKeycode(detail)
		if androidKey != 0 {
			select {
			case c.events <- Event{Type: EventKeyUp, Keycode: androidKey}:
			default:
			}
		}
	case 15: // RawButtonPress
		c.emitButtonPress(detail)
	case 16: // RawButtonRelease
		c.emitButtonRelease(detail)
	}
}

// emitButtonPress handles mouse button press events.
func (c *X11Capture) emitButtonPress(button int) {
	switch button {
	case 1: // Left button → touch down
		select {
		case c.events <- Event{Type: EventTouchDown, X: 0.5, Y: 0.5}:
		default:
		}
	case 3: // Right button → Back key
		select {
		case c.events <- Event{Type: EventKeyDown, Keycode: 4}: // KEYCODE_BACK
		default:
		}
	case 2: // Middle button → Home key
		select {
		case c.events <- Event{Type: EventKeyDown, Keycode: 3}: // KEYCODE_HOME
		default:
		}
	}
}

// emitButtonRelease handles mouse button release events.
func (c *X11Capture) emitButtonRelease(button int) {
	switch button {
	case 1: // Left button → touch up
		select {
		case c.events <- Event{Type: EventTouchUp, X: 0.5, Y: 0.5}:
		default:
		}
	}
}

// emitMotion handles pointer motion events.
func (c *X11Capture) emitMotion(eventType int, detail int, relX, relY float64) {
	// Only emit touch events when button 1 is held (motion with button press = touch move)
	// xinput RawMotion events don't tell us button state directly.
	// For simplicity, we emit relative motion as touch move.
	if relX != 0 || relY != 0 {
		// Normalize relative motion to small position changes
		// Actual position tracking requires state, simplified for v1
		select {
		case c.events <- Event{Type: EventTouchMove, X: 0.5 + relX*0.001, Y: 0.5 + relY*0.001}:
		default:
		}
	}
}

// Events returns the channel of captured input events.
func (c *X11Capture) Events() <-chan Event {
	return c.events
}

// Close stops the input capture.
func (c *X11Capture) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	select {
	case <-c.done:
		return nil
	default:
		close(c.done)
	}

	if c.cmd != nil && c.cmd.Process != nil {
		c.cmd.Process.Kill()
		c.cmd.Wait()
	}

	return nil
}
