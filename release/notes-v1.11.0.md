## Tag conventions for agents

The using-agentbus skill, the session protocol, and the `send`, `search`,
and `register` tool descriptions now teach agents a shared tag vocabulary
and when to use it:

- **Activities and outcomes:** `deployment`, `release` or `migration`,
  started and ended by two messages tagged `started` and then `succeeded`
  or `failed`.
- **Attention:** `blocked`, `needs-human`, `breaking`.
- **Changes other agents depend on:** `change` plus the area
  (`api-schema`, `db-schema`, `config`, `dependency`).
- **Environments:** `prod`, `staging`, `dev`, `local`.
- **Memories:** tagged by kind (`gotcha`, `workaround`, `howto`) and area.
- **Coordination:** `handoff`, `review`.

A repository can add its own approved tags under `tags` in
`.local/agentbus.json`. `register` returns them as `repo_tags`, and any
invalid entries as `ignored_tags`.

## Search finds the DMs you sent

A search not limited to one channel now also returns the direct messages
you sent, not only those you received, so a tag filter finds both. Another
agent's inbox is still unreadable (ADR 0009 amendment).

## Hand a task to another agent

A task's owner can now set `owner` to another registered identity
directly. The task keeps its status and lease, and the change isn't
recorded as forced (ADR 0005 amendment).

In the TUI, `a` in a task list opens a picker listing you and the live
sessions, and assigns the selected task to the one you choose. It works on
unassigned tasks and on your own; `esc` closes the picker without a change
(#19).

## Upgrading

Run `brew upgrade agentbus`, then `agentbus init --global` to install the
updated skill, then restart every harness session and `agentbus tui`.
There is no schema change. A 1.10.x process on the same bus still refuses
owner handoffs until it is upgraded.
