//go:build windows

package main

import (
	_ "embed"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

//go:embed agentos.ico
var iconICO []byte

const (
	windowTitle   = "AgentOS"
	webviewClass  = "webview" // window class registered by go-webview2
	instanceMutex = `Local\AgentOSDesktop`
	trayMutex     = `Local\AgentOSTray` // contract with the tray launcher (M8)
	webview2URL   = "https://go.microsoft.com/fwlink/p/?LinkId=2124703"

	wmClose         = 0x0010
	wmNCCalcSize    = 0x0083
	wmNCLButtonDown = 0x00A1
	wmSetIcon       = 0x0080
	htCaption       = 2
	swHide          = 0
	swMaximize      = 3
	swMinimize      = 6
	swRestore       = 9
	swShow          = 5
	gwlpWndProc     = ^uintptr(3) // -4
	smCxFrame       = 32
	smCyFrame       = 33
	smCxPaddedBordr = 92
	swpFrameChanged = 0x0020
	swpNoMove       = 0x0002
	swpNoSize       = 0x0001
	swpNoZOrder     = 0x0004
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	pSetWindowLongPtrW      = user32.NewProc("SetWindowLongPtrW")
	pCallWindowProcW        = user32.NewProc("CallWindowProcW")
	pShowWindow             = user32.NewProc("ShowWindow")
	pIsZoomed               = user32.NewProc("IsZoomed")
	pIsWindowVisible        = user32.NewProc("IsWindowVisible")
	pReleaseCapture         = user32.NewProc("ReleaseCapture")
	pPostMessageW           = user32.NewProc("PostMessageW")
	pSendMessageW           = user32.NewProc("SendMessageW")
	pFindWindowW            = user32.NewProc("FindWindowW")
	pSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	pGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	pSetWindowPos           = user32.NewProc("SetWindowPos")
	pGetWindowPlacement     = user32.NewProc("GetWindowPlacement")
	pSetWindowPlacement     = user32.NewProc("SetWindowPlacement")
	pGetDpiForWindow        = user32.NewProc("GetDpiForWindow")
	pSetProcessDpiAwareness = user32.NewProc("SetProcessDpiAwarenessContext")
	pLoadImageW             = user32.NewProc("LoadImageW")
)

var (
	mainHwnd   uintptr
	origProc   uintptr
	instanceMx windows.Handle
)

// ---------- single instance ----------

func acquireSingleInstance() bool {
	name, _ := windows.UTF16PtrFromString(instanceMutex)
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		if h != 0 {
			windows.CloseHandle(h)
		}
		return false
	}
	instanceMx = h
	return true
}

func findMainWindow() uintptr {
	cls, _ := windows.UTF16PtrFromString(webviewClass)
	title, _ := windows.UTF16PtrFromString(windowTitle)
	h, _, _ := pFindWindowW.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)))
	return h
}

func focusExistingInstance() {
	for i := 0; i < 20; i++ { // the first instance may still be starting up
		if h := findMainWindow(); h != 0 {
			pShowWindow.Call(h, swShow)
			if z, _, _ := pIsZoomed.Call(h); z == 0 {
				pShowWindow.Call(h, swRestore)
			}
			pSetForegroundWindow.Call(h)
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func trayRunning() bool {
	name, _ := windows.UTF16PtrFromString(trayMutex)
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err != nil {
		return false
	}
	windows.CloseHandle(h)
	return true
}

// ---------- server process ----------

func hideChildWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP}
}

func stopServer(cmd *exec.Cmd) {
	// No console is attached (CREATE_NO_WINDOW), so CTRL_BREAK cannot be
	// delivered; terminate. Agent processes are cleaned up by the node's Job
	// Objects (kill-on-close, module M1).
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

// startService starts an installed-but-stopped AgentOS Windows service.
// Returns false when there is no such service or access is denied.
func startService(name string) bool {
	if name == "" {
		return false
	}
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(scm)
	n, _ := windows.UTF16PtrFromString(name)
	svc, err := windows.OpenService(scm, n, windows.SERVICE_QUERY_STATUS|windows.SERVICE_START)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(svc)
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(svc, &st); err == nil && st.CurrentState == windows.SERVICE_RUNNING {
		return true // running but not (yet) healthy – caller waits
	}
	if err := windows.StartService(svc, 0, nil); err != nil {
		log.Printf("start service %q: %v", name, err)
		return false
	}
	return true
}

// ---------- dialogs ----------

func fatalDialog(title, msg string) {
	log.Printf("%s: %s", title, msg)
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(msg)
	windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONERROR)
}

func openURL(u string) { _ = exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start() }

func waitForInterrupt() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}

// ---------- window placement ----------

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }
type windowPlacement struct {
	Length, Flags, ShowCmd uint32
	MinPos, MaxPos         point
	Normal                 rect
	Device                 rect
}

