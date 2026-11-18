# switcch Phase 5 — Hardening, Router Seam, Model Fallback, Wait-for-Reset, Release

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish switcch v1: close the three deferred Phase 4 review items, finalise the router seam (`status --json` + one exhaustion event), add model-cap fallback and opt-in wait-for-reset, and ship a README/LICENSE/install path.

**Architecture:** Everything builds on the Phase 4 supervisor (`internal/supervise`): new behaviour is new branches in `chat.tick` / `chat.commit` plus new `config` keys. The seam is a schema-versioned `status --json` fed by the session registry (now written by the supervisor, which knows the Claude session id) and an `eventCommand` the supervisor runs with a JSON payload. Tasks 1–4 are independent of the real-account relaunch path; Tasks 5–6 reuse it and come last.

**Tech Stack:** Go 1.23 (module floor `go 1.23.0`), stdlib + `golang.org/x/sys` + `golang.org/x/text` only; macOS.

**Spec:** `docs/superpowers/specs/2026-11-04-switcch-design.md` (§5.5 model-cap fallback, §5.6 exhaustion / wait-for-reset, §5.9 interop seam, §2.8 installable). Prior plan: `docs/superpowers/plans/2026-11-17-switcch-phase4-failover.md` (its Global Constraints still apply). Phase 4 decision ledger: `.superpowers/sdd/2026-11-17-switcch-phase4-failover/progress.md`.

## Global Constraints

