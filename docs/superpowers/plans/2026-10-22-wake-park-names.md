# Plan: background wait, park, memory never pushed, session names (ADR 0025)

Design: `docs/adr/0025-wake-park-and-session-names.md`. Its decisions are frozen.
If you find a gap, stop and report it; don't make a design call yourself.

Rules for every task:
- Write a failing test first (TDD), and for each bug show the test failing before the fix and passing after.
- Done gate: `make verify`. Show its tail in the report.
- Commit straight to `main` (no branch, no PR), with a conventional-commit message that names ADR 0025.
- Stage only the task's files.
- Use American English.
- Use `rg` with an explicit path.
- Reject unknown inputs, and test the rejection.
- New CLI commands reject positional args the same way 8d61684 does.

## Task 1: names for home-directory sessions, and the register guard (decisions 4, 5)

- `internal/cli/identity.go` `IdentityName`: when the name comes from a directory (the cwd basename fallback, or `gitBaseOr`'s git root), compare that directory with `os.UserHomeDir()`. Run both through `filepath.EvalSymlinks` first; on Windows, compare case-insensitively. If they match, the name is `<user>-home`. An identity from `.local/agentbus.json` is never rewritten. If either path fails to resolve, don't rewrite.
- `<user>` must match the TUI's default name. Export the sanitizing logic of `config.defaultTUIName`, for example `config.DefaultTUIName()`, and reuse it. Add seams (package vars) for the home dir and user name so tests can set them.
- Tests to cover:
  - cwd == home with no repo gives `<user>-home`.
  - Git root == home (a dotfiles repo) also gives `<user>-home`.
  - A symlinked home is detected.
  - A subdirectory of home that isn't a repo keeps its own basename.
  - An explicit identity file in home is not rewritten.
- MCP register handler (`internal/mcpserver/server.go`): refuse `name == cfg.TUIName` with a validation error. Suggested text: `name "<n>" is the human TUI identity (tui_name); register under another name, e.g. "<n>-home"`. Include a test. The TUI's `bus.Register` path must keep working; test that too.
- Docs: wherever README describes how the identity name is derived, add the rule.

## Task 2: one top-level name per process (decision 6)

- `internal/bus/sessions.go` `RegisterWithClient`: when `parent == ""`, inside the same transaction, delete the other rows with `owner = b.owner` that have no `/` in `sender` and aren't the new display name. After commit, remove them from `b.remembered` (ADR 0022) so `Heartbeat` doesn't restore them.
- Leave the process's subagent rows (`parent/name`) alone, and leave all subscriptions alone.
- Tests:
  - Register A then B on one Bus: only B is live, and a Heartbeat doesn't bring A back.
  - Re-register A then A: still one row.
  - A subagent register keeps the parent's row.
  - Register A, then B, then A again: A comes back with its old cursors.
- Amend ADR 0022 with a short note pointing at ADR 0025 decision 6.

## Task 3: agents never receive memory channels (decision 7)

- `bus.Subscribe`: refuse a channel whose kind is memory and that isn't a task list (`IsTaskChannel`), unless `b.isObserver(as)`. Use a validation error: `memory channels are searched, not pushed; use search (ADR 0025)`.
  - This covers the MCP `subscribe` tool and the `agentbus subscribe` CLI. Check that the CLI path goes through `bus.Subscribe`.
  - Check what `subscribe persistent=true` does with a memory channel in `.local/agentbus.json`: refuse it too, and don't write it to the file.
- The MCP register handler drops every memory, non-task subscription the identity holds, not only the configured ones.
- `peek`/receive (`internal/bus/receive.go`, `wait.go`): a non-observer never gets messages from memory, non-task channels it is still subscribed to. If the cheapest way to do that is deleting the leftover subscription during receive, do it that way.
  - This also keeps them out of `wait`, `watch` and the Stop hook, which all use `peek`.
- Tests:
  - Subscribe to `memory/x` is refused.
  - Subscribe to `tasks/x` still works.
  - The observer (TUI) can still subscribe to a memory channel and receive from it.
  - A leftover memory subscription (inserted directly) delivers nothing to receive and doesn't wake Wait.
  - Register drops an unconfigured memory subscription.
- Update the ADR 0008 header with "Amended by ADR 0025". Update the MCP tool descriptions for subscribe and register, and the skill/README text that says an agent may subscribe to memories.

## Task 4: `agentbus park` / `agentbus unpark` (decision 3)

- Park state is the file `<data dir>/park/<identity>`, written atomically. Add bus methods `Park(as)`, `Unpark(as)` and `Parked(as) bool`. They reject a name containing `/` or failing `NameRule`.
- Second runs must be no-ops: park twice succeeds and leaves one file; unpark when not parked succeeds.
- `register` (the bus `RegisterWithClient`, so the TUI path is covered too) clears the park file for the display name. That way a park left behind by a dead session never lasts.
- `agentbus reset` clears the park directory.
- CLI `agentbus park [-as name]` and `agentbus unpark [-as name]`:
  - Without `-as`, resolve the name with `WaitIdentity`. If that gives an empty name, `park` exits 2 with an actionable message.
  - `unpark` (the hook) never prints anything and always exits 0.
  - Both reject positional args.
  - Add both to help and README.
- `wait` and `watch` (`internal/cli/wait.go`, `watch.go`): wrap the match predicate so that while `Parked(as)` (checked per poll or per message), only messages on `dm/<as>` that also pass any `-filter` are accepted.
- `StopHook`: while parked, keep only `dm/<as>` messages. If none remain, print nothing.
- MCP server: call `Unpark(as)` on register, and on every tool call except `receive`, before dispatch, for the call's `as`. Find the one central place to do it. Ignore errors (log them).
- `init --global`: install `agentbus unpark` as a `UserPromptSubmit` hook in Claude Code (`settings.json`), Codex (`hooks.json`) and Grok (`hooks/agentbus.json`), using the same `in.hook` writer as Stop.
  - Add init tests covering a second `init` run: idempotent, no duplicate entries.
- Tests: park/unpark state transitions, a parked wait ignoring a channel message but waking on a DM, the Stop hook silent while parked unless there's a DM, unpark on register and on a non-receive MCP tool, and a receive that does not unpark.

## Task 5: one wake text for every harness, and a shorter subagent hook (decisions 1, 2)

- `identity.go` `protocol`: one bullet.
  - On every harness except Codex, run `agentbus wait` in a background shell (no `-filter`). When it exits on a message, call receive, handle the message, and start it again. Exit 3 or 4: do nothing.
  - Codex has no idle wake.
  - Before you end a session with a handoff, run `agentbus park`.
  - Rewrite the DM bullet: every subscribed message wakes an idle agent, and only DMs wake a parked one.
- `stophook.go`: one tail with no `-filter`: "Then, except on Codex, make sure a background `agentbus wait -as X` is running ...". Remove the `SessionClient` branch, and remove `claudeCodeClient` if nothing else uses it.
- The `receive` tool description in server.go: drop the Monitor/watch sentence.
- `subagenthook.go`: shorten the text to the register call (parent and name), "then follow the using-agentbus skill", and "do not start `agentbus wait` or `watch`; use receive". Aim for under 300 characters.
- The using-agentbus skill (`internal/cli/skills/using-agentbus/SKILL.md`) and README:
  - Background wait on Claude Code.
  - Mark `watch` as deprecated (still available).
  - Describe park.
- ADR 0021: set Status to "Superseded by ADR 0025". Add a short amendment note to ADR 0012 and ADR 0017.
- Update tests that assert the old texts.