func placementFile() string { return filepath.Join(appDir(), "window.json") }

func savePlacement(h uintptr) {
	wp := windowPlacement{Length: uint32(unsafe.Sizeof(windowPlacement{}))}
	if r, _, _ := pGetWindowPlacement.Call(h, uintptr(unsafe.Pointer(&wp))); r == 0 {
		return
	}
	if b, err := json.Marshal(wp); err == nil {
		_ = os.WriteFile(placementFile(), b, 0o644)
	}
}

func restorePlacement(h uintptr) bool {
	b, err := os.ReadFile(placementFile())
	if err != nil {
		return false
	}
	var wp windowPlacement
	if json.Unmarshal(b, &wp) != nil || wp.Normal.Right-wp.Normal.Left < 200 {
		return false
	}
	wp.Length = uint32(unsafe.Sizeof(windowPlacement{}))
	if wp.ShowCmd != swMaximize {
		wp.ShowCmd = swRestore
	}
	r, _, _ := pSetWindowPlacement.Call(h, uintptr(unsafe.Pointer(&wp)))
	return r != 0
}

// ---------- frameless window: subclass the go-webview2 window proc ----------

type ncCalcSizeParams struct {
	Rgrc  [3]rect
	Lppos uintptr
}

func subclassProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case wmNCCalcSize:
		if wp == 1 {
			// Remove the caption but keep the standard (invisible) resize
			// borders on the left, right and bottom edges.
			p := *(**ncCalcSizeParams)(unsafe.Pointer(&lp))
			top := p.Rgrc[0].Top
			r, _, _ := pCallWindowProcW.Call(origProc, hwnd, msg, wp, lp)
			p.Rgrc[0].Top = top
			if z, _, _ := pIsZoomed.Call(hwnd); z != 0 {
				fy, _, _ := pGetSystemMetrics.Call(smCyFrame)
				pad, _, _ := pGetSystemMetrics.Call(smCxPaddedBordr)
				p.Rgrc[0].Top += int32(fy + pad)
			}
			return r
		}
	case wmEraseBkgnd:
		if eraseBackground(hwnd, wp) {
			return 1
		}
	case wmSize:
		r, _, _ := pCallWindowProcW.Call(origProc, hwnd, msg, wp, lp)
		syncWebViewBounds()
		forceRepaint(hwnd)
		return r
	case wmDpiChanged:
		// Apply the suggested rect (DefWindowProc does not), then re-fit WebView2.
		if lp != 0 {
			rc := **(**rect)(unsafe.Pointer(&lp))
			pSetWindowPos.Call(hwnd, 0, uintptr(rc.Left), uintptr(rc.Top), uintptr(rc.Right-rc.Left), uintptr(rc.Bottom-rc.Top), swpNoZOrder|0x0010 /*SWP_NOACTIVATE*/)
		}
		syncWebViewBounds()
		forceRepaint(hwnd)
		return 0
	case wmClose:
		savePlacement(hwnd)
		if trayRunning() {
			// Minimise to tray: hide; the tray (or a second launch) shows it again.
			pShowWindow.Call(hwnd, swHide)
			return 0
		}
	}
	r, _, _ := pCallWindowProcW.Call(origProc, hwnd, msg, wp, lp)
	return r
}

func setIcon(h uintptr) {
	p := filepath.Join(appDir(), "agentos.ico")
	if err := os.WriteFile(p, iconICO, 0o644); err != nil {
		return
	}
	pp, _ := windows.UTF16PtrFromString(p)
	for _, sz := range []struct{ which, px uintptr }{{1, 32}, {0, 16}} { // ICON_BIG, ICON_SMALL
		dpi := uintptr(96)
		if d, _, _ := pGetDpiForWindow.Call(h); d != 0 {
			dpi = d
		}
		px := sz.px * dpi / 96
		ico, _, _ := pLoadImageW.Call(0, uintptr(unsafe.Pointer(pp)), 1 /*IMAGE_ICON*/, px, px, 0x10 /*LR_LOADFROMFILE*/)
		if ico != 0 {
			pSendMessageW.Call(h, wmSetIcon, sz.which, ico)
		}
	}
}

// initialPasswordFile is the on-disk first-run password path (tray / internal).
// Prefer an explicit --data dir; otherwise the Windows service default under ProgramData.
// Do NOT surface this path in the login UI — long/temp/e2e paths are hostile UX.
func initialPasswordFile(cfg config) string {
	base := cfg.DataDir
	if base == "" {
		if pd := os.Getenv("PROGRAMDATA"); pd != "" {
			base = filepath.Join(pd, "AgentOS")
		} else {
			base = `C:\ProgramData\AgentOS`
		}
	}
	return filepath.Join(base, "server", "initial-admin-password.txt")
}

