# switcch Phase 1 — Isolation MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a Go CLI where `switcch add` registers a logged-in Claude account and `switcch <account>` launches a fully isolated Claude Code session for it (own credential, own keychain entry) that shares the user's real `~/.claude` config — so multiple accounts run side by side.

**Architecture:** A per-session profile is a directory passed to Claude Code via `CLAUDE_CONFIG_DIR`; Claude derives a per-profile Keychain entry from that path, giving credential isolation. switcch registers accounts (identity + a backup copy of their OAuth credential in its own Keychain service), and on launch it refreshes the token, seeds the profile's `.credentials.json` + `.claude.json`, symlinks shared config from `~/.claude`, scrubs auth-override env vars, and `exec`s `claude`. No failover yet (Phase 3).

**Tech Stack:** Go (stdlib + `golang.org/x/text/unicode/norm`); macOS `/usr/bin/security` for Keychain; Claude OAuth endpoints.

**Spec:** `docs/superpowers/specs/2026-11-04-switcch-design.md` (see also `docs/superpowers/notes/2026-11-04-tech-discovery.md` for verified mechanisms).

## Global Constraints

- Language **Go**; module `github.com/00mkp/switcch`; binary `switcch`. Target macOS (v1); keep OS-specific code behind build-tagged files so Linux/Windows can follow later.
- **Never** run `claude logout` or revoke tokens. Never print or log credential contents.
- Keychain service recipe (verbatim, verified): with `CLAUDE_CONFIG_DIR` set, service = `"Claude Code-credentials-" + hex(sha256(NFC(dir)))[:8]`; unset ⇒ `"Claude Code-credentials"`. Keychain account = `$USER`, else OS username, else `claude-code-user`.
- OAuth token endpoint: `POST https://platform.claude.com/v1/oauth/token`, `client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e`. Usage endpoint is Phase 2.
- Credential blob shape: `{"claudeAiOauth":{accessToken,refreshToken,expiresAt(ms),scopes[],...}, ...}` — preserve all unknown keys on rewrite. Treat a token within 5 min of `expiresAt` as expired.
- Refresh tokens **rotate**: before refreshing a profile's token, take Claude's refresh locks (`<configdir>/.oauth_refresh.lock`, then `<configdir>.lock`) exactly as Claude Code does.
- Env scrub before launch (remove): `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR`, `CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR`.
- Shared config items (symlinked into each profile from `~/.claude`): `settings.json`, `keybindings.json`, `CLAUDE.md`, `skills`, `commands`, `agents`, `rules`, **`plugins`** (added per spike finding). Never share: `.credentials.json`, `.claude.json`, `sessions`, `ide`, `statsig`.
- No hardcoded account identities or paths — accounts are user-registered; "personal"/"work" are only aliases a user may set.
- State dir: `${XDG_CONFIG_HOME:-~/.config}/switcch/`; profile dirs: `${XDG_DATA_HOME:-~/.local/share}/switcch/profiles/<slug>/`; backup credentials in macOS Keychain service `switcch`.

## Review Focus

