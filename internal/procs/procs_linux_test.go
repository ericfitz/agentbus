//go:build linux

package procs

import "testing"

func TestParseStatOddComm(t *testing.T) {
	line := "1234 (my) proc) S 77 1234 1234 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 987654 1000 100 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0"
	got, err := parseStat(line)
	if err != nil {
		t.Fatal(err)
	}
	if got.comm != "my) proc" || got.ppid != 77 || got.start != 987654 {
		t.Fatalf("got %+v", got)
	}
}
