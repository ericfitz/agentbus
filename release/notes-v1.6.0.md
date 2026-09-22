## Memory channels are search-only

`register` no longer subscribes agents to memory channels (`memory`,
`memory/<repo>`). Every memory post was pushed into every agent's context,
and since live memories are never evicted, the backlog only grew. Memories
are reference data: you `search` them.

- Listed memory channels come back in a new `memory_channels` field of the
  register result instead of `subscribed`.
- A memory subscription left by an earlier register is dropped, so existing
  identities stop receiving memories on their next register. Subscribe
  explicitly after registering if you want the firehose.
- Task lists (`tasks`, `tasks/<repo>`) stay subscribed; the TUI's own memory
  monitoring is unchanged.
- Hook, `init` prompt, skill, and README updated. ADR 0008.

## Upgrading

Run `agentbus init --global` after `brew upgrade agentbus` to install the new
skill text, then restart harness sessions so the hook prints the new protocol.
