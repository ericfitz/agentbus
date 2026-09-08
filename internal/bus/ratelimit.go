package bus

import (
	"sync"
	"time"

	"github.com/ericfitz/agentbus/internal/config"
)

// limiter holds per-sender token buckets for count and bytes, one-second burst.
type limiter struct {
	mu       sync.Mutex
	perSec   float64
	bytesSec float64
	byteCap  float64
	buckets  map[string]*bucket
}

type bucket struct {
	count, bytes float64
	last         time.Time
}

func newLimiter(cfg config.Config) *limiter {
	byteCap := float64(cfg.SendKiBPerSecond * 1024)
	if m := float64(cfg.MaxMessageKiB * 1024); byteCap < m {
		byteCap = m
	}
	return &limiter{perSec: float64(cfg.SendMessagesPerSecond), bytesSec: float64(cfg.SendKiBPerSecond * 1024), byteCap: byteCap, buckets: map[string]*bucket{}}
}

func (l *limiter) allow(sender string, n int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	bk, ok := l.buckets[sender]
	if !ok {
		bk = &bucket{count: l.perSec, bytes: l.byteCap, last: now}
		l.buckets[sender] = bk
	}
	el := now.Sub(bk.last).Seconds()
	if el > 0 {
		bk.count = min(l.perSec, bk.count+el*l.perSec)
		bk.bytes = min(l.byteCap, bk.bytes+el*l.bytesSec)
		bk.last = now
	}
	if bk.count < 1 || bk.bytes < float64(n) {
		return false
	}
	bk.count--
	bk.bytes -= float64(n)
	return true
}
