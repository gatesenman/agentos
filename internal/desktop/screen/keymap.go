package screen

// KeyDef maps a DOM KeyboardEvent.code to platform key identifiers.
// The tables are platform-independent so they can be unit-tested anywhere.
type KeyDef struct {
	VK       uint16 // Windows virtual-key code
	Extended bool   // Windows KEYEVENTF_EXTENDEDKEY
	Keysym   uint32 // X11 keysym
}

var keyMap = buildKeyMap()

// LookupKey returns the key definition for a DOM code.
func LookupKey(code string) (KeyDef, bool) {
	k, ok := keyMap[code]
	return k, ok
}

// KeyCodes returns all supported DOM codes (for tests/diagnostics).
func KeyCodes() []string {
	out := make([]string, 0, len(keyMap))
	for c := range keyMap {
		out = append(out, c)
	}
	return out
}

func buildKeyMap() map[string]KeyDef {
	m := map[string]KeyDef{}
	for c := 'A'; c <= 'Z'; c++ {
		m["Key"+string(c)] = KeyDef{VK: uint16(c), Keysym: uint32(c - 'A' + 'a')}
	}
	for c := '0'; c <= '9'; c++ {
		m["Digit"+string(c)] = KeyDef{VK: uint16(c), Keysym: uint32(c)}
		m["Numpad"+string(c)] = KeyDef{VK: 0x60 + uint16(c-'0'), Keysym: 0xffb0 + uint32(c-'0')}
	}
	for i := 1; i <= 24; i++ {
		m["F"+itoa(i)] = KeyDef{VK: 0x70 + uint16(i-1), Keysym: 0xffbe + uint32(i-1)}
	}
	add := func(code string, vk uint16, ext bool, ks uint32) { m[code] = KeyDef{VK: vk, Extended: ext, Keysym: ks} }
	// Navigation / editing (extended on Windows).
	add("ArrowLeft", 0x25, true, 0xff51)
	add("ArrowUp", 0x26, true, 0xff52)
	add("ArrowRight", 0x27, true, 0xff53)
	add("ArrowDown", 0x28, true, 0xff54)
	add("Insert", 0x2D, true, 0xff63)
	add("Delete", 0x2E, true, 0xffff)
	add("Home", 0x24, true, 0xff50)
	add("End", 0x23, true, 0xff57)
	add("PageUp", 0x21, true, 0xff55)
	add("PageDown", 0x22, true, 0xff56)
	// Modifiers.
	add("ShiftLeft", 0xA0, false, 0xffe1)
	add("ShiftRight", 0xA1, false, 0xffe2)
	add("ControlLeft", 0xA2, false, 0xffe3)
	add("ControlRight", 0xA3, true, 0xffe4)
	add("AltLeft", 0xA4, false, 0xffe9)
	add("AltRight", 0xA5, true, 0xfe03) // AltGr (ISO_Level3_Shift) on X11
	add("MetaLeft", 0x5B, true, 0xffeb)
	add("MetaRight", 0x5C, true, 0xffec)
	add("OSLeft", 0x5B, true, 0xffeb) // legacy Firefox names
	add("OSRight", 0x5C, true, 0xffec)
	add("CapsLock", 0x14, false, 0xffe5)
	add("NumLock", 0x90, true, 0xff7f)
	add("ScrollLock", 0x91, false, 0xff14)
	// Whitespace / control.
	add("Enter", 0x0D, false, 0xff0d)
	add("Escape", 0x1B, false, 0xff1b)
	add("Backspace", 0x08, false, 0xff08)
	add("Tab", 0x09, false, 0xff09)
	add("Space", 0x20, false, 0x20)
	add("ContextMenu", 0x5D, true, 0xff67)
	add("PrintScreen", 0x2C, true, 0xff61)
	add("Pause", 0x13, false, 0xff13)
	// Punctuation (US layout positions).
	add("Minus", 0xBD, false, 0x2d)
	add("Equal", 0xBB, false, 0x3d)
	add("BracketLeft", 0xDB, false, 0x5b)
	add("BracketRight", 0xDD, false, 0x5d)
	add("Backslash", 0xDC, false, 0x5c)
	add("IntlBackslash", 0xE2, false, 0x3c)
	add("Semicolon", 0xBA, false, 0x3b)
	add("Quote", 0xDE, false, 0x27)
	add("Backquote", 0xC0, false, 0x60)
	add("Comma", 0xBC, false, 0x2c)
	add("Period", 0xBE, false, 0x2e)
	add("Slash", 0xBF, false, 0x2f)
	// Numpad operators.
	add("NumpadAdd", 0x6B, false, 0xffab)
	add("NumpadSubtract", 0x6D, false, 0xffad)
	add("NumpadMultiply", 0x6A, false, 0xffaa)
	add("NumpadDivide", 0x6F, true, 0xffaf)
	add("NumpadDecimal", 0x6E, false, 0xffae)
	add("NumpadEnter", 0x0D, true, 0xff8d)
	// Media / browser keys (Windows VKs; X11 XF86 keysyms).
	add("AudioVolumeMute", 0xAD, true, 0x1008ff12)
	add("AudioVolumeDown", 0xAE, true, 0x1008ff11)
	add("AudioVolumeUp", 0xAF, true, 0x1008ff13)
	add("MediaPlayPause", 0xB3, true, 0x1008ff14)
	add("MediaTrackNext", 0xB0, true, 0x1008ff17)
	add("MediaTrackPrevious", 0xB1, true, 0x1008ff16)
	add("MediaStop", 0xB2, true, 0x1008ff15)
	return m
}

// RuneKeysym returns the X11 keysym for a unicode rune.
func RuneKeysym(r rune) uint32 {
	switch {
	case r == '\n' || r == '\r':
		return 0xff0d
	case r == '\t':
		return 0xff09
	case r == '\b':
		return 0xff08
	case (r >= 0x20 && r <= 0x7e) || (r >= 0xa0 && r <= 0xff):
		return uint32(r)
	default:
		return 0x01000000 | uint32(r)
	}
}

// singleRune returns the only rune of s when s is exactly one printable
// character (DOM KeyboardEvent.key for printable keys).
func singleRune(s string) (rune, bool) {
	rs := []rune(s)
	if len(rs) != 1 || rs[0] < 0x20 {
		return 0, false
	}
	return rs[0], true
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
