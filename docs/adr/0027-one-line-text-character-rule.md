# ADR 0027: one character rule for one-line text fields

Status: Accepted (Eric, 2026-11-04) for the scope; the character set and
the per-field choices under "Implementation choices" are Claude's and
await Eric's review.

## Context

Issue #46 (from the #43 review) found that status text accepted characters
that `unicode.IsControl` does not cover: the line and paragraph separators
U+2028 and U+2029, which some renderers break lines on, and the bidi
formatting characters, such as U+202E RIGHT-TO-LEFT OVERRIDE, which make
text display in a different order from the bytes sent. In the TUI rail, in
`discover` output and in an agent's context window, such text can
impersonate another agent's name or hide what a status really says. Invalid
UTF-8 could also reach the bus through CLI arguments (the MCP path cannot
carry it: JSON decoding replaces bad bytes with U+FFFD).

Commit 83dff46 fixed status text and problem only. The same gap remained in
every other one-line field: identity names, channel names, register
`parent` and `context`, task subjects (control characters only) and message
subjects (CR and LF only).

## Decision (Eric, 2026-11-04)

Every one-line text field follows one character rule (Eric: "fix the follow
up issue", after the #46 report listed the remaining fields). The rule
lives in one place, `lineRule` in `internal/bus/textrule.go`, and each
field's validator calls it.

## The rule

A one-line text field must be valid UTF-8 and must not contain:

| Characters | Code points | Why |
|---|---|---|
| C0 and C1 controls | U+0000-U+001F, U+007F-U+009F | Line breaks, tabs, terminal escapes; NEL (U+0085) is C1 |
| Line and paragraph separators | U+2028, U+2029 | Line breaks in some renderers |
| Bidi embeddings and overrides | U+202A-U+202E | Reorder the displayed text |
| Bidi isolates | U+2066-U+2069 | Reorder the displayed text |
| Bidi marks | U+200E, U+200F, U+061C | Invisible; change the display order around them |

Every other character is allowed, including the other format characters
(Unicode category Cf). The zero-width joiner U+200D is Cf and is what joins
emoji sequences, so refusing all of Cf would refuse ordinary emoji.

## Where it applies

| Field | Validator | On a violation |
|---|---|---|
| Identity name | `NameRule` (`sessions.go`) | validation error |
| Channel name | `ChannelNameRule`, through `NameRule` | validation error |
| Register `parent` | `validateParent` | validation error |
| Register `context` | `validateContext` | validation error |
| Status `text` and `problem` | `validStatusLine` (`status.go`) | validation error |
| Message and memory subject | `normalizeSubject` (`messages.go`; `Send`, `EditMemory`) | validation error |
| Task subject | `validateTaskSubject` (`tasks.go`; create and update) | validation error |
| Client name and version | `cleanClientField` | characters dropped, never refused |

`NameRule` and `ChannelNameRule` also check `.local/agentbus.json`
(`repoconfig`) and `agentbus init`'s name.

Each field keeps its own length limit; the rule covers characters only.

## Implementation choices (Claude, 2026-11-04; for Eric's review)

1. **The character set above.** Not chosen: all of Unicode category Cf
   (refuses ZWJ emoji); controls plus separators only (leaves the bidi
   spoofing open).
2. **Refuse, do not clean**, for every field an agent or user types. The
   caller gets a validation error naming the field and can fix the text;
   silently changing a name or subject would hide what was stored. The one
   exception is clientInfo, which is advisory display text the harness
   supplies, so a register must not fail on it (as before).
3. **Message subjects now refuse tabs and other controls**, not only CR and
   LF. A subject is drawn as one aligned row in the rail and stream.
4. **Names are tightened in place**, with no migration. An identity or
   channel whose stored name already holds one of these characters cannot be
   registered or subscribed under that name again; none is expected, since
   names come from agents and config files that did not use them.

## Not covered

Multi-line fields (message and memory content, task descriptions) keep
their current rules: line breaks are legitimate there. Tags have their own
ASCII-only rule (`validTag` in `tags.go`, ADR 0009), which already excludes
these characters.
