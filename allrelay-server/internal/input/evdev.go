package input

import (
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Linux input event constants (from linux/input.h and linux/input-event-codes.h)
const (
	// Event types
	evSyn      uint16 = 0x00
	evKey      uint16 = 0x01
	evRel      uint16 = 0x02
	evAbs      uint16 = 0x03
	evMsc      uint16 = 0x04

	// Synchronization events
	synReport uint16 = 0x00
	synConfig uint16 = 0x01
	synDropped uint16 = 0x03

	// Relative axes
	relX uint16 = 0x00
	relY uint16 = 0x01
	relWheel uint16 = 0x08
	relHWheel uint16 = 0x06

	// Absolute axes
	absX uint16 = 0x00
	absY uint16 = 0x01
	absMTslot       uint16 = 0x2f
	absMTtouchMajor uint16 = 0x30
	absMTwidthMajor uint16 = 0x32
	absMTpositionX  uint16 = 0x35
	absMTpositionY  uint16 = 0x36
	absMTtrackingID uint16 = 0x39

	// Key/Button codes
	btnLeft   uint16 = 0x110
	btnRight  uint16 = 0x111
	btnMiddle uint16 = 0x112
	btnTouch  uint16 = 0x14a

	// Key value (1 = pressed, 0 = released)
	keyPressed uint32 = 1
)

// inputEvent is the raw Linux input_event struct (from linux/input.h).
//
//	struct input_event {
//	    struct timeval time;  // 16 bytes (8 + 8 on 64-bit)
//	    __u16 type;           // 2 bytes
//	    __u16 code;           // 2 bytes
//	    __s32 value;          // 4 bytes
//	};
//
// Total: 24 bytes on 64-bit (with 4-byte padding at the end)
type inputEvent struct {
	Sec   int64
	Usec  int64
	Type  uint16
	Code  uint16
	Value int32
}

// evdevEventSize is the size of the input_event struct in bytes.
const evdevEventSize = int(unsafe.Sizeof(inputEvent{}))

// EvdevCapture captures input events directly from /dev/input/event* devices.
//
// This approach works on both X11 and Wayland, and requires read access to
// the input device files (/dev/input/event*). The user must be in the "input"
// group or run as root.
//
// The capturer opens all available event devices, filters for keyboard and
// pointer/touch devices, and reads raw events. It maps them to the same
// Event types used by X11Capture.
type EvdevCapture struct {
	devices []*evdevDevice
	events  chan Event
	done    chan struct{}
	mu      sync.Mutex
}

// evdevDevice represents an open input device.
type evdevDevice struct {
	name string
	file *os.File
	kind deviceKind
}

type deviceKind int

const (
	kindUnknown  deviceKind = iota
	kindKeyboard
	kindPointer
	kindTouch
)

// NewEvdevCapture creates a new evdev input capturer.
// It opens all accessible /dev/input/event* devices and begins reading events.
func NewEvdevCapture() (*EvdevCapture, error) {
	c := &EvdevCapture{
		events: make(chan Event, 256),
		done:   make(chan struct{}),
	}

	if err := c.scanDevices(); err != nil {
		return nil, fmt.Errorf("evdev capture: %w", err)
	}

	if len(c.devices) == 0 {
		return nil, fmt.Errorf("no accessible input devices found. " +
			"Add user to 'input' group: sudo usermod -a -G input $USER")
	}

	// Start reading from all devices
	for _, dev := range c.devices {
		go c.readDevice(dev)
	}

	slog.Info("evdev input capture started",
		"devices", len(c.devices),
		"kbd", c.countByKind(kindKeyboard),
		"ptr", c.countByKind(kindPointer),
		"touch", c.countByKind(kindTouch))

	return c, nil
}

// countByKind counts devices of a specific kind.
func (c *EvdevCapture) countByKind(k deviceKind) int {
	n := 0
	for _, d := range c.devices {
		if d.kind == k {
			n++
		}
	}
	return n
}

// scanDevices scans /dev/input/event* and opens relevant devices.
func (c *EvdevCapture) scanDevices() error {
	matches, err := filepath.Glob("/dev/input/event*")
	if err != nil {
		return fmt.Errorf("glob /dev/input/event*: %w", err)
	}

	for _, path := range matches {
		dev, err := c.openDevice(path)
		if err != nil {
			slog.Debug("evdev: skip device", "path", path, "error", err)
			continue
		}
		if dev == nil {
			continue
		}
		c.devices = append(c.devices, dev)
	}

	return nil
}

// openDevice opens and identifies an input device.
func (c *EvdevCapture) openDevice(path string) (*evdevDevice, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}

	// Get device name via EVIOCGNAME ioctl
	name := getDeviceName(f.Fd())
	if name == "" {
		f.Close()
		return nil, fmt.Errorf("cannot read device name")
	}

	// Get supported event types via EVIOCGBIT ioctl
	evBits := getEventBits(f.Fd())

	kind := classifyDevice(name, evBits)
	if kind == kindUnknown {
		f.Close()
		return nil, nil // Not interested
	}

	return &evdevDevice{
		name: name,
		file: f,
		kind: kind,
	}, nil
}

