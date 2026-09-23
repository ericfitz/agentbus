package tui

import (
	"cmp"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
)

// tagPanePrefix names a tag set's rail entry: "tags:" plus the sorted set
// joined by commas. bus channel names never contain ':' in that position
// with this prefix (see bus.ChannelNameRule; the list is synthetic anyway).
const tagPanePrefix = "tags:"

const tagReadOnlyToast = "tag views are read-only; select a channel to post"

func isTagPane(ch string) bool { return strings.HasPrefix(ch, tagPanePrefix) }

func tagPaneSet(ch string) []string { return strings.Split(strings.TrimPrefix(ch, tagPanePrefix), ",") }

// tagPaneMsgs is the loaded messages of every chat channel in the rail
// that carry all of the pane's tags, ascending by seq.
func (m *Model) tagPaneMsgs(ch string) []bus.Message {
	set := tagPaneSet(ch)
	var out []bus.Message
	for _, c := range m.channels {
		if c.Kind != "ordinary" {
			continue
		}
		for _, x := range m.msgs[c.Name] {
			all := true
			for _, t := range set {
				if !slices.Contains(x.Tags, t) {
					all = false
				}
			}
			if all {
				out = append(out, x)
			}
		}
	}
	slices.SortFunc(out, func(a, b bus.Message) int { return cmp.Compare(a.Seq, b.Seq) })
	return out
}

// splitTags splits a comma-separated tag list, trimming space around each
// tag so typing "a, b" (the natural style) does not fail NormalizeTags,
// which rejects a tag carrying its own leading/trailing space (ADR 0009).
func splitTags(v string) []string {
	parts := strings.Split(v, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

// tagPrompt asks for a comma-separated tag set and subscribes to it; the
// status refresh that follows adds it to the rail.
func (m *Model) tagPrompt() tea.Cmd {
	return m.openPrompt("tags <tag>[,<tag>...]", func(m *Model, v string) tea.Cmd {
		c := m.c
		tags := splitTags(v)
		return func() tea.Msg { return subscribedMsg{channel: tagPanePrefix + v, err: c.b.SubscribeTags(c.as, tags)} }
	})
}
