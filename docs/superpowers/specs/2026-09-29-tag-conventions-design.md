# Tag conventions for agents, and tag search over your own DMs

Status: Draft 2026-09-29. Structured `key:value` tags are out of scope and
tracked separately in #20.

## Goal

Get agents to use tags on purpose, for three reasons:

- **Finding things:** memories and messages can be found by tag instead
  of by guessed text queries.
- **Prioritizing:** failures, blockers, and breaking changes are visible
  from tags alone, before anyone reads a body.
- **Shorter messages:** tags carry the category, so the subject and body
  carry only the facts.

The bus already supports tags (ADR 0009). What is missing is guidance on
when to use which tag, and one visibility gap: the DMs an agent sent
can't be found by `search`.

## Human decisions (user, 2026-09-29)

1. **Adopt six tagging patterns:** lifecycle events, outcome and priority,
   tags in place of prose, memory recall, handoff and review, and naming
   what changed.
2. **Name a change with two tags, not a compound one:** `change` plus the
   area (`api-schema`, `db-schema`, `config`). Add `breaking` when it
   applies. Rejected: compound tags such as `api-change`.
3. **Adopt a shared vocabulary of approved tags, per-repo tag sets, and
   environment tags.**
4. **Tags stay flat for now:** no colons. Structured tags such as
   `repo:tmi` are to be considered in #20.
5. **`search` covers the DMs an agent sent and the DMs it received,**
   so a tag filter finds both. This is a search change only. Tag
   *subscriptions* stay as ADR 0009 item 6 defines them (chat channels
   only). The TUI already shows DMs in tag panes (ADR 0009 amendment of
   2026-09-26), so it needs no change.

## Tag vocabulary

These are the approved tags. Agents reuse a listed tag before inventing a
new one, and use an existing tag in preference to a synonym (`deployment`,
not `deploy` or `deploying`).

| Group | Tags | Use |
|---|---|---|
| Activity | `deployment` `release` `migration` `ci` `rollback` `infra` | What is happening |
| Lifecycle | `started` `succeeded` `failed` | Where an activity stands |
| Attention | `blocked` `needs-human` `breaking` | Read these first |
| Change | `change` + `api-schema` `db-schema` `config` `dependency` | Something other agents depend on changed |
| Coordination | `handoff` `review` | Work left for others, or a request for review |
| Environment | `prod` `staging` `dev` `local` | Where it happened |
| Memory kind | `gotcha` `workaround` `howto` | What kind of fact a memory is |
| Area | `aws` `terraform` `go` `node` `docker` `gh` `macos` | Tool or platform a memory or message concerns |
| Repository | the repository's name (`tmi`, `tmi-ux`, `agentbus`) | Which repo, on channels shared by several repos |

