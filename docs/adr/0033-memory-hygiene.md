# ADR 0033: memory hygiene: serve counter, memory_feedback, soft pruning

Status: Accepted (Eric, 2026-11-27). Human decision, recorded by Claude.

Amends ADR 0013 items 6 and 7 (memory expiry and what counts as access).

## Context

ADR 0013 expires a memory that no `get_memory` call or `search` hit has
touched for `memory_expiry_hours` (item 6), and counts a search hit as a
touch (item 7). A memory that matches unrelated searches but never helps
therefore lives forever, and a wrong memory is never demoted. Nothing records
whether a memory was useful. The live bus held 583 memories after seven
weeks, and under the 1-year rule nothing expires before October 2027.

The design is in `docs/superpowers/specs/2026-11-27-memory-hygiene-design.md`
(issue #53). Approaches considered: A, extend `memory_access`; B, a `pruned`
column on `messages`; C, an archive table.

## Decision (Eric, 2026-11-27)

Human decisions, as the spec records them:

1. **Pruning is the noise rule, advisory only.** A memory served at least N
   times with zero `USED`, past a grace period, is marked pruned rather than
   deleted, and behaves as deleted for agents.
2. **Pruned memories are hidden from agents, visible to the human.** MCP
   `search`, `get_memory`, `history`, `edit_memory`, `delete_memory` and
   `memory_feedback` treat a pruned memory as nonexistent. The TUI shows it
   dimmed and labeled pruned, with actions to restore it or delete it for
   real. The CLI can export pruned rows for analysis.
3. **The ADR 0013 1-year rule stays a hard delete.** Only the new rules
   soft-prune.
4. **INCORRECT handling.** A flagged memory is marked on search hits and
   ranked last. It becomes eligible for soft-pruning (reason `incorrect`)
   only when all hold: at least 336 hours since the flag; served in more
   than 5 MCP calls since the flag; no `USED` since the flag; not corrected
   (an `edit_memory` clears the flag). A flagged memory no search returns is
   not pruned by this rule; it lives until the 1-year rule, giving people and
   agents time to notice.
5. **Pruned memories follow the same 1-year rule.** No exemption and no
   separate purge setting: a pruned memory is hard-deleted when its
   `accessed_at` is older than `memory_expiry_hours`, like any other.
6. **Approach A** (below): extend `memory_access`.

Proposals Eric approved ("lgtm"):

7. "Served" means a memory appears in the result of an MCP `search` (a hit)
   or `get_memory`, counted once per memory per call. `history`, the TUI and
   CLI reads do not count.
8. Noise rule settings in hours: `memory_noise_grace_hours` (default 720,
   measured from the memory's grace start, see below) and
   `memory_noise_serves` (default 20). Either at 0 disables the rule.
9. INCORRECT settings: `memory_incorrect_prune_hours` (default 336) and
   `memory_incorrect_prune_serves` (default 5; "more than 5" means served
   count since the flag > 5). Either at 0 disables the rule.
10. `memory_feedback` takes a list of `{memory_id, verdict}` pairs, validates
    every item before applying any, and rejects unknown verdicts, unknown,
    deleted or pruned ids, and task or chat ids with an error naming each bad
    item. `USED` from the memory's own author counts.
11. `edit_memory` clears the INCORRECT flag and restarts the noise grace
    period; the counters carry over (they are keyed by `memory_id`).
12. A TUI restore clears the pruned state and restarts the grace period.
13. The TUI shows served and used counts and the flag on the memory's
    metadata line. Agents see the flag on hits (`incorrect: true`), not the
    counts.
14. Agents are told to send feedback through the using-agentbus skill, the
    register instructions and the SessionStart hook text. No nudge in
    search or receive results.

Orchestrator's calls during planning (2026-11-27). These are not human
decisions; they are recorded so the spec and the plan agree:

15. **The noise rule counts serves since the grace start.** `memory_access`
    gains `grace_served_base`, set to `served_count` whenever `grace_from`
    is set (create, edit, TUI restore, migration backfill). The noise rule
    compares `served_count - grace_served_base` with `memory_noise_serves`,
    so a restored or edited memory needs that many new serves, not only a
    new grace period, before it can be pruned again. Refines items 8, 11
    and 12.
16. **`get_memory` carries the flag.** Its result has `incorrect: true` when
    the memory is flagged (omitted otherwise), the same JSON field as search
    hits. Extends item 13.
17. **Accepted as they are, for a follow-up issue:** `list_channels` message
    counts include pruned memories; `send` with `reply_to` naming a pruned
    memory's seq still succeeds; `memory_feedback`, restore and clear-flag
    do not run the inspection hook (the TUI delete does); the flagged-last
    ordering applies within text search's existing 1000-candidate ceiling.

Human decision during execution (Eric, 2026-11-27):

18. **TUI markers are an icon plus the word.** The pruned and incorrect
    markers in memory headers and search hits are icons from the TUI icon
    system, followed by the word: a trash can for pruned and a red X for
    incorrect, in both the emoji and Nerd Font sets, under new icon names
    `memory_pruned` and `memory_incorrect` that `icon_map` can override.
    State markers come before the counts so a narrow pane keeps them
    (orchestrator's call during execution, after the Task 8 review).

## Consequences

- The database schema moves to version 15 (`memory_access` gains ten
  columns); every running session restarts after the upgrade, as with every
  migration, and an older binary refuses the file.
- Agents see no counts. A flagged memory carries `incorrect: true` on search
  hits and `get_memory`; a pruned memory answers `not_found` exactly as a
  deleted one, so an agent can neither see nor revive it.
- Pruning is soft: a pruned memory keeps its row, its embedding and its
  full-text row, so a TUI restore needs no re-embed. The 1-year rule still
  hard-deletes a pruned memory, counted from its last access.
- The human gets restore (`R`), clear-flag (`f`) and delete (`x`) in the TUI,
  and `agentbus export-memories` for later analysis of which memories pay off.
- Known gaps accepted for a follow-up issue are listed in item 17 above.
