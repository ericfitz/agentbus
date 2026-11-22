package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

// ErrPurgeRefused means purge could not verify the bus is idle, or found a
// live session; nothing was deleted.
var ErrPurgeRefused = errors.New("purge refused")

// ErrPurgeCancelled means the answer was not exactly "yes"; nothing was deleted.
var ErrPurgeCancelled = errors.New("purge cancelled")

// dataFiles are the files agentbus writes directly in the data directory;
// dataDirs are the subdirectories it owns. agentbus.log.* (rotations and the
// lock file) is matched by glob.
var (
	dataFiles = []string{"agentbus.db", "agentbus.db-wal", "agentbus.db-shm", "agentbus.db-journal", "agentbus.log"}
	dataDirs  = []string{"park", "wait"}
)

// purge deletes the config file and the bus data, but only after verifying no
// session is live and the user typed yes. It deletes only what agentbus
// wrote and removes a directory only when it is empty.
func (in *initer) purge(o UninstallOptions) error {
	cfg, cfgPath, err := config.Load(o.ConfigPath)
	if err != nil {
		return in.refuse(o, cfgPath, "", err)
	}
	targets, err := purgeTargets(cfg.DataDirectory)
	if err != nil {
		return in.refuse(o, cfgPath, cfg.DataDirectory, err)
	}
	var cfgExists bool
	if _, err := os.Lstat(cfgPath); err == nil {
		cfgExists = true
	}
	if in.DryRun {
		in.say("would delete:")
		for _, t := range targets {
			in.say("  %s", t)
		}
		if cfgExists {
			in.say("  %s", cfgPath)
		}
		return nil
	}
	db := filepath.Join(cfg.DataDirectory, "agentbus.db")
	if _, err := os.Stat(db); err == nil {
		if err := in.checkIdle(cfg); err != nil {
			if errors.Is(err, ErrPurgeRefused) {
				return err
			}
			return in.refuse(o, cfgPath, cfg.DataDirectory, err)
		}
	}
	in.say("this deletes:")
	for _, t := range targets {
		in.say("  %s", t)
	}
	if cfgExists {
		in.say("  %s", cfgPath)
	}
	if len(targets) == 0 && !cfgExists {
		in.say("  (nothing: already removed)")
	}
	_, _ = fmt.Fprint(in.out, "Type yes to continue: ")
	line, readErr := bufio.NewReader(o.In).ReadString('\n')
	if readErr != nil && readErr != io.EOF {
		return readErr
	}
	if strings.TrimSpace(line) != "yes" {
		in.say("purge cancelled")
		return ErrPurgeCancelled
	}
	if err := in.deleteData(cfg.DataDirectory, targets); err != nil {
		return err
	}
	if !cfgExists {
		in.say("already removed: %s", cfgPath)
		return nil
	}
	if err := os.Remove(cfgPath); err != nil {
		return err
	}
	in.say("removed %s", cfgPath)
	_ = os.Remove(filepath.Dir(cfgPath)) // keep it silently when it is not empty
	return nil
}

// refuse prints why the bus could not be verified idle and what to delete by
// hand, and returns ErrPurgeRefused.
func (in *initer) refuse(o UninstallOptions, cfgPath, dataDir string, cause error) error {
	if cfgPath == "" {
		cfgPath, _ = config.DefaultPath()
	}
	if dataDir == "" {
		dataDir = config.Default().DataDirectory
		if rest, ok := strings.CutPrefix(dataDir, "~/"); ok {
			dataDir = filepath.Join(o.Home, rest)
		}
	}
	in.say("uninstall: cannot verify that no session is live: %v", cause)
	in.say("delete by hand after closing every harness:\n  %s\n  %s", cfgPath, dataDir)
	return ErrPurgeRefused
}

// checkIdle opens the existing database and refuses while a session is live.
// It closes the bus before returning so the files can be deleted.
func (in *initer) checkIdle(cfg config.Config) error {
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	n, err := b.LiveSessionCount()
	if cerr := b.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > 0 {
		in.say("uninstall: %d live agentbus session(s); close or restart every harness, then run agentbus uninstall --purge again", n)
		return ErrPurgeRefused
	}
	return nil
}

// purgeTargets lists the paths agentbus wrote in dir that exist. A missing
// dir has none.
func purgeTargets(dir string) ([]string, error) {
	var out []string
	for _, n := range dataFiles {
		out = append(out, filepath.Join(dir, n))
	}
	rot, err := filepath.Glob(filepath.Join(dir, "agentbus.log.*"))
	if err != nil {
		return nil, err
	}
	sort.Strings(rot)
	out = append(out, rot...)
	for _, n := range dataDirs {
		out = append(out, filepath.Join(dir, n))
	}
	var found []string
	for _, p := range out {
		if _, err := os.Lstat(p); err == nil {
			found = append(found, p)
		}
	}
	return found, nil
}

// deleteData removes targets, then the data directory if nothing else is in
// it; otherwise it lists what it kept.
func (in *initer) deleteData(dir string, targets []string) error {
	for _, p := range targets {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
		in.say("removed %s", p)
	}
	if err := os.Remove(dir); err == nil {
		in.say("removed %s", dir)
		return nil
	}
	left, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		in.say("already removed: %s", dir)
		return nil
	}
	if err != nil {
		return err
	}
	if len(left) == 0 {
		return fmt.Errorf("could not remove %s", dir)
	}
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	in.say("kept %s: it holds files agentbus did not write:\n  %s", dir, strings.Join(names, "\n  "))
	return nil
}
