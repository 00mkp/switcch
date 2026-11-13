# switcch Phase 3 — Credential Durability (capture-back) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans (native) to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** switcch's stored credential never goes stale for accounts run through switcch. The source of truth for an account's *current* token becomes the profile's keychain entry (where Claude Code rotates it), with the encrypted `credfile` backup kept in sync and used as the seed/offline copy. A dead backup self-heals from the live login when possible.

**Architecture (why this works):** When Claude runs in a profile, it rotates the OAuth token into that profile's hashed keychain entry (`Claude Code-credentials-<hash(CLAUDE_CONFIG_DIR)>`). Phase 1/2 only ever *seeded* from the static `credfile` backup and never read Claude's rotated token back, so the backup drifted stale. The fix is to read the profile keychain first (freshest), sync it into `credfile`, and — critically — capture it **before** a launch's re-seed deletes it. This mirrors claude-swap ("read where Claude writes"). No hooks or supervisor required (those come with Phase 4 failover).

**Tech Stack:** Go; existing Phase 1/2 packages (keychain, credfile, credblob, profile, oauth, account).

**Spec:** `docs/superpowers/specs/2026-11-04-switcch-design.md` (§5.8.1 credential-at-rest; this phase adds the durability model) and `docs/superpowers/notes/2026-11-04-tech-discovery.md` §3/§4/§5 (rotation, keychain recipe, seed/migration), §8.2/§8.3 (the lineage-ownership open issue this phase closes for switcch-managed sessions).

## Global Constraints

- The profile keychain service is `keychain.ServiceName(profile.Dir(a))`; read with `keychain.Read` (handles ~20KB credentials — reads have no line limit). switcch never *writes* a full credential to the keychain (the `security -i` 4096B limit stands); it re-seeds the profile via `.credentials.json` as in Phase 1.
- Capture-back must happen **before** `profile.Seed` deletes the profile keychain entry, or Claude's latest rotation is lost.
- A captured credential replaces the backup only when it parses and carries an access token; never overwrite a good backup with an empty/garbage read.
- Self-heal reads the live default credential (`keychain.ServiceName("")`) ONLY when that login's identity (`~/.claude.json` `oauthAccount.emailAddress`) matches the target account — never capture one account's token under another's slot.
- macOS v1; OS-specifics build-tagged `darwin`. No secrets logged.

## Review Focus

- **Capture-back ordering**: if capture runs after the seed's keychain delete, Claude's freshest token is destroyed → the bug persists. The launch path must capture first. → Task 3.
- **Identity mismatch on self-heal**: capturing the live-login credential into the wrong slot would cross accounts (an isolation breach). Self-heal must verify email identity. → Task 4.
- **Empty/absent profile keychain** (account never launched, or keychain locked): `Current` must fall back to the `credfile` seed cleanly, not error the whole operation. → Task 1.
- **Stale backup overwriting a fresh keychain token**: reading order must prefer the keychain when it is newer; a blind credfile read would reintroduce staleness. → Task 1 (prefer keychain; compare `expiresAt` when both exist).
- **Double-write races**: capture-back writes credfile while a refresh may also write it; both must hold the profile refresh lock. → Task 2/3.

---

### Task 1: `Current` — read the freshest credential (keychain-first)

**Files:** Create `internal/credsync/credsync.go`; Test `internal/credsync/credsync_test.go`

**Interfaces:**
- Produces: `func Current(a account.Account) (string, error)` — returns the freshest credential JSON: prefer the profile keychain entry; if absent/locked, fall back to `credfile`. When both exist, prefer the one whose `claudeAiOauth.expiresAt` is later. `var readProfileKeychain = func(a account.Account) (string, error) { return keychain.Read(keychain.ServiceName(profile.Dir(a))) }` (test seam).
- Also: `func SyncBackup(a account.Account, cred string) error` — write cred to `credfile` (the offline backup) when it parses and has an access token.

