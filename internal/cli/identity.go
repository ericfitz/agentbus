// Package cli implements the status, reset, and identity commands.
package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/procs"
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

// findHarness finds the harness running this process: the first non-shell
// ancestor (ADR 0014). Tests replace it.
var findHarness = func() (procs.Ref, error) { return procs.FindHarness(procs.System, os.Getpid()) }

// SessionIdentity is the identity of the session this process runs for
// (ADR 0017): the live top-level session registered from the same harness
// process, else IdentityName(cwd). IdentityName is only the name a session
// asks for; a second session in the same repository gets a suffixed one.
// It returns "" when this harness has registered no session and the cwd
// name is live under another known harness: that name is provably another
// session's, so a session that has not registered yet does not borrow it.
func SessionIdentity(b *bus.Bus, cwd string, warn io.Writer) string {
	h, herr := findHarness()
	if herr == nil {
		if name, err := b.IdentityForHarness(h); err == nil && name != "" {
			return name
		}
	}
	name := IdentityName(cwd, warn)
	if herr == nil {
		if other, live, err := b.SessionHarness(name); err == nil && live && other.Pid > 0 && other != h {
			return ""
		}
	}
	return name
}

// WaitIdentity is SessionIdentity for a caller with no bus open: `agentbus
// wait` without -as, "" included. If the bus cannot be opened it falls back
// to IdentityName(cwd); the wait itself then reports the open error.
func WaitIdentity(cfg config.Config, cwd string, warn io.Writer) string {
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return IdentityName(cwd, warn)
	}
	defer func() { _ = b.Close() }()
	return SessionIdentity(b, cwd, warn)
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
- Keep a background wait armed so new messages wake you: after your first
  receive, run "agentbus wait -filter @<your-name>" in a background shell.
  When it exits, call receive, handle what concerns you, and start it again.
  Before you end a turn, make sure one is running. Never poll receive from
  model turns to wait. Codex has no idle wake: on Codex, skip the background
  wait and rely on receive at the end of each task.
- Post when you start, finish, or get blocked on a task, and when you change
  something other agents depend on. Post to your project chat channel: any
  subscribed chat channel other than general (for example general/<repo>, or
  one channel shared by related repos). Use general only when you have no
  project channel, or when the message is for agents on unrelated projects.
  Reply to messages addressed to you.
- When a message concerns exactly one agent, send it to the channel
  dm/<that agent's name> instead of a shared channel. Direct messages are
  one-way: answer one by sending to dm/<its sender>, with reply_to set to
  its seq. An idle agent's background wait wakes only for messages
  addressed to it, so to wake an idle agent, send to its dm/ channel; a
  post on a shared channel waits until it next checks. An offline agent
  receives nothing until it comes back online.
- Give every send a subject: a one-line summary of the message. The TUI
  shows it as the message's title, and search matches it.
- Tag what you send so others can find it and triage it without reading
  it: the activity (deployment, release, migration), its outcome (started,
  succeeded, failed), what needs attention (blocked, needs-human, breaking),
  and the environment (prod, staging). A changed interface is change plus
  its area (api-schema, db-schema, config). Reuse the vocabulary in the
  using-agentbus skill and register's repo_tags before inventing a tag.
  With tags carrying the category, keep the body to the facts.
- Search the memory channels register returned before starting unfamiliar work,
  and whenever something you believe should work is not working.
- Post to a memory channel whenever you discover a non-obvious fact that would
  save another agent time. Examples: "tool X does not honor --y; workaround is
  Z"; "the spec for feature A says B, but I verified with <test> that the
  correct behavior is C"; "to accomplish J, I tried K, L, and M, which failed;
  P worked." Use your project memory channel for facts specific to the
  project, and memory for facts useful on any project.
- Use a task list (tasks/<effort>, created with create_channel by whoever
  starts the effort; find one with list_channels and subscribe to it) for
  multi-step work, work shared or handed between agents or sessions, and
  plans that must outlive your session; use chat for everything else.
  Check task_list first,
  claim before you start (task_claim), complete or release when you stop,
  and never force another live agent's task. Details: the using-agentbus
  skill, "Task lists".
- Register returns the other live agents in "others"; call discover only to
  refresh that list before assuming you are the only agent working.
`

// identityLine is what the SessionStart hook prints: the register sentence
// naming the identity, then the protocol.
func identityLine(name string) string {
	return "Agentbus: call the register tool now with the name parameter set to \"" + name + "\",\n" +
		"and pass the \"as\" value it returns on every later Agentbus call. Register\n" +
		"subscribes you to this repository's persistent chat channels (from\n" +
		".local/agentbus.json). Task lists are separate: find one with\n" +
		"list_channels and subscribe to it, or create tasks/<effort> when you\n" +
		"start an effort. Memory channels are not pushed; register returns them\n" +
		"in memory_channels for you to search.\n" +
		protocol
}
