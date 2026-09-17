package bus

import (
	"math/rand"
	"sort"
	"testing"
)

func TestRankBetweenOrdersStrictly(t *testing.T) {
	cases := [][2]string{{"", ""}, {"", "V"}, {"V", ""}, {"A", "B"}, {"A", "A1"}, {"Az", "B"}, {"", "1"}, {"zz", ""}, {"A", "A01"}}
	for _, c := range cases {
		m := rankBetween(c[0], c[1])
		if m == "" || (c[0] != "" && m <= c[0]) || (c[1] != "" && m >= c[1]) {
			t.Fatalf("rankBetween(%q,%q)=%q is not strictly between", c[0], c[1], m)
		}
		if m[len(m)-1] == '0' {
			t.Fatalf("rankBetween(%q,%q)=%q ends in '0' (nothing sorts between x and x0)", c[0], c[1], m)
		}
	}
}

// TestRankBetweenClampsOffAlphabetBytes covers Minor 1 of the final review:
// a byte outside rankDigits (reachable only from a corrupt or hand-written
// row, since edit_memory refuses tasks/ channels) must not index IndexByte's
// -1 result and panic.
func TestRankBetweenClampsOffAlphabetBytes(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("rankBetween panicked: %v", r)
		}
	}()
	if m := rankBetween("-", "0"); m == "" {
		t.Fatal("rankBetween(\"-\",\"0\") returned empty")
	}
	if m := rankBetween("~", ""); m == "" {
		t.Fatal("rankBetween(\"~\",\"\") returned empty")
	}
}

// TestRankStepAppendAndPrependStayShort covers Minor 2 of the final review:
// repeated appends or prepends at one end of a list must grow the rank key
// by about a byte per len(rankDigits)-1 (61) operations, not one every ~6,
// and must keep every invariant rankBetween's own property test checks.
func TestRankStepAppendAndPrependStayShort(t *testing.T) {
	key := rankBetween("", "")
	for i := 0; i < 5000; i++ {
		next := rankStep(key, "")
		if next <= key {
			t.Fatalf("append %d: %q not after %q", i, next, key)
		}
		if next[len(next)-1] == '0' {
			t.Fatalf("append %d: %q ends in '0'", i, next)
		}
		key = next
	}
	if len(key) > 90 {
		t.Fatalf("5000 appends grew to %d bytes: %q", len(key), key)
	}

	key = rankBetween("", "")
	for i := 0; i < 5000; i++ {
		prev := rankStep("", key)
		if prev == "" {
			t.Fatalf("prepend %d produced the empty string", i)
		}
		if prev >= key {
			t.Fatalf("prepend %d: %q not before %q", i, prev, key)
		}
		if prev[len(prev)-1] == '0' {
			t.Fatalf("prepend %d: %q ends in '0'", i, prev)
		}
		key = prev
	}
	if len(key) > 90 {
		t.Fatalf("5000 prepends grew to %d bytes: %q", len(key), key)
	}
}

// TestRankStepBetweenTwoKeysStillMidpoints checks that a bounded insert
// (both neighbors known) is unaffected by rankStep's append/prepend paths:
// it still lands strictly between them.
func TestRankStepBetweenTwoKeysStillMidpoints(t *testing.T) {
	cases := [][2]string{{"A", "B"}, {"A", "A1"}, {"Az", "B"}}
	for _, c := range cases {
		m := rankStep(c[0], c[1])
		if m <= c[0] || m >= c[1] {
			t.Fatalf("rankStep(%q,%q)=%q is not strictly between", c[0], c[1], m)
		}
	}
}

// Repeated insertion at one spot (the worst case for float or sparse-int
// ranks) and at random spots keeps a strict total order.
func TestRankBetweenSurvivesManyInserts(t *testing.T) {
	keys := []string{rankBetween("", "")}
	for i := 0; i < 500; i++ { // always insert at the front
		keys = append([]string{rankBetween("", keys[0])}, keys...)
	}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		j := r.Intn(len(keys) + 1)
		lo, hi := "", ""
		if j > 0 {
			lo = keys[j-1]
		}
		if j < len(keys) {
			hi = keys[j]
		}
		k := rankBetween(lo, hi)
		keys = append(keys[:j], append([]string{k}, keys[j:]...)...)
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatal("keys are not sorted")
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] == keys[i-1] {
			t.Fatalf("duplicate key %q", keys[i])
		}
	}
}
