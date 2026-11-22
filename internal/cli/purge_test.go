package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

// purgeEnv is a temp home with a config file naming a data directory that
// holds a real bus and the other files agentbus writes.
type purgeEnv struct {
	home, data, cfgPath string
	f                   *fakeHarness
}

func newPurgeEnv(t *testing.T) *purgeEnv {
	t.Helper()
	t.Setenv("AGENTBUS_DATA_DIR", "")
	t.Setenv("AGENTBUS_CONFIG", "")
	home := t.TempDir()
	// A missing config means defaults, which live under the home directory:
	// keep them away from the real one.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	e := &purgeEnv{
		home:    home,
		data:    filepath.Join(home, "data"),
		cfgPath: filepath.Join(home, "cfg", "config.json"),
		f:       &fakeHarness{home: home},
	}
	body, err := json.Marshal(map[string]string{"data_directory": e.data})
	if err != nil {
		t.Fatal(err)
	}
	e.writeFile(t, e.cfgPath, string(body))
	return e
}

func (e *purgeEnv) writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seed creates a real database (with one live session when live) and the
// log, park and wait files.
func (e *purgeEnv) seed(t *testing.T, live bool) {
	t.Helper()
	cfg, _, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if live {
		if _, err := b.Register("sam", "", "repo", true); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"agentbus.log", "agentbus.log.1", "agentbus.log.lock", "park/a.json", "wait/b.pid"} {
		e.writeFile(t, filepath.Join(e.data, filepath.FromSlash(n)), "x")
	}
}

func (e *purgeEnv) opts(in string) UninstallOptions {
	o := uninstallOpts(e.home, e.f)
	o.Purge = true
	o.ConfigPath = e.cfgPath
	o.In = strings.NewReader(in)
	o.Executable = fakeExe(filepath.Join(e.home, "agentbus"))
	return o
}

func (e *purgeEnv) run(t *testing.T, o UninstallOptions) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Uninstall(o, &out)
	return out.String(), err
}

func TestPurgeYesDeletesEverything(t *testing.T) {
	e := newPurgeEnv(t)
	e.seed(t, false)
	out, err := e.run(t, e.opts("yes\n"))
	if err != nil {
		t.Fatal(err, out)
	}
	if exists(e.data) || exists(e.cfgPath) || exists(filepath.Dir(e.cfgPath)) {
		t.Errorf("data or config survived:\n%s", out)
	}
}

func TestPurgeEOFCancels(t *testing.T) {
	e := newPurgeEnv(t)
	e.seed(t, false)
	out, err := e.run(t, e.opts(""))
	if !errors.Is(err, ErrPurgeCancelled) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "purge cancelled") || !exists(filepath.Join(e.data, "agentbus.db")) || !exists(e.cfgPath) {
		t.Errorf("out=%q", out)
	}
}

func TestPurgeOtherAnswerCancels(t *testing.T) {
	e := newPurgeEnv(t)
	e.seed(t, false)
	_, err := e.run(t, e.opts("y\n"))
	if !errors.Is(err, ErrPurgeCancelled) {
		t.Fatalf("err = %v", err)
	}
	if !exists(filepath.Join(e.data, "agentbus.db")) || !exists(e.cfgPath) {
		t.Error("something was deleted")
	}
}

func TestPurgeRefusesLiveSession(t *testing.T) {
	e := newPurgeEnv(t)
	e.seed(t, true)
	out, err := e.run(t, e.opts("yes\n"))
	if !errors.Is(err, ErrPurgeRefused) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "1 live agentbus session") {
		t.Errorf("out=%q", out)
	}
	if !exists(filepath.Join(e.data, "agentbus.db")) || !exists(e.cfgPath) {
		t.Error("something was deleted")
	}
}

func TestPurgeRefusesMalformedConfig(t *testing.T) {
	e := newPurgeEnv(t)
	e.seed(t, false)
	e.writeFile(t, e.cfgPath, "{")
	out, err := e.run(t, e.opts("yes\n"))
	if !errors.Is(err, ErrPurgeRefused) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, e.cfgPath) || !strings.Contains(out, "delete by hand") {
		t.Errorf("out=%q", out)
	}
	if !exists(filepath.Join(e.data, "agentbus.db")) || !exists(e.cfgPath) {
		t.Error("something was deleted")
	}
}

func TestPurgeRefusesUnopenableDB(t *testing.T) {
	e := newPurgeEnv(t)
	db := filepath.Join(e.data, "agentbus.db")
	e.writeFile(t, db, strings.Repeat("not a database ", 100))
	out, err := e.run(t, e.opts("yes\n"))
	if !errors.Is(err, ErrPurgeRefused) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if !exists(db) || !exists(e.cfgPath) {
		t.Error("something was deleted")
	}
}

func TestPurgeKeepsForeignFiles(t *testing.T) {
	e := newPurgeEnv(t)
	e.seed(t, false)
	notes := filepath.Join(e.data, "notes.txt")
	e.writeFile(t, notes, "mine")
	out, err := e.run(t, e.opts("yes\n"))
	if err != nil {
		t.Fatal(err, out)
	}
	if !exists(notes) || !strings.Contains(out, "notes.txt") || !strings.Contains(out, "did not write") {
		t.Errorf("out=%q", out)
	}
	for _, n := range []string{"agentbus.db", "agentbus.log", "park", "wait"} {
		if exists(filepath.Join(e.data, n)) {
			t.Errorf("%s survived", n)
		}
	}
	if exists(e.cfgPath) {
		t.Error("config survived")
	}
}