- [ ] **Step 1: failing test** — seam `readProfileKeychain`; (a) keychain returns a newer cred than credfile ⇒ `Current` returns the keychain one AND `credfile` now holds it; (b) keychain `ErrNotFound` ⇒ returns the credfile seed; (c) keychain returns an older/expired cred than credfile ⇒ returns the credfile one. `SyncBackup` with an empty/garbage cred is a no-op.
- [ ] **Step 2: run, verify fail.**
- [ ] **Step 3: implement** using `credblob` to compare `expiresAt`.
- [ ] **Step 4: run, verify pass.**
- [ ] **Step 5: commit** — `feat(credsync): keychain-first current credential + backup sync`.

### Task 2: Refresh uses `Current` and captures inside the lock

**Files:** Modify `internal/usage/cache.go` (`refreshToken`, `defaultTokenFor`); Test `internal/usage/cache_darwin_test.go`

**Interfaces:**
- Consumes: Task 1. `defaultTokenFor`/`refreshToken` read via `credsync.Current` (not `credfile.Read` directly), so usage polling sees Claude's rotated token. On refresh, persist via `credsync.SyncBackup`. The re-read inside `WithRefreshLock` (Phase 2 fix) stays.

- [ ] **Step 1: failing test** — seam the profile-keychain read to return a *valid, newer* token; assert `defaultTokenFor` returns it WITHOUT calling `oauthRefresh` (no needless refresh when Claude already rotated a fresh one).
- [ ] **Step 2–4:** implement + verify; whole suite green.
- [ ] **Step 5: commit** — `feat(usage): token source is the live profile keychain, not the stale backup`.

### Task 3: Launch captures-back before re-seeding

**Files:** Modify `internal/launch/launch.go`; Test `internal/launch/launch_test.go`

**Interfaces:**
- Consumes: Task 1. `Prepare` calls `credsync.Current(a)` (which syncs the backup) **before** `seed(a, cred)` — so Claude's last rotation is captured before the seed clears the profile keychain. The credential used for the session is the freshest one.

- [ ] **Step 1: failing test** — stub `currentFn` (new seam) and `seed`; assert `Current` is invoked before `seed`, and the cred passed to `seed` is the one `Current` returned.
- [ ] **Step 2–4:** implement + verify.
- [ ] **Step 5: commit** — `feat(launch): capture Claude's rotated token before re-seeding the profile`.

### Task 4: Self-heal a dead backup from the live login

**Files:** Modify `internal/credsync/credsync.go` (add `Recover`); wire into `usage.refreshToken` and `launch.Prepare` on `oauth.ErrInvalidGrant`; Tests in `credsync` + a wiring test.

**Interfaces:**
- Produces: `func Recover(a account.Account) (string, error)` — if `~/.claude.json` `oauthAccount.emailAddress` equals `a.Email`, read the live default credential (`keychain.ServiceName("")`), `SyncBackup` it, and return it; otherwise `ErrCannotRecover`. Seams: `readLiveIdentityEmail`, `readLiveDefaultCred`.

- [ ] **Step 1: failing test** — identity matches ⇒ `Recover` returns the live cred and backup is updated; identity differs ⇒ `ErrCannotRecover` and nothing written (isolation). Wiring: a refresh that hits `ErrInvalidGrant` calls `Recover` once and retries.
- [ ] **Step 2–4:** implement + verify.
- [ ] **Step 5: commit** — `feat(credsync): self-heal a dead backup from the matching live login`.

### Task 5: Manual verification (real accounts)

- [ ] Re-register personal once. Run `switcch personal`, do a turn or two, exit. Confirm `switcch list` then shows personal's usage, and keeps showing it across a token rotation (use the account enough to force a refresh) WITHOUT re-registering — proving capture-back holds the backup fresh.
- [ ] Let a backup expire while that account is the live `cswap`/global login; confirm `switcch list` self-heals (shows usage) rather than reporting "unavailable". (Needs the user; touches real credentials.)
