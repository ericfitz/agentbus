package tui

import (
	"slices"
	"strings"
	"testing"
)

func TestParseSearchInput(t *testing.T) {
	tests := []struct {
		in      string
		query   string
		tags    []string
		errPart string
	}{
		{"tag:foo tag:bar keyword", "keyword", []string{"bar", "foo"}, ""},
		{"keyword tag:foo", "keyword", []string{"foo"}, ""},
		{"tag:foo tag:FOO x", "x", []string{"foo"}, ""},
		{"TAG:Foo x", "x", []string{"foo"}, ""},
		{"tag:env:* deploy", "deploy", []string{"env:*"}, ""},
		{`"tag:foo" bar`, "tag:foo bar", nil, ""},
		{"tag:deployment tag:failed", "", []string{"deployment", "failed"}, ""},
		{"tag: x", "", nil, "tag: needs a tag"},
		{"tag:bad_tag x", "", nil, "bad_tag"},
		{"a  b", "a b", nil, ""},
	}
	for _, tc := range tests {
		got, err := parseSearchInput(tc.in)
		if tc.errPart != "" {
			if err == nil || !strings.Contains(err.Error(), tc.errPart) {
				t.Errorf("%q: err=%v, want containing %q", tc.in, err, tc.errPart)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", tc.in, err)
			continue
		}
		if got.Query != tc.query || !slices.Equal(got.Tags, tc.tags) {
			t.Errorf("%q: got %q %v, want %q %v", tc.in, got.Query, got.Tags, tc.query, tc.tags)
		}
	}
}
