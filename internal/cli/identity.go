// Package cli implements the status, reset, and identity commands.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ericfitz/agentbus/internal/repoconfig"
)

// Identity prints the registration prompt for cwd: the identity in the
// nearest .local/agentbus.json walking up, else the basename of the nearest
// git repository root, else the basename of cwd. The walk stops at the
// nearest .git even if no identity file was found there, so a nested
// repository reports its own (inner) name rather than an enclosing repo's.
// A malformed or unreadable identity file, or one whose identity fails the
// name rule, is reported to stderr and treated as absent.
func Identity(cwd string, out io.Writer) error {
	return identity(cwd, out, os.Stderr)
}

func identity(cwd string, out, warn io.Writer) error {
	name := filepath.Base(cwd)
	f, err := repoconfig.Find(cwd)
	switch {
	case err != nil:
		_, _ = fmt.Fprintf(warn, "agentbus: %v\n", err)
		name = gitBaseOr(cwd, name)
	case f != nil:
		name = f.Identity
		if _, bad := f.Channels(); len(bad) > 0 {
			_, _ = fmt.Fprintf(warn, "agentbus: %s: ignoring invalid channels %q\n", f.Path, bad)
		}
	default:
		name = gitBaseOr(cwd, name)
	}
	_, err = fmt.Fprint(out, identityLine(name))
	return err
}

// gitBaseOr returns the basename of the nearest directory at or above cwd
// that contains .git, or fallback when there is none.
func gitBaseOr(cwd, fallback string) string {
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return filepath.Base(dir)
		}
		if filepath.Dir(dir) == dir {
			return fallback
		}
	}
}

// Protocol is what every agent is told to do on Agentbus after registering.
// The SessionStart hook prints it after the register sentence and the init
// prompt embeds it, so there is one source of truth.
const Protocol = `Then follow this protocol:
- Call receive right after registering, whenever you finish a task, and before
  you ask the user a question. Pass each batch's token as ack on your next
  receive.
- Post to your subscribed chat channel when you start, finish, or get blocked
  on a task, and when you change something other agents depend on. If you are
  subscribed to more than one chat channel, post to the one most relevant to
  the message. Reply to messages addressed to you.
- Search all your subscribed memory channels before starting unfamiliar work,
  and whenever something you believe should work is not working.
- Post to a memory channel whenever you discover a non-obvious fact that would
  save another agent time. Examples: "tool X does not honor --y; workaround is
  Z"; "the spec for feature A says B, but I verified with <test> that the
  correct behavior is C"; "to accomplish J, I tried K, L, and M, which failed;
  P worked."
- Call discover before assuming you are the only agent working.
`

// identityLine is what the SessionStart hook prints: the register sentence
// naming the identity, then the protocol.
func identityLine(name string) string {
	return "Agentbus: call the register tool now with the name parameter set to \"" + name + "\",\n" +
		"and pass the \"as\" value it returns on every later Agentbus call. Register\n" +
		"subscribes you to this repository's persistent channels (from\n" +
		".local/agentbus.json; default: general for chat, memory for memories).\n" +
		Protocol
}