// classifyDevice determines the device kind based on name and capabilities.
func classifyDevice(name string, evBits []byte) deviceKind {
	nameLower := strings.ToLower(name)

	// Skip clearly non-input devices
	skipPatterns := []string{
		"power button", "lid switch", "sleep button",
		"video bus", "pc speaker",
	}
	for _, p := range skipPatterns {
		if strings.Contains(nameLower, p) {
			return kindUnknown
		}
	}

	hasKey := testBit(evBits, evKey)
	hasRel := testBit(evBits, evRel)
	hasAbs := testBit(evBits, evAbs)

	// Touchscreens and touchpads have ABS + KEY (BTN_TOUCH)
	if hasAbs {
		if strings.Contains(nameLower, "touch") ||
			strings.Contains(nameLower, "wacom") ||
			strings.Contains(nameLower, "pen") {
			return kindTouch
		}
		// Touchpads are typically pointer-like with ABS
		if strings.Contains(nameLower, "touchpad") ||
			strings.Contains(nameLower, "trackpad") ||
			strings.Contains(nameLower, "synaptics") {
			return kindPointer
		}
	}

	// Mice have REL + KEY (BTN_LEFT, BTN_RIGHT)
	if hasRel && hasKey {
		if strings.Contains(nameLower, "mouse") ||
			strings.Contains(nameLower, "pointer") ||
			!strings.Contains(nameLower, "keyboard") {
			return kindPointer
		}
	}

	// Keyboards have KEY
	if hasKey {
		return kindKeyboard
	}

	return kindUnknown
}

// readDevice reads events from a single device in a loop.
func (c *EvdevCapture) readDevice(dev *evdevDevice) {
	defer dev.file.Close()

	buf := make([]byte, evdevEventSize*64) // Read up to 64 events at once

	for {
		select {
		case <-c.done:
			return
		default:
		}

		n, err := dev.file.Read(buf)
		if err != nil {
			if err == io.EOF {
				return
			}
			slog.Debug("evdev read error", "device", dev.name, "error", err)
			return
		}

		// Process events (each is evdevEventSize bytes)
		for i := 0; i+evdevEventSize <= n; i += evdevEventSize {
			ev := parseEvent(buf[i : i+evdevEventSize])
			c.handleEvent(dev, ev)
		}
	}
}

// parseEvent parses a raw input_event from bytes.
func parseEvent(data []byte) inputEvent {
	return inputEvent{
		Sec:   int64(binary.LittleEndian.Uint64(data[0:8])),
		Usec:  int64(binary.LittleEndian.Uint64(data[8:16])),
		Type:  binary.LittleEndian.Uint16(data[16:18]),
		Code:  binary.LittleEndian.Uint16(data[18:20]),
		Value: int32(binary.LittleEndian.Uint32(data[20:24])),
	}
}

// handleEvent processes a single input event and emits the corresponding Event.
func (c *EvdevCapture) handleEvent(dev *evdevDevice, ev inputEvent) {
	switch ev.Type {
	case evKey:
		c.handleKeyEvent(dev, ev)
	case evRel:
		c.handleRelEvent(dev, ev)
	case evAbs:
		c.handleAbsEvent(dev, ev)
	}
}

