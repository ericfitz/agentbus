# Per-effort task channels and inactivity expiry

Date: 2026-09-27. Decisions: ADR 0013 (all decided by the user).

## Goal

Task lists stop being per-repository or per-agent. A task list is one
channel per effort (`tasks/aws-eip-logging`), created by whoever starts the
effort and removed once the effort goes quiet. Memories that no agent has
fetched for a long time are removed as well. Every inactivity period has a
default and can be changed in `config.json`.

## 1. Task channels

### Creation and discovery

- An agent starting an effort creates `tasks/<effort>` with `create_channel`
  and subscribes to it. Other agents find task lists with `list_channels`
  and subscribe to the ones they work on.
- `task_create` on a missing channel still fails `not_found`, so a typo
  cannot create a stray list.
- The machine-wide `tasks` list is no longer a default. `bus.DefaultChannels`
  and `repoconfig.DefaultChannels` become `general` and `memory`, so
  `ensureDefaults` stops creating it and `register` stops subscribing it.
- `create_channel` keeps refusing the bare name `tasks`. `IsTaskChannel`
  keeps matching it, so an existing `tasks` channel works as a task list
  until it expires.
- `agentbus init` stops creating `tasks/<repo>`. When it runs in a
  repository whose `.local/agentbus.json` lists `tasks` or any `tasks/...`
  channel, it removes those entries and says so. The channels stay on the
  bus until they expire.

### Removing existing subscriptions

Existing agents get task-list subscriptions from three places; all three
are closed:

1. Defaults: fixed by the `DefaultChannels` change above.
2. `.local/agentbus.json` in repositories where `init` has not run again:
   `register` ignores `tasks` and `tasks/...` entries and lists them in a
   new `ignored_channels` field of its result. `register` does not rewrite
   the file. The using-agentbus skill tells an agent that sees
   `ignored_channels` to remove those entries from `.local/agentbus.json`
   itself, and optionally to subscribe to the effort list it is working on
   (`tasks/<effort>`).
3. Subscriptions stored on the bus (a resumed name keeps them): the schema
   v7 migration deletes every subscription to `tasks` and `tasks/...`, for
   every sender. Names cannot tell a per-repo list from an effort list, so
   all are dropped. Task tools do not need a subscription; an agent
   mid-effort re-subscribes to its list as the skill says. The TUI
   re-subscribes to every channel on its own.

### Activity and expiry

- A task channel's last activity is the greatest of: `max(created_at)` of
  its message rows (every task create, update, claim, lease renewal, and
  release writes a revision), `max(tombstone_at)` of its tombstones, and
  the channel's own creation time. It is computed from existing columns;
  no new column.
- The maintenance tick gets a `task expiry` step: every `tasks` or
  `tasks/...` channel whose last activity is older than `task_expiry_hours`
  is deleted through the existing `DeleteChannel` path, taking all its tasks
  whatever their status and all subscriptions. Live subscriptions do not
  keep a task channel alive (a running TUI subscribes to everything).
- The empty-channel reaper is unchanged.

### TUI idle marker

A task channel's rail row shows the existing `idle` icon when its last
activity is older than `task_idle_hours`. `StatusReport`'s channel list
gains `last_activity` (ms) for task channels so the TUI does not query per
row. The marker clears on the next status refresh after any task write.

## 2. Memory expiry

### Last accessed

- New table `memory_access(memory_id INTEGER PRIMARY KEY, accessed_at
  INTEGER NOT NULL)`, apart from `messages` so revisions need not carry it.
- Set to now when a memory is created (`send` to a memory channel) or
  edited (`edit_memory`).
- Updated to now only when the memory is returned by `get_memory` or is a
  `search` hit: one batched `UPDATE` per call. `history`, `receive`, the
  TUI, and the CLI do not update it.
- Task rows (memories on `tasks` and `tasks/...`) get no row; the
  task-channel rule governs them.
- The v7 migration inserts a row for every live memory with `accessed_at`
  set to the migration time, so no existing memory expires sooner than
  `memory_expiry_hours` after the upgrade.
- Deleting a memory (tombstone) deletes its row.

### Expiry

A `memory expiry` maintenance step tombstones, in chunks of
`cleanupChunk`, every live memory whose `accessed_at` is older than
`memory_expiry_hours`, through the same code path as `delete_memory`, so
readers see the deletion and the tombstone is purged by the existing
tombstone step.

## 3. Settings

New `config.json` keys, in hours like the existing retention settings:

| Key | Default | 0 means |
|---|---|---|
| `task_idle_hours` | 168 (7 days) | no idle marker |
| `task_expiry_hours` | 720 (30 days) | task channels never expire |
| `memory_expiry_hours` | 8760 (1 year) | memories never expire |

Negative values are rejected at load. The TUI health view shows all three.

## 4. Documentation

- using-agentbus skill: task lists are `tasks/<effort>`, created by whoever
  starts the effort, subscribed by the agents working on it, removed after
  `task_expiry_hours` of inactivity; drop `tasks/<repo>` and the
  machine-wide `tasks` from the scope table.
  It also says: when `register` returns `ignored_channels`, delete those
  entries from `.local/agentbus.json` and, if you are working on an
  effort, subscribe to its `tasks/<effort>` list.
- `/agentbus` command, SessionStart hook text, README, `docs/install.md`:
  same change.
- ADR 0013 records the decisions and supersedes ADR 0007 decisions 1 and 2
  for task lists.
- Release notes: schema v7; upgrade and restart all hosts together; re-run
  `agentbus init` in each repository to clean `.local/agentbus.json`
  (optional, since `register` ignores the entries).

## 5. Testing

- Task expiry: a channel whose revisions are older than the limit is
  deleted with its tasks; one with a recent lease renewal survives; a live
  subscription does not save an expired channel; 0 disables.
- Defaults: a fresh bus has no `tasks` channel; `register` does not
  subscribe one; `register` ignores and reports `tasks/...` entries in
  `.local/agentbus.json`.
- `init`: does not create `tasks/<repo>`; removes task entries from an
  existing file.
- Migration v6 to v7: task-list subscriptions dropped, other
  subscriptions kept, `memory_access` backfilled with the migration time.
- Memory access: `get_memory` and a `search` hit update `accessed_at`;
  `history` and `receive` do not; create and edit set it.
- Memory expiry: an old memory is tombstoned at the tick; a recently
  fetched one survives; task rows are untouched; 0 disables.
- TUI: the idle marker appears past `task_idle_hours` and clears after a
  task write.
- Config: defaults, overrides, and rejection of negatives.
