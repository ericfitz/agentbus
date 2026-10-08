package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// testBinary builds the agentbus binary once and returns its path.
func testBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "agentbus-main-test")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "agentbus")
		if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
			buildErr = errors.New(string(out))
		}
	})
	if buildErr != nil {
		t.Fatalf("build: %v", buildErr)
	}
	return binPath
}

func TestMain(m *testing.M) {
	code := m.Run()
	if binPath != "" {
		_ = os.RemoveAll(filepath.Dir(binPath))
	}
	os.Exit(code)
}

type result struct {
	code           int
	stdout, stderr string
}

func execBin(t *testing.T, args ...string) result {
	t.Helper()
	cmd := exec.Command(testBinary(t), args...)
	// Isolate from any real configuration or bus.
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "HOME="+cmd.Dir, "USERPROFILE="+cmd.Dir)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return result{code, so.String(), se.String()}
}

func TestTopLevelHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		r := execBin(t, args...)
		if r.code != 0 {
			t.Errorf("%v: exit %d, want 0 (stderr %q)", args, r.code, r.stderr)
		}
		if strings.TrimSpace(r.stdout) != strings.TrimSpace(usage) {
			t.Errorf("%v: stdout %q, want usage", args, r.stdout)
		}
		if r.stderr != "" {
			t.Errorf("%v: unexpected stderr %q", args, r.stderr)
		}
	}
}

func TestVersionFlags(t *testing.T) {
	want := execBin(t, "version")
	if want.code != 0 || strings.TrimSpace(want.stdout) == "" {
		t.Fatalf("version: exit %d stdout %q", want.code, want.stdout)
	}
	for _, flagName := range []string{"--version", "-v"} {
		got := execBin(t, flagName)
		if got != want {
			t.Errorf("%s: got %+v, want %+v", flagName, got, want)
		}
	}
}

func TestNoArgsUsageExit2(t *testing.T) {
	r := execBin(t)
	if r.code != 2 {
		t.Errorf("exit %d, want 2", r.code)
	}
	if strings.TrimSpace(r.stderr) != strings.TrimSpace(usage) || r.stdout != "" {
		t.Errorf("stdout %q stderr %q, want usage on stderr only", r.stdout, r.stderr)
	}
}

func TestUnknownCommandExit2(t *testing.T) {
	r := execBin(t, "bogus")
	if r.code != 2 || !strings.Contains(r.stderr, `unknown command "bogus"`) {
		t.Errorf("exit %d stderr %q", r.code, r.stderr)
	}
}

func TestSubcommandHelpExitsZero(t *testing.T) {
	cmds := []string{"mcp", "init", "tui", "wait", "status", "reset", "delete-channel",
		"subscribe", "unsubscribe", "stop-hook", "subagent-hook"}
	for _, c := range cmds {
		for _, h := range []string{"-h", "--help"} {
			t.Run(c+" "+h, func(t *testing.T) {
				r := execBin(t, c, h)
				if r.code != 0 {
					t.Errorf("exit %d, want 0 (stderr %q)", r.code, r.stderr)
				}
				if !strings.Contains(r.stderr+r.stdout, "Usage of agentbus") {
					t.Errorf("no flag usage printed: stdout %q stderr %q", r.stdout, r.stderr)
				}
			})
		}
	}
}

func TestUnknownFlagStillFails(t *testing.T) {
	for _, c := range []string{"init", "tui", "wait", "status", "reset", "delete-channel", "subscribe", "unsubscribe"} {
		t.Run(c, func(t *testing.T) {
			r := execBin(t, c, "-bogus")
			if r.code == 0 {
				t.Errorf("%s -bogus exited 0", c)
			}
		})
	}
}
