# ADR 0025: background wait everywhere, park, and session names that never collide with the human

Status: Accepted 2026-10-22. Supersedes ADR 0021 (watch under Monitor) and
amends ADR 0012 (idle wake filter), ADR 0017 (identity from the working
directory) and ADR 0022 (what a process remembers). The decisions under
"Decisions (human)" were made by Eric on 2026-10-22, from a Claude-usage
review relayed on dm/agentbus (seqs 1324, 1326) and four questions answered
in session. The rulings under "Rulings (drafted by Claude)" are builder's
calls and may be revisited.

## Context

- **Idle wakes that carry no message.** On Claude Code the idle wake was
  `agentbus watch` under Monitor (ADR 0021). Monitor kills it after at most
  30 minutes, and every expiry costs a model turn to re-arm it: 475 Monitor
  calls across Eric's sessions in 14 days. A background `agentbus wait` under
  Bash `run_in_background` has no timeout on Claude Code 2.1.307 (observed
  alive for more than 10 minutes) and wakes the session only when it exits on
  a message.
- **Sessions that have finished.** A session that has written its handoff
  still wakes for every subscribed message.
- **Name collision.** `agentbus tui` registers as the OS user name. A session
  started in the home directory derived its name from the directory, which is
  the same name. One such session registered as `efitz`, inherited the TUI's
  subscriptions (every channel, memory included) and acked Eric's messages
  1318-1323.
- **Several names per session.** `register` from one MCP process accepted
  three names (`efitz`, `claude-usage`, `claude`). ADR 0022's restore keeps
  every one of them alive while the process runs.
- **Subagent hook cost.** `agentbus subagent-hook` adds about 600 characters
  to every dispatch (1,200+ dispatches in 14 days).

## Decisions (human)

1. **Background `agentbus wait` on Claude Code**, the same as Grok. Codex
   still has no idle wake. The protocol text, the Stop hook reason and the
   using-agentbus skill have one branch for every harness except Codex.
2. **Every subscribed message wakes an idle agent.** The protocol and the
   hook text no longer tell agents to pass `-filter @<name>`. Messages on
   shared channels, direct messages and tag-subscription matches all wake it.
3. **Park.** `agentbus park` puts a session into a state where only its
   direct messages wake it and the Stop hook is silent except for direct
   messages. `/session:handoff` calls it at session end. These end it:
   `agentbus unpark` run by a `UserPromptSubmit` hook, `register`, any MCP
   tool call other than `receive`, and an explicit `agentbus unpark`.
4. **Home-directory names.** When a session name is derived from a directory
   (cwd or git root) that is the user's home directory, the name is
   `<user>-home`, for example `efitz-home`. `<user>` is the name the TUI
   defaults to. An explicit identity (`.local/agentbus.json`) is never
   rewritten. agentbus2 adopts the same rule (its decisions 15-16).
5. **Register guard.** `register` over MCP refuses the configured `tui_name`
   with an error telling the agent to pick another name. The TUI's own
   register is not affected.
6. **One top-level name per process.** A top-level `register` under a new name
   drops the process's other top-level session rows. Subagent rows
   (`parent/name`) stay. Subscriptions and cursors stay, so an old name
   resumes if it is registered again.
7. **Agents never receive memory channels.** Amends ADR 0008, which let an
   agent subscribe to a memory channel explicitly. `subscribe` (MCP and CLI)
   refuses a memory channel with a message to use search. The TUI, the bus
   observer, is the one exception. `register` over MCP drops all of the
   agent's memory subscriptions, not only those for channels the repo
   config lists. `receive`, `wait`, `watch` and the Stop hook ignore any
   memory subscription an agent still has. Task lists (`tasks/...`) are
   memory-kind but stay pushed (ADR 0008 decision 2).

## Rulings (drafted by Claude)

- **`agentbus watch` stays** as a command, but nothing recommends it any more.
  Removing it would break scripts, and the next release already breaks
  scripts that pass stray arguments (8d61684).
- **Park state is a file**, `<data dir>/park/<identity>`, so the database
  schema does not change. Only top-level identities can be parked: a name
  with `/` is rejected. `park` on a parked identity and `unpark` on an
  unparked one are no-ops that succeed. `register` clears the file, so a
  park left behind by a session that died never affects the next session
  under that name.
- **A running wait sees park changes.** `wait` and `watch` check the park
  file on every poll. While the session is parked they skip messages
  outside `dm/<as>`. A message skipped this way does not wake that wait
  later. The session picks it up at its next `receive`, which an unparked
  session makes at the end of its next turn.
- **Park and an explicit `-filter`.** While parked, a message must be a
  direct message and must also pass any `-filter`. When unparked, `-filter`
  works as before (ADR 0012).
- **Unpark hook.** `agentbus unpark` without `-as` resolves the session the
  same way the Stop hook does (ADR 0017). It never prints anything and
  always exits 0, so a prompt is never blocked. `agentbus init --global`
  installs it as a `UserPromptSubmit` hook for Claude Code, Codex (0.163.0
  ships the event) and Grok.
- **Home check.** The directory the name came from and `os.UserHomeDir()`
  are compared after `filepath.EvalSymlinks` (on Windows the comparison
  ignores case). If either cannot be resolved, the name is not rewritten.
- **Register guard placement.** The guard is in the MCP server's register
  handler: the TUI calls `bus.Register` directly and keeps its name. The
  error is a validation error naming `tui_name`.
- **Rename and restore.** A top-level register under a new name drops the
  process's other top-level rows inside the same transaction, and removes
  them from the in-memory restore set (ADR 0022) so a heartbeat does not put
  them back. Re-registering the same name changes nothing, and neither does
  a subagent register.
- **Subagent hook text** is the register call (parent and suggested name),
  a pointer to the using-agentbus skill, and the sentence telling the
  subagent not to start a wait or watch.

## Consequences

- An idle Claude Code session takes a model turn only when a message
  arrives. There are no window-expiry re-arms.
- Agents wake for more messages than before (decision 2). Park is the way to
  narrow it to direct messages.
- Each host must run `agentbus init --global` again to install the
  `UserPromptSubmit` hook. Release notes must say that the protocol no
  longer uses `-filter @<name>` and that `watch` is deprecated.
