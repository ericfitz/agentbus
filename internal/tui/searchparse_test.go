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
		{`""`, "", nil, ""},
		{`foo "" bar`, "foo bar", nil, ""},
		{`"tag:foo`, `"tag:foo`, nil, ""},
		{"\u0130ag:foo", "\u0130ag:foo", nil, ""},
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

func TestParseSearchInputScopeTokens(t *testing.T) {
	tests := []struct {
		in      string
		query   string
		kinds   []string
		channel string
		tags    []string
		errPart string
	}{
		{in: "in:chat foo", query: "foo", kinds: []string{"chat"}},
		{in: "IN:Tasks in:chat", kinds: []string{"tasks", "chat"}},
		{in: "in:chat in:chat x", query: "x", kinds: []string{"chat"}},
		{in: "channel:general x", query: "x", channel: "general"},
		{in: "CHANNEL:general x", query: "x", channel: "general"},
		{in: `"in:chat" x`, query: "in:chat x"},
		{in: `"channel:general" x`, query: "channel:general x"},
		{in: "in:memories tag:deployment x", query: "x", kinds: []string{"memories"}, tags: []string{"deployment"}},
		{in: "in: x", errPart: "in:"},
		{in: "in:chats x", errPart: "chats"},
		{in: "in:memory x", errPart: "memory"},
		{in: "channel: x", errPart: "channel:"},
		{in: "channel:a channel:b x", errPart: "more than once"},
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
		if got.Query != tc.query || got.Channel != tc.channel || !slices.Equal(got.Kinds, tc.kinds) || !slices.Equal(got.Tags, tc.tags) {
			t.Errorf("%q: got %+v", tc.in, got)
		}
	}
}
