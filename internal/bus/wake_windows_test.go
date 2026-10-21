//go:build windows

package bus

import (
	"testing"
	"time"
)

func TestWindowsMonoAdvancesAndIsMonotone(t *testing.T) {
	if _, ok := unbiasedInterruptTime(); !ok || !haveUnbiased {
		t.Fatal("QueryUnbiasedInterruptTime failed")
	}
	prev := monotonicSinceStart()
	time.Sleep(50 * time.Millisecond)
	cur := monotonicSinceStart()
	if cur-prev < 40*time.Millisecond {
		t.Fatalf("Mono advanced %v over a 50 ms sleep", cur-prev)
	}
	for i := 0; i < 1000; i++ {
		next := monotonicSinceStart()
		if next < cur {
			t.Fatalf("Mono went back: %v -> %v", cur, next)
		}
		cur = next
	}
}
