//go:build windows

package main

import (
	"log"
	"os"
	"path/filepath"
	"reflect"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

// Window frame + WebView2 background handling (cgo-free).
//
// Default (robust) mode: the WebView2 is OPAQUE. Its DefaultBackgroundColor is
// the theme base colour (#202020 dark / #f3f3f3 light) and is updated whenever
// the UI theme changes; the host window erases its background with the same
// colour, and every resize / DPI / theme change forces a full repaint.
//
// Opt-in Mica (AGENTOS_MICA=1, Windows 11 only): transparent WebView2 +
// DWMWA_SYSTEMBACKDROP_TYPE + sheet-of-glass frame. Off by default because a
// transparent WebView2 composited over an extended DWM frame left stale/white
// translucent rectangles on some machines (0.1.0 bug).
//
// Always: rounded corners (Win11) and a dark/light DWM frame matching the UI.

const (
	dwmwaUseImmersiveDarkModeOld = 19 // Windows 10 1809–1909
	dwmwaUseImmersiveDarkMode    = 20
	dwmwaWindowCornerPreference  = 33
	dwmwaSystemBackdropType      = 38
	dwmwaMicaEffect              = 1029 // undocumented, Windows 11 21H2 (build 22000)

	dwmwcpRound      = 2
	dwmsbtNone       = 1
	dwmsbtMainWindow = 2 // Mica
	dwmsbtTabbed     = 4 // Mica Alt

	wmEraseBkgnd = 0x0014
	wmSize       = 0x0005
	wmDpiChanged = 0x02E0

	rdwInvalidate  = 0x0001
	rdwErase       = 0x0004
	rdwAllChildren = 0x0080
	rdwUpdateNow   = 0x0100
	rdwFrame       = 0x0400
)

var (
	dwmapi                        = windows.NewLazySystemDLL("dwmapi.dll")
	pDwmSetWindowAttribute        = dwmapi.NewProc("DwmSetWindowAttribute")
	pDwmExtendFrameIntoClientArea = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
	gdi32                         = windows.NewLazySystemDLL("gdi32.dll")
	pCreateSolidBrush             = gdi32.NewProc("CreateSolidBrush")
	pDeleteObject                 = gdi32.NewProc("DeleteObject")
	pFillRect                     = user32.NewProc("FillRect")
	pGetClientRect                = user32.NewProc("GetClientRect")
	pRedrawWindow                 = user32.NewProc("RedrawWindow")

	backdropKind = ""    // "mica" when the opt-in backdrop is active — reported via agentosShellInfo
	uiDark       = false // effective UI theme (from the page via agentosSetTheme)
	bgBrush      uintptr
	chromium     *edge.Chromium // go-webview2 internals (nil if the layout ever changes)
)

type margins struct{ Left, Right, Top, Bottom int32 }

func winBuild() uint32 { return windows.RtlGetVersion().BuildNumber }

func dwmSetInt(hwnd uintptr, attr uint32, v uint32) bool {
	if pDwmSetWindowAttribute.Find() != nil {
		return false
	}
	hr, _, _ := pDwmSetWindowAttribute.Call(hwnd, uintptr(attr), uintptr(unsafe.Pointer(&v)), unsafe.Sizeof(v))
	return hr == 0 // S_OK
}

// micaWanted: explicit opt-in only.
func micaWanted() bool { return os.Getenv("AGENTOS_MICA") == "1" && winBuild() >= 22000 }

// theme base colours (must match --mica in web/static/css/app.css)
func themeRGB(dark bool) (r, g, b uint8) {
	if dark {
		return 0x20, 0x20, 0x20
	}
	return 0xf3, 0xf3, 0xf3
}

func themeFile() string { return filepath.Join(appDir(), "theme") }

// initialDark: last theme the UI reported, else the Windows app theme.
func initialDark() bool {
	if b, err := os.ReadFile(themeFile()); err == nil {
		return string(b) == "dark"
	}
	return systemDarkMode()
}

// prepareWebViewBackground runs before the WebView2 controller is created:
// WebView2 reads WEBVIEW2_DEFAULT_BACKGROUND_COLOR (ARGB hex) at creation, so
// the very first frame is already the right colour (no white flash).
func prepareWebViewBackground() {
	uiDark = initialDark()
	if micaWanted() {
		_ = os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", "0")
		return
	}
	r, g, b := themeRGB(uiDark)
	_ = os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", hex2("FF", r, g, b))
	updateBrush()
}

func hex2(prefix string, bs ...uint8) string {
	const d = "0123456789ABCDEF"
	s := []byte(prefix)
	for _, b := range bs {
		s = append(s, d[b>>4], d[b&15])
	}
	return string(s)
}

func updateBrush() {
	r, g, b := themeRGB(uiDark)
	nb, _, _ := pCreateSolidBrush.Call(uintptr(r) | uintptr(g)<<8 | uintptr(b)<<16) // COLORREF 0x00BBGGRR
	if nb == 0 {
		return
	}
	if bgBrush != 0 {
		pDeleteObject.Call(bgBrush)
	}
	bgBrush = nb
}

// grabChromium fetches go-webview2's unexported *edge.Chromium so we can reach
// ICoreWebView2Controller(2). Defensive: returns nil if the layout changes.
func grabChromium(w webview2.WebView) *edge.Chromium {
	defer func() { _ = recover() }()
	v := reflect.ValueOf(w)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		return nil
	}
	f := v.Elem().FieldByName("browser")
	if !f.IsValid() || !f.CanAddr() {
		return nil
	}
	f = reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
	c, _ := f.Interface().(*edge.Chromium)
	return c
}