// readInitialPassword returns the plaintext initial admin password if the file
// exists (for in-dialog reveal+copy). Empty string when missing/unreadable.
func readInitialPassword(cfg config) string {
	b, err := os.ReadFile(initialPasswordFile(cfg))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ---------- main window ----------

func openWindow(cfg config, url string) {
	if cfg.Browser {
		_ = openAppBrowser(url, 1280, 800)
		return
	}
	if v, err := webviewloader.GetInstalledVersion(); err != nil || v == "" {
		log.Printf("WebView2 runtime not found (%v)", err)
		t, _ := windows.UTF16PtrFromString("AgentOS")
		m, _ := windows.UTF16PtrFromString("未检测到 Microsoft Edge WebView2 运行时。\n" +
			"WebView2 runtime is not installed.\n\n" +
			"是 / Yes：打开下载页面 (open the download page)\n" +
			"否 / No：使用 Microsoft Edge 应用窗口打开控制台 (use an Edge app window)")
		ret, _ := windows.MessageBox(0, m, t, windows.MB_YESNOCANCEL|windows.MB_ICONWARNING)
		switch ret {
		case 6: // IDYES
			openURL(webview2URL)
			return
		case 7: // IDNO
			if err := openAppBrowser(url, 1280, 800); err != nil {
				fatalDialog("AgentOS", err.Error())
			}
		}
		return
	}
	pSetProcessDpiAwareness.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
	prepareWebViewBackground()                // before the WebView2 controller exists (backdrop_windows.go)

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     cfg.Debug,
		AutoFocus: true,
		DataPath:  filepath.Join(appDir(), "WebView2"),
		WindowOptions: webview2.WindowOptions{
			Title: windowTitle, Width: 1280, Height: 800, Center: true,
		},
	})
	if w == nil {
		log.Printf("WebView2 initialisation failed; falling back to Edge --app")
		if err := openAppBrowser(url, 1280, 800); err != nil {
			fatalDialog("AgentOS", "WebView2: "+err.Error())
		}
		return
	}
	defer w.Destroy()
	mainHwnd = uintptr(w.Window())

	dpi := uintptr(96)
	if d, _, _ := pGetDpiForWindow.Call(mainHwnd); d != 0 {
		dpi = d
	}
	w.SetSize(int(1100*dpi/96), int(700*dpi/96), webview2.HintMin)
	setIcon(mainHwnd)
	chromium = grabChromium(w)
	applyFrameStyle(mainHwnd)
	applyWebViewBackground()
	origProc, _, _ = pSetWindowLongPtrW.Call(mainHwnd, gwlpWndProc, windows.NewCallback(subclassProc))
	pSetWindowPos.Call(mainHwnd, 0, 0, 0, 0, 0, swpFrameChanged|swpNoMove|swpNoSize|swpNoZOrder)
	if !restorePlacement(mainHwnd) {
		w.SetSize(int(1280*dpi/96), int(800*dpi/96), webview2.HintNone)
	}

	// Window controls for the custom title bar (web/static/js/app.js). Bound
	// functions run on the UI thread (WebView2 message callback).
	_ = w.Bind("agentosWinMinimize", func() { pShowWindow.Call(mainHwnd, swMinimize) })
	_ = w.Bind("agentosWinToggleMaximize", func() bool {
		if z, _, _ := pIsZoomed.Call(mainHwnd); z != 0 {
			pShowWindow.Call(mainHwnd, swRestore)
			return false
		}
		pShowWindow.Call(mainHwnd, swMaximize)
		return true
	})
	_ = w.Bind("agentosWinIsMaximized", func() bool { z, _, _ := pIsZoomed.Call(mainHwnd); return z != 0 })
	_ = w.Bind("agentosWinClose", func() { pPostMessageW.Call(mainHwnd, wmClose, 0, 0) })
	_ = w.Bind("agentosWinDrag", func() {
		pReleaseCapture.Call()
		pPostMessageW.Call(mainHwnd, wmNCLButtonDown, htCaption, 0)
	})
	_ = w.Bind("agentosOpenExternal", func(u string) { openURL(u) })
	_ = w.Bind("agentosShellInfo", func() map[string]any {
		wv, _ := webviewloader.GetInstalledVersion()
		info := map[string]any{"shell": "webview2", "tray": trayRunning(), "version": version, "webview2": wv, "backdrop": backdropKind, "build": winBuild()}
		// Prefer in-dialog reveal+copy over dumping a filesystem path (e2e/temp dirs).
		if pw := readInitialPassword(cfg); pw != "" {
			info["initial_password"] = pw
		}
		return info
	})
	// Theme sync: the UI reports its effective theme so the DWM frame matches.
	_ = w.Bind("agentosSetTheme", func(dark bool) { setUITheme(mainHwnd, dark) })
	_ = w.Bind("agentosSetBackdrop", func(kind string) bool { return setBackdrop(mainHwnd, kind) })
	w.Navigate(url)
	w.Run()
}
