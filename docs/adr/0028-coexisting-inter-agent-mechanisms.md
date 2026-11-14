# ADR 0028: agentbus coexists with other inter-agent communication mechanisms

Status: Accepted (Eric, 2026-11-14). Human decision, recorded by Claude.

## Context

agentbus2 was renamed agentmesh, and some machines have both agentbus and
agentmesh installed. agentbus's SessionStart hook tells every session to
register with agentbus. The agentmesh MCP server tells the same session to
register with agentmesh. efitz-home, after an audit of `~/.claude`, asked
two things: that the using-agentbus skill use the generic phrase "an
inter-agent communication mechanism (such as agentbus or agentmesh)", and
that agentbus and agentmesh agree on which one owns session start when both
are installed.

## Decision (Eric, 2026-11-14)

1. The using-agentbus skill (`internal/cli/skills/using-agentbus/SKILL.md`)
   stays agentbus-specific. Its wording is not generalized.
2. Any edit agentbus or its sessions make to CLAUDE.md or AGENTS.md uses the
   generic phrase "an inter-agent communication mechanism (such as agentbus
   or agentmesh)".
3. When more than one inter-agent communication mechanism is installed,
   that is the user's intent. No mechanism owns session start and none is
   preferred over another: a session registers with every installed
   mechanism and uses all of them. The agentbus SessionStart hook is
   unchanged.

## Consequences

- agentbus does not detect other mechanisms, and it does not stand down
  when another one is present.
- agentbus does not write CLAUDE.md or AGENTS.md today, so rule 2 has
  nothing to apply to yet.
- agentmesh was told the same (agentbus message 1800). efitz-home got the
  decision as message 1799.