// handleKeyEvent processes keyboard and button events.
func (c *EvdevCapture) handleKeyEvent(dev *evdevDevice, ev inputEvent) {
	pressed := ev.Value == int32(keyPressed)
	released := ev.Value == 0
	repeat := ev.Value == 2

	if repeat {
		return // Skip auto-repeat events
	}

	switch dev.kind {
	case kindKeyboard:
		if pressed {
			androidKey := LinuxToAndroidKeycode(int(ev.Code))
			if androidKey != 0 {
				c.emit(Event{Type: EventKeyDown, Keycode: androidKey})
			}
		} else if released {
			androidKey := LinuxToAndroidKeycode(int(ev.Code))
			if androidKey != 0 {
				c.emit(Event{Type: EventKeyUp, Keycode: androidKey})
			}
		}

	case kindPointer:
		switch ev.Code {
		case btnLeft:
			if pressed {
				c.emit(Event{Type: EventTouchDown, X: 0.5, Y: 0.5})
			} else if released {
				c.emit(Event{Type: EventTouchUp, X: 0.5, Y: 0.5})
			}
		case btnRight:
			if pressed {
				c.emit(Event{Type: EventKeyDown, Keycode: 4}) // KEYCODE_BACK
			} else if released {
				c.emit(Event{Type: EventKeyUp, Keycode: 4})
			}
		case btnMiddle:
			if pressed {
				c.emit(Event{Type: EventKeyDown, Keycode: 3}) // KEYCODE_HOME
			} else if released {
				c.emit(Event{Type: EventKeyUp, Keycode: 3})
			}
		}

	case kindTouch:
		if ev.Code == btnTouch {
			if pressed {
				c.emit(Event{Type: EventTouchDown, X: 0.5, Y: 0.5})
			} else if released {
				c.emit(Event{Type: EventTouchUp, X: 0.5, Y: 0.5})
			}
		}
	}
}

// handleRelEvent processes relative motion events (mice, scroll wheels).
func (c *EvdevCapture) handleRelEvent(dev *evdevDevice, ev inputEvent) {
	if dev.kind != kindPointer {
		return
	}

	switch ev.Code {
	case relWheel:
		if ev.Value != 0 {
			c.emit(Event{Type: EventScroll, ScrollD: float64(ev.Value)})
		}
	}
}

// handleAbsEvent processes absolute axis events (touchscreens).
func (c *EvdevCapture) handleAbsEvent(dev *evdevDevice, ev inputEvent) {
	if dev.kind != kindTouch {
		return
	}
	// Absolute touch events track position across multiple event slots.
	// For v1, we simplify: just emit touch move with normalized coordinates.
	// Full MT protocol support is a Phase 4 enhancement.
}

// emit sends an event to the channel (non-blocking).
func (c *EvdevCapture) emit(ev Event) {
	select {
	case c.events <- ev:
	default:
	}
}

// Events returns the channel of captured input events.
func (c *EvdevCapture) Events() <-chan Event {
	return c.events
}

// Close stops the evdev capturer and closes all device files.
func (c *EvdevCapture) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	select {
	case <-c.done:
		return nil
	default:
		close(c.done)
	}

	var errs []error
	for _, dev := range c.devices {
		if dev.file != nil {
			if err := dev.file.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}
	return nil
}

// --- ioctl helpers ---

// EVIOCGNAME ioctl to get device name (max 255 chars).
const (
	evIocGName uintptr = 0x82004506 // EVIOCGNAME(255)
	evIocGBit  uintptr = 0x80ff451f // EVIOCGBIT(0, EV_MAX)
	evMax      int     = 0x1f       // EV_MAX
)

// getDeviceName gets the device name via EVIOCGNAME ioctl.
func getDeviceName(fd uintptr) string {
	name := make([]byte, 256)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, evIocGName, uintptr(unsafe.Pointer(&name[0])))
	if errno != 0 {
		return ""
	}
	// Find null terminator
	for i, b := range name {
		if b == 0 {
			return string(name[:i])
		}
	}
	return string(name)
}

