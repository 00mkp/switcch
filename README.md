# switcch

Pool several Claude subscriptions behind your Claude Code sessions, and fail over between them **mid-conversation** when one runs out — without losing context, logging out, or managing two setups.

Run 20+ sessions at once, each pinned to its own account or to an ordered **pool** of accounts. A session allowed to fail over switches to its next account the instant it hits a usage limit and continues the *same* conversation; a session pinned to one account simply rations. Every session shares your one real `~/.claude` config, and the accounts stay isolated so a session can never spend usage outside its pool.

- One config surface — same settings, skills, `CLAUDE.md`, memories everywhere.
- Per-session account control, chosen explicitly at launch.
- Seamless same-conversation failover across accounts in a pool.
- Hard isolation: a pinned session structurally cannot touch another account.
- A status view of every live session and each account's headroom.

> **Status: design phase — not built yet.** The approved design lives in
> [`docs/superpowers/specs/2026-11-04-switcch-design.md`](docs/superpowers/specs/2026-11-04-switcch-design.md).
> Target: macOS first, written in Go. Name = "switch" with Claude Code's `cc` inside.
