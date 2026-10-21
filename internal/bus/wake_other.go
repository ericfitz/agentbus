//go:build !windows

package bus

import "time"

var processStart = time.Now()

// monotonicSinceStart is Bus.Mono's default: time.Since reads the
// monotonic clock, which processStart carries and which stops while the
// machine sleeps on Linux and macOS.
func monotonicSinceStart() time.Duration { return time.Since(processStart) }
