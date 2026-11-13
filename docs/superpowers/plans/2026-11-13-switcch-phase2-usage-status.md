# switcch Phase 2 — Usage & Status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans (native, as in Phase 1) to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** `switcch list` shows every account with its live sessions nested underneath and its 5h/7d usage headroom; `switcch status --json` emits the same as machine-readable data (the interop seam). Usage is polled out-of-band against a shared, budget-aware cache so many sessions don't exhaust the usage endpoint.

**Architecture:** A `usage` client calls `GET /api/oauth/usage` with an account's access token; a cache layer (file + lock, ~3-min cadence) serves cached readings and refreshes only when stale. A session registry records each `switcch <account>` launch (slot, pid, cwd) and detects liveness by pid. `list`/`status` compose the registry + cached usage. Builds on Phase 1 (`credfile`, `credblob`, `oauth`, `account`).

**Tech Stack:** Go stdlib (`net/http`, `encoding/json`, `syscall`); Phase 1 internal packages.

**Spec:** `docs/superpowers/specs/2026-11-04-switcch-design.md` (§5.4 status, §5.5 coordination, §4a usage facts) and `docs/superpowers/notes/2026-11-04-tech-discovery.md` §2 (endpoint, response shape, budget constants).

## Global Constraints

- Usage endpoint: `GET https://api.anthropic.com/api/oauth/usage`; headers `Authorization: Bearer <accessToken>`, `anthropic-beta: oauth-2025-04-20`, `Accept: application/json`. Endpoint overridable via unexported var for tests.
- Response: `five_hour.utilization` (0–100 float) + `resets_at` (ISO-8601 or null); `seven_day` likewise; model-scoped caps appear ONLY in `limits[]` as `{kind:"weekly_scoped", percent, scope.model.display_name, is_active, severity}`. Tolerate unknown keys. 401 ⇒ token expired; 429 carries `Retry-After`.
- Budget: ~28–30 requests/hour per account. Cache serve-TTL 180s; never refresh an account more than once per 180s; jitter ±10%; back off after a 429 using `Retry-After`.
- macOS v1; OS-specifics build-tagged `darwin` (pid liveness is portable; nothing new needs darwin here except via Phase 1 deps).
- No secrets in `status --json` or logs — only utilization numbers, reset times, account email/alias, pids, cwds.

## Review Focus

- **Model-only exhaustion** (e.g. Fable weekly at 100% while 5h/7d look healthy): a reader that parses only `five_hour`/`seven_day` reports a capped account as fine. → Task 1 parses `limits[]`.
- **Expired access token when polling** (401): the cache must refresh the credential (reuse `oauth` under the refresh lock) and retry once, not surface a raw 401. → Task 2.
- **Dead session pid reuse**: a recorded pid that has exited (or been reused by an unrelated process) must not show as a live session. → Task 3 (match recorded start-time/cwd, prune on liveness check).
- **Usage endpoint 429 under many sessions**: concurrent `switcch list` calls must coalesce on the cache + lock, not each fire a request. → Task 2.
- **Missing/garbled cache file**: a corrupt cache must degrade to a refresh, never crash. → Task 2.

---

### Task 1: Usage API client

**Files:** Create `internal/usage/usage.go`; Test `internal/usage/usage_test.go`

**Interfaces:**
- Produces: `type Window struct { Utilization float64; ResetsAt time.Time; HasReset bool }`; `type ModelCap struct { Model string; Percent float64; Active bool }`; `type Usage struct { FiveHour, SevenDay Window; Models []ModelCap }`; `func Fetch(ctx context.Context, accessToken string) (Usage, error)`; `var ErrUnauthorized error` (401).

- [ ] **Step 1: failing test** — `httptest` serving the `liveresponse` fixture shape (five_hour.utilization 2.0, seven_day 59.0, a `limits[]` entry `weekly_scoped` Fable percent 100 is_active true); assert `FiveHour.Utilization==2`, `SevenDay.Utilization==59`, one `ModelCap{Model:"Fable",Percent:100,Active:true}`; a 401 response ⇒ `errors.Is(err, ErrUnauthorized)`.
- [ ] **Step 2: run, verify fail.**
- [ ] **Step 3: implement** the GET + headers; parse `five_hour`/`seven_day` and walk `limits[]` for `kind=="weekly_scoped"` with `scope.model.display_name`; tolerate unknown keys; map 401 → ErrUnauthorized.
- [ ] **Step 4: run, verify pass.**
- [ ] **Step 5: commit** — `feat(usage): /api/oauth/usage client with model-scoped caps`.

### Task 2: Budget-aware shared usage cache

**Files:** Create `internal/usage/cache.go`; Test `internal/usage/cache_test.go`

**Interfaces:**
- Consumes: Task 1; Phase 1 `credfile`, `credblob`, `oauth`, `account`.
- Produces: `func Cached(ctx, a account.Account) (Usage, time.Time, error)` returning the usage + the time it was read; refreshes only if the cached reading is older than the serve-TTL. `var serveTTL = 180*time.Second`, `var nowFn = time.Now`, `var fetch = usage.Fetch` (test seams).

