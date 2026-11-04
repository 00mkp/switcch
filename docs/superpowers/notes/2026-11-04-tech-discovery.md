# switcch technical discovery

Sources. Paths below are shorthand:
- `cux:` = github.com/inulute/cux@main (a local copy is in `.../scratchpad/cux/`). Line numbers are from that copy.
- `cswap:` = `~/.local/share/uv/tools/claude-swap/lib/python3.12/site-packages/claude_swap/`
- `CC bundle` = local Claude Code 2.1.318 binary (`~/.local/share/claude/versions/2.1.318`). I inspected it with `strings` and a regex over the output. This is the authoritative source for Claude Code internals.
- `CC docs` = code.claude.com/docs/en/{hooks,env-vars,cli-reference,sessions,authentication,statusline,errors}.md, fetched 2026-11-04.

Tags: [VERIFIED] = read in source or docs. [INFERRED] = deduced. [UNKNOWN] = could not determine.

---
## 1. Rate-limit / exhaustion detection

**Hook event: `StopFailure`.** It runs instead of `Stop` when the turn ends on an API error, after Claude Code has exhausted its own retries. [VERIFIED]
- CC docs hooks.md:2853-2879.
- CC bundle zod schema: `{hook_event_name:"StopFailure", error, error_details?, last_assistant_message?}`.

**Payload.** It also carries the common fields: `session_id`, `transcript_path`, `cwd`, `hook_event_name`, and so on (hooks.md:726-740). Example from the docs:
```json
{"session_id":"abc123","transcript_path":".../projects/.../<sid>.jsonl","cwd":"/Users/...",
 "hook_event_name":"StopFailure","error":"rate_limit",
 "error_details":"429 Too Many Requests","last_assistant_message":"API Error: Rate limit reached"}
```
- `error` is an enum: `rate_limit | overloaded | authentication_failed | oauth_org_not_allowed | account_on_hold | billing_error | invalid_request | model_not_found | server_error | max_output_tokens | cloud_credential_error | unknown`. The docs say it is also the matcher field.
- For StopFailure, `last_assistant_message` holds the rendered API error string. The `/errors` doc lists user-visible strings such as "You've hit your session limit", "You've hit your weekly limit", "You've hit your Opus limit" and "Request rejected (429)" (errors.md:47-55).
- Hook output and exit code are ignored; there is no decision control.

**What cux does.** [VERIFIED]
- It registers 6 hook events in `~/.claude/settings.json`, each with `matcher:".*"` and `type:"command"` (cux `internal/hookinstall/hookinstall.go:36-42`, builder at :252-262):

| Event | cux subcommand | Timeout |
|---|---|---|
| UserPromptSubmit | `prompt-submit` | 20 s |
| UserPromptExpansion | `prompt-expansion` | 5 s |
| Stop | `stop` | 10 s |
| StopFailure | `rate-limit` | 5 s |
| SessionStart | `session-start` | 5 s |
| PostToolUseFailure | `rate-limit` | 5 s |

