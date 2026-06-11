package input

// X11ToAndroidKeycode maps an X11 keycode to an Android KeyEvent keycode.
//
// Common X11 keycodes and their Android equivalents:
//
//	X11: 9 (Escape)    → Android: 4 (KEYCODE_BACK)
//	X11: 22 (Backspace) → Android: 67 (KEYCODE_DEL)
//	X11: 36 (Return)    → Android: 66 (KEYCODE_ENTER)
//	X11: 37 (Ctrl_L)    → Android: 113 (KEYCODE_CTRL_LEFT)
//	X11: 50 (Shift_L)   → Android: 59 (KEYCODE_SHIFT_LEFT)
//	X11: 64 (Alt_L)     → Android: 57 (KEYCODE_ALT_LEFT)
//	X11: 111 (Up)       → Android: 19 (KEYCODE_DPAD_UP)
//	X11: 113 (Left)     → Android: 21 (KEYCODE_DPAD_LEFT)
//	X11: 114 (Right)    → Android: 22 (KEYCODE_DPAD_RIGHT)
//	X11: 116 (Down)     → Android: 20 (KEYCODE_DPAD_DOWN)
//	X11: 133 (Super_L)  → Android: 3 (KEYCODE_HOME)
//
// For printable keys (letters, numbers), the X11 keycode is mapped to
// the corresponding Android keycode:
//
//	X11: 24-33 (a-z) → Android: 29-38 (KEYCODE_A - KEYCODE_Z)
//	X11: 10-19 (1-9,0) → Android: 8-16 (KEYCODE_1 - KEYCODE_9) + 7 (KEYCODE_0)
func X11ToAndroidKeycode(x11Keycode int) int {
	switch x11Keycode {
	// Modifier keys
	case 37: // Ctrl_L
		return 113 // KEYCODE_CTRL_LEFT
	case 50: // Shift_L
		return 59 // KEYCODE_SHIFT_LEFT
	case 62: // Shift_R
		return 60 // KEYCODE_SHIFT_RIGHT
	case 64: // Alt_L
		return 57 // KEYCODE_ALT_LEFT
	case 108: // Alt_R
		return 58 // KEYCODE_ALT_RIGHT
	case 133: // Super_L (Windows key)
		return 3 // KEYCODE_HOME

	// Navigation
	case 9: // Escape
		return 4 // KEYCODE_BACK
	case 111: // Up
		return 19 // KEYCODE_DPAD_UP
	case 116: // Down
		return 20 // KEYCODE_DPAD_DOWN
	case 113: // Left
		return 21 // KEYCODE_DPAD_LEFT
	case 114: // Right
		return 22 // KEYCODE_DPAD_RIGHT
	case 110: // Home
		return 122 // KEYCODE_MOVE_HOME
	case 115: // End
		return 123 // KEYCODE_MOVE_END
	case 112: // Page Up
		return 92 // KEYCODE_PAGE_UP
	case 117: // Page Down
		return 93 // KEYCODE_PAGE_DOWN

	// Editing
	case 22: // Backspace
		return 67 // KEYCODE_DEL
	case 119: // Delete
		return 112 // KEYCODE_FORWARD_DEL
	case 36: // Return/Enter
		return 66 // KEYCODE_ENTER
	case 23: // Tab
		return 61 // KEYCODE_TAB
	case 65: // Space
		return 62 // KEYCODE_SPACE

	// Function keys
	case 67: // F1
		return 131 // KEYCODE_F1
	case 68: // F2
		return 132 // KEYCODE_F2
	case 69: // F3
		return 133 // KEYCODE_F3
	case 70: // F4
		return 134 // KEYCODE_F4
	case 71: // F5
		return 135 // KEYCODE_F5
	case 72: // F6
		return 136 // KEYCODE_F6
	case 73: // F7
		return 137 // KEYCODE_F7
	case 74: // F8
		return 138 // KEYCODE_F8
	case 75: // F9
		return 139 // KEYCODE_F9
	case 76: // F10
		return 140 // KEYCODE_F10
	case 95: // F11
		return 141 // KEYCODE_F11
	case 96: // F12
		return 142 // KEYCODE_F12

	// Media keys
	case 121: // XF86AudioMute
		return 164 // KEYCODE_VOLUME_MUTE
	case 122: // XF86AudioLowerVolume
		return 25 // KEYCODE_VOLUME_DOWN
	case 123: // XF86AudioRaiseVolume
		return 24 // KEYCODE_VOLUME_UP

	default:
		break
	}

	// Letter keys: X11 24-33 (Q-P) → 45-54, X11 38-48 (A-L) → 29-39, etc.
	if letterCode := x11KeycodeToLetter(x11Keycode); letterCode != 0 {
		return letterCode
	}

	// Digit keys: X11 10-19 (1-9, 0) → Android 8-16 and 7
	if digitCode := x11KeycodeToDigit(x11Keycode); digitCode != 0 {
		return digitCode
	}

	return 0 // unknown
}

// x11KeycodeToLetter maps X11 keycodes for QWERTY letters to Android keycodes.
// X11 keycodes 24-33 = QWERTY row, 38-48 = ASDF row, 52-61 = ZXCV row.
func x11KeycodeToLetter(x11Keycode int) int {
	// These are approximate for US QWERTY layout
	mapping := map[int]int{
		24:  45, // Q → KEYCODE_Q
		25:  51, // W → KEYCODE_W
		26:  33, // E → KEYCODE_E
		27:  46, // R → KEYCODE_R
		28:  48, // T → KEYCODE_T
		29:  53, // Y → KEYCODE_Y
		30:  49, // U → KEYCODE_U
		31:  37, // I → KEYCODE_I
		32:  43, // O → KEYCODE_O
		33:  44, // P → KEYCODE_P

		38:  29, // A → KEYCODE_A
		39:  47, // S → KEYCODE_S
		40:  32, // D → KEYCODE_D
		41:  34, // F → KEYCODE_F
		42:  35, // G → KEYCODE_G
		43:  36, // H → KEYCODE_H
		44:  38, // J → KEYCODE_J
		45:  39, // K → KEYCODE_K
		46:  40, // L → KEYCODE_L

		52:  54, // Z → KEYCODE_Z
		53:  52, // X → KEYCODE_X
		54:  31, // C → KEYCODE_C
		55:  50, // V → KEYCODE_V
		56:  30, // B → KEYCODE_B
		57:  42, // N → KEYCODE_N
		58:  41, // M → KEYCODE_M
	}
	if k, ok := mapping[x11Keycode]; ok {
		return k
	}
	return 0
}

// x11KeycodeToDigit maps X11 keycodes for numeric keys to Android keycodes.
func x11KeycodeToDigit(x11Keycode int) int {
	switch x11Keycode {
	case 10: // 1
		return 8
	case 11: // 2
		return 9
	case 12: // 3
		return 10
	case 13: // 4
		return 11
	case 14: // 5
		return 12
	case 15: // 6
		return 13
	case 16: // 7
		return 14
	case 17: // 8
		return 15
	case 18: // 9
		return 16
	case 19: // 0
		return 7
	}
	return 0
}
