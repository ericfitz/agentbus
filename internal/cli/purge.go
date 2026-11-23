package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

// ErrPurgeRefused means purge could not verify the bus is idle, or found a
// live session; nothing was deleted.
var ErrPurgeRefused = errors.New("purge refused")

// ErrPurgeCanceled means the answer was not exactly "yes"; nothing was deleted.
var ErrPurgeCanceled = errors.New("purge canceled")

// dataFiles are the files agentbus writes directly in the data directory;
// dataDirs are the subdirectories it owns. The log's rotations
// (agentbus.log.<digits>) are matched by isLogRotation.
var (
	dataFiles = []string{"agentbus.db", "agentbus.db-wal", "agentbus.db-shm", "agentbus.db-journal", "agentbus.log", "agentbus.log.lock"}
	dataDirs  = []string{"park", "wait"}
)

// isLogRotation reports whether name is agentbus.log.<digits>, the only
// rotated names the log writer makes.
func isLogRotation(name string) bool {
	rest, ok := strings.CutPrefix(name, "agentbus.log.")
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// purge deletes the config file and the bus data, but only after verifying no
// session is live and the user typed yes. It deletes only what agentbus
// wrote and removes a directory only when it is empty.
//
// An explicitly named config (--config or AGENTBUS_CONFIG) that does not exist
// never falls back to the default data directory (decision 7): only
// AGENTBUS_DATA_DIR can then name a data directory.
func (in *initer) purge(o UninstallOptions) error {
	cfg, cfgPath, err := config.Load(o.ConfigPath)
	if err != nil {
		return in.refuse(o, cfgPath, "", err)
	}
	var cfgExists bool
	if _, err := os.Lstat(cfgPath); err == nil {
		cfgExists = true
	}
	explicit := o.ConfigPath != "" || os.Getenv("AGENTBUS_CONFIG") != ""
	dataDir := cfg.DataDirectory
	if explicit && !cfgExists && os.Getenv("AGENTBUS_DATA_DIR") == "" {
		dataDir = ""
	}
	if in.DryRun {
		targets, err := purgeTargets(dataDir)
		if err != nil {
			return in.refuse(o, cfgPath, dataDir, err)
		}
		if len(targets) == 0 && !cfgExists {
			in.sayNothing(explicit, cfgPath)
			return nil
		}
		in.say("would delete:")
		for _, t := range targets {
			in.say("  %s", t)
		}
		if cfgExists {
			in.say("  %s", cfgPath)
		}
		for _, d := range in.emptyDirCandidates(dataDir, cfgPath) {
			in.say("  %s (the directory, if left empty)", d)
		}
		return nil
	}
	if err := in.ensureIdle(o, cfg, cfgPath, dataDir); err != nil {
		return err
	}
	// After the liveness check: opening the bus can create -wal and -shm.
	targets, err := purgeTargets(dataDir)
	if err != nil {
		return in.refuse(o, cfgPath, dataDir, err)
	}
	if len(targets) == 0 && !cfgExists {
		in.sayNothing(explicit, cfgPath)
		return nil
	}
	in.say("this deletes:")
	for _, t := range targets {
		in.say("  %s", t)
	}
	if cfgExists {
		in.say("  %s", cfgPath)
	}
	for _, d := range in.emptyDirCandidates(dataDir, cfgPath) {
		in.say("  %s (the directory, if left empty)", d)
	}
	_, _ = fmt.Fprint(in.out, "Type yes to continue: ")
	line, readErr := bufio.NewReader(o.In).ReadString('\n')
	if readErr != nil && readErr != io.EOF {
		return readErr
	}
	if strings.TrimSpace(line) != "yes" {
		return ErrPurgeCanceled
	}
	// The prompt can wait without limit: a session may have started.
	if err := in.ensureIdle(o, cfg, cfgPath, dataDir); err != nil {
		return err
	}
	if targets, err = purgeTargets(dataDir); err != nil {
		return in.refuse(o, cfgPath, dataDir, err)
	}
	return in.deleteData(dataDir, targets, cfgPath, cfgExists)
}

// emptyDirCandidates lists the directories a real run removes when nothing
// else is left in them: the data directory, then the config file's directory
// when it is a different one. Only existing real directories count.
func (in *initer) emptyDirCandidates(dataDir, cfgPath string) []string {
	var dirs []string
	if dataDir != "" {
		dirs = append(dirs, dataDir)
	}
	if cfgDir := filepath.Dir(cfgPath); filepath.Clean(cfgDir) != filepath.Clean(dataDir) {
		dirs = append(dirs, cfgDir)
	}
	var found []string
	for _, d := range dirs {
		if fi, err := os.Lstat(d); err == nil && fi.IsDir() {
			found = append(found, d)
		}
	}
	return found
}

// sayNothing reports that there is nothing to delete.
func (in *initer) sayNothing(explicit bool, cfgPath string) {
	if explicit {
		in.say("already removed: %s", cfgPath)
	}
	in.say("nothing to remove: no config file or bus data found")
}

// ensureIdle verifies, when the database exists, that no session is live.
func (in *initer) ensureIdle(o UninstallOptions, cfg config.Config, cfgPath, dataDir string) error {
	if dataDir == "" {
		return nil
	}
	_, err := os.Stat(filepath.Join(dataDir, "agentbus.db"))
	switch {
	case err == nil:
		if err := in.checkIdle(cfg); err != nil {
			if errors.Is(err, ErrPurgeRefused) {
				return err
			}
			return in.refuse(o, cfgPath, dataDir, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return in.refuse(o, cfgPath, dataDir, err)
	}
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
		if d := os.Getenv("AGENTBUS_DATA_DIR"); d != "" {
			dataDir = d
		}
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
	// Opening may migrate an older database, as any agentbus command would.
	// That is harmless: the user is about to delete it, and a canceled purge
	// leaves a normally upgraded database.
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
// dir has none. File entries that are directories are not agentbus's.
func purgeTargets(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range entries {
		n := e.Name()
		switch {
		case slices.Contains(dataDirs, n):
			if e.IsDir() {
				found = append(found, filepath.Join(dir, n))
			}
		case slices.Contains(dataFiles, n), isLogRotation(n):
			if !e.IsDir() {
				found = append(found, filepath.Join(dir, n))
			}
		}
	}
	return found, nil
}

// deleteData removes targets, then the config file, then the data and config
// directories when nothing else is in them; a data directory that holds other
// files is kept and they are listed. A path that is already gone is reported,
// not an error, and a symbolic link is never unlinked in place of a directory.
func (in *initer) deleteData(dataDir string, targets []string, cfgPath string, cfgExists bool) error {
	for _, p := range targets {
		if err := in.removePath(p, slices.Contains(dataDirs, filepath.Base(p))); err != nil {
			return err
		}
	}
	if cfgExists {
		if err := in.removePath(cfgPath, false); err != nil {
			return err
		}
	} else {
		in.say("already removed: %s", cfgPath)
	}
	if dataDir != "" {
		if err := in.removeDataDir(dataDir); err != nil {
			return err
		}
	}
	if cfgDir := filepath.Dir(cfgPath); filepath.Clean(cfgDir) != filepath.Clean(dataDir) {
		if fi, err := os.Lstat(cfgDir); err == nil && fi.IsDir() {
			_ = os.Remove(cfgDir) // keep it silently when it is not empty
		}
	}
	return nil
}

// removePath deletes one file (or, when all is set, a directory tree).
func (in *initer) removePath(p string, all bool) error {
	rm := os.Remove
	if all {
		rm = os.RemoveAll
	}
	err := rm(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		in.say("already removed: %s", p)
	case err != nil:
		return err
	default:
		in.say("removed %s", p)
	}
	return nil
}

// removeDataDir removes dir when it is an empty real directory; otherwise it
// lists what it kept.
func (in *initer) removeDataDir(dir string) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		in.say("already removed: %s", dir)
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		in.say("kept %s: it is a symbolic link, not a directory", dir)
		return nil
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
