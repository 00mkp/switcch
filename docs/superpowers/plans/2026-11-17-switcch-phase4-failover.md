# switcch Phase 4 — Failover + Resume/Move/Import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans (native) to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** An account can be given a fallback list once (`switcch fallback work personal`); every session launched on it then has that pool. When the active account hits its limit (or crosses a usage threshold), switcch moves the *same conversation* to the next account automatically and tells you in the chat. Any existing chat — started in switcch on another account, or outside switcch with plain `claude`/cswap — can be resumed under any switcch account.

**Architecture:**
- **Shared transcripts.** `projects/` and `history.jsonl` join the shared set, so every profile sees every chat. That alone makes `switcch <account> -- --resume` a working *move* and *import* (plain-`claude` chats live in `~/.claude/projects`, which profiles now link to).
- **Failover = relaunch under the next account's profile.** No per-session profiles: each account keeps its Phase 1 profile; switching a chat means exiting Claude and running `claude --resume <session-id> "<notice>"` with the next account's `CLAUDE_CONFIG_DIR`. Cross-account resume is validated (spike, 2026-11-11).
- **Supervisor instead of `exec`.** `switcch <pool>` stays alive as Claude's parent (inherited stdio, no PTY), so it can relaunch. Ctrl-C reaches Claude; switcch ignores it while a child runs.
- **Detection via Claude Code hooks.** switcch installs `Stop` and `StopFailure` command hooks in `~/.claude/settings.json` (by signature, removable). Each runs `switcch __hook <event>`, which is a no-op unless the session was launched by switcch (`SWITCCH_SIGNAL_DIR` set). The hook classifies the payload (conservative, ported from cux's rules) and writes a signal file the supervisor polls.
- **Notice in the chat.** The relaunch passes a first prompt (e.g. `[switcch] work hit its usage limit — continuing on personal.`), which is both the visible notice and the "keep going".

**Tech Stack:** Go; Phase 1–3 packages (`profile`, `launch`, `credsync`, `usage`, `session`, `account`).

**Spec:** `docs/superpowers/specs/2026-11-04-switcch-design.md` §5.2 pools, §5.3 modes/triggers, §5.6 exhaustion, §5.8.1 move/import. Mechanisms: `docs/superpowers/notes/2026-11-04-tech-discovery.md` §1 (StopFailure + classifier), §6 (resume wiring), §7 (simulation).

## Global Constraints

- **Failover is configured per account, once** (decided with the user 2026-11-17; comma/pool-name syntaxes rejected): `switcch fallback <account> <account>[ <account>...]` sets an ordered fallback list on an account; `switcch fallback <account> --clear` removes it. Stored on the account in the registry **by slot number** (alias renames don't break it). Launching stays `switcch <account>`: the session's pool is that account followed by its fallback list. An account with no fallback is a **pin** (no failover; rations as today). **No transitive chaining** — only the launched account's own list is used (prevents loops). Per-launch overrides: `--pin` (no failover this session) and `--fallback <account>[,<account>]` (use this list instead). `switcch accounts` shows each account's fallback.
- Defaults (config `${XDG_CONFIG_HOME:-~/.config}/switcch/config.json`, overridable per launch with `--mode auto|advisory`, `--threshold N`): mode `auto`, threshold `95` (% of 5h or 7d), notice text as above.
- Hooks are installed into the user's real `~/.claude/settings.json` (shared into every profile). Preserve all other keys and hooks; tag switcch's entries so `switcch uninstall-hooks` removes exactly them. Outside a switcch session the hook must exit 0 and do nothing.
- Classifier must be conservative (a false positive restarts a healthy session): match only the strong limit phrases and `rate_limit` on `StopFailure`; reject foreign-content markers and the context/token-limit denylist (tech-discovery §1).
- Unset `CLAUDE_CODE_RETRY_WATCHDOG` in the child env (it can suppress `StopFailure`).
- Never pick a next account the usage cache reports at/over its limit; if none remain, do not switch (normal rate-limit behavior — "ration").
- macOS v1. No secrets logged.

## Out of scope (Phase 5)

Model-cap fallback (try another model before switching accounts), opt-in wait-for-reset-and-resume, the exhaustion event / `status --json` seam finalisation, folder rules, packaging.

## Review Focus

- **Hook false positive** (e.g. a web page containing "rate limit", or "context window exceeded"): must NOT trigger a switch. → Task 4.
- **Hooks in non-switcch sessions** (plain `claude`, cswap): must be inert. → Task 4.
- **Pre-existing per-profile transcripts** when converting `projects/` to a shared link: chats must be merged into `~/.claude/projects`, never lost. → Task 1.
- **No viable next account** (all in pool exhausted, or the pool is a pin): supervisor must not loop relaunching. → Task 5.
- **Ctrl-C / normal exit**: the user quitting Claude must end switcch too, not trigger a relaunch. → Task 5.
- **Settings.json corruption**: install/uninstall must round-trip a settings file with unrelated hooks and keys untouched. → Task 3.

---

### Task 1: Share transcripts across profiles (enables move + import)

**Files:** Modify `internal/profile/shared.go`; Test `internal/profile/shared_test.go`

**Interfaces:** `SharedItems` gains `projects` and `history.jsonl`. `SyncShared`, before linking an item that already exists as a **real** dir/file in the profile, merges it into the `~/.claude` source (dirs: move each child that doesn't exist at the destination; on a name clash keep the newer file; `history.jsonl`: append profile lines to the source), then replaces it with the link.

- [ ] **Step 1: failing test** — profile has a real `projects/-x/abc.jsonl` and `history.jsonl` line "p"; home has `projects/-y/def.jsonl` and `history.jsonl` line "h". After `SyncShared`: profile `projects` and `history.jsonl` are links to home; home contains both `-x/abc.jsonl` and `-y/def.jsonl`; home `history.jsonl` contains both lines.
- [ ] **Step 2–4:** implement + verify; whole suite green.
- [ ] **Step 5: commit** — `feat(profile): share transcripts across profiles (move/import)`.

### Task 2: Per-account fallback, modes, thresholds (registry + CLI + config)

**Files:** Modify `internal/account/account.go`, `internal/account/store.go` (+ tests); Create `internal/config/config.go` (+ test); Modify `cmd/switcch/main.go` (+ test)

**Interfaces:**
- `Account.Fallback []int` (slot numbers, `json:"fallback,omitempty"`). `(*Store) SetFallback(ref string, refs []string) error` (resolves each ref to a slot; rejects self-reference and unknown refs); `(*Store) Pool(a Account) []Account` returns `[a] + a.Fallback` resolved, silently skipping slots that no longer exist. `Remove` also strips the removed slot from every other account's fallback.
- `config.Load() (Config, error)`: `Mode string`, `Threshold float64`, `Notice string`, defaults above.
- `route`: `fallback <acct> <acct>...` / `fallback <acct> --clear`; launch accepts `--pin`, `--fallback a,b`, `--mode M`, `--threshold N`, and `--` for claude args.

- [ ] **Step 1: failing tests** — `SetFallback("work", ["personal"])` stores personal's slot; `Pool(work)` ⇒ `[work, personal]`; `Pool(personal)` ⇒ `[personal]` (pin); self-reference and unknown refs error; removing personal strips it from work's fallback; fallback survives an alias rename. `route(["work","--pin"])` ⇒ pin; `route(["work","--fallback","other","--threshold","90","--","--resume"])` ⇒ override list, threshold 90, claudeArgs `[--resume]`; config defaults when the file is absent; malformed config errors clearly.
- [ ] **Step 2–4:** implement + verify; `switcch accounts` lists `→ fallback: personal`.
- [ ] **Step 5: commit** — `feat(account): per-account failover list, mode and threshold`.

### Task 3: Hook installer

**Files:** Create `internal/hooks/install.go` (+ test); Modify `cmd/switcch/main.go` (`setup`, `uninstall-hooks`)

**Interfaces:** `Install(settingsPath string, exe string) error` adds `Stop` and `StopFailure` entries `{matcher:".*", hooks:[{type:"command", command:"<exe> __hook <event>", timeout:5}]}` tagged by the command prefix; idempotent. `Uninstall(settingsPath string) error` removes only tagged entries. Launch calls `Install` (idempotent) before starting.

- [ ] **Step 1: failing test** — settings with an unrelated `PreToolUse` hook and an unrelated top-level key: `Install` twice ⇒ exactly one switcch entry per event, unrelated content byte-equivalent after JSON round-trip; `Uninstall` ⇒ back to the original structure.
- [ ] **Step 2–4:** implement (atomic write; resolve a symlinked settings.json to its target, as `SyncShared` does) + verify.
- [ ] **Step 5: commit** — `feat(hooks): install/uninstall switcch Stop + StopFailure hooks`.

### Task 4: `switcch __hook` — classify and signal

**Files:** Create `internal/hooks/classify.go`, `internal/hooks/signal.go` (+ tests); Modify `cmd/switcch/main.go`

**Interfaces:** `Classify(event string, payload []byte) Signal` returning `None | RateLimited | Stopped`, carrying `SessionID`. `WriteSignal(dir string, s Signal) error` writes `<dir>/<kind>` atomically with the session id. `__hook <event>` reads stdin; if `SWITCCH_SIGNAL_DIR` is unset ⇒ exit 0 with no output.

- [ ] **Step 1: failing test** — table (ported from cux fixtures): `StopFailure` `error:"rate_limit"` ⇒ RateLimited; `last_assistant_message:"You've hit your session limit · resets 7pm"` with `error:"error"` ⇒ RateLimited; `error:"overloaded"` alone ⇒ None; "context window exceeded" ⇒ None; text containing `<html` + "rate limit" ⇒ None; `Stop` ⇒ Stopped; every result carries the payload `session_id`. Env unset ⇒ nothing written.
- [ ] **Step 2–4:** implement + verify.
- [ ] **Step 5: commit** — `feat(hooks): conservative limit classifier + signal files`.

### Task 5: Supervisor + reactive failover

**Files:** Create `internal/supervise/supervise.go` (+ tests); Modify `internal/launch/launch.go`, `cmd/switcch/main.go`

**Interfaces:** `Run(ctx, pool []account.Account, opts Options) error` loop: `launch.Prepare(active)` ⇒ start `claude` as a child (inherited stdio, env + `SWITCCH_SIGNAL_DIR=<per-supervisor dir>`, watchdog unset) ⇒ poll the signal dir every 100ms ⇒ on `RateLimited`: SIGTERM the child, wait (≤5s, then SIGKILL), choose the next account via `next(pool, active, usageOf)` (skips accounts at/over limit; `nil` ⇒ no switch), update the session registry, relaunch with `--resume <sid> "<notice>"`. Child exits with no pending signal ⇒ capture-back (`credsync.Current`) and return. Pure `next(...)` and a `decide(signal, state) action` function hold the logic.

- [ ] **Step 1: failing tests** — `next` skips an exhausted account and returns nil for a pin; `decide` maps RateLimited+target ⇒ Switch, RateLimited+no target ⇒ Stay, plain exit ⇒ Done. Integration: a fake `claude` (test helper binary placed first on `PATH`) that records its argv/env and blocks until SIGTERM; write a `rate-limited` signal ⇒ assert the second launch got `--resume <sid>`, the notice prompt, and the next account's `CLAUDE_CONFIG_DIR`.
- [ ] **Step 2–4:** implement + verify. Session registry records the supervisor pid with the current slot and updates it on switch.
- [ ] **Step 5: commit** — `feat(supervise): reactive same-conversation failover`.

### Task 6: Proactive threshold + advisory mode

**Files:** Modify `internal/supervise/supervise.go` (+ tests)

**Interfaces:** While running, poll `usage.Cached(active)` every 60s; if max(5h, 7d) ≥ threshold: `auto` ⇒ arm a switch and perform it on the next `Stopped` signal (turn end, so no turn is cut off); `advisory` ⇒ post one macOS notification (`osascript -e 'display notification ...'`, seam for tests) and never switch.

- [ ] **Step 1: failing test** — with stubbed usage at 96% and threshold 95: auto + `Stopped` ⇒ Switch; auto with no `Stopped` yet ⇒ no switch; advisory ⇒ one notification, no switch.
- [ ] **Step 2–4:** implement + verify.
- [ ] **Step 5: commit** — `feat(supervise): proactive threshold switch and advisory notices`.

### Task 7: Manual verification (real accounts)

- [ ] `switcch setup`; `switcch fallback work personal`; open a chat with `switcch work --threshold 1` (forces a proactive switch at the first turn end). Confirm the next turn continues the same conversation on personal and the notice appears in the chat; `switcch list` shows the session under personal.
- [ ] Exit a chat, then `switcch personal -- --resume` and pick a chat started with plain `claude` — it resumes under personal (import).
- [ ] `switcch uninstall-hooks` leaves `~/.claude/settings.json` otherwise unchanged.
