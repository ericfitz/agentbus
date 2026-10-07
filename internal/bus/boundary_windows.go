//go:build windows

package bus

import (
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WNetGetConnectionW is not wrapped by x/sys/windows; it lives in mpr.dll.
var (
	modmpr                 = windows.NewLazySystemDLL("mpr.dll")
	procWNetGetConnectionW = modmpr.NewProc("WNetGetConnectionW")
)

// driveTarget resolves a mapped drive ("Z:") to the UNC path it maps to.
// ok is false for a local drive, an unmapped letter, or any error.
func driveTarget(drive string) (string, bool) {
	local, err := windows.UTF16PtrFromString(drive)
	if err != nil {
		return "", false
	}
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	r, _, _ := procWNetGetConnectionW.Call(uintptr(unsafe.Pointer(local)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r != 0 { // NO_ERROR is 0; ERROR_NOT_CONNECTED (2250) for a local drive
		return "", false
	}
	return windows.UTF16ToString(buf), true
}

// platformBoundary refuses a directory inside a WSL distribution, reached
// directly by UNC path or through a mapped drive letter.
func platformBoundary(dir string) (fs, suggestion string, refused bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", false
	}
	fs, refused = wslPathRefusal(abs, driveTarget)
	return fs, `%LocalAppData%\agentbus`, refused
}
