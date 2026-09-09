package tui

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRejectsBadNameBeforeStartingTheProgram(t *testing.T) {
	cfg := testConfig(t)
	var stderr bytes.Buffer
	err := Run(cfg, "bad/name", &stderr)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("want a name validation error, got %v", err)
	}
}
