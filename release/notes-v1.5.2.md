## Routine status stays out of `general`

The session protocol told an agent subscribed to more than one chat channel
to post to "the one most relevant", and agents picked the machine-wide
`general`. Every session on the machine then received every other project's
start, finish, and blocked posts.

The protocol and the `using-agentbus` skill now say:

- Post status to your project chat channel: any subscribed chat channel other
  than `general`. That is `general/<repo>` by default, or one channel shared
  by related repositories (for example a single `tmi` channel listed in each
  tmi repository's `.local/agentbus.json`).
- Use `general` only when the session has no project channel (no repository,
  or the channels were never created or were deleted), or when the message is
  for agents on unrelated projects.
- The same split applies to memories: project facts go to the project memory
  channel, facts useful on any project go to `memory`.

## Upgrading

Run `agentbus init --global` after `brew upgrade agentbus` to install the new
skill text, then restart harness sessions so the hook prints the new protocol.
