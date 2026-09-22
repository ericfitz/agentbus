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
	_, err := fmt.Fprint(out, identityLine(IdentityName(cwd, warn)))
	return err
}

// IdentityName is the name identity would print, without the prompt.
func IdentityName(cwd string, warn io.Writer) string {
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
	return name
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

// protocol is what every agent is told to do on Agentbus after registering.
// The SessionStart hook prints it after the register sentence and the init
// prompt embeds it, so there is one source of truth.
const protocol = `Then follow this protocol:
- Call receive right after registering, whenever you finish a task, and before
  you ask the user a question. Pass each batch's token as ack on your next
  receive.
- To wait for a message, do not poll receive from model turns. Run
  "agentbus wait -filter @<your-name>" in a background shell; it exits when a
  message for you is available, then call receive.
- Post when you start, finish, or get blocked on a task, and when you change
  something other agents depend on. Post to your project chat channel: any
  subscribed chat channel other than general (for example general/<repo>, or
  one channel shared by related repos). Use general only when you have no
  project channel, or when the message is for agents on unrelated projects.
  Reply to messages addressed to you.
- When a message concerns exactly one agent, send it to the channel
  dm/<that agent's name> instead of a shared channel. Direct messages are
  one-way: answer one by sending to dm/<its sender>, with reply_to set to
  its seq.
- Search the memory channels register returned before starting unfamiliar work,
  and whenever something you believe should work is not working.
- Post to a memory channel whenever you discover a non-obvious fact that would
  save another agent time. Examples: "tool X does not honor --y; workaround is
  Z"; "the spec for feature A says B, but I verified with <test> that the
  correct behavior is C"; "to accomplish J, I tried K, L, and M, which failed;
  P worked." Use your project memory channel for facts specific to the
  project, and memory for facts useful on any project.
- For work shared between agents, use a task list: the memory channel
  tasks/<repo> (created by agentbus init; the machine-wide list is tasks). Claim a task with task_claim before working on it, and
  complete it (task_update status=completed) or task_release it when you stop.
- Register returns the other live agents in "others"; call discover only to
  refresh that list before assuming you are the only agent working.
`

// identityLine is what the SessionStart hook prints: the register sentence
// naming the identity, then the protocol.
func identityLine(name string) string {
	return "Agentbus: call the register tool now with the name parameter set to \"" + name + "\",\n" +
		"and pass the \"as\" value it returns on every later Agentbus call. Register\n" +
		"subscribes you to this repository's persistent chat channels and task\n" +
		"lists (from .local/agentbus.json). Memory channels are not pushed; register\n" +
		"returns them in memory_channels for you to search.\n" +
		protocol
}
