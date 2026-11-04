package bus

import (
	"strings"
	"testing"
)

// spoofRunes are the one-line-text characters #46 rejects beyond C0/C1
// controls: line and paragraph separators, bidi embeddings, overrides,
// isolates and marks; plus an invalid UTF-8 byte.
var spoofRunes = []string{"\u2028", "\u2029", "\u202a", "\u202e", "\u2066", "\u2069", "\u200e", "\u200f", "\u061c", "\xff"}

// zwjEmoji is a format character that stays allowed: emoji sequences use it.
const zwjEmoji = "\U0001F469\u200d\U0001F4BB"

func TestLineRule(t *testing.T) {
	for _, ok := range []string{"", "plain", "naïve café", zwjEmoji, "tab-free"} {
		if err := lineRule(ok); err != nil {
			t.Errorf("lineRule(%q): %v", ok, err)
		}
	}
	for _, r := range append(spoofRunes, "\x00", "\t", "\n", "\x1b", "\u0085") {
		if err := lineRule("a" + r + "b"); err == nil {
			t.Errorf("lineRule(%q) accepted", r)
		}
	}
	if err := lineRule("a\xffb"); err == nil || err.Error() != "must be valid UTF-8" {
		t.Errorf("invalid UTF-8: %v", err)
	}
}

// Every one-line field that the rule now covers refuses each spoofing
// character, and keeps accepting a ZWJ emoji where a free-text field
// allows it (#46 follow-up).
func TestOneLineFieldsRejectSpoofRunes(t *testing.T) {
	b, sam, _ := setupTwo(t)
	taskList(t, b)
	for _, r := range spoofRunes {
		bad := "a" + r + "b"
		if err := NameRule(bad); err == nil {
			t.Errorf("NameRule(%q) accepted", bad)
		}
		if err := ChannelNameRule("tasks/" + bad); err == nil {
			t.Errorf("ChannelNameRule(tasks/%q) accepted", bad)
		}
		if _, err := b.Register(bad, "", "", true); err == nil {
			t.Errorf("Register name %q accepted", bad)
		}
		_, err := b.Register("impl", bad, "", true)
		wantCode(t, err, "validation")
		_, err = b.Register("Kim", "", bad, true)
		wantCode(t, err, "validation")
		_, err = b.Send(sam, SendInput{Channel: "dev", Subject: bad, Content: "x"})
		wantCode(t, err, "validation")
		_, err = b.TaskCreate(sam, TaskCreateInput{Channel: "tasks/work", Subject: bad})
		wantCode(t, err, "validation")
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Subject: "ship " + zwjEmoji, Content: "x"}); err != nil {
		t.Errorf("ZWJ emoji subject: %v", err)
	}
	if _, err := b.TaskCreate(sam, TaskCreateInput{Channel: "tasks/work", Subject: "ship " + zwjEmoji}); err != nil {
		t.Errorf("ZWJ emoji task subject: %v", err)
	}
	// A subject with a tab is refused too: it is a control character and
	// the rail and stream draw a subject as one aligned row.
	_, err := b.Send(sam, SendInput{Channel: "dev", Subject: "a\tb", Content: "x"})
	wantCode(t, err, "validation")
}

// clientInfo is advisory display text: spoofing characters and invalid
// UTF-8 are dropped, never a reason to refuse a register.
func TestCleanClientFieldDropsSpoofRunes(t *testing.T) {
	for _, r := range spoofRunes {
		if got := cleanClientField("claude" + r + "-code"); got != "claude-code" {
			t.Errorf("cleanClientField with %q = %q", r, got)
		}
	}
	if got := cleanClientField(" v" + zwjEmoji + " "); got != "v"+zwjEmoji {
		t.Errorf("ZWJ emoji = %q", got)
	}
	if got := cleanClientField(strings.Repeat("é", 100)); len(got) > 128 || !strings.HasPrefix(got, "é") {
		t.Errorf("long field = %q", got)
	}
}