- Phase 4 constraints hold: conservative classification; hooks inert outside switcch sessions; never pick an account at/over its limit; never lose or replay a user prompt; never write to stderr while Claude's TUI is running (in-session messages go through `notify`).
- Spec §5.9 exhaustion event payload is exactly `{session_id, account, reason, timestamp}` (snake_case keys; `timestamp` RFC 3339 UTC; `account` = the account's email). `reason` ∈ `limit_hit` | `pool_exhausted`. No router logic, no provider knowledge in switcch.
- Spec §5.6: wait-for-reset is **opt-in** — config `waitForReset`, default `false`.
- Spec §5.5 / §7: model-cap fallback **default on** — config `modelFallback`, default `["sonnet"]`; `switcch config set modelFallback ""` turns it off.
- New config keys and defaults (all settable with `switcch config set <key> <value>`, shown by `config show`): `eventCommand` (`""` = off), `waitForReset` (`false`), `modelFallback` (`sonnet`, comma list), `modelNotice` (`[switcch] {account} hit its {from} limit — continuing with {to}.`), `resumeMessage` (`[switcch] The usage limit has reset — continue where you left off.`).
- `config.Load` validates every field (closes the Phase 4 M5 class): a malformed or out-of-range value is an error naming the key, not a silent behaviour change.
- License MIT, `Copyright (c) 2026 Maks Popov` (matches the author's other repos). Install = build from source (`go install` or clone + `go build`), as in the author's other tools. Homebrew tap / release binaries are out of scope until a public GitHub repo exists.
- No secrets in logs, events or `status --json`.

## Out of scope

Homebrew tap and goreleaser (needs the public repo first); menu-bar UI; folder rules; company-skills overlay (spec §5.8, open); cross-provider routing.

## Checkpoint before Task 5

Tasks 5 and 6 relaunch Claude through the Phase 4 path (`claude --resume <sid> -- <msg>` on another profile), which awaits the user's real-account test (Phase 4 plan, Task 7). Before starting Task 5, ask the user whether that test has been run and passed; if not, ask whether to proceed anyway (they may say yes — the work is independent of the outcome unless the relaunch path itself is wrong).

## Review Focus

- **Model name in an account-wide limit message** (e.g. `error_details` mentions `claude-opus-4` on a plain 429): must NOT be treated as a model cap. → Task 5 test `TestClassifyModelCap`.
- **Model cap never signalled** (Claude Code may switch Opus→Sonnet itself and never fire `StopFailure`): model fallback must simply never trigger — no polling, no side effects. → covered by construction (it acts only on a `RateLimited` signal carrying `Model`); Task 5 asserts a plain limit still rotates accounts.
- **User types while a chat is waiting for a reset**: the wait is cancelled; switcch must not later restart a chat the user has resumed by hand. → Task 6 `TestWaitForResetCancelledByUser`.
- **`eventCommand` that hangs, fails or prints**: never blocks or crashes the supervisor, never writes to the terminal. → Task 4 `TestEventCommandNeverBlocks`.
- **`status --json` consumers**: field names are fixed by `schema: 1`; arrays are `[]` never `null`; an account with no usage still lists its sessions. → Task 3 `TestStatusJSONSchema`.

---

### Task 1: Exit status on signals + stable hook binary path (Phase 4 review M4, M8)

**Files:** Modify `internal/supervise/supervise.go` (`exitErr`, SIGTERM/SIGHUP branch of `Run`), `cmd/switcch/main.go` (`hookPaths`); Test `internal/supervise/supervise_test.go`, `cmd/switcch/main_test.go`

**Interfaces:**
- Produces: `exitErr(err error) error` returns `&ExitError{Code: 128 + signo}` when the child was killed by a signal; the `term` branch of `Run` returns `&ExitError{Code: 128 + signo}` for the signal it forwarded. `hookExe(exe string, lookPath func(string) (string, error)) string` in `cmd/switcch/main.go`.

- [ ] **Step 1: Failing tests**

```go
func TestExitErrSignal(t *testing.T) {
	err := exec.Command("sh", "-c", "kill -TERM $$").Run()
	var ee *ExitError
	if !errors.As(exitErr(err), &ee) || ee.Code != 143 { t.Fatalf("got %v", exitErr(err)) }
	if exitErr(exec.Command("true").Run()) != nil { t.Fatal("clean exit must be nil") }
}
```
`TestHookExePrefersStablePathEntry` (main_test.go): in a temp dir, `cellar/1.0/switcch` (real file) and `bin/switcch` → symlink to it. `hookExe(cellar/1.0/switcch, lookPath returning bin/switcch)` == `bin/switcch`; with `lookPath` returning an unrelated file or an error, the result is the `exe` argument **unresolved** (no `EvalSymlinks`).

- [ ] **Step 2:** `go test ./internal/supervise/ ./cmd/switcch/` → FAIL (143 expected, got nil; `hookExe` undefined).
- [ ] **Step 3: Implement.** `exitErr`: `ee.Sys().(syscall.WaitStatus)`; `Signaled()` ⇒ `128+int(ws.Signal())`. `hookExe`: return the `lookPath("switcch")` result when `os.SameFile(stat(p), stat(exe))`, else `exe`. `hookPaths` uses `hookExe(os.Executable(), exec.LookPath)` and drops `EvalSymlinks`. (Existing installs pinned to a resolved path are replaced on the next launch — `Install` already replaces older switcch entries.)
- [ ] **Step 4:** `go test ./...` → PASS.
- [ ] **Step 5: Commit** — `fix: exit 128+signo when Claude dies by signal; hooks use the stable switcch path`.

### Task 2: Safe concurrent settings.json edits (Phase 4 review M7)

**Files:** Modify `internal/hooks/install.go`; Test `internal/hooks/install_test.go`

**Interfaces:**
- Produces: package vars `lockPath func() string` (default `${XDG_DATA_HOME:-~/.local/share}/switcch/settings.lock` — **not** next to settings.json, which lives in a git-tracked dir) and `beforeRename func()` (test seam, default no-op). `Install`/`Uninstall` signatures unchanged.

- [ ] **Step 1: Failing tests**
  - `TestInstallConcurrent`: 8 goroutines call `Install(p, exe)` at once on the `original` fixture ⇒ no error, file is valid JSON, exactly one switcch group per event, `PreToolUse` intact.
  - `TestInstallRetriesWhenFileChangesUnderIt`: `beforeRename` (first call only) rewrites `p` adding top-level `"theme":"dark"`; after `Install` the file has both `"theme": "dark"` and the switcch hooks.
- [ ] **Step 2:** run → FAIL (lost `theme`; concurrent run errors or duplicates).
- [ ] **Step 3: Implement.** `edit()` holds `syscall.Flock(LOCK_EX)` on `lockPath()` (MkdirAll its dir) for the whole read-modify-write; temp file via `os.CreateTemp(filepath.Dir(path), ".settings.json.switcch-*")`; just before the rename, re-read `path` and if its bytes differ from what was parsed, discard and redo the edit (max 3 attempts, then error `settings.json kept changing; try again`).
- [ ] **Step 4:** `go test ./internal/hooks/ -count=20` → PASS.
- [ ] **Step 5: Commit** — `fix(hooks): lock settings.json edits and retry if it changes underneath`.

### Task 3: Router seam — `status --json` v1 (spec §5.9)

**Files:** Modify `internal/session/session.go`, `internal/launch/launch.go`, `internal/supervise/supervise.go`, `cmd/switcch/main.go` (`statusJSON`); Tests alongside.

**Interfaces:**
- `session.Session` gains `SessionID string \`json:"sessionId,omitempty"\`` and `Pool []int \`json:"pool,omitempty"\`` (slots, active first). `session.Update(pid int, fn func(*Session)) error` (locked read-modify-write; no-op when pid absent).
- Recording moves from `launch.Prepare` to the supervisor: `Prepare` no longer calls `record` (it now runs for a *target* before the switch is certain). Supervisor seam `recordSession func(session.Session) error` (default `session.Record`) is called after each child starts, with `PID: os.Getpid()`, current `Slot`, `Pool`, `SessionID`, `Cwd`, and `Started` = first start of this supervisor; called again when `c.sid` changes.
- `supervise.LimitedRecently(a account.Account) bool` exported (default runtime root) for status.
- `statusJSON` output:

```json
{"schema":1,"accounts":[{"slot":1,"email":"…","alias":"work","limited":false,
  "fiveHour":{"utilization":42,"resetsAt":"…"},"sevenDay":{…},"models":[…],"usageError":"",
  "sessions":[{"pid":123,"cwd":"…","started":"…","sessionId":"…","pool":[1,2]}]}]}
```

- [ ] **Step 1: Failing tests**
  - `TestStatusJSONSchema` (main_test): decode into `map[string]any`; `schema == 1`; every account has `limited` (bool) and `sessions` (array, `[]` when empty — never `null`); a session carries `sessionId` and `pool`; an account whose usage errors still lists its sessions and has `usageError`.
  - `TestSupervisorRecordsSession` (supervise_test, fake claude `ratelimit`): `recordSession` captured calls show slot 1 then slot 2, same `PID`, `Pool == [1 2]`, `SessionID == "sid-1"` after the hook signal, `Started` unchanged across the switch.
  - `TestPrepareDoesNotRecord` (launch_test): `Prepare` leaves the `record` seam uncalled.
  - `TestSessionUpdate` (session_test): `Update` mutates only the matching pid.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3:** implement as specified above.
- [ ] **Step 4:** `go test ./...` → PASS; `go run ./cmd/switcch status --json | python3 -m json.tool` shows `"schema": 1`.
- [ ] **Step 5: Commit** — `feat(seam): status --json v1 with session ids, pools and limit state`.

### Task 4: Exhaustion event + config validation (spec §5.9)

**Files:** Create `internal/event/event.go` (+ test); Modify `internal/config/config.go` (+ test), `internal/supervise/supervise.go` (+ test), `cmd/switcch/main.go` (`config show`, usage text).

**Interfaces:**
- `event.Event{SessionID string \`json:"session_id"\`; Account string \`json:"account"\`; Reason string \`json:"reason"\`; Timestamp time.Time \`json:"timestamp"\`}`; consts `LimitHit = "limit_hit"`, `PoolExhausted = "pool_exhausted"`.
- `event.Emit(command string, e Event)` — returns immediately; in a goroutine runs `/bin/sh -c command` with the JSON payload (one line) on stdin, stdout/stderr discarded, killed after 10s. `command == ""` ⇒ no-op. The command string is the user's own config (like a git hook) and is run as written; event data is passed **only** on stdin and is never interpolated into the command line.
- `config.Config` gains `EventCommand string`, `WaitForReset bool`, `ModelFallback []string`, `ModelNotice string`, `ResumeMessage string` (JSON keys = the names in Global Constraints); defaults per Global Constraints. `config.Set` accepts the new keys (`waitForReset`: `true|false`; `modelFallback`: comma list, `""` = empty). `config.Load` validates all fields (trigger/mode enums, threshold 1–100, idleMigrateMinutes ≥ 0) and errors naming the key.
- Supervisor seam `emit func(event.Event)` (default `func(e) { event.Emit(o.Config.EventCommand, e) }` bound in `Run`). Emit `limit_hit` for every `RateLimited` signal (account = active email); emit `pool_exhausted` when a hard limit leaves no viable target (including after `commit` runs out of preparable accounts).

- [ ] **Step 1: Failing tests**
  - `TestEmitPayload`: command `cat > <tmp>/out`; after `Emit`, poll ≤2s ⇒ file parses to exactly the four keys with the given values; `timestamp` is RFC 3339.
  - `TestEventCommandNeverBlocks`: `Emit("sleep 30; echo x", e)` returns in <50ms.
  - `TestRunEmitsLimitAndExhaustion` (fake claude `ratelimit-always`, pool [work, personal]): captured events = `limit_hit/work`, `limit_hit/personal`, `pool_exhausted/personal`, each with `session_id "sid-1"`.
  - `TestLoadValidates`: config file `{"mode":"Auto"}` ⇒ error mentioning `mode`; `{"threshold":0}` ⇒ error mentioning `threshold`; absent file ⇒ defaults incl. `ModelFallback == ["sonnet"]`, `WaitForReset == false`. `Set("modelFallback","")` persists an empty list; `Set("waitForReset","maybe")` errors.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** `go test ./...` → PASS.
- [ ] **Step 5: Commit** — `feat(seam): exhaustion event via eventCommand; validate config on load`.

**→ Checkpoint (see above) before Task 5.**

### Task 5: Model-cap fallback (spec §5.5, default on)

**Files:** Modify `internal/hooks/handle.go` (+ test), `internal/supervise/args.go`, `internal/supervise/supervise.go` (+ tests).

**Interfaces:**
- `hooks.Signal` gains `Model string \`json:"model,omitempty"\``. `Classify` sets it on a `RateLimited` StopFailure whose lowercased text matches `\b(opus|sonnet|haiku)(?: weekly)? limit\b` (the captured family name).
- `relaunchArgs(orig []string, sp optSpec, model, sid, msg string) []string` — new `model` param: when non-empty, the user's own `--model`/`--model=…` is dropped and `--model <model>` is appended before the session part.
- `move` gains `model string`; `chat` gains `model string` (current override, reset to `""` whenever the account changes) and `triedModels map[string]bool` (reset on account change). `commit` must accept a same-account move when `m.model != ""`.
- Rule: on `RateLimited` with `s.Model != ""`, in `auto` mode, pick the first `m` in `cfg.ModelFallback` with `m != s.Model` and not tried on this account ⇒ same-account move with `model: m` and message from `cfg.ModelNotice` (`{account}` = active, `{from}` = `s.Model`, `{to}` = `m`); **do not** write the limited marker and **do not** emit `pool_exhausted` (still emit `limit_hit`). No untried model left ⇒ fall through to the existing account rotation (which marks the account limited).

- [ ] **Step 1: Failing tests**
  - `TestClassifyModelCap`: `"You've reached your Opus limit · resets Mon"` ⇒ RateLimited, Model `opus`; `"Sonnet weekly limit reached"` ⇒ `sonnet`; `error:"rate_limit", error_details:"429 … model: claude-opus-4"` ⇒ RateLimited, Model `""`.
  - `TestRelaunchArgsModelOverride`: orig `[--model opus --verbose]`, model `sonnet` ⇒ `[--verbose --model sonnet --resume s -- m]`; `[--model=opus]` handled the same.
  - `TestRunModelFallbackThenRotate`: fake claude emits a model-cap signal naming the model in its own `--model` arg (default `opus`) on every launch (`ratelimit-model-always`); user args `--model opus`; pool [work, personal] ⇒ launches are exactly: work/opus, work/sonnet, personal/opus, personal/sonnet (then stays); the 2nd launch's message is the model notice; the 3rd launch's args contain the user's `--model opus` again.
  - `TestRunPlainLimitSkipsModelFallback`: plain `ratelimit` (no Model) ⇒ rotates to personal on launch 2 (Phase 4 behaviour unchanged).
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3:** implement per the rule.
- [ ] **Step 4:** `go test ./...` → PASS.
- [ ] **Step 5: Commit** — `feat(supervise): try another model on the same account before switching accounts`.

### Task 6: Opt-in wait-for-reset and auto-resume (spec §5.6)

**Files:** Modify `internal/supervise/supervise.go` (+ test).

**Interfaces:**
- `chat` gains `waitUntil time.Time`, `waitFor account.Account` (zero = not waiting).
- `earliestReset(pool []account.Account, usageOf …) (account.Account, time.Time, bool)`: for each pool account, the latest `ResetsAt` among its windows with `Utilization >= 100 && HasReset`; returns the account with the earliest such time; `false` if none is known.
- Rule: when a hard limit leaves no viable target, `auto` mode and `cfg.WaitForReset` ⇒ set the wait from `earliestReset` and `notify` once `"All accounts for this chat are at their limit — continuing on <name> at <15:04>."`; unknown reset ⇒ notify `"…no reset time known; not waiting."` and do not wait. In `tick`, while waiting: a `busy` marker (user typed) cancels the wait; at `clock() >= waitUntil` and not busy ⇒ remove `waitFor`'s limited marker and return a move to `waitFor` carrying `cfg.ResumeMessage` as the prompt (`hasPrompt`), `limit 100`.

- [ ] **Step 1: Failing tests** (use the `scripted` harness and its 1000× clock; usage stub returns `FiveHour{Utilization:100, ResetsAt: start+3min, HasReset:true}` for work and `+5min` for personal)
  - `TestEarliestReset`: picks work at +3min; an account with `Utilization 100` but `HasReset false` is ignored; none known ⇒ `false`.
  - `TestWaitForResetResumes`: `WaitForReset: true`, pool [work, personal], hard limit at 10ms ⇒ exactly 2 launches; launch 2 on work, `resumeOf` msg == `cfg.ResumeMessage`; one notification mentioning `work`.
  - `TestWaitForResetCancelledByUser`: as above but a `busy` step at 100ms ⇒ 1 launch.
  - `TestWaitForResetOffByDefault`: defaults ⇒ 1 launch, no wait notification.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3:** implement per the rule.
- [ ] **Step 4:** `go test ./... -count=3` → PASS.
- [ ] **Step 5: Commit** — `feat(supervise): opt-in wait for the earliest reset and auto-resume`.

### Task 7: README, LICENSE, `switcch version`

**Files:** Create `LICENSE`; Rewrite `README.md`; Modify `cmd/switcch/main.go` (+ test).

**Interfaces:** `versionString(info *debug.BuildInfo, ldflag string) string` — `ldflag` (set via `-ldflags "-X main.version=…"`) wins; else `info.Main.Version` unless `""`/`(devel)`; else `dev`. `switcch version` prints `switcch <v>`.

- [ ] **Step 1: Failing test** — `TestVersionString`: (`nil`, `"v1.2.0"`) ⇒ `v1.2.0`; (`{Main.Version:"v0.5.0"}`, `""`) ⇒ `v0.5.0`; (`{Main.Version:"(devel)"}`, `""`) ⇒ `dev`. `route(["version"])` ⇒ kind `version`.
- [ ] **Step 2–4:** implement; `go test ./...` → PASS.
- [ ] **Step 5: LICENSE** — MIT, `Copyright (c) 2026 Maks Popov`.
- [ ] **Step 6: README** — benefit-first, in the style of the author's `dryp/README.md` (short plain sentences, what it does before how). Sections: one-paragraph pitch (keep the existing first paragraph's promise, drop "design phase"); **Install** (`go install github.com/00mkp/switcch/cmd/switcch@latest`, Go 1.23+; or clone + `go build -o ~/.local/bin/switcch ./cmd/switcch`; macOS only); **Quick start** (`switcch add --alias personal` per account, `switcch fallback work personal`, `switcch setup`, `switcch work`, `switcch list`); **How failover works** (proactive at 90% → moves at turn end or on your next message; hard limit → moves at once; same conversation via `--resume`; pins never move; idle chats; background work); **Configuration** (table of every `config` key, default, meaning); **For tools** (`status --json` schema 1 and the `eventCommand` payload, verbatim from Tasks 3–4); **Uninstall** (`switcch uninstall-hooks`, delete the binary, `~/.config/switcch`, `~/.local/share/switcch`); **License**. Every command shown must exist in `switcch help`.
- [ ] **Step 7:** verify each README command against `go run ./cmd/switcch help`; commit — `docs: README, MIT license, switcch version`.

### Task 8: Manual verification (user, real accounts)

- [ ] If not yet done: Phase 4 plan Task 7 (turn-end switch, idle intercept, import, uninstall).
- [ ] `switcch status --json` shows `"schema": 1` and the live chat's `sessionId` + `pool`.
- [ ] `switcch config set eventCommand 'cat >> /tmp/switcch-events.jsonl'`; run `switcch work --threshold 1 --trigger reactive` only if a real limit is available to hit — otherwise skip (events are covered by tests).
- [ ] `switcch version` prints a version; a fresh clone + `go build` per the README works.
