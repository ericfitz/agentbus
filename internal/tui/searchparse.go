package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ericfitz/agentbus/internal/bus"
)

// searchSpec is a parsed search line: free text plus filters.
type searchSpec struct {
	Query   string
	Tags    []string
	Kinds   []string // in: values, lowercased, in typed order
	Channel string   // channel: value, as typed
}

// searchPrefixes maps a case-insensitive word prefix to the function that
// folds the word's remainder into the spec. New filters add rows.
var searchPrefixes = []struct {
	prefix string
	apply  func(*searchSpec, string) error
}{
	{"tag:", func(s *searchSpec, v string) error {
		if v == "" {
			return errors.New("tag: needs a tag, e.g. tag:deployment")
		}
		s.Tags = append(s.Tags, v)
		return nil
	}},
	{"in:", func(s *searchSpec, v string) error {
		k := strings.ToLower(v)
		if !slices.Contains(bus.SearchKinds, k) {
			return fmt.Errorf("in: needs one of %s, got %q", strings.Join(bus.SearchKinds, ", "), v)
		}
		if !slices.Contains(s.Kinds, k) {
			s.Kinds = append(s.Kinds, k)
		}
		return nil
	}},
	{"channel:", func(s *searchSpec, v string) error {
		if v == "" {
			return errors.New("channel: needs a channel name, e.g. channel:general")
		}
		if s.Channel != "" {
			return errors.New("channel: given more than once")
		}
		s.Channel = v
		return nil
	}},
}

// parseSearchInput splits the line on whitespace. A word with a known prefix
// is a filter; a word wrapped in double quotes is text with the quotes
// stripped (so a literal "tag:x" can be searched for; an empty "" adds
// nothing); anything else is text.
func parseSearchInput(line string) (searchSpec, error) {
	var spec searchSpec
	var text []string
words:
	for _, w := range strings.Fields(line) {
		if len(w) >= 2 && w[0] == '"' && w[len(w)-1] == '"' {
			if len(w) > 2 {
				text = append(text, w[1:len(w)-1])
			}
			continue
		}
		for _, p := range searchPrefixes {
			if len(w) >= len(p.prefix) && strings.EqualFold(w[:len(p.prefix)], p.prefix) {
				if err := p.apply(&spec, w[len(p.prefix):]); err != nil {
					return searchSpec{}, err
				}
				continue words
			}
		}
		text = append(text, w)
	}
	if len(spec.Tags) > 0 {
		tags, err := bus.NormalizeTagPatterns(spec.Tags)
		if err != nil {
			return searchSpec{}, err
		}
		spec.Tags = tags
	}
	spec.Query = strings.Join(text, " ")
	return spec, nil
}
