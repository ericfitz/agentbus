package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ericfitz/agentbus/internal/repoconfig"
)

var errNotRepoRoot = errors.New("run this from the repository root (the directory containing .git)")

// Subscribe adds channel to the persistent list in cwd/.local/agentbus.json,
// creating the file when absent. cwd must be a repository root.
func Subscribe(cwd, channel string, out io.Writer) error {
	f, err := openRepoFile(cwd, true)
	if err != nil {
		return err
	}
	list, err := f.AddChannel(channel)
	if err != nil {
		return err
	}
	return printChannels(out, f.Identity, list)
}

// Unsubscribe removes channel from the persistent list in
// cwd/.local/agentbus.json. cwd must be a repository root with the file present.
func Unsubscribe(cwd, channel string, out io.Writer) error {
	f, err := openRepoFile(cwd, false)
	if err != nil {
		return err
	}
	list, err := f.RemoveChannel(channel)
	if err != nil {
		return err
	}
	return printChannels(out, f.Identity, list)
}

// openRepoFile loads cwd/.local/agentbus.json after checking cwd is a
// repository root. With create set, a missing file is created with the
// identity agentbus init would choose (the basename of cwd).
func openRepoFile(cwd string, create bool) (*repoconfig.File, error) {
	if _, err := os.Stat(filepath.Join(cwd, ".git")); err != nil {
		return nil, errNotRepoRoot
	}
	f, err := repoconfig.Load(cwd)
	if os.IsNotExist(err) && create {
		return repoconfig.Create(cwd, filepath.Base(cwd))
	}
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%w; run `agentbus init` first", err)
	}
	return f, err
}

func printChannels(out io.Writer, identity string, list []string) error {
	shown := strings.Join(list, ", ")
	if shown == "" {
		shown = "(none)"
	}
	_, err := fmt.Fprintf(out, "Agentbus: persistent channels for %s: %s\nApplied at the next register in this repository.\n", identity, shown)
	return err
}

// SubscribeTags adds a tag set to the persistent list in
// cwd/.local/agentbus.json ("tag_subscriptions"), creating the file when absent.
func SubscribeTags(cwd string, tags []string, out io.Writer) error {
	f, err := openRepoFile(cwd, true)
	if err != nil {
		return err
	}
	sets, err := f.AddTagSet(tags)
	if err != nil {
		return err
	}
	return printTagSets(out, f.Identity, sets)
}

// UnsubscribeTags removes a tag set from the persistent list.
func UnsubscribeTags(cwd string, tags []string, out io.Writer) error {
	f, err := openRepoFile(cwd, false)
	if err != nil {
		return err
	}
	sets, err := f.RemoveTagSet(tags)
	if err != nil {
		return err
	}
	return printTagSets(out, f.Identity, sets)
}

func printTagSets(out io.Writer, identity string, sets [][]string) error {
	shown := make([]string, len(sets))
	for i, s := range sets {
		shown[i] = strings.Join(s, ",")
	}
	list := strings.Join(shown, " ")
	if list == "" {
		list = "(none)"
	}
	_, err := fmt.Fprintf(out, "Agentbus: persistent tag subscriptions for %s: %s\nApplied at the next register in this repository.\n", identity, list)
	return err
}
