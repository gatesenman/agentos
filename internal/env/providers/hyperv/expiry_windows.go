//go:build windows

package hyperv

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// buildExpiry returns the expiry of a Windows preview (Insider) build; zero
// time for retail builds.
func buildExpiry() (time.Time, string) {
	build := ""
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		build, _, _ = k.GetStringValue("CurrentBuild")
		k.Close()
	}
	proc := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryLicenseValue")
	if proc.Find() != nil {
		return time.Time{}, build
	}
	name, err := windows.NewNTUnicodeString("Kernel-ExpirationDate")
	if err != nil {
		return time.Time{}, build
	}
	var typ, ret uint32
	buf := make([]byte, 64)
	st, _, _ := proc.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&ret)))
	if st != 0 || ret < 12 {
		return time.Time{}, build
	}
	t, ok := parseSystemTime(buf[:ret])
	if !ok {
		return time.Time{}, build
	}
	return t, build
}
