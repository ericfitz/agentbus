package tui

import (
	"errors"
	"strings"

	"github.com/ericfitz/agentbus/internal/bus"
)

// searchSpec is a parsed search line: free text plus filters.
type searchSpec struct {
	Query string
	Tags  []string
}

// searchPrefixes maps a case-insensitive word prefix to the function that
// folds the word's remainder into the spec. New filters (in:, channel:) add rows.
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
}

// parseSearchInput splits the line on whitespace. A word with a known prefix
// is a filter; a word wrapped in double quotes is text with the quotes
// stripped (so a literal "tag:x" can be searched for); anything else is text.
func parseSearchInput(line string) (searchSpec, error) {
	var spec searchSpec
	var text []string
words:
	for _, w := range strings.Fields(line) {
		if len(w) >= 2 && w[0] == '"' && w[len(w)-1] == '"' {
			text = append(text, w[1:len(w)-1])
			continue
		}
		lw := strings.ToLower(w)
		for _, p := range searchPrefixes {
			if strings.HasPrefix(lw, p.prefix) {
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
