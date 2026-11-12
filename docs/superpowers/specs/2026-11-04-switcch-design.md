# Design — `switcch`: a per-session Claude account pooler

**Status:** Draft for review. Not approved. No code until the human approves this document.
**Author:** Claude (with Maks)
**Date:** 2026-11-04
**Name:** `switcch` — "switch" with Claude Code's `cc` inside.

---

## 1. Intent & why

**This is a general, installable open-source tool — not bespoke software for one setup.** Anyone with more than one Claude subscription (a personal plan + a work/team seat, two orgs, etc.) can install it, register their own accounts, define their own pools, and get the same behavior. Nothing about specific accounts, directories, or the words "personal"/"work" is hardcoded — those are just how the motivating user happens to configure it. The design below must stay general; the scenario that follows is its test case, not its scope.

**Motivating example (the design's test case):** Maks runs 20+ concurrent Claude Code sessions across unrelated contexts (work, school, academic research, personal). Today they all draw on a single personal Max 20x account, so he routinely hits usage limits and has to ration or spill work to other providers. He now has a second Claude account — a work **Premier/Team seat** with its own usage — and wants to use that extra usage **for work only**, while keeping one identical config/tool/connector surface and never thinking about account management.

The hard part is a combination no existing tool delivers: **many simultaneously-running sessions, each pinned to a specific account or pool, with seamless mid-conversation failover for the ones that are allowed to switch — and an absolute guarantee that personal/academic sessions never touch the work seat or come under work-org management.**

Two existing tools each solve half:
- **claude-swap** (Python): true per-session credential isolation via `CLAUDE_CONFIG_DIR`, but no seamless in-session failover (its switching is global).
- **cux** (Go): seamless in-session failover (PTY wrapper + Claude Code hooks + `claude --resume`), but writes a single *global* live credential, so it has no real per-session isolation and is not built for many simultaneous cross-account sessions.

This tool fuses the two: **claude-swap's isolation foundation + cux's failover loop, with the failover scoped to each session's own isolated profile.**

## 2. Goals

1. **One config surface.** Every session shares the user's real `~/.claude` — settings, `CLAUDE.md`, skills, commands, agents, rules, hooks, memories — identical everywhere. In-session `/config` edits write back to the real files.
2. **Per-session account control.** Each session runs under exactly one account at a time, chosen explicitly by the user at launch.
3. **Pools + seamless failover.** A session may be given an ordered list of accounts (a "pool"). When its active account crosses a threshold, it moves to the next account in *its* pool and continues the **same conversation** with no manual context management. A one-account pool is a hard pin: it rations, never switches.
4. **Hard isolation.** Nothing one session does can change the account of any other session. A session pinned to an account/pool structurally cannot reach any account outside that pool. (In the example: a pinned personal/academic session cannot reach the work seat.)
5. **Trust/observability.** The user can see, at a glance, every live session and which account it's on, plus per-account usage headroom.
6. **Low/zero babysitting.** Default behavior should let the user keep working without manual account juggling.
7. **Preserve conversations across a switch.** Switching accounts must not fork, lose, or reset the conversation/history.
8. **Installable & general.** Ships as an open-source tool others install (Homebrew/release binaries), configured per-user: register N arbitrary accounts, define arbitrary pools, no hardcoded identities or paths. macOS is the v1 target, but the core mechanism is not macOS-locked.

## 3. Non-goals / anti-goals

**Non-goals (out of scope):**
- Cross-provider pooling (OpenAI, etc.) — **not in switcch**; it can't carry a Claude conversation onto another provider's agent. It belongs to a separate router layer (the Agent Ecosystem project), which switcch *supplements* via a minimal consumable seam (§5.9), not absorbs. That router isn't built yet (that project just reset to requirements), so the seam is forward-looking, not an integration dependency.
- Merging two subscriptions into one literal usage meter — impossible; this orchestrates which account serves each session.
- A GUI is not required (a status *view* is; its surface is undecided — see §5.4).

**Anti-goals (these make it a failure even if it "works"):**
- A session ever drawing on an account *outside its configured pool* (example: a personal/academic session consuming work-seat usage).
- Config/policy from one account bleeding into sessions the user meant to keep separate (example: work-org policy reaching a personal session).
- Requiring the user to track two configs or two connector/tool surfaces.
- Losing conversation context on a switch, or silently switching a session the user believed was pinned.
- Hammering the usage API because many sessions each poll independently.

## 4. Constraints (facts, not choices)

- **Platform:** v1 targets macOS (the motivating user's personal machine; not work-managed, no MDM). The core isolation mechanism (`CLAUDE_CONFIG_DIR` + file-based credential seed) is **not** macOS-specific — claude-swap and cux both run on Linux/Windows — so the design must not preclude them, even though v1 focuses on and is tested on macOS.
- **Accounts:** personal (Max 20x) + work (Premier/Team seat). Design must generalize to N accounts.
- **`~/.claude` is heavily customized** (ADR framework, SessionStart hooks, memories) and must remain the single shared source of config.
- **Validated isolation mechanism (confirmed in claude-swap's `session.py`):** launching Claude Code with `CLAUDE_CONFIG_DIR` set to a per-session profile dir fully isolates its config *and* credential lookup; on macOS Claude Code hashes the (NFC-normalized) env value into its Keychain **service name**, so each profile gets its own Keychain entry — real isolation despite Keychain storage. Profiles seed a plaintext `.credentials.json`, which Claude migrates into that profile's hashed Keychain entry on first write. Shared config is provided by **symlinking** `settings.json`, `CLAUDE.md`, `skills/`, `commands/`, `agents/`, `rules/` (and, for cross-account resume, `projects/` transcripts and `history.jsonl`) from the real `~/.claude` into each profile.
- **Switching safety:** Claude Code (and claude-swap/cux) never run `/logout` on switch — that revokes the refresh token. Switches swap stored credentials directly. Refresh tokens rotate, so a credential captured and left idle can go stale; the design must refresh/capture correctly.

## 4a. Feasibility notes (from design review)

- **Usage is pollable out-of-band, per account.** cux extracts each account's OAuth access token (`ExtractAccessToken`) and calls Claude's usage API directly, and refreshes tokens independently of a running Claude (`RefreshBlob` / `IsTokenExpired`); claude-swap already displays each account's 5h/7d/model utilisation and reset times without that account's sessions running. So threshold (pre-429) failover and the status view's headroom are backed by a real usage source, not just scraped rate-limit metadata. The central poller reads **each account's own usage with that account's own stored credential** — reading account X's usage with X's token is not cross-account routing and does not breach the isolation anti-goals.
- **The work Premier/Team seat is capturable.** Demonstrated empirically: the work account was logged in via normal `claude` `/login`, captured into a local account store, and it reports usage. Both accounts sign in via **Google** (personal and work alike), producing standard Claude OAuth credentials with normal refresh — no enterprise SSO/Okta layer. Residual, minor: the work Google Workspace could enforce a periodic session re-auth; normal token refresh plus the dead-token recovery path handle it.
- **Cross-account resume — VALIDATED (spike, 2026-11-11).** `claude --resume` of a conversation created under account A, resumed after switching the live credential to account B, loaded with no error, retained full context (recalled the planted value), and billed the turn to account B — with a transcript that contained a thinking block (no signature rejection). The failover premise holds. Minor observation for the plan: resume showed a benign "reload plugins" notice — `plugins/` is not among claude-swap's shared items, so switcch should share `plugins/` across profiles too (for the identical-config goal) to avoid per-profile plugin drift.

## 5. Core model (decided behavior)

### 5.1 Sessions & isolation
Each Claude Code session runs inside its own isolated profile (its own `CLAUDE_CONFIG_DIR` → its own hashed Keychain entry + its own `.credentials.json`), with the shared `~/.claude` config symlinked in and `projects/` shared so `--resume` works across accounts. Because every session's live credential is private to its profile, a switch in one session touches only that profile — the isolation guarantee is structural, not policy.

### 5.2 Pools & assignment
- A **pool** is an ordered list of accounts a session may use. First entry is the starting account; later entries are failover targets in order.
- **Pin = pool of one.** A personal/academic session gets pool `[personal]`: no target to switch to, so it simply rations as today and can never reach work.
- **Assignment is explicit at launch** — the user names the account/pool when starting a session (e.g. `switcch personal`, `switcch work`, or a named pool). This is the primary signal; there is no silent auto-classification. *(My call: if the user starts a session with no account/pool given and no opt-in rule matches, the tool refuses/prompts rather than guessing. Flagged for the human — alternative is "default to a named account.")*
- **Optional, opt-in rules** (e.g. directory-based) may supply a default *only when the user turns them on*; folders are explicitly **not** the baseline. *(My call: rules are a later enhancement, not MVP, unless the human wants them in v1.)*

### 5.3 Failover modes & triggers
- Two modes, chosen by the user (global default, per-session override):
  - **Auto:** on trigger, switch to the next account in the pool and surface a one-line notice (e.g. `usage 95% on work → switched to personal, resuming…`), continuing the same chat. Notice placement — in the conversation, in the terminal, or both — is left to whatever fits the workflow best (builder's call, since it depends on how the wrapper mediates the session).
  - **Advisory:** on trigger, notify at the user's threshold and let the user switch (via an in-session command and/or exit + relaunch). No automatic switch.
- **Triggers:** a hard rate-limit response, and/or a **configurable usage threshold** (percent of the 5h and/or 7d window).
- *All per-session configurable (global defaults, per-session overrides):* whether a chat may fall back at all (its pool), the fallback order, auto-vs-advisory, the threshold %, and whether/how it notifies.
- *(My call, flagged:)* on a **model-specific** cap (e.g. Opus), optionally try other models on the same account before rotating accounts (cux has this). Default on.

### 5.4 Status / observability
A status view lists **accounts, with their live sessions nested underneath**, plus per-account usage (5h/7d) and headroom — the `cswap list` shape. It must make it trivially obvious that no session is on an account outside its pool (example: no personal/academic session on the work seat). The same data is exposed as `switcch status --json` (see §5.9). *(Surface undecided and left to design: a CLI command is the baseline; an always-on menu-bar dropdown is an option, not a requirement.)*

### 5.5 Multi-session coordination
*(My call:)* one shared, cached source of truth for each account's usage (polled centrally, not once per session) so 20+ sessions don't independently hammer the usage API. Each session then decides **independently** per its own pool + mode. When a shared account runs dry, each session allowed to leave it fails over on its own terms; pinned sessions stay and ration.

### 5.6 Exhaustion (whole pool dry)
*(Decided — default, changeable:)* when every account in a session's pool is exhausted, behave like Claude Code does today (normal rate-limit), with an **optional, user-enabled** "wait for the earliest reset and auto-resume" behavior (cux has this).

### 5.7 Trust guarantee
The guarantee that personal/academic never touches work is **structural** (a pinned pool contains no work account) and **visible** (explicit launch arg + status view). *(Decided: no personal reserve in v1 — work-overflow may use personal until empty; a reserve cap can be added later if wanted.)*

### 5.8 Config vs policy isolation (sharpened by review)
Goal 1 deliberately shares *user-preference* config across all profiles (symlinked `settings.json`, `CLAUDE.md`, skills, etc.), including `/config` write-back — that is wanted. The anti-goal is narrower: *work-organization enforced policy* must never govern a personal/academic session. These collide only if work delivers enforced settings. On this machine there is no MDM/enterprise `managed-settings.json`, and server-side org policy applies per-account at runtime (only while a session is on that account) with no write path into shared local user settings. The one genuine local bleed vector is a work session's own `/config` edit landing in the shared `settings.json` and then being read by personal sessions.
*(Resolved:)* the human reports **no enforced settings or restrictions** on the work account. So config stays fully shared as Goal 1 wants, with **nothing quarantined** in v1.

**Additive per-profile overlays (e.g. company-managed skills).** The per-profile mechanism works additively, not only restrictively: a profile can carry extras *on top of* the shared `~/.claude` baseline, scoped to just the sessions that should have them. Example: work sessions get the user's full local config **plus** the company's managed skills, while personal/academic profiles get only the user's own skills. How the extras wire in depends on how the org distributes them:
- If Claude Code delivers org/managed skills **server-side** (tied to the work login), work sessions get them automatically and personal sessions never do — no switcch work needed; our account isolation already does it. *(Whether Claude Code does this is unverified — needs confirming.)*
- If they're **local files / a plugin / a marketplace**, switcch overlays them into the work profile's `skills/` (a real dir that links the user's personal skills *and* the company skills), or enables the plugin only in the work profile — leaving personal profiles pointing at just `~/.claude/skills`.

*(Open:)* when a work-pool session fails over to a personal account, should the company skills stay available (pool/session-scoped overlay) or drop with the account (account-scoped overlay)? Depends whether the skills are licensed/bound to the work seat. Decide once we know how the org ships them.

### 5.8.1 Credential-at-rest & cross-account move (decided 2026-11-12, during Phase 1)
- **Backup credential storage:** real credentials are ~10–20KB (every `mcpOAuth` connector token), past the macOS `security -i` ~4096B line limit, so the Keychain CLI can't write them without argv exposure. switcch stores the backup as a file, **encrypted at rest** via envelope encryption: a random AES-256 key held in the Keychain (tiny, fits the CLI; read/written via the stable `/usr/bin/security` so no per-rebuild prompt), the credential AES-256-GCM-encrypted into a `0600` file. Pure Go, any size, no cgo. (A cgo `SecItemAdd` whole-credential store is an alternative; rejected for cgo build cost + unsigned-binary keychain prompts.)
- **Cross-account move (manual):** moving an in-progress chat from one account to another = exit it and resume it under the other account (the validated cross-account resume). It **bundles with Phase 3** (same resume-under-account machinery as failover): add `projects/` and `history.jsonl` to the shared set so every account's resume list shows all chats, then `switcch <account> -- --resume` (arg-forwarding already exists) continues any chat under the chosen account. The all-accounts-in-one-resume-list behavior is accepted (it doubles as a switch mechanism). Until then, the one-off is `cswap switch <account>` + `claude --resume`.

### 5.9 Interop seam for a future cross-provider router (confirmed with the Agent Ecosystem project)
switcch stays Claude-only and does **not** perform cross-provider handoff (a handoff to another provider is a *new* task on a different agent, re-briefed from shared context like AGENTS.md/memory — not a resumed session). To let a future router consume switcch without over-building, v1 exposes a tiny, stable seam:
- `switcch status --json` — per-account usage % / limit state, and which account each live session is on (same data as the §5.4 status view).
- **One exhaustion event** — a hook / exec-on-event firing a JSON payload `{session_id, account, reason, timestamp}` when a session's pool is exhausted or a limit is hit.

No library, no provider knowledge, no router logic inside switcch. The counterpart router is not built yet (that project just reset to requirements), so this seam is forward-looking, not a dependency. *(Recommended for v1 — cheap, and `status --json` already underpins the status view. Flagged for the human's yes.)*

**Cross-project alignment:** the Agent Ecosystem project also treats account isolation as a hard requirement (Claude Desktop stays personal/academic; work/team accounts are Claude-Code-only). switcch's per-profile isolation is the right primitive for that — keep it strict.

## 6. Implementation path (decided)

**Decision (human): clean rebuild in our own stack** — not a fork. Own codebase, own license, no GPL-3 inheritance, no derivative-work constraints. We borrow *architecture* (not code) from cux (PTY supervisor, Claude Code hook protocol, 429 detection, `--resume` relaunch with auto-continue, independent OAuth token refresh, out-of-band usage polling, swap history, `attach`/`sessions`) and from claude-swap (the `CLAUDE_CONFIG_DIR` per-session isolation mechanism).

**Language (my call): Go.** Rationale: the workload is process supervision (PTY), concurrent usage polling, OS credential handling, and a cross-platform CLI — exactly Go's strengths, and cux proves the pattern works there. "Performant" here means low startup/overhead and solid concurrency, not CPU throughput, so Go delivers what matters without the extra cost of hand-rolling async PTY/supervision. **Alternative if house-consistency is preferred: Rust** (matches the portfolio's CLIs, max raw performance, more implementation effort for the supervisor/async/OAuth plumbing). Flagged for veto.

## 7. Decided calls (mine) — for quick veto on read
- Per-session `CLAUDE_CONFIG_DIR` isolation + symlinked shared config + shared `projects/` for cross-account resume (the validated mechanism).
- Failover rewrites only the current session's profile credential, clears that profile's hashed Keychain entry, and relaunches `claude --resume <same session>` — never global state.
- Explicit launch assignment is primary; folder/rules are opt-in and post-MVP.
- No-arg + no-rule ⇒ refuse/prompt, don't guess.
- Central cached usage poll; per-session independent decisions.
- Model-cap fallback before account rotation, default on.
- Clean rebuild in **Go** (my language call; Rust is the alternative if house-consistency is preferred).
- Smaller operating defaults (all changeable later): both-accounts-exhausted ⇒ ration like normal Claude, with opt-in wait-for-reset-and-resume; no personal reserve (work-overflow may use personal until empty); no-arg-and-no-rule ⇒ refuse/prompt; folder rules are post-v1; status view is CLI-only in v1.
- General, installable tool (not bespoke): N user-registered accounts, user-defined pools, no hardcoded identities/paths; macOS v1, mechanism not macOS-locked.
- Include the minimal interop seam in v1 — `status --json` + one exhaustion event — for a future cross-provider router; no cross-provider logic in switcch.

## 8. Open items

**Resolved during review:** implementation path → clean rebuild, not a fork (§6); language → Go, my call, Rust the alternative (§6); work-enforced policy → none; config stays fully shared (§5.8); login → Google for both accounts, standard OAuth, no enterprise SSO (§4a); the smaller operating knobs → defaulted (§7), all changeable later; cross-provider pooling → out of switcch, handled by a separate router layer that consumes switcch's minimal seam (§5.9, confirmed with the Agent Ecosystem project); scope → general installable OSS tool, not bespoke (§1, §2.8).

**Still open:** nothing blocking. (Name resolved: `switcch`.) License choice (MIT vs Apache-2.0) to settle before first release — builder default: MIT unless the human prefers otherwise.

## 9. Left to the builder (design/plan stage)
- Concrete CLI surface/verbs and config file format.
- Profile lifecycle: creation, symlink manifest, cleanup, and the exact credential-refresh/capture sequence (handling rotating refresh tokens so a captured credential doesn't go stale).
- How the hooks + PTY wrapper detect and distinguish a hard 429 vs a threshold crossing.
- Status-view data model and (if chosen) the menu-bar implementation.
- Test strategy (incl. a "remember a number, hit the limit, confirm context survives the switch" end-to-end check).
- Packaging/release in the user's house style (README voice, LICENSE, PR flow, spec committed first).
