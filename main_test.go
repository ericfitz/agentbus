package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
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
		if runtime.GOOS == "windows" {
			binPath += ".exe"
		}
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

// TestNoArgsUsageExit0: bare agentbus prints usage like help and exits 0;
// winget validation runs the executable with no arguments and needs 0.
func TestNoArgsUsageExit0(t *testing.T) {
	r := execBin(t)
	if r.code != 0 {
		t.Errorf("exit %d, want 0", r.code)
	}
	if strings.TrimSpace(r.stdout) != strings.TrimSpace(usage) || r.stderr != "" {
		t.Errorf("stdout %q stderr %q, want usage on stdout only", r.stdout, r.stderr)
	}
}

func TestUnknownCommandExit2(t *testing.T) {
	r := execBin(t, "bogus")
	if r.code != 2 || !strings.Contains(r.stderr, `unknown command "bogus"`) {
		t.Errorf("exit %d stderr %q", r.code, r.stderr)
	}
}

func TestSubcommandHelpExitsZero(t *testing.T) {
	cmds := []string{"mcp", "init", "tui", "wait", "watch", "status", "reset", "delete-channel",
		"subscribe", "unsubscribe", "stop-hook", "subagent-hook", "identity", "park", "unpark"}
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
	for _, c := range []string{"init", "tui", "wait", "watch", "status", "reset", "delete-channel", "subscribe", "unsubscribe", "park"} {
		t.Run(c, func(t *testing.T) {
			r := execBin(t, c, "-bogus")
			if r.code == 0 {
				t.Errorf("%s -bogus exited 0", c)
			}
		})
	}
}

// TestExtraArgumentsRejected: a command that takes flags only (or nothing)
// reports a positional argument instead of ignoring it. The hooks report it
// but still exit 0, so a misconfigured hook never blocks the harness. Each
// run has a deadline: before the fix, wait, watch and tui ran with the
// argument ignored and never exited.
func TestExtraArgumentsRejected(t *testing.T) {
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"mcp", "extra"}, 1, `agentbus mcp: unexpected argument "extra"`},
		{[]string{"init", "extra"}, 2, `agentbus init: unexpected argument "extra"`},
		{[]string{"tui", "extra"}, 2, `agentbus tui: unexpected argument "extra"`},
		{[]string{"wait", "-as", "Pat", "extra"}, 2, `agentbus wait: unexpected argument "extra"`},
		{[]string{"watch", "-as", "Pat", "extra"}, 2, `agentbus watch: unexpected argument "extra"`},
		{[]string{"status", "extra"}, 1, `agentbus status: unexpected argument "extra"`},
		{[]string{"reset", "extra"}, 1, `agentbus reset: unexpected argument "extra"`},
		{[]string{"identity", "Sam"}, 2, `agentbus identity: unexpected argument "Sam"`},
		{[]string{"stop-hook", "extra"}, 0, `agentbus stop-hook: unexpected argument "extra"`},
		{[]string{"subagent-hook", "extra"}, 0, `agentbus subagent-hook: unexpected argument "extra"`},
		{[]string{"park", "-as", "Pat", "extra"}, 2, `agentbus park: unexpected argument "extra"`},
		{[]string{"unpark", "-as", "Pat", "extra"}, 0, `agentbus unpark: unexpected argument "extra"`},
		{[]string{"version", "extra"}, 2, `agentbus version: unexpected argument "extra"`},
		{[]string{"--version", "extra"}, 2, `agentbus --version: unexpected argument "extra"`},
		{[]string{"help", "status"}, 2, `agentbus help: unexpected argument "status"`},
		{[]string{"-h", "status"}, 2, `agentbus -h: unexpected argument "status"`},
	} {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, testBinary(t), c.args...)
			cmd.Dir = t.TempDir()
			cmd.Env = append(os.Environ(), "HOME="+cmd.Dir, "USERPROFILE="+cmd.Dir)
			var so, se bytes.Buffer
			cmd.Stdout, cmd.Stderr = &so, &se
			err := cmd.Run()
			if ctx.Err() != nil {
				t.Fatalf("did not exit within 10s: the argument was ignored")
			}
			code := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != c.code || so.Len() != 0 || !strings.Contains(se.String(), c.want) {
				t.Errorf("exit %d stdout %q stderr %q; want exit %d, %q on stderr only", code, so.String(), se.String(), c.code, c.want)
			}
		})
	}
}

// TestIdentityUnknownFlag: identity has no flags, so any flag is an error.
func TestIdentityUnknownFlag(t *testing.T) {
	r := execBin(t, "identity", "-bogus")
	if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, "Usage of agentbus identity") {
		t.Errorf("exit %d stdout %q stderr %q; want exit 2 with usage on stderr only", r.code, r.stdout, r.stderr)
	}
}

func TestWatchErrorsExit2WithNothingOnStdout(t *testing.T) {
	for name, args := range map[string][]string{
		"bad filter":       {"watch", "-as", "Pat", "-filter", "("},
		"unknown identity": {"watch", "-as", "Ghost"},
		"no -timeout":      {"watch", "-as", "Pat", "-timeout", "1s"},
	} {
		t.Run(name, func(t *testing.T) {
			r := execBin(t, args...)
			if r.code != 2 || r.stdout != "" || r.stderr == "" {
				t.Errorf("exit %d stdout %q stderr %q; want exit 2, error on stderr only", r.code, r.stdout, r.stderr)
			}
		})
	}
}