- [ ] **Step 1: failing test** — temp `XDG_DATA_HOME`; stub `fetch` counting calls; first `Cached` fetches (count 1); an immediate second `Cached` serves cache (count still 1); advancing `nowFn` past serveTTL makes the next `Cached` refetch (count 2). A stubbed `fetch` returning `ErrUnauthorized` triggers one credential refresh (stub) + retry. A corrupt cache file is treated as empty (refetch), not a panic.
- [ ] **Step 2: run, verify fail.**
- [ ] **Step 3: implement** a JSON cache at `${XDG_DATA_HOME}/switcch/usage-cache.json` keyed by slot, guarded by a file lock (reuse the dir-lock pattern); the access token comes from `credfile.Read`→`credblob`; on expiry or `ErrUnauthorized`, refresh under `oauth.WithRefreshLock` and persist, then retry once; honor `Retry-After` backoff.
- [ ] **Step 4: run, verify pass.**
- [ ] **Step 5: commit** — `feat(usage): shared budget-aware cache with refresh-on-401`.

### Task 3: Session registry

**Files:** Create `internal/session/session.go`; Test `internal/session/session_test.go`

**Interfaces:**
- Produces: `type Session struct { Slot int; PID int; Cwd string; Started time.Time }`; `func Record(s Session) error`; `func List() ([]Session, error)` (prunes dead entries); `var aliveFn = func(pid int) bool { … }` (test seam). Registry file `${XDG_DATA_HOME}/switcch/sessions.json`, file-locked.

- [ ] **Step 1: failing test** — temp home; `Record` two sessions; stub `aliveFn` so one pid is dead; `List` returns only the live one and the dead entry is pruned from the file.
- [ ] **Step 2: run, verify fail.**
- [ ] **Step 3: implement**; default `aliveFn` uses `syscall.Kill(pid, 0)` semantics (ESRCH ⇒ dead). Match on PID only for v1 (note: PID reuse is a known limitation; Review Focus — add a `Started` sanity field now, full guard later).
- [ ] **Step 4: run, verify pass.**
- [ ] **Step 5: commit** — `feat(session): launch registry with liveness pruning`.

### Task 4: Record a session on launch

**Files:** Modify `internal/launch/launch.go`; Test `internal/launch/launch_test.go`

**Interfaces:**
- Consumes: Task 3. Add `var record = session.Record` (seam). `Prepare` (or a new `Prepare` tail) records `session.Session{Slot:a.Slot, PID:os.Getpid(), Cwd:<wd>, Started:now}` before returning (the pid becomes `claude` after `Exec`).

- [ ] **Step 1: failing test** — stub `record`; assert `Prepare` records a session with the account's slot and the current pid.
- [ ] **Step 2–4:** implement + verify. (Keep `Exec` unchanged.)
- [ ] **Step 5: commit** — `feat(launch): record the session in the registry before exec`.

### Task 5: `switcch list`

**Files:** Modify `cmd/switcch/main.go`; Test `cmd/switcch/main_test.go`

**Interfaces:**
- Consumes: `account`, `session`, `usage` cache. A pure `func renderList(accts []account.Account, sessions []session.Session, usageOf func(account.Account) (usage.Usage, bool)) string` builds the `cswap list`-style tree so it is testable without network.

- [ ] **Step 1: failing test** — `renderList` with two accounts, one live session under each, and stub usage ⇒ output lists each account with `5h`/`7d` percentages and the session's cwd nested beneath the right account; an account with no live session shows none.
- [ ] **Step 2–4:** implement; wire `route`’s existing `list` case to gather registry + `usage.Cached` per account and call `renderList`. (`accounts` stays the bare identity list; `list` is the rich view — or merge; decide in-task and ledger.)
- [ ] **Step 5: commit** — `feat(cli): switcch list — accounts with live sessions + usage`.

### Task 6: `switcch status --json`

**Files:** Modify `cmd/switcch/main.go`; Test `cmd/switcch/main_test.go`

**Interfaces:**
- Produces: `func statusJSON(accts, sessions, usageOf) ([]byte, error)` emitting `{accounts:[{slot,email,alias,fiveHour:{utilization,resetsAt},sevenDay:{…},models:[…],sessions:[{pid,cwd,started}]}]}`. `route` gains `status --json`.

- [ ] **Step 1: failing test** — `statusJSON` over stub data parses back to the documented shape with the right per-account utilization and nested sessions; contains no token/secret fields.
- [ ] **Step 2–4:** implement + wire.
- [ ] **Step 5: commit** — `feat(cli): switcch status --json (interop seam)`.

### Task 7: Manual verification (real accounts)

- [ ] `switcch list` shows both accounts with real 5h/7d numbers and any live `switcch` sessions nested correctly; run it repeatedly and confirm it is not hammering the API (cache serves within the TTL). `switcch status --json | jq .` parses and carries no secrets. (Needs the user; touches real usage endpoint.)
