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