- **Rotated/stale refresh token** (`invalid_grant` on refresh): a captured credential left idle can be spent by Claude's own rotation — launch must surface a clear "re-add this account" error, not a cryptic 400. → Task 5.
- **Keychain locked/denied** (exit code ≠ 0 and ≠ 44): distinguish "not found" (44) from "locked/denied/unavailable" so the user isn't told an account is missing when the Keychain is just locked. → Task 2.
- **Non-ASCII / spaces in `CLAUDE_CONFIG_DIR`**: the service-name hash is over the NFC-normalized exported string with no realpath — a wrong normalization silently points at the wrong (empty) Keychain entry and logs the user out. → Task 1.
- **Missing shared-config source** (`~/.claude/rules` doesn't exist): symlinking a nonexistent source must skip cleanly, not abort the launch. → Task 7.
- **Stale Keychain entry shadowing a fresh seed**: Claude reads Keychain before `.credentials.json`, so seeding without first deleting the profile's hashed entry resumes the wrong account. → Task 6.

---

### Task 1: Keychain service-name derivation

**Files:**
- Create: `internal/keychain/service.go`
- Test: `internal/keychain/service_test.go`

**Interfaces:**
- Produces: `func ServiceName(configDir string) string`, `func AccountName() string`

- [ ] **Step 1: Write the failing test**

```go
func TestServiceName(t *testing.T) {
    // hex(sha256(NFC("/tmp/x")))[:8] computed independently in the test
    if got := ServiceName("/tmp/x"); got != "Claude Code-credentials-"+first8SHA256("/tmp/x") { t.Fatal(got) }
    if got := ServiceName(""); got != "Claude Code-credentials" { t.Fatal(got) }
    // NFC: composed and decomposed forms hash identically
    if ServiceName("café") != ServiceName("café") { t.Fatal("NFC not applied") }
}
func TestAccountName(t *testing.T) { /* $USER respected; junk USER -> "claude-code-user" */ }
```

- [ ] **Step 2: Run test, verify it fails** — `go test ./internal/keychain/ -run TestServiceName` → FAIL (undefined).
- [ ] **Step 3: Implement** `ServiceName` (NFC via `norm.NFC.String`, `sha256.Sum256`, `hex.EncodeToString(sum[:])[:8]`; empty dir ⇒ unsuffixed) and `AccountName` (`$USER` → `user.Current().Username` → `claude-code-user`; validate `^[A-Za-z0-9._-]+$`) in `internal/keychain/service.go`.
- [ ] **Step 4: Run test, verify it passes.**
- [ ] **Step 5: Commit** — `feat(keychain): CLAUDE_CONFIG_DIR -> service-name recipe`.

### Task 2: Keychain read/write/delete wrapper (darwin)

**Files:**
- Create: `internal/keychain/keychain_darwin.go` (build tag `darwin`)
- Test: `internal/keychain/keychain_darwin_test.go`

**Interfaces:**
- Consumes: `ServiceName`, `AccountName` (Task 1).
- Produces: `func Read(service string) (string, error)`, `func Write(service, value string) error`, `func Delete(service string) error`, and `var ErrNotFound error`.

- [ ] **Step 1: Write the failing test** (integration, uses a throwaway service name so it never touches the real `Claude Code-credentials`):

```go
func TestKeychainRoundTrip(t *testing.T) {
    svc := "switcch-test-"+randHex()
    t.Cleanup(func(){ Delete(svc) })
    if _, err := Read(svc); !errors.Is(err, ErrNotFound) { t.Fatal("want ErrNotFound") }
    if err := Write(svc, `{"a":1}`); err != nil { t.Fatal(err) }
    if v,_ := Read(svc); v != `{"a":1}` { t.Fatal(v) }
}
```

- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** via `/usr/bin/security`: `Read` = `find-generic-password -a <acct> -w -s <svc>` (exit 44 ⇒ `ErrNotFound`; other non-zero ⇒ wrapped "keychain unavailable" error, NOT not-found); `Write` = `security -i` fed `add-generic-password -U -a <acct> -s <svc> -X <hex>` (hex-encode value; fall back to argv if the `-i` 4096-byte line limit is exceeded); `Delete` = `delete-generic-password -s <svc>` (tolerate 44).
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Commit** — `feat(keychain): security-backed read/write/delete`.

### Task 3: Credential blob parse/refresh-patch

**Files:**
- Create: `internal/credblob/credblob.go`
- Test: `internal/credblob/credblob_test.go`

**Interfaces:**
- Produces: `func Parse([]byte) (*Blob, error)`; `(*Blob) AccessToken() string`; `(*Blob) RefreshToken() string`; `(*Blob) IsExpired(now time.Time) bool`; `(*Blob) ApplyRefresh(accessToken, refreshToken string, expiresInSec int, scopes []string)`; `(*Blob) Marshal() ([]byte, error)` (preserves unknown keys).

- [ ] **Step 1: Write the failing test** — parse a blob with an extra unknown top-level key (`mcpOAuth`) and an unknown field inside `claudeAiOauth`; assert `Marshal()` round-trips them; assert `IsExpired` true when `expiresAt` is 2 min out, false when 1 h out; assert `ApplyRefresh` updates accessToken/refreshToken/expiresAt and leaves unknown keys intact.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** with `map[string]json.RawMessage` at both levels so unknown keys survive; `expiresAt` is ms epoch; expiry buffer 5 min.
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Commit** — `feat(credblob): parse + refresh-patch preserving unknown keys`.

### Task 4: Account store (registry)

**Files:**
- Create: `internal/account/account.go`, `internal/account/store.go`
- Test: `internal/account/store_test.go`

**Interfaces:**
- Produces: `type Account struct { Slot int; Email, OrgUUID, DisplayName, OrgName, Alias string }`; `type Store struct{...}`; `func Load() (*Store, error)`; `(*Store) Add(a Account) (Account, error)` (assigns next Slot); `(*Store) List() []Account`; `(*Store) Resolve(ref string) (Account, error)` (by slot number, email, or alias; `ErrNoAccount` if none); `(*Store) Remove(ref string) error`; `(*Store) Save() error`. Backup credential stored separately in Keychain service `switcch` keyed by slot — `func CredService(slot int) string`.

- [ ] **Step 1: Write the failing test** — use a temp `XDG_CONFIG_HOME`; Add two accounts, assert slots 1 and 2; `Resolve("2")`, `Resolve(email)`, `Resolve(alias)` all return the right one; `Resolve("nope")` ⇒ `ErrNoAccount`; `Save`+`Load` round-trips.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** JSON state at `${XDG_CONFIG_HOME:-~/.config}/switcch/state.json` (0600); identity only — credentials live in Keychain (`switcch` service via Task 2), never in the state file.
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Commit** — `feat(account): identity registry with slot/email/alias resolution`.

### Task 5: OAuth token refresh (with Claude's locks)

**Files:**
- Create: `internal/oauth/refresh.go`, `internal/oauth/lock.go`
- Test: `internal/oauth/refresh_test.go`

**Interfaces:**
- Consumes: `credblob` (Task 3).
- Produces: `func Refresh(ctx, refreshToken string) (TokenResponse, error)` where `TokenResponse{AccessToken, RefreshToken string; ExpiresIn int; Scopes []string}`; `var ErrInvalidGrant error`; `func WithRefreshLock(configDir string, fn func() error) error`. Endpoint overridable via unexported var for tests.

- [ ] **Step 1: Write the failing test** — spin an `httptest.Server`; 200 returns a token JSON ⇒ parsed fields correct; `400 {"error":"invalid_grant"}` ⇒ `errors.Is(err, ErrInvalidGrant)`; 5xx ⇒ generic error. Test `WithRefreshLock` creates and releases `<dir>/.oauth_refresh.lock`.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** form-encoded POST (`grant_type=refresh_token&refresh_token=&client_id=`), parse `{access_token,refresh_token,expires_in,scope}`; map `invalid_grant` → `ErrInvalidGrant`. Lock = create the lock directory (mkdir-based, matching proper-lockfile), stale after 60 s, retry with jitter; also take legacy `<dir>.lock`.
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Commit** — `feat(oauth): refresh with rotation handling + Claude refresh locks`.

### Task 6: Profile seed

**Files:**
- Create: `internal/profile/profile.go`, `internal/profile/seed.go`
- Test: `internal/profile/seed_test.go`

**Interfaces:**
- Consumes: `keychain` (Tasks 1–2), `account.Account`, `credblob`.
- Produces: `func Dir(a account.Account) string`; `func Seed(a account.Account, credJSON string) error` — creates the profile dir (0700), **deletes the profile's hashed Keychain entry first** (`keychain.Delete(keychain.ServiceName(Dir(a)))`), writes `<dir>/.credentials.json` (0600, atomic), and merges `<dir>/.claude.json` with `oauthAccount` (from `a`), `hasCompletedOnboarding:true`, `theme` (default "dark" via setdefault).

- [ ] **Step 1: Write the failing test** — temp data dir; stub `keychain.Delete` via an injected interface to assert it's called before the file write; after `Seed`, `.credentials.json` has mode 0600 and the given content, and `.claude.json` has the three keys with an existing unrelated key preserved.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement**; slug = NFC-normalized `email`, non-`[A-Za-z0-9._-]`→`_`. Write files atomically (temp + rename).
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Commit** — `feat(profile): seed .credentials.json + .claude.json, clear stale keychain`.

### Task 7: Shared-config symlink + manifest + env scrub

**Files:**
- Create: `internal/profile/shared.go`
- Test: `internal/profile/shared_test.go`

**Interfaces:**
- Produces: `func SyncShared(profileDir, claudeHome string) error` (symlinks each existing item in the shared list; resolves a symlinked source to its target; writes a `.switcch-shared.json` manifest of what it created; idempotent; **skips missing sources**); `func ScrubEnv(env []string) []string`; `var SharedItems []string`.

- [ ] **Step 1: Write the failing test** — temp `claudeHome` containing `settings.json`, `skills/`, and a symlinked `CLAUDE.md`→real file, but NO `rules`; after `SyncShared`, `<profile>/settings.json` and `<profile>/skills` are symlinks to the sources, `<profile>/CLAUDE.md` points at the resolved target, `<profile>/rules` does not exist (skipped, no error), manifest lists the created links; second call is a no-op. `ScrubEnv` removes exactly the five vars and keeps others.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement**; `SharedItems` = the Global-Constraints list (including `plugins`).
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Commit** — `feat(profile): shared-config symlinks + manifest + env scrub`.

### Task 8: `switcch add` — capture the logged-in account

**Files:**
- Create: `internal/capture/capture.go`
- Test: `internal/capture/capture_test.go`

**Interfaces:**
- Consumes: `keychain` (read the live default `Claude Code-credentials`), `credblob`, `account`.
- Produces: `func Capture(store *account.Store, alias string) (account.Account, error)` — reads the live credential, derives identity from the blob (and/or the live `~/.claude.json` `oauthAccount`), stores the credential under `account.CredService(slot)`, and adds the identity to the store. `ErrNotLoggedIn` if no live credential.

- [ ] **Step 1: Write the failing test** — inject a fake credential reader returning a known blob + identity; assert the account is added with the parsed email/org and the credential is written to the slot's `switcch` Keychain service (fake). No-live-credential ⇒ `ErrNotLoggedIn`.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement**; identity fields come from `~/.claude.json` `oauthAccount` when present, else from the blob's `account`.
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Commit** — `feat(capture): register the currently logged-in account`.

### Task 9: Launch pipeline

**Files:**
- Create: `internal/launch/launch.go`
- Test: `internal/launch/launch_test.go`

**Interfaces:**
- Consumes: Tasks 2–7.
- Produces: `func Prepare(a account.Account) (configDir string, env []string, err error)` — loads the slot's backup credential, refreshes it if `IsExpired` (under `WithRefreshLock`, persisting the rotated blob back to the `switcch` Keychain slot), `profile.Seed`, `profile.SyncShared`, returns `CLAUDE_CONFIG_DIR=<dir>` prepended to `ScrubEnv(os.Environ())`. `func Exec(configDir string, env, claudeArgs []string) error` — `exec` `claude` with that env (separated so it's unit-testable without launching claude).

