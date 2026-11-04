package bus

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// unsafeLineRune reports whether r must not appear in one-line display
// text: C0/C1 controls, the line and paragraph separators U+2028/U+2029,
// and the bidi formatting characters (embeddings, overrides, isolates and
// marks), which can make text display differently from what was sent.
// Other format characters, such as the zero-width joiner in emoji
// sequences, are allowed.
func unsafeLineRune(r rune) bool {
	switch {
	case unicode.IsControl(r),
		r == '\u2028', r == '\u2029',
		r >= '\u202A' && r <= '\u202E',
		r >= '\u2066' && r <= '\u2069',
		r == '\u200E', r == '\u200F', r == '\u061C':
		return true
	}
	return false
}

// lineRule is the character rule for every one-line text field (names,
// channel names, parent, context, status text and problem, message and
// task subjects): valid UTF-8 without an unsafeLineRune. The error is a
// plain predicate without a subject, so callers name their own field.
func lineRule(s string) error {
	if !utf8.ValidString(s) {
		return errors.New("must be valid UTF-8")
	}
	if strings.IndexFunc(s, unsafeLineRune) >= 0 {
		return errors.New("must be a single line without control, line separator or bidi formatting characters")
	}
	return nil
}
