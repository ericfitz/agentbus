//go:build windows

package bus

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Go's monotonic clock on Windows counts time asleep, which would hide every
// wake, so Bus.Mono reads the unbiased interrupt time instead: it excludes
// sleep and hibernation (ADR 0023).
var (
	procQueryUnbiasedInterruptTime = windows.NewLazySystemDLL("kernel32.dll").NewProc("QueryUnbiasedInterruptTime")
	processStart                   = unbiasedInterruptTime()
)

// unbiasedInterruptTime is QueryUnbiasedInterruptTime in 100 ns units
// converted to a Duration; 0 if the call fails.
func unbiasedInterruptTime() time.Duration {
	var t uint64
	if r, _, _ := procQueryUnbiasedInterruptTime.Call(uintptr(unsafe.Pointer(&t))); r == 0 {
		return 0
	}
	return time.Duration(t) * 100
}

// monotonicSinceStart is Bus.Mono's default on Windows.
func monotonicSinceStart() time.Duration { return unbiasedInterruptTime() - processStart }
