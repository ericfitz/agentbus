// Package repoconfig reads and edits the per-repository Agentbus file,
// .local/agentbus.json, which names the identity to register and the
// channels to subscribe persistently.
package repoconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ericfitz/agentbus/internal/bus"
)

// DefaultChannels is the persistent list when the file has no "channels" key.
var DefaultChannels = []string{"general", "memory"}

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
// are not strings or fail bus.NameRule are returned in bad and omitted.
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
		if !ok || bus.NameRule(s) != nil {
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