- Every hook is a no-op unless the env var `CUX_WRAPPED` is set, so hooks are harmless outside the wrapper (hooks.go:9-12, `isWrapped` at :1323).
- The hook writes a signal file at `$BACKUP_ROOT/runtime/signals/{wrapperPID}-{name}`. The wrapper polls the directory every 100 ms (signals.go:1-20, wrapper.go:57). Signal names: `session-started`, `stopped`, `rate-limited`, `switch-requested`, `turn-failed`, `prompt-submitted`.
- `RateLimit()` (hooks.go:1031) feeds `classifyFailure` (:1069) and `classifyRateLimit` (:1121). The classifier is deliberately conservative, because a false positive restarts a healthy session (cux issue #39). Guards, in order:
  1. Reject if the text contains foreign-content markers: `url: http`, `<!doctype`, `<html`, `<head`, `<body`.
  2. Reject on a denylist: "do not retry", "concurrent subagent", "context window", "token limit", "output limit", "max tokens", and similar.
  3. Strong prose, accepted on any event (hooks.go:1164): `usage limit`, `session limit`, `hit your limit`, `reached your limit`, `weekly limit`, `limit will reset`, `upgrade to increase your usage`.
  4. Only on a `StopFailure`: `rate_limit_error`, `rate_limit`, `overloaded_error` (:1184). Also `429`, `too many requests`, `quota exceeded` and `rate limit`, but only when an API-context token is present (`api`, `anthropic`, `claude`, `http`, `status`, `response`, `overloaded`, `retry-after`).
  5. A separate `isAPIFailure` path (:1212) produces `turn-failed` (retry/backoff, not swap). It matches `api_error`, 5xx codes, timeout, connection reset, and similar, and applies to `StopFailure` only.
- The searched text is the structured `error`, `error_details`, and `last_assistant_message` only. `tool_input` is never searched.
- Test fixtures (`internal/hooks/apifailure_test.go`) include real payload variants. One is `Error:"error"` with `LastAssistantMessage:"You've hit your limit · resets 7pm"`.
- Gotcha. Claude Code exposes `CLAUDE_CODE_RETRY_WATCHDOG` and `CLAUDE_CODE_MAX_RETRIES` (default 10; the retry-watchdog docs say the watchdog waits out a usage limit). If either is set, `StopFailure` may never fire. cux warns about this (`internal/wrapper/claudeenv.go:7-60`). A switcch session should unset the watchdog and consider a low `CLAUDE_CODE_MAX_RETRIES`.

**Claude Code hook events** (CC bundle `_c` array, matching the docs): PreToolUse, PostToolUse, PostToolUseFailure, PostToolBatch, Notification, UserPromptSubmit, UserPromptExpansion, SessionStart, SessionEnd, Stop, StopFailure, SubagentStart, SubagentStop, PreCompact, PostCompact, PreModelSwitch, PostModelSwitch, PermissionRequest, PermissionDenied, Setup, TeammateIdle, TaskCreated, TaskCompleted, Elicitation, ElicitationResult, ConfigChange, WorktreeCreate, WorktreeRemove, InstructionsLoaded, CwdChanged, FileChanged, DirectoryAdded, MessageDisplay.

**SessionStart payload.** `source` is one of `startup|resume|clear|compact|fork`; there is also `model`, `session_title`, and others (CC bundle). cux reads `session_id`, `cwd` and `source`.

**Proactive signal without polling: statusLine.** [VERIFIED, statusline.md:193-194, 344]
- The statusLine JSON includes `rate_limits.five_hour.{used_percentage,resets_at}` and `rate_limits.seven_day.{...}`. `resets_at` is unix epoch seconds.
- It appears only for Pro/Max/Ultra subscribers, only after the first API response in the session, and each window may be absent.
- A per-session statusLine command is therefore a free, per-session usage feed that makes no `/api/oauth/usage` call. cux and claude-swap do not use it. This could ease the usage-endpoint budget problem in section 2.

**Not determined.**
- Whether `StopFailure.error` is always `rate_limit` for a subscription usage cap, versus `unknown` or `billing_error`. cux's "hit your limit" test case (`Error:"error"`) suggests the structured field is sometimes uninformative, so match on `last_assistant_message` too. [UNKNOWN: no live capture]
- Whether `StopFailure` fires when the 5h cap is hit mid-stream, or only at turn start. [UNKNOWN]

---
## 2. Usage / limits API

**Endpoint.** `GET https://api.anthropic.com/api/oauth/usage` [VERIFIED]
- cux `internal/usage/usage.go:36`; cswap `oauth.py:399`.

**Auth headers.**
- `Authorization: Bearer <claudeAiOauth.accessToken>`
- `anthropic-beta: oauth-2025-04-20` (usage.go:37, :463-464; `oauth.py:17,402`)
- Also `Accept: application/json`. claude-swap sends `User-Agent: claude-swap/1.0` (oauth.py:403).
- The account must be a Pro/Max subscription OAuth token. cux can override the endpoint and beta header via `CUX_USAGE_ENDPOINT` and `CUX_USAGE_BETA`, which is useful for mocks (usage.go:38-39).

**Response shape.** Taken from a live capture in cux `internal/usage/liveresponse_test.go`:
```json
{"five_hour":{"utilization":2.0,"resets_at":"2026-09-23T12:59:59.593033+00:00","limit_dollars":null,...},
 "seven_day":{"utilization":59.0,"resets_at":"...+00:00",...},
 "seven_day_opus":null,"seven_day_sonnet":null,"seven_day_oauth_apps":null,"seven_day_cowork":null,
 "extra_usage":{"is_enabled":false,...},
 "limits":[{"kind":"session","group":"session","percent":2,"severity":"normal","resets_at":"...","scope":null,"is_active":false},
           {"kind":"weekly_all","percent":59,...},
           {"kind":"weekly_scoped","percent":100,"severity":"critical","is_active":true,"scope":{"model":{"display_name":"Fable"}}}],
 "spend":{...},"seven_day_breakdown":{...}}
```
- `utilization` is a float from 0 to 100. `resets_at` is an ISO-8601 string or null.
- Model-scoped weekly caps appear ONLY in `limits[]` (`kind:"weekly_scoped"`, `scope.model.display_name`). A parser that reads only `five_hour` and `seven_day` will rank a model-capped seat as healthy (cux issue #52). Parse by shape: any object with numeric `utilization`, plus `limits[]`.
- Unknown obfuscated keys appear (`nimbus_quill`, `cinder_cove`, `tangelo`, and so on). Tolerate them.

**Status handling.** [VERIFIED]
- 401 means the token is expired; refresh or re-login (usage.go:476).
- 429 carries `Retry-After`, in seconds or as an HTTP date. cux parses both; claude-swap parses seconds only.

**Out-of-band pollability: yes, but budgeted.** [VERIFIED, cswap `poll_policy.py:1-60`]
- It needs only an access token, with no Claude process running.
- The endpoint is rate-limited for non-first-party clients to about 28-30 requests per rolling hour per identity (account/org-scoped in one 429 regime, token-scoped in another). A burst saturates it for up to an hour.
- claude-swap's policy constants:

| Constant | Value |
|---|---|
| `SERVE_TTL_S` / `MIN_INTERVAL_S` | 180 s |
| `URGENT_INTERVAL_S` (active account closing on its switch line) | 60 s |
| `ACTIVE_MAX_INTERVAL_S` | 300 s |
| `CANDIDATE_MAX_INTERVAL_S` | 600 s |
| `EXHAUSTED_INTERVAL_S` | 600 s |
| `JITTER_FRAC` | ±10% |
| `EDGE_BACKOFF_S` | 300 s |
| `POST_429_BACKOFF_MULT` | 1.5 |
| `usage_store.RETRY_AFTER_MARGIN_S` | 900 s |
| `usage_store.STALE_OK_S` | 300 s |

  Target average: at most 1 request per 3 minutes per identity.
- cux:
  - Polls are event-driven, not periodic. There is no daemon.
  - It does a coalesced refresh after each `Stop`, with a 20 s window for the active seat (wrapper.go:795, :1178) and a 2 min window for idle seats.
  - It uses an exponential per-account cooldown: base 1 min, cap 15 min (usage.go:90-97).
  - `StaleAfter` is 30 min (usage.go:~375).
  - Default thresholds are 100/100, meaning reactive only (usage.go:435).
- Design implication: a pool of N accounts and M sessions MUST share one usage cache with cross-process coalescing (file lock), or the endpoint will 429.

---
## 3. Token refresh without Claude running

**Endpoint.** `POST https://platform.claude.com/v1/oauth/token`, with public `client_id = 9d1c250a-e61b-44d9-88ed-5944d1962f5e` (cux `creds.go:79-80`; cswap `oauth.py:19-20`). [VERIFIED]

**Request.** Two encodings are used by the two tools and both work, so the endpoint accepts both:
- cux: `application/x-www-form-urlencoded` with `grant_type=refresh_token&refresh_token=<rt>&client_id=<id>` (creds.go:436-440).
- claude-swap: a JSON body `{grant_type, refresh_token, client_id}` with `Content-Type: application/json` (oauth.py:170-182).

**Response.** `{access_token, refresh_token?, expires_in (seconds), scope? (space-separated), account?}`. [VERIFIED]

**Patching the credential blob.** The credential JSON is `{"claudeAiOauth":{accessToken, refreshToken, expiresAt(ms epoch), scopes[], subscriptionType, rateLimitTier,...}, "mcpOAuth":{...}}`. Refresh updates:
- `accessToken`
- `refreshToken`, if returned
- `expiresAt = now_ms + expires_in*1000`
- `scopes`, if returned

Preserve all unknown keys, including the top-level `mcpOAuth`. Both tools do this (cux `RefreshBlob` creds.go:407-495; cswap oauth.py:186-195).

**Expiry check.** `IsTokenExpired` treats a token as expired if it is within 5 minutes of `expiresAt` (cux `refreshBuffer` = 5 min, creds.go:83, :391-400).

**CRITICAL: refresh tokens rotate.** [VERIFIED/INFERRED]
- claude-swap calls reuse of a rotated token a "spent grant". It gets `invalid_grant`, treated as a permanent strike (`AUTH_DEAD_STRIKES=1`, `PERMANENT_AUTH_ERRORS={invalid_grant,no_refresh_token}`; usage_store.py:227-234).
- Consequence: exactly ONE holder may own a given account's refresh lineage. If switcch refreshes out of band while a Claude process also refreshes, one side will be invalidated. Whoever refreshes must persist the new blob atomically, and Claude must be reading that same blob. Two copies of one credential drift.
- Claude Code guards its own refresh with `proper-lockfile`: `<config-home>/.oauth_refresh.lock` then the legacy `<config-home>.lock` (e.g. `~/.claude.lock`). These are directory locks, stale after 60 s, touched every 5 s, with 5 retries of 1-2 s jitter (cswap `claude_locks.py:1-48`, citing the 2.1.218 bundle). An external refresher must take the same locks. [VERIFIED via claude-swap's citation; I did not re-verify in the 2.1.318 bundle]
- On macOS, Claude rotates tokens into the keychain only. The plaintext `.credentials.json` goes stale (credentials.py:~177, session.py:`read_session_credentials` docstring).

**Related env-var escape hatches** (env-vars.md:339-341). [VERIFIED]
- `CLAUDE_CODE_OAUTH_TOKEN` is an access token. It takes precedence over keychain credentials and is used for the whole session. It is NOT refreshed; replacing it means a restart. `claude setup-token` generates a long-lived token.
- `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` plus `CLAUDE_CODE_OAUTH_SCOPES` are used by `claude auth login` to exchange a refresh token without a browser.
- These could allow keychain-free per-session injection, but whether a long-lived `setup-token` token reports `rate_limits` or can reach the usage endpoint is [UNKNOWN].

---
## 4. CLAUDE_CONFIG_DIR to Keychain service name (exact recipe)

**Authoritative (CC bundle 2.1.318, minified function `qz`):**
```js
function qz(e=""){                                   // e = "-credentials" for the OAuth item (const AHe="-credentials"), "" for the managed-key item
  let n = process.env.CLAUDE_SECURESTORAGE_CONFIG_DIR,
      t = n!==void 0 ? !n : !process.env.CLAUDE_CONFIG_DIR,   // t = "use the default (unsuffixed) store"
      r = n!==void 0 ? n.normalize("NFC") : $e(),             // $e() = config dir (CLAUDE_CONFIG_DIR, NFC-normalized, else ~/.claude)
      a = t ? "" : `-${sha256(r).hex.substring(0,8)}`;
  return `Claude Code${OAUTH_FILE_SUFFIX}${e}${a}`;           // OAUTH_FILE_SUFFIX = "" in prod (staging builds differ)
}
function R0(){ USER env || os.userInfo().username; if !/^[a-zA-Z0-9._-]+$/ => "claude-code-user" }  // keychain account
```

**Recipe.** [VERIFIED]
- **Default profile** (neither env var set, or `CLAUDE_SECURESTORAGE_CONFIG_DIR=""`): service = `Claude Code-credentials`. The local keychain has exactly this item.
- **With `CLAUDE_CONFIG_DIR` set** (and `CLAUDE_SECURESTORAGE_CONFIG_DIR` undefined): service = `Claude Code-credentials-` + the first 8 hex chars of SHA-256 over the UTF-8 bytes of `NFC(CLAUDE_CONFIG_DIR)`. NFC is applied by `$e()` in the bundle and by the `unicodedata.normalize("NFC", ...)` call in claude-swap. There is NO realpath, trailing-slash stripping, or `~` expansion. Hash exactly the string exported.
  - Go sketch: `golang.org/x/text/unicode/norm.NFC.String(dir)`, `sha256.Sum256([]byte(...))`, `hex.EncodeToString(sum[:])[:8]`.
  - cswap `session.py:232-243`:
    ```python
    normalized = unicodedata.normalize("NFC", str(config_dir))
    digest = hashlib.sha256(normalized.encode("utf-8")).hexdigest()[:8]
    return f"Claude Code-credentials-{digest}"
    ```
- **`CLAUDE_SECURESTORAGE_CONFIG_DIR` defined** overrides `CLAUDE_CONFIG_DIR` for the hash input. Defined-but-empty selects the unsuffixed default item. This can decouple the keychain item from the config dir. (cswap `credentials.py:69-107` cites Claude 2.1.220 `getMacOsKeychainStorageServiceName`; the 2.1.318 bundle matches.)
- Edge case: `CLAUDE_CONFIG_DIR=$HOME/.claude` explicitly set hashes to a SUFFIXED item (`7c7fc3ce` on this machine, `Claude Code-credentials-7c7fc3ce`), NOT the unsuffixed one. I computed this hash; there is no such item locally. claude-swap tries the suffixed name first, then the unsuffixed one, when the dir resolves to the default (credentials.py:104-106).
- Symlinked config dir: Claude hashes the exported string, not the target (session.py:344-379).

**Keychain item shape.**
- Account (`-a`) is `$USER`, else the OS username, else `claude-code-user` (bundle `R0`; cswap `macos_keychain.py:78-95`).
- Generic password. The value is the credentials JSON text (printable ASCII).
- Read: `/usr/bin/security find-generic-password -a <acct> -w -s <service>`. Exit 44 means not found; other non-zero exits mean locked, denied, or unavailable (macos_keychain.py:109-136).
- Write: `security -i` fed `add-generic-password -U -a "<acct>" -s "<svc>" -X <hex>`, with the payload hex-encoded so it stays out of argv. The `security -i` stdin line limit is 4096 bytes, so fall back to argv hex if longer (macos_keychain.py:161-204; the comment says this mirrors Claude Code's `macOsKeychainStorage.ts`).
- cux read path: `security find-generic-password -s "Claude Code-credentials"`. It also tries a managed variant, `Orca Claude Code Managed Credentials` (creds.go:61-64). It has no per-config-dir logic, so it is a single global live credential.
- Other items seen locally: `Claude Safe Storage` (Electron/Desktop) and `claude-swap` (backup accounts). Claude Code's managed API key item is the service `Claude Code` (`qz("")`), seen in cswap `credentials.py:124`.

**Keychain-vs-file routing.** [VERIFIED, CC docs authentication.md:219-222; cswap credentials.py:619-700]
- Read order on macOS: Keychain first (hashed service name for the profile), then `<config-dir>/.credentials.json`. If the keychain write is rejected (for example locked over SSH), Claude writes `<config-dir>/.credentials.json` at mode 0600 instead.
- A stale keychain entry shadows a fresh `.credentials.json` seed, so a seeder must delete the hashed keychain entry first (cswap `delete_macos_keychain_entry`, session.py:~250).
- Claude keeps an in-process keychain read cache (`class i{cache:{data,cachedAt}...}`, `kon=30000`), apparently 30 s [INFERRED: constant named `kon` equals 30000 and sits next to the cache class; semantics not confirmed]. Writes call an invalidate function.

---
## 5. Credential seed/migration + shared-config symlinks (claude-swap `cswap run`)

**Profile dir.** `<backup_dir>/sessions/<num>-<email-slug>/` (session.py:~213-230). The slug is NFC-normalized, with every character outside `[A-Za-z0-9._-]` replaced by `_`. Launch is `CLAUDE_CONFIG_DIR=<dir> exec claude ...` (session.py:536-547, via `os.execvpe`).

**Env scrub.** These are removed before launch because they bypass OAuth account selection (session.py:192-198):
- `ANTHROPIC_API_KEY`
- `ANTHROPIC_AUTH_TOKEN`
- `CLAUDE_CODE_OAUTH_TOKEN`
- `CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR`
- `CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR`

**Seed (`_bootstrap`, session.py:~820-880).**
1. Delete the hashed keychain entry for the dir.
2. Read the account's backup credentials (macOS Keychain service `claude-swap`).
3. `mkdir -p` the profile dir and chmod it 0700.
4. Write `.credentials.json` atomically with the credential JSON. It is a plaintext seed, deliberately: "Claude migrates it into its hashed keychain entry on first write" (module docstring, :8-24). claude-swap never writes the hashed keychain entry itself, to avoid coupling to Claude's internal naming.
5. Merge into `<dir>/.claude.json`:
   - `oauthAccount` = the stored identity (email, organizationUuid, and so on)
   - `hasCompletedOnboarding: true`
   - `theme` via `setdefault` (default "dark")
   - `theme` is load-bearing: Claude shows onboarding if `!theme || !hasCompletedOnboarding`.
6. Before the seed, do one proactive refresh so the profile starts with a fresh access token (session.py:~740-790).
7. Validate with `claude auth status`, which is local-only and makes no API call. Run it with `CLAUDE_CONFIG_DIR=<dir>` and the auth-override env vars scrubbed; 10 s timeout.

**Shared config (`_sync_sharing`, session.py:~1075-1180).**
- `SHARED_ITEMS` (session.py:75): `settings.json`, `keybindings.json`, `CLAUDE.md`, `skills`, `commands`, `agents`, `rules`.
- On macOS/Linux these are symlinks `<profile>/<name>` -> `~/.claude/<name>`. If the source is itself a symlink, resolve it first, to avoid a link-to-link that Claude's atomic settings write would replace (anthropics/claude-code#78162).
- On Windows, copies re-synced on every launch.
- Idempotent on every launch. A manifest `.cswap-shared.json` records which entries cswap created, so removal never touches user data.
- Never shared: `plugins/`, `sessions/`, `ide/`, `.claude.json`, `.credentials.json`, `statsig/`. User-scope `mcpServers` from `~/.claude.json` is mirrored into the profile's `.claude.json` by `_sync_mcp_servers`, which takes Claude's own `.claude.json.lock` directory lock.
- Opt-in `--share-history` additionally symlinks `HISTORY_ITEMS = (projects, history.jsonl)`. This unifies `--resume` across accounts. It is POSIX-only; if the profile already has its own history, it is merged first.
- Because `settings.json` is shared, user hooks (including switcch's StopFailure hook) are inherited by every profile. That is a possible way to wire hooks, but conflicts are possible. See the next bullet.
- `claude --settings <file-or-inline-json>` (cli-reference.md:130) can inject switcch hooks per launch without editing `~/.claude/settings.json`. Keys you omit keep their file values. [VERIFIED docs; hook merge semantics when both define the same event are [UNKNOWN]]

**Liveness and invalidation.** `process_detection.py` reads Claude's `<config-dir>/sessions/<pid>.json` PID files to detect live sessions. A profile is never rewritten under a live claude. Instead a sibling `.<dir>.cswap-stale-credentials` marker defers re-bootstrap to the next quiescent launch.

---
## 6. `--resume` relaunch wiring, and is resume local?

**cux flow (wrapper.go header :1-18).**
1. The wrapper runs `claude` as a child, optionally on a PTY (`ptyhost`, enabling `cux attach`; `attach.go` is a PTY mirror, unrelated to resume).
2. `StopFailure` writes the `rate-limited` signal. The wrapper requests a clean exit of claude without waiting for a `Stop`, because at the hard limit there may never be another one.
3. The wrapper then:
   - swaps the live credential,
   - waits for the transcript to be flushed (`waitForTranscript`, :2214, poll 50 ms, stable for 200 ms, timeout 2 s),
   - relaunches with `claude <relaunchFlags(argv)> --resume <session_id> [auto_message|replayed prompt]` (`resumeArgv`, :2038-2053). `relaunchFlags` strips the user's own session-selection flags (`--resume/-r/--continue/-c/--session-id/--fork-session`) so they do not conflict (:2056-2118).
4. `session_id` comes from the `SessionStart` hook payload, falling back to the newest `.jsonl` in the project dir (hooks.go:966-995).
5. Resume is attempted only if at least one turn has completed or argv already contains `--resume`, because claude rejects `--resume` for an empty session (`resumableSession`, :2170-2187).
6. If the wrapper cannot resume, it waits for the next usable account (`waitForReset`). It re-polls every `waitPollInterval` = 1 min with a `resetSlack`.

**Alternative cux uses for `/switch`: in-place credential rewrite with no restart.** `cmd/cux/slashcmd_switch.md` runs `cux __slash-switch`, which rewrites the live credentials. Its docs claim "Claude Code reads credentials on every request". That is plausible via the 401 re-read and a short cache, but it is in tension with the apparent 30 s keychain cache in the CC bundle (see section 4). [UNKNOWN: whether the swap is instant]. It also depends on a single global credential. That is incompatible with switcch's per-session isolation, where only a relaunch or a write to that session's hashed item/file would apply.

**Is resume purely local?** [VERIFIED docs, mostly]
- sessions.md:15 and :248: transcripts are local JSONL at `<config-dir>/projects/<cwd with non-alphanumerics -> '-'; >200 chars truncated + hash>/<session-id>.jsonl`. cux's own `ProjectTranscriptDir` only replaces `/` with `-` (`internal/paths/paths.go:79-85`), which differs for dots and spaces. Do not copy that. Use the docs rule.
- `claude --resume <absolute path to .jsonl>` works (cli-reference.md:126; sessions.md:22). So a relaunch under a DIFFERENT `CLAUDE_CONFIG_DIR` (different account) can resume a transcript that lives in another profile dir WITHOUT symlinking `projects/`.
- With the default `--resume <id>`, the new profile must see the transcript. Options: symlink `projects/` (claude-swap `--share-history`), use the absolute path form, or use `CLAUDE_CODE_PROJECT_DIR_NAME` + a shared dir (env-vars.md:362).
- Cross-account resume is therefore a local-file operation. The request history is replayed from the transcript to the new account's token.
- Not determined: whether server-side artifacts tie to the org or account and break on cross-account resume, such as thinking-block signatures or prompt-cache entries. Prompt caches are certainly per-org, so the first request after a swap pays full input tokens. Whether the API rejects foreign thinking signatures is [UNKNOWN: test with a spike].
- Resume restores the permission mode, agent, active goal, etc. (sessions.md:44-55). In-flight tool runs are not rerun and background work is noted as unfinished.
- cux notes that a swap killing the process kills in-flight subagents and background work (issue #39 context). Seamless mid-turn failover is therefore a restart plus a resume, not a hot swap.

---
## 7. Simulating a usage limit without exhausting a real account

Ranked from least to most realistic:

1. **Hook-level injection.** Feed switcch's own hook handler a synthetic StopFailure on stdin. Payload (from the docs and cux fixtures):
   `{"hook_event_name":"StopFailure","session_id":"...","error":"rate_limit","error_details":"429 Too Many Requests","last_assistant_message":"You've hit your session limit · resets 7pm"}`.
   Copy the test tables in cux `internal/hooks/apifailure_test.go` and `promptsubmitted_test.go` for positive and negative cases, such as the web-page and subagent-limit false positives. [VERIFIED approach]
2. **Mock usage endpoint.** Make the usage poller's base URL and beta header configurable (cux does this with `CUX_USAGE_ENDPOINT` and `CUX_USAGE_BETA`). Serve the `liveresponse_test.go` fixture with `five_hour.utilization` set to 100, a `limits[]` entry with `severity:"critical", is_active:true`, a `429` + `Retry-After` case, and a `401` case. [VERIFIED approach]
3. **Mock token endpoint.** Make the OAuth token URL configurable too. Cover `200`, `400 {"error":"invalid_grant"}`, `invalid_client`, and `5xx`.
4. **Real Claude with a fake API.** Set `ANTHROPIC_BASE_URL=http://127.0.0.1:PORT` to a local server that returns 429 on `/v1/messages`. Pair it with `CLAUDE_CODE_MAX_RETRIES=0` (or a small number) so the turn dies quickly and the real `StopFailure` hook fires. [INFERRED; docs confirm the env vars, but I did not run it. UNKNOWN: whether subscription-OAuth auth still works against a non-Anthropic base URL, and whether Claude requires the `anthropic-ratelimit-unified-*` response headers or a particular body (`{"type":"error","error":{"type":"rate_limit_error","message":...}}`) to render "You've hit your session limit" versus a generic "Request rejected (429)". Capture a real response once for the fixture. The `/errors` doc says the first is a "usage limit" and the second a generic 429 path (errors.md:47-55).] An `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN` can sidestep the OAuth question for a mock test, but then the account-selection layer is not exercised.
5. **Fake credentials/keychain.** Use `CLAUDE_CONFIG_DIR=<tmp>` plus a throwaway hashed keychain item or `.credentials.json` for isolation tests. `CLAUDE_SECURESTORAGE_CONFIG_DIR=<tmp>` decouples the keychain name from the config dir. Cleanup is `security delete-generic-password -s "Claude Code-credentials-<hash>"`. Never touch the default `Claude Code-credentials` item in tests.
6. **Real cap.** The only fully faithful test is a real account at its limit (for example, a Pro account that is already capped). Keep this as a manual smoke test, not a CI test.

Note: `claude --settings '<inline json>'` lets a test harness register a hook that logs payloads, to capture real `StopFailure` payloads from a genuinely capped account once and replay them as fixtures.

---
## 8. Open unknowns a plan must resolve

1. **Cross-account resume validity.** Do thinking-block signatures or other server-side state in a transcript from account A get rejected on account B (same org vs different org)? Spike: take a transcript with thinking blocks and `--resume` it under another account.
2. **Seeding a hashed keychain item vs a plaintext seed.** claude-swap always seeds `.credentials.json` and lets Claude migrate it. For Go, decide whether to follow that (safe, lower coupling) or write the hashed item directly with the verified recipe from section 4. Confirm on this machine that Claude 2.1.318 reads `.credentials.json` when the hashed item is absent and then migrates it into the keychain. [UNKNOWN: not run.]
3. **Token-lineage ownership.** Who refreshes? If Claude owns refresh inside a session profile (it rotates into the hashed keychain item), switcch's out-of-band usage polling must read the CURRENT blob from that hashed item (cswap `read_session_credentials` does this) and never refresh a lineage a live Claude owns. Needs a policy: refresh only under Claude's `.oauth_refresh.lock` or only for idle accounts. See section 3.
4. **Usage-endpoint budget.** About 28-30 requests/hour per identity, shared across tools. Decide on the statusLine `rate_limits` feed versus polling. Confirm the statusLine feed is enough to trigger proactive swaps. Check whether `anthropic-ratelimit-unified-*` response headers exist on `/v1/messages` (not determined here).
5. **`StopFailure` coverage.** Does it fire on a mid-turn hard cap versus the first request of a turn? Is `error` reliably `rate_limit`? What is the latency from cap to hook, given up to 10 retries with backoff by default (`CLAUDE_CODE_MAX_RETRIES`)? Would a lower `CLAUDE_CODE_MAX_RETRIES` for switcch sessions be wanted? Is a cheap PreToolUse/Notification check useful for early detection? None determined.
6. **Restart vs hot-swap.** Seamless failover without losing in-flight work (background subagents, running Bash) is not possible with the relaunch approach. Whether swapping the hashed keychain item for a live session takes effect, given Claude's apparent 30 s credential cache, and whether a 401 forces a re-read (`CLAUDE_CODE_OAUTH_401_WAIT_MS` exists in the bundle), is [UNKNOWN]. A spike is needed.
7. **Hook ownership.** Hooks go in user `settings.json` (shared into profiles) or per-launch via `--settings`. Precedence and merge semantics when both define `StopFailure` are not verified. Hooks run as the `claude` process's children, so they inherit `CLAUDE_CONFIG_DIR` and any `SWITCCH_*` env var, which is good for identifying the session and its seat.
8. **Wrapper model.** Per-session isolation needs a long-lived parent (PTY or exec supervisor) to relaunch. cux's PTY layer is large (about 400 lines plus drain/history/attach). Decide whether to PTY-wrap, or only supervise with inherited stdio. Raw exec with inherited stdio loses the ability to inject the replay prompt but is simpler. Not evaluated.
9. **Transcript path derivation.** Use the docs rule (non-alphanumerics -> `-`, truncation at 200 chars plus hash). The hash algorithm is not documented. Prefer taking `transcript_path` from the hook payload over deriving it.
10. **Account identity binding.** `.claude.json` `oauthAccount` must be seeded and kept consistent with the credential, or Claude shows onboarding or the wrong account. An in-session `/login` can re-point a profile to another account (session.py `session_identity_drifted`). The plan needs drift detection.
11. **Platform scope.** All keychain facts above are macOS. Linux/Windows use `.credentials.json` only, so the recipe in section 4 is irrelevant there.
12. **`CLAUDE_CODE_OAUTH_TOKEN` path.** Long-lived tokens from `claude setup-token` would avoid rotation entirely, but whether they support `rate_limits` and the usage endpoint, and whether they count against the same subscription limits, is unknown.
13. **Oddities worth re-verifying in the bundle.** Claude 2.1.318 has `PreModelSwitch`/`PostModelSwitch` hooks and an automatic `fallbackModel` setting. Neither is account failover, but `fallbackModel` interacts with `529`/overload handling and could mask some rate-limit events.
