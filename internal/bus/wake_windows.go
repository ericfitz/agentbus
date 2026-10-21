//go:build windows

package bus

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Go's monotonic clock on Windows counts time asleep, which would hide every
// wake, so Bus.Mono reads the unbiased interrupt time instead: it excludes
// sleep and hibernation (ADR 0023). If the export is missing or the call
// fails, Mono falls back to Go's clock, which only loses wake detection.
var (
	procQueryUnbiasedInterruptTime = windows.NewLazySystemDLL("kernel32.dll").NewProc("QueryUnbiasedInterruptTime")
	goStart                        = time.Now()
	unbiasedStart, haveUnbiased    = unbiasedInterruptTime()
)

// unbiasedInterruptTime is QueryUnbiasedInterruptTime in 100 ns units
// converted to a Duration; ok is false when the export is missing or the
// call fails. Find is checked first because LazyProc.Call panics on a
// missing export.
func unbiasedInterruptTime() (d time.Duration, ok bool) {
	if procQueryUnbiasedInterruptTime.Find() != nil {
		return 0, false
	}
	var t uint64
	if r, _, _ := procQueryUnbiasedInterruptTime.Call(uintptr(unsafe.Pointer(&t))); r == 0 {
		return 0, false
	}
	return time.Duration(t) * 100, true
}

// monotonicSinceStart is Bus.Mono's default on Windows.
func monotonicSinceStart() time.Duration {
	if haveUnbiased {
		if d, ok := unbiasedInterruptTime(); ok {
			return d - unbiasedStart
		}
	}
	return time.Since(goStart)
}