// applyWebViewBackground pushes the theme colour (or transparency in Mica mode)
// into the live controller.
func applyWebViewBackground() {
	if chromium == nil {
		return
	}
	defer func() { _ = recover() }()
	ctl := chromium.GetController()
	if ctl == nil {
		return
	}
	c2 := ctl.GetICoreWebView2Controller2()
	if c2 == nil {
		return
	}
	col := edge.COREWEBVIEW2_COLOR{A: 255}
	if backdropKind == "" {
		col.R, col.G, col.B = themeRGB(uiDark)
	} else {
		col.A = 0
	}
	if err := c2.PutDefaultBackgroundColor(col); err != nil {
		log.Printf("webview2 background: %v", err)
	}
}

// syncWebViewBounds re-fits the controller to the client area and tells it the
// parent moved (fixes stale/offset content after resize or DPI change).
func syncWebViewBounds() {
	if chromium == nil {
		return
	}
	defer func() { _ = recover() }()
	chromium.Resize()
	_ = chromium.NotifyParentWindowPositionChanged()
}

func forceRepaint(hwnd uintptr) {
	pRedrawWindow.Call(hwnd, 0, 0, rdwInvalidate|rdwErase|rdwAllChildren|rdwUpdateNow|rdwFrame)
}

// eraseBackground paints the host window with the theme colour (visible only
// for the instant before WebView2 covers a newly exposed area).
func eraseBackground(hwnd, hdc uintptr) bool {
	if backdropKind != "" || bgBrush == 0 {
		return false
	}
	var rc rect
	pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), bgBrush)
	return true
}

// applyFrameStyle sets corners, frame theme and (opt-in) the Mica backdrop.
func applyFrameStyle(hwnd uintptr) {
	if winBuild() >= 22000 {
		dwmSetInt(hwnd, dwmwaWindowCornerPreference, dwmwcpRound)
	}
	setFrameDark(hwnd, uiDark)
	if !micaWanted() {
		return
	}
	ok := false
	if winBuild() >= 22621 {
		ok = dwmSetInt(hwnd, dwmwaSystemBackdropType, dwmsbtMainWindow)
	}
	if !ok {
		ok = dwmSetInt(hwnd, dwmwaMicaEffect, 1)
	}
	if ok {
		m := margins{-1, -1, -1, -1}
		pDwmExtendFrameIntoClientArea.Call(hwnd, uintptr(unsafe.Pointer(&m)))
		backdropKind = "mica"
	}
	log.Printf("window backdrop: %q (build %d, AGENTOS_MICA=%q)", backdropKind, winBuild(), os.Getenv("AGENTOS_MICA"))
}

func setFrameDark(hwnd uintptr, dark bool) {
	v := uint32(0)
	if dark {
		v = 1
	}
	if !dwmSetInt(hwnd, dwmwaUseImmersiveDarkMode, v) {
		dwmSetInt(hwnd, dwmwaUseImmersiveDarkModeOld, v)
	}
}

// setUITheme is called by the page (agentosSetTheme) on load and on every theme change.
func setUITheme(hwnd uintptr, dark bool) {
	uiDark = dark
	s := "light"
	if dark {
		s = "dark"
	}
	_ = os.WriteFile(themeFile(), []byte(s), 0o644)
	setFrameDark(hwnd, dark)
	updateBrush()
	applyWebViewBackground()
	syncWebViewBounds()
	forceRepaint(hwnd)
}

// setBackdrop switches Mica at runtime (only when Mica mode was opted into).
func setBackdrop(hwnd uintptr, kind string) bool {
	if !micaWanted() || winBuild() < 22621 {
		return false
	}
	t := uint32(dwmsbtNone)
	switch kind {
	case "mica":
		t = dwmsbtMainWindow
	case "mica-alt":
		t = dwmsbtTabbed
	}
	if !dwmSetInt(hwnd, dwmwaSystemBackdropType, t) {
		return false
	}
	return true
}

// systemDarkMode reads the Windows app theme (AppsUseLightTheme = 0 → dark).
func systemDarkMode() bool {
	k, err := windows.UTF16PtrFromString(`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`)
	if err != nil {
		return false
	}
	var h windows.Handle
	if windows.RegOpenKeyEx(windows.HKEY_CURRENT_USER, k, 0, windows.KEY_READ, &h) != nil {
		return false
	}
	defer windows.RegCloseKey(h)
	var v, typ uint32
	n := uint32(4)
	name, _ := windows.UTF16PtrFromString("AppsUseLightTheme")
	if windows.RegQueryValueEx(h, name, nil, &typ, (*byte)(unsafe.Pointer(&v)), &n) != nil {
		return false
	}
	return v == 0
}