// getEventBits gets the supported event types via EVIOCGBIT ioctl.
func getEventBits(fd uintptr) []byte {
	// Size: ceil(EV_MAX/8) = ceil(31/8) = 4 bytes minimum
	// Actually: EVIOCGBIT(0, EV_MAX) returns a bitset of size ceil(EV_MAX/8)
	// We'll allocate enough for EV_MAX bits
	size := (evMax + 7) / 8
	bits := make([]byte, size*8) // generous sizing
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, evIocGBit, uintptr(unsafe.Pointer(&bits[0])))
	if errno != 0 {
		return nil
	}
	return bits[:size]
}

// testBit tests if a bit is set in a byte slice.
func testBit(bits []byte, idx uint16) bool {
	byteIdx := idx / 8
	bitIdx := idx % 8
	if int(byteIdx) >= len(bits) {
		return false
	}
	return bits[byteIdx]&(1<<bitIdx) != 0
}

// --- Linux evdev keycode → Android keycode mapping ---

// LinuxToAndroidKeycode maps a Linux input event keycode to Android KeyEvent keycode.
//
// Linux input keycodes are defined in linux/input-event-codes.h (usually in
// /usr/include/linux/input-event-codes.h). Common values:
//
//	KEY_ESC=1, KEY_1=2, KEY_A=30, KEY_ENTER=28, KEY_LEFTCTRL=29, etc.
//
// This is similar to X11ToAndroidKeycode but maps from Linux evdev keycodes
// (which differ from X11 keycodes! X11 keycodes are hardware-specific;
// Linux keycodes are standardized).
func LinuxToAndroidKeycode(linuxKey int) int {
	// Linux keycodes for letters: KEY_A=30 to KEY_Z=31+25=55 (actually KEY_A=30, KEY_B=48, ...)
	// Wait — Linux keycodes follow US QWERTY row-by-row:
	// Row 1: KEY_Q=16, KEY_W=17, KEY_E=18, KEY_R=19, KEY_T=20, KEY_Y=21, KEY_U=22, KEY_I=23, KEY_O=24, KEY_P=25
	// Row 2: KEY_A=30, KEY_S=31, KEY_D=32, KEY_F=33, KEY_G=34, KEY_H=35, KEY_J=36, KEY_K=37, KEY_L=38
	// Row 3: KEY_Z=44, KEY_X=45, KEY_C=46, KEY_V=47, KEY_B=48, KEY_N=49, KEY_M=50
	// Numbers: KEY_1=2 ... KEY_9=10, KEY_0=11

	switch linuxKey {
	// Modifier keys
	case 29: // KEY_LEFTCTRL
		return 113 // KEYCODE_CTRL_LEFT
	case 97: // KEY_RIGHTCTRL
		return 114 // KEYCODE_CTRL_RIGHT
	case 42: // KEY_LEFTSHIFT
		return 59 // KEYCODE_SHIFT_LEFT
	case 54: // KEY_RIGHTSHIFT
		return 60 // KEYCODE_SHIFT_RIGHT
	case 56: // KEY_LEFTALT
		return 57 // KEYCODE_ALT_LEFT
	case 100: // KEY_RIGHTALT
		return 58 // KEYCODE_ALT_RIGHT
	case 125: // KEY_LEFTMETA (Super/Windows)
		return 3 // KEYCODE_HOME

	// Navigation
	case 1: // KEY_ESC
		return 4 // KEYCODE_BACK
	case 103: // KEY_UP
		return 19 // KEYCODE_DPAD_UP
	case 108: // KEY_DOWN
		return 20 // KEYCODE_DPAD_DOWN
	case 105: // KEY_LEFT
		return 21 // KEYCODE_DPAD_LEFT
	case 106: // KEY_RIGHT
		return 22 // KEYCODE_DPAD_RIGHT
	case 102: // KEY_HOME
		return 122 // KEYCODE_MOVE_HOME
	case 107: // KEY_END
		return 123 // KEYCODE_MOVE_END
	case 104: // KEY_PAGEUP
		return 92 // KEYCODE_PAGE_UP
	case 109: // KEY_PAGEDOWN
		return 93 // KEYCODE_PAGE_DOWN

	// Editing
	case 14: // KEY_BACKSPACE
		return 67 // KEYCODE_DEL
	case 111: // KEY_DELETE
		return 112 // KEYCODE_FORWARD_DEL
	case 28: // KEY_ENTER
		return 66 // KEYCODE_ENTER
	case 15: // KEY_TAB
		return 61 // KEYCODE_TAB
	case 57: // KEY_SPACE
		return 62 // KEYCODE_SPACE

	// Letters (Linux keycodes for US QWERTY)
	case 16:
		return 45 // Q
	case 17:
		return 51 // W
	case 18:
		return 33 // E
	case 19:
		return 46 // R
	case 20:
		return 48 // T
	case 21:
		return 53 // Y
	case 22:
		return 49 // U
	case 23:
		return 37 // I
	case 24:
		return 43 // O
	case 25:
		return 44 // P
	case 30:
		return 29 // A
	case 31:
		return 47 // S
	case 32:
		return 32 // D
	case 33:
		return 34 // F
	case 34:
		return 35 // G
	case 35:
		return 36 // H
	case 36:
		return 38 // J
	case 37:
		return 39 // K
	case 38:
		return 40 // L
	case 44:
		return 54 // Z
	case 45:
		return 52 // X
	case 46:
		return 31 // C
	case 47:
		return 50 // V
	case 48:
		return 30 // B
	case 49:
		return 42 // N
	case 50:
		return 41 // M

	// Numbers (top row)
	case 2:
		return 8 // 1
	case 3:
		return 9 // 2
	case 4:
		return 10 // 3
	case 5:
		return 11 // 4
	case 6:
		return 12 // 5
	case 7:
		return 13 // 6
	case 8:
		return 14 // 7
	case 9:
		return 15 // 8
	case 10:
		return 16 // 9
	case 11:
		return 7 // 0

	// Function keys
	case 59:
		return 131 // F1
	case 60:
		return 132 // F2
	case 61:
		return 133 // F3
	case 62:
		return 134 // F4
	case 63:
		return 135 // F5
	case 64:
		return 136 // F6
	case 65:
		return 137 // F7
	case 66:
		return 138 // F8
	case 67:
		return 139 // F9
	case 68:
		return 140 // F10
	case 87:
		return 141 // F11
	case 88:
		return 142 // F12

	// Media keys
	case 113: // KEY_MUTE
		return 164 // KEYCODE_VOLUME_MUTE
	case 114: // KEY_VOLUMEDOWN
		return 25 // KEYCODE_VOLUME_DOWN
	case 115: // KEY_VOLUMEUP
		return 24 // KEYCODE_VOLUME_UP

	// Symbols
	case 12: // KEY_MINUS
		return 69 // KEYCODE_MINUS
	case 13: // KEY_EQUAL
		return 70 // KEYCODE_EQUALS
	case 26: // KEY_LEFTBRACE
		return 71 // KEYCODE_LEFT_BRACKET
	case 27: // KEY_RIGHTBRACE
		return 72 // KEYCODE_RIGHT_BRACKET
	case 39: // KEY_SEMICOLON
		return 74 // KEYCODE_SEMICOLON
	case 40: // KEY_APOSTROPHE
		return 75 // KEYCODE_APOSTROPHE
	case 41: // KEY_GRAVE
		return 68 // KEYCODE_GRAVE
	case 43: // KEY_BACKSLASH
		return 73 // KEYCODE_BACKSLASH
	case 51: // KEY_COMMA
		return 55 // KEYCODE_COMMA
	case 52: // KEY_DOT
		return 56 // KEYCODE_PERIOD
	case 53: // KEY_SLASH
		return 76 // KEYCODE_SLASH
	}

	return 0 // unknown
}

// IsEvdevAvailable checks if evdev input devices are accessible.
func IsEvdevAvailable() bool {
	matches, _ := filepath.Glob("/dev/input/event*")
	for _, path := range matches {
		f, err := os.OpenFile(path, os.O_RDONLY, 0)
		if err == nil {
			f.Close()
			return true
		}
	}
	return false
}

// NewBestCapture creates the best available input capturer.
// Tries evdev first (works on both X11/Wayland), falls back to X11.
func NewBestCapture() (interface {
	Events() <-chan Event
	Close() error
}, error) {
	// Try evdev first — it's universal
	if IsEvdevAvailable() {
		return NewEvdevCapture()
	}

	// Fall back to X11 (if running under X11)
	return NewX11Capture()
}

// Ensure wall-clock time import isn't flagged
var _ = time.Now