- [ ] **Step 1: Write the failing test** — fakes for keychain/oauth/profile; `Prepare` on an expired cred calls refresh once and persists the new blob; returns env containing `CLAUDE_CONFIG_DIR=<profileDir>` and none of the five scrubbed vars.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** `Prepare`; `Exec` uses `syscall.Exec`/`exec.Command` with inherited stdio (no PTY in Phase 1).
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Commit** — `feat(launch): refresh+seed+sync+exec pipeline`.

### Task 10: CLI wiring (`main.go`)

**Files:**
- Create: `cmd/switcch/main.go`, `go.mod`
- Test: `cmd/switcch/main_test.go`

**Interfaces:**
- Consumes: all prior.
- Produces: commands `add [--alias A]`, `accounts`/`list`, `remove <ref>`, and bare `<ref>` ⇒ launch. No `<ref>` and no rule ⇒ usage error (never guess an account).

- [ ] **Step 1: Write the failing test** — table test over an args-dispatch function `route(args) (cmd string, err error)`: `["add"]`→add; `["accounts"]`→list; `["work"]`→launch with ref "work"; `[]`→usage error.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** dispatch + `go.mod` (`go 1.23`, require `golang.org/x/text`). Keep `main` thin; logic in the dispatch function.
- [ ] **Step 4: Run** `go build ./... && go test ./...` → all pass.
- [ ] **Step 5: Commit** — `feat(cli): switcch add/accounts/remove/<account> dispatch`.

### Task 11: End-to-end verification (manual, real accounts)

**Files:** none (manual smoke test; documented in `docs/superpowers/notes/phase1-smoke.md`).

- [ ] **Step 1:** `switcch add` while logged in as account A; `cswap switch`/re-login as B; `switcch add --alias <B>`. Confirm `switcch accounts` lists both.
- [ ] **Step 2:** In two terminals, `switcch <A>` and `switcch <B>`; confirm each launches Claude Code, each reports its own account (`/status`), and the two run simultaneously without disturbing each other or the user's default login.
- [ ] **Step 3:** Confirm in-session `/config` edits land in `~/.claude` (shared), and that a skill/CLAUDE.md the user has is present in both.
- [ ] **Step 4:** Document results; note any gaps for Phase 2. (This step needs the user; it touches real credentials and prompts Keychain approval.)
