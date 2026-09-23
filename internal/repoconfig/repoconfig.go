// Package repoconfig reads and edits the per-repository Agentbus file,
// .local/agentbus.json, which names the identity to register and the
// channels to subscribe persistently.
package repoconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ericfitz/agentbus/internal/bus"
)

// DefaultChannels is the persistent list when the file has no "channels" key.
var DefaultChannels = []string{"general", "memory", "tasks"}

// File is a parsed .local/agentbus.json. Raw holds every key so writers
// preserve ones they do not understand.
type File struct {
	Path     string
	Identity string
	Raw      map[string]any
}

func pathIn(dir string) string { return filepath.Join(dir, ".local", "agentbus.json") }

// Load reads exactly dir/.local/agentbus.json. os.IsNotExist(err) is true
// when the file is absent.
func Load(dir string) (*File, error) {
	p := pathIn(dir)
	body, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	raw := map[string]any{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	id, _ := raw["identity"].(string)
	if id == "" {
		return nil, fmt.Errorf("%s: missing identity", p)
	}
	if err := bus.NameRule(id); err != nil {
		return nil, fmt.Errorf("%s: identity %q: %w", p, id, err)
	}
	return &File{Path: p, Identity: id, Raw: raw}, nil
}

// Find walks up from dir to the nearest .local/agentbus.json, stopping at the
// nearest .git. It returns (nil, nil) when no file is found. A file that is
// unreadable or malformed, or whose identity fails bus.NameRule, returns an
// error naming the path.
func Find(dir string) (*File, error) {
	for {
		f, err := Load(dir)
		if err == nil {
			return f, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return nil, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, nil
		}
		dir = parent
	}
}

// Channels returns the persistent channel list: DefaultChannels when the key
// is absent, otherwise the deduplicated entries in file order. Entries that
// are not strings or fail bus.ChannelNameRule are returned in bad and omitted.
func (f *File) Channels() (channels []string, bad []string) {
	v, ok := f.Raw["channels"]
	if !ok {
		return append([]string(nil), DefaultChannels...), nil
	}
	list, _ := v.([]any)
	channels = []string{}
	seen := map[string]bool{}
	for _, e := range list {
		s, ok := e.(string)
		if !ok || bus.ChannelNameRule(s) != nil {
			bad = append(bad, fmt.Sprint(e))
			continue
		}
		if !seen[s] {
			seen[s] = true
			channels = append(channels, s)
		}
	}
	return channels, bad
}

// Create writes a new file at dir/.local/agentbus.json with the given identity
// and no channels key, creating .local/ if needed. It fails if the file exists.
func Create(dir, identity string) (*File, error) {
	if err := bus.NameRule(identity); err != nil {
		return nil, fmt.Errorf("identity %q: %w", identity, err)
	}
	f := &File{Path: pathIn(dir), Identity: identity, Raw: map[string]any{"identity": identity}}
	if _, err := os.Stat(f.Path); err == nil {
		return nil, fmt.Errorf("%s already exists", f.Path)
	}
	return f, f.write()
}

// AddChannel appends channel to the file's list (materializing DefaultChannels
// first when the key is absent), dedupes, writes the file, and returns the
// resulting list. channel must pass bus.ChannelNameRule.
func (f *File) AddChannel(channel string) ([]string, error) {
	if err := bus.ChannelNameRule(channel); err != nil {
		return nil, fmt.Errorf("channel %q: %w", channel, err)
	}
	// bus.DMPrefix ("dm/") already fails ChannelNameRule's '/' check above; the
	// bare name "dm" would not, and would otherwise resubscribe (and fail)
	// at every register. Direct-message inboxes are managed by register,
	// not the persistent list.
	if channel == "dm" || strings.HasPrefix(channel, bus.DMPrefix) {
		return nil, fmt.Errorf("channel %q: direct-message channels cannot be added to the persistent list; register manages your inbox", channel)
	}
	list, _ := f.Channels()
	found := false
	for _, c := range list {
		if c == channel {
			found = true
		}
	}
	if !found {
		list = append(list, channel)
	}
	return list, f.setChannels(list)
}

// RemoveChannel removes channel from the list (materializing DefaultChannels
// first when the key is absent), writes the file, and returns the resulting
// list. Removing an absent channel is a no-op that still writes.
func (f *File) RemoveChannel(channel string) ([]string, error) {
	list, _ := f.Channels()
	out := []string{}
	for _, c := range list {
		if c != channel {
			out = append(out, c)
		}
	}
	return out, f.setChannels(out)
}

func (f *File) setChannels(list []string) error {
	arr := make([]any, len(list))
	for i, c := range list {
		arr[i] = c
	}
	f.Raw["channels"] = arr
	return f.write()
}

// TagSubscriptions returns the persistent tag sets under "tag_subscriptions"
// (a list of tag lists), each normalized; entries that are not a list of
// valid tags are returned in bad and omitted. Absent key: none.
func (f *File) TagSubscriptions() (sets [][]string, bad []string) {
	list, _ := f.Raw["tag_subscriptions"].([]any)
	for _, e := range list {
		raw, ok := e.([]any)
		tags := make([]string, 0, len(raw))
		for _, v := range raw {
			s, isStr := v.(string)
			ok = ok && isStr
			tags = append(tags, s)
		}
		norm, err := bus.NormalizeTags(tags)
		if !ok || err != nil || len(norm) == 0 {
			bad = append(bad, fmt.Sprint(e))
			continue
		}
		sets = append(sets, norm)
	}
	return sets, bad
}

// AddTagSet appends a normalized set (idempotent), writes, and returns the list.
func (f *File) AddTagSet(tags []string) ([][]string, error) {
	norm, err := bus.NormalizeTags(tags)
	if err != nil || len(norm) == 0 {
		return nil, fmt.Errorf("tags %q: must be 1-10 tags of 1-20 characters a-z, 0-9, _ or -", tags)
	}
	sets, _ := f.TagSubscriptions()
	if !slices.ContainsFunc(sets, func(s []string) bool { return slices.Equal(s, norm) }) {
		sets = append(sets, norm)
	}
	return sets, f.setTagSets(sets)
}

// RemoveTagSet removes a set (a no-op that still writes when absent).
func (f *File) RemoveTagSet(tags []string) ([][]string, error) {
	norm, err := bus.NormalizeTags(tags)
	if err != nil {
		return nil, err
	}
	sets, _ := f.TagSubscriptions()
	sets = slices.DeleteFunc(sets, func(s []string) bool { return slices.Equal(s, norm) })
	if sets == nil {
		sets = [][]string{}
	}
	return sets, f.setTagSets(sets)
}

func (f *File) setTagSets(sets [][]string) error {
	arr := make([]any, len(sets))
	for i, s := range sets {
		inner := make([]any, len(s))
		for j, t := range s {
			inner[j] = t
		}
		arr[i] = inner
	}
	f.Raw["tag_subscriptions"] = arr
	return f.write()
}

func (f *File) write() error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f.Raw, "", "  ")
	if err != nil {
		return err
	}
	// Write to a sibling temp file and rename it into place so a concurrent
	// reader (another persistent edit, or a register) never sees a truncated
	// file. 0o644 matches what agentbus init writes; the file holds no secrets.
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), ".agentbus-*.json")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), f.Path)
}