func TestPurgeMissingDataDir(t *testing.T) {
	e := newPurgeEnv(t)
	out, err := e.run(t, e.opts("yes\n"))
	if err != nil {
		t.Fatal(err, out)
	}
	if !strings.Contains(out, "already removed") || exists(e.data) || exists(e.cfgPath) {
		t.Errorf("out=%q", out)
	}
}

type failReader struct{ t *testing.T }

func (r failReader) Read([]byte) (int, error) {
	r.t.Error("purge read stdin under --dry-run")
	return 0, io.EOF
}

func TestPurgeDryRun(t *testing.T) {
	e := newPurgeEnv(t)
	e.seed(t, false)
	o := e.opts("")
	o.In = failReader{t}
	o.DryRun = true
	before := snapshot(t, e.home)
	out, err := e.run(t, o)
	if err != nil {
		t.Fatal(err, out)
	}
	if !strings.Contains(out, "would delete:") {
		t.Errorf("out=%q", out)
	}
	if after := snapshot(t, e.home); len(after) != len(before) {
		t.Error("dry run changed files")
	}
}

func TestPurgeTwice(t *testing.T) {
	e := newPurgeEnv(t)
	e.seed(t, false)
	if out, err := e.run(t, e.opts("yes\n")); err != nil {
		t.Fatal(err, out)
	}
	if out, err := e.run(t, e.opts("yes\n")); err != nil {
		t.Fatal(err, out)
	}
}

func TestPurgeNoHarnessStillPurges(t *testing.T) {
	e := newPurgeEnv(t)
	e.seed(t, false)
	out, err := e.run(t, e.opts("yes\n"))
	if err != nil {
		t.Fatal(err, out)
	}
	if !strings.Contains(out, "no harness found") || exists(e.data) {
		t.Errorf("out=%q", out)
	}
}

func TestPurgeSkippedAfterHarnessError(t *testing.T) {
	e := newPurgeEnv(t)
	e.seed(t, false)
	e.writeFile(t, filepath.Join(e.home, ".claude", "settings.json"), "{")
	_, err := e.run(t, e.opts("yes\n"))
	if err == nil || errors.Is(err, ErrPurgeCancelled) || errors.Is(err, ErrPurgeRefused) {
		t.Fatalf("err = %v, want the settings.json error", err)
	}
	if !exists(filepath.Join(e.data, "agentbus.db")) || !exists(e.cfgPath) {
		t.Error("purge ran after a harness error")
	}
}

func TestPurgeRefusesWhenDBCannotBeStatted(t *testing.T) {
	e := newPurgeEnv(t)
	e.seed(t, false)
	db := filepath.Join(e.data, "agentbus.db")
	if err := os.Remove(db); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(e.home, "nowhere"), db); err != nil {
		t.Skip("no symlinks:", err)
	}
	// A dangling link stats as ENOENT; make a real stat failure instead.
	if err := os.Remove(db); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("agentbus.db", db); err != nil { // ELOOP
		t.Skip("no symlinks:", err)
	}
	out, err := e.run(t, e.opts("yes\n"))
	if !errors.Is(err, ErrPurgeRefused) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if !exists(e.cfgPath) || !exists(filepath.Join(e.data, "park")) {
		t.Error("something was deleted")
	}
}

func TestPurgeDataDirWithGlobCharacters(t *testing.T) {
	e := newPurgeEnv(t)
	e.data = filepath.Join(e.home, "d[a]ta*")
	body, _ := json.Marshal(map[string]string{"data_directory": e.data})
	e.writeFile(t, e.cfgPath, string(body))
	e.seed(t, false)
	out, err := e.run(t, e.opts("yes\n"))
	if err != nil {
		t.Fatal(err, out)
	}
	if exists(e.data) || strings.Contains(out, "did not write") {
		t.Errorf("out=%q", out)
	}
}

func TestPurgeKeepsUserDirNamedLikeALogRotation(t *testing.T) {
	e := newPurgeEnv(t)
	e.seed(t, false)
	mine := filepath.Join(e.data, "agentbus.log.mine", "keep.txt")
	e.writeFile(t, mine, "x")
	out, err := e.run(t, e.opts("yes\n"))
	if err != nil {
		t.Fatal(err, out)
	}
	if !exists(mine) || !strings.Contains(out, "agentbus.log.mine") || !strings.Contains(out, "did not write") {
		t.Errorf("out=%q", out)
	}
	if exists(filepath.Join(e.data, "agentbus.log.1")) {
		t.Error("rotation survived")
	}
}

func TestPurgeWalFilesAreNotReportedForeign(t *testing.T) {
	e := newPurgeEnv(t)
	e.seed(t, false)
	e.writeFile(t, filepath.Join(e.data, "agentbus.db-wal"), "x")
	e.writeFile(t, filepath.Join(e.data, "agentbus.db-shm"), "x")
	out, err := e.run(t, e.opts("yes\n"))
	if err != nil {
		t.Fatal(err, out)
	}
	if strings.Contains(out, "did not write") || exists(e.data) {
		t.Errorf("out=%q", out)
	}
}
