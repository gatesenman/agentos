//go:build windows

package screen

import (
	"errors"
	"fmt"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                            = windows.NewLazySystemDLL("user32.dll")
	procSendInput                     = user32.NewProc("SendInput")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procMapVirtualKeyW                = user32.NewProc("MapVirtualKeyW")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseeventfMove       = 0x0001
	mouseeventfLeftDown   = 0x0002
	mouseeventfLeftUp     = 0x0004
	mouseeventfRightDown  = 0x0008
	mouseeventfRightUp    = 0x0010
	mouseeventfMiddleDown = 0x0020
	mouseeventfMiddleUp   = 0x0040
	mouseeventfXDown      = 0x0080
	mouseeventfXUp        = 0x0100
	mouseeventfWheel      = 0x0800
	mouseeventfHWheel     = 0x1000
	mouseeventfVirtualDsk = 0x4000
	mouseeventfAbsolute   = 0x8000

	keyeventfExtendedKey = 0x0001
	keyeventfKeyUp       = 0x0002
	keyeventfUnicode     = 0x0004

	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	vkReturn = 0x0D
)

// Win32 INPUT layout: DWORD type + union{MOUSEINPUT,KEYBDINPUT,HARDWAREINPUT}.
// MOUSEINPUT is the largest member; keyboard inputs are padded to the same size.
type mouseInput struct {
	Dx, Dy    int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

type keybdInput struct {
	Vk, Scan  uint16
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

type mouseINPUT struct {
	Type uint32
	Mi   mouseInput
}

type keybdINPUT struct {
	Type uint32
	Ki   keybdInput
	_    [unsafe.Sizeof(mouseInput{}) - unsafe.Sizeof(keybdInput{})]byte
}

type winInjector struct{}

func newPlatformInjector() (Injector, error) {
	if sessionID() == 0 {
		return nil, errors.New("session 0: no interactive desktop")
	}
	// Physical pixel coordinates, matching the capture bounds.
	if procSetProcessDpiAwarenessContext.Find() == nil {
		procSetProcessDpiAwarenessContext.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
	} else if procSetProcessDPIAware.Find() == nil {
		procSetProcessDPIAware.Call()
	}
	return winInjector{}, nil
}

func metric(i int) int32 {
	r, _, _ := procGetSystemMetrics.Call(uintptr(i))
	return int32(r)
}

func sendMouse(in []mouseINPUT) error {
	if len(in) == 0 {
		return nil
	}
	n, _, err := procSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0]))
	if int(n) != len(in) {
		return fmt.Errorf("SendInput: %v", err)
	}
	return nil
}

func sendKeys(in []keybdINPUT) error {
	if len(in) == 0 {
		return nil
	}
	n, _, err := procSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0]))
	if int(n) != len(in) {
		return fmt.Errorf("SendInput: %v", err)
	}
	return nil
}

// absMove builds an absolute move over the whole virtual desktop (0..65535).
func absMove(x, y int, extra uint32) mouseINPUT {
	vx, vy := metric(smXVirtualScreen), metric(smYVirtualScreen)
	vw, vh := metric(smCXVirtualScreen), metric(smCYVirtualScreen)
	if vw < 2 {
		vw = 2
	}
	if vh < 2 {
		vh = 2
	}
	nx := (int64(int32(x)-vx)*65535 + int64(vw-1)/2) / int64(vw-1)
	ny := (int64(int32(y)-vy)*65535 + int64(vh-1)/2) / int64(vh-1)
	return mouseINPUT{Type: inputMouse, Mi: mouseInput{Dx: int32(nx), Dy: int32(ny),
		Flags: mouseeventfMove | mouseeventfAbsolute | mouseeventfVirtualDsk | extra}}
}

func (winInjector) MouseMove(x, y int) error { return sendMouse([]mouseINPUT{absMove(x, y, 0)}) }

func (winInjector) MouseButton(x, y, button int, down bool) error {
	var flag, data uint32
	switch button {
	case 0:
		flag = pick(down, mouseeventfLeftDown, mouseeventfLeftUp)
	case 1:
		flag = pick(down, mouseeventfMiddleDown, mouseeventfMiddleUp)
	case 2:
		flag = pick(down, mouseeventfRightDown, mouseeventfRightUp)
	case 3, 4: // DOM back/forward → XBUTTON1/2
		flag = pick(down, mouseeventfXDown, mouseeventfXUp)
		data = uint32(button - 2)
	default:
		return fmt.Errorf("unsupported button %d", button)
	}
	in := absMove(x, y, flag)
	in.Mi.MouseData = data
	return sendMouse([]mouseINPUT{in})
}

func (winInjector) MouseWheel(x, y, dx, dy int) error {
	in := []mouseINPUT{absMove(x, y, 0)}
	if dy != 0 { // DOM: positive = down; Windows: positive = away from user (up)
		in = append(in, mouseINPUT{Type: inputMouse, Mi: mouseInput{MouseData: uint32(int32(-dy)), Flags: mouseeventfWheel}})
	}
	if dx != 0 {
		in = append(in, mouseINPUT{Type: inputMouse, Mi: mouseInput{MouseData: uint32(int32(dx)), Flags: mouseeventfHWheel}})
	}
	return sendMouse(in)
}

func (winInjector) Key(def KeyDef, down bool) error {
	scan, _, _ := procMapVirtualKeyW.Call(uintptr(def.VK), 0) // MAPVK_VK_TO_VSC
	var flags uint32
	if def.Extended {
		flags |= keyeventfExtendedKey
	}
	if !down {
		flags |= keyeventfKeyUp
	}
	return sendKeys([]keybdINPUT{{Type: inputKeyboard, Ki: keybdInput{Vk: def.VK, Scan: uint16(scan), Flags: flags}}})
}

// Text types arbitrary unicode via KEYEVENTF_UNICODE (UTF-16 units, surrogate
// pairs sent as two units). Newlines become VK_RETURN.
func (winInjector) Text(s string) error {
	var in []keybdINPUT
	for _, r := range s {
		if r == '\r' {
			continue
		}
		if r == '\n' {
			in = append(in, keybdINPUT{Type: inputKeyboard, Ki: keybdInput{Vk: vkReturn}},
				keybdINPUT{Type: inputKeyboard, Ki: keybdInput{Vk: vkReturn, Flags: keyeventfKeyUp}})
			continue
		}
		for _, u := range utf16.Encode([]rune{r}) {
			in = append(in, keybdINPUT{Type: inputKeyboard, Ki: keybdInput{Scan: u, Flags: keyeventfUnicode}},
				keybdINPUT{Type: inputKeyboard, Ki: keybdInput{Scan: u, Flags: keyeventfUnicode | keyeventfKeyUp}})
		}
	}
	return sendKeys(in)
}

func (winInjector) Close() error { return nil }

func pick(c bool, a, b uint32) uint32 {
	if c {
		return a
	}
	return b
}
