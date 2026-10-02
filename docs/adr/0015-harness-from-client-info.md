# ADR 0015: A session's harness comes from the MCP clientInfo

Status: Accepted 2026-10-02. Decided by the user. Issue #23.

## Context

#23 asked for agents to report their kind (Claude Code, Codex, Grok) and
build, for the UI to use. The original design had `register` accept optional
kind and build fields, filled in by the agent from SessionStart hook text.

## Human decisions (2026-10-02)

1. **The source is the MCP `initialize` handshake's `clientInfo`** (name and
   version), which each harness sends to its own `agentbus mcp` process. The
   server records it on the session at every `register`, so it refreshes each
   session and tracks harness upgrades. No `register` parameters, no hook
   change, no reliance on the agent reporting it.
2. **A subagent registering through its parent's connection inherits the
   parent's harness.** That is correct: it runs in the same harness.
3. **Display now: harness name and version only, in the TUI `h` health view.**
   Per-harness glyphs in the session list and message headers are later,
   separate work.
4. **Model id is out of scope.** `clientInfo` does not carry it.
5. **Agents whose client sends no `clientInfo` still work.** The health view
   shows `unknown` for them.

## Implementation rulings

- **Schema v8.** `sessions` gains `harness` and `harness_version`
  (`TEXT NOT NULL DEFAULT ''`). Existing rows read as unknown until their
  next `register`. A v7 binary refuses the migrated bus, so all hosts upgrade
  and restart together.
- **Refresh at every register**, including a re-register that reuses the
  session's name; a register with no `clientInfo` clears the old values.
- **`discover` and `register`'s `others` return `harness` and
  `harness_version`**, omitted when empty (the issue's acceptance criterion).
- **Cleaned, never rejected.** `clientInfo` is advisory display text, so
  control characters are dropped and each field is cut to 128 bytes; a
  client with odd values still registers.
- **API.** `Bus.RegisterWithClient` takes the two values; `Bus.Register`
  calls it with none. The MCP `register` handler reads them with
  `CallToolRequest.ClientInfo()`.
- **TUI.** The health view lists each agent session as
  `<name> <harness> <version>`; the TUI's own session is skipped.
