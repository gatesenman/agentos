package screen

import (
	"fmt"
	"testing"
)

func TestKeyMapCoverage(t *testing.T) {
	var codes []string
	for c := 'A'; c <= 'Z'; c++ {
		codes = append(codes, "Key"+string(c))
	}
	for c := '0'; c <= '9'; c++ {
		codes = append(codes, "Digit"+string(c), "Numpad"+string(c))
	}
	for i := 1; i <= 12; i++ {
		codes = append(codes, fmt.Sprintf("F%d", i))
	}
	codes = append(codes, "ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown",
		"ShiftLeft", "ShiftRight", "ControlLeft", "ControlRight", "AltLeft", "AltRight", "MetaLeft", "MetaRight",
		"Enter", "Escape", "Backspace", "Tab", "Space", "Delete", "Insert", "Home", "End", "PageUp", "PageDown",
		"CapsLock", "Minus", "Equal", "Comma", "Period", "Slash", "Semicolon", "Quote", "Backquote",
		"BracketLeft", "BracketRight", "Backslash")
	for _, c := range codes {
		k, ok := LookupKey(c)
		if !ok {
			t.Errorf("missing code %s", c)
			continue
		}
		if k.VK == 0 || k.Keysym == 0 {
			t.Errorf("%s: incomplete def %+v", c, k)
		}
	}
	if _, ok := LookupKey("NoSuchKey"); ok {
		t.Error("unexpected mapping for NoSuchKey")
	}
}

func TestKeyMapValues(t *testing.T) {
	cases := []struct {
		code   string
		vk     uint16
		ext    bool
		keysym uint32
	}{
		{"KeyA", 0x41, false, 'a'},
		{"KeyZ", 0x5A, false, 'z'},
		{"Digit0", 0x30, false, '0'},
		{"Digit9", 0x39, false, '9'},
		{"F1", 0x70, false, 0xffbe},
		{"F12", 0x7B, false, 0xffc9},
		{"ArrowLeft", 0x25, true, 0xff51},
		{"ArrowDown", 0x28, true, 0xff54},
		{"ShiftLeft", 0xA0, false, 0xffe1},
		{"ControlRight", 0xA3, true, 0xffe4},
		{"AltLeft", 0xA4, false, 0xffe9},
		{"MetaLeft", 0x5B, true, 0xffeb},
		{"Enter", 0x0D, false, 0xff0d},
		{"NumpadEnter", 0x0D, true, 0xff8d},
		{"Numpad5", 0x65, false, 0xffb5},
		{"Space", 0x20, false, 0x20},
	}
	for _, c := range cases {
		k, _ := LookupKey(c.code)
		if k.VK != c.vk || k.Extended != c.ext || k.Keysym != c.keysym {
			t.Errorf("%s = %+v, want vk=%#x ext=%v ks=%#x", c.code, k, c.vk, c.ext, c.keysym)
		}
	}
	if n := len(KeyCodes()); n < 100 {
		t.Errorf("only %d codes mapped", n)
	}
}

func TestRuneKeysym(t *testing.T) {
	cases := map[rune]uint32{'a': 'a', 'Z': 'Z', ' ': 0x20, 'é': 0xe9, '\n': 0xff0d, '你': 0x01004f60, '€': 0x010020ac}
	for r, want := range cases {
		if got := RuneKeysym(r); got != want {
			t.Errorf("RuneKeysym(%q)=%#x want %#x", r, got, want)
		}
	}
	if r, ok := singleRune("ä"); !ok || r != 'ä' {
		t.Error("singleRune(ä)")
	}
	if _, ok := singleRune("Shift"); ok {
		t.Error("singleRune(Shift) should be false")
	}
}