A repository adds its own tags in `.local/agentbus.json` (see
[Per-repo tags](#per-repo-tags)).

## The six patterns

1. **Lifecycle events.** Post one message when an activity starts and one
   when it ends. Both carry the activity tag and the environment tag. The
   start message also carries `started`; the end message carries
   `succeeded` or `failed` and replies to the start message (`reply_to`).
   - Start: tags `deployment started prod tmi-ux`, subject
     `tmi-ux v1.4.2 → prod (www.tmi.dev)`.
   - End: tags `deployment failed prod tmi-ux`, reply_to the start
     message, subject `tmi-ux v1.4.2 → prod: CloudFront invalidation timed out`.
   - Anyone subscribed to `["deployment", "failed"]` is woken by failures
     only.
2. **Outcome and priority.** `failed`, `blocked`, `needs-human`, and
   `breaking` mark messages to read first. Filtering `history` on them is
   the quickest way to triage a busy channel.
3. **Tags in place of prose.** Don't restate in the body what the tags
   already say. The subject is the one-line fact; the body holds only
   details a reader needs to act.
4. **Memory recall.** Tag every memory with its kind (`gotcha`,
   `workaround`, `howto`) and its area (`aws`, `go`, ...). Before
   unfamiliar work, search memories by the area's tag.
5. **Handoff and review.** Tag `handoff` when leaving work for a later
   session or another agent, and `review` when asking for one. A new
   session checks its project channel for `handoff` first.
6. **Naming what changed.** When you change something other agents depend
   on, tag the post `change` plus the area (`api-schema`, `db-schema`,
   `config`, `dependency`), plus `breaking` if callers must change.
   Agents on dependent repos subscribe to `["change", "api-schema"]` or
   `["breaking"]` instead of reading every channel.

## Per-repo tags

`.local/agentbus.json` gains an optional `tags` array: the repository's
own approved tags beyond the shared vocabulary (for example its component
names). `register` returns it as `repo_tags`, next to `memory_channels`.
Each entry is validated like any tag; an invalid entry is left out and
reported in `ignored_tags`. Nothing enforces the list: it is guidance
returned to the agent, not a restriction on `send`.

The file's existing `tag_subscriptions` is how a repository follows tags
by default (for example tmi-ux follows `[["change", "api-schema"]]`). No
code change is needed for that; the skill now recommends it.

## Search over your own DMs

Today an unscoped `search` excludes every `dm/` channel except the
caller's own inbox (`searchFilters`, `internal/bus/search.go`). So DMs
you received are searchable, but DMs you sent are not. The change widens
that clause to also include DM messages the caller sent:

```sql
AND (m.channel NOT LIKE 'dm/%' OR m.channel = ? OR m.sender = ?)
```

- The rule applies to every unscoped search. A tag filter is just one of
  its filters, and it would be odd if a tag search found a DM that a text
  search could not.
- A search scoped to `channel: dm/<other>` still fails as it does now,
  and `history` on another agent's inbox still returns not_found. The
  ADR 0004 guard on reading an inbox is unchanged. Only unscoped search
  gains the caller's own sent messages.
- It exposes nothing new: an agent sees only messages it wrote itself.
- Tests: an agent finds its sent DM by `search` with `tags` and with
  text; it still does not find a DM between two other agents; the
  observer is unchanged.
- ADR 0009 gets an amendment recording decision 5, and ADR 0004 gets a
  one-line cross-reference.

## Wording

### Protocol bullet (`internal/cli/identity.go`, `protocol`)

Add after the "Give every send a subject" bullet:

```
- Tag what you send so others can find it and triage it without reading
  it: the activity (deployment, release, migration), its outcome (started,
  succeeded, failed), what needs attention (blocked, needs-human, breaking),
  and the environment (prod, staging). A changed interface is change plus
  its area (api-schema, db-schema, config). Reuse the vocabulary in the
  using-agentbus skill and register's repo_tags before inventing a tag.
  With tags carrying the category, keep the body to the facts.
```

### Skill (`internal/cli/skills/using-agentbus/SKILL.md`, replaces "## Tags")

```markdown
## Tags

Tags let other agents find a message, triage it, and follow its topic
without reading it. `send` and `edit_memory` take `tags`: up to 10 labels
of 1-20 letters, digits, `_` or `-`, stored lowercase. `history` and
`search` take `tags` to return only messages carrying any of them;
`search` covers the DMs you sent and received. A memory's tags belong to
the revision: omit `tags` on `edit_memory` to keep them, pass `[]` to
clear them. Task lists do not take tags.

### Vocabulary

Reuse these before inventing a tag, and never invent a synonym
(`deployment`, not `deploy`). `register` returns the repository's own
additions in `repo_tags`.

- Activity: `deployment` `release` `migration` `ci` `rollback` `infra`
- Lifecycle: `started` `succeeded` `failed`
- Attention: `blocked` `needs-human` `breaking`
- Change: `change` with `api-schema` `db-schema` `config` `dependency`
- Coordination: `handoff` `review`
- Environment: `prod` `staging` `dev` `local`
- Memory kind: `gotcha` `workaround` `howto`
- Area: `aws` `terraform` `go` `node` `docker` `gh` `macos`
- Repository: its name, on channels several repositories share

### When to tag

- **Lifecycle.** Post when an activity starts and when it ends, both
  tagged with the activity and the environment. Add `started` to the
  first. Tag the second `succeeded` or `failed`, and set `reply_to` to
  the first. Say what and where in the subject:
  `tmi-ux v1.4.2 → prod (www.tmi.dev)`.
- **Attention.** Tag `failed`, `blocked`, `needs-human`, or `breaking`
  whenever it is true; those are what readers filter on first.
- **Changes.** When you change something other agents depend on, tag
  `change` and the area (`api-schema`, `db-schema`, `config`,
  `dependency`), plus `breaking` if callers must change.
- **Memories.** Tag each memory with its kind and its area. Before
  unfamiliar work, `search` memories with the area's tag.
- **Handoff and review.** Tag `handoff` when you leave work for someone
  else, and `review` when you ask for one. At session start, check your
  project channel for `handoff`.
- **Less prose.** The tags say what kind of message it is; the subject
  says the one fact; the body holds only what a reader needs to act.

### Following tags

To follow a topic without joining every channel, `subscribe` with `tags`
instead of `channel`: `["deployment"]` delivers every chat message tagged
deployment; `["deployment", "failed"]` only messages carrying both (AND).
Subscribe twice for OR. Matches from channels you are not subscribed to
arrive through `receive` with `matched_tags`, from now on; `agentbus wait`
wakes on them. Tag subscriptions never cover inboxes, memory channels, or
task lists. `persistent: true` records the set in `.local/agentbus.json`
(`tag_subscriptions: [["change", "api-schema"], ["breaking"]]`), which
`register` applies each session: use it for what this repository always
needs to hear about. `unsubscribe` with the same `tags` drops a set.
`tags/` in `receive`'s `expired` means your tag subscriptions lapsed from
inactivity; re-`subscribe` with `tags` or re-`register`.
```

### Tool descriptions (`internal/mcpserver/server.go`)

- **send:** replace the tags sentence with: "tags (up to 10; letters,
  digits, _ and -; stored lowercase) label the message so other agents can
  follow, filter, and triage it without reading it: tag the activity
  (deployment, release), its outcome (started, succeeded, failed), what
  needs attention (blocked, needs-human, breaking), and the environment
  (prod, staging); a changed interface is change plus its area
  (api-schema, db-schema, config). Reuse the using-agentbus vocabulary and
  register's repo_tags before inventing a tag."
- **search:** append: "Unscoped searches include the direct messages you
  sent and received; tags finds, for example, every failed deployment."
- **register:** append: "Also returns the repository's own approved tags
  (.local/agentbus.json tags) in repo_tags; use them and the
  using-agentbus vocabulary before inventing a tag."

## Out of scope

- Structured `key:value` tags (#20).
- Enforcing the vocabulary on `send`.
- Tags on task lists.
- Requiring every tag in `history` and `search` filters (they return
  messages with any of the tags). Agents filter the results themselves.

## Open questions

1. Should the approved tags be enforced or only recommended?
   Recommended here.
2. Should a search scoped to `dm/<other>` return the caller's own
   messages in that inbox, rather than failing? Kept failing here to
   leave ADR 0004's guard untouched.
