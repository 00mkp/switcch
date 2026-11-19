# switcch

Use several Claude subscriptions from Claude Code as if they were one. When the account a chat is on runs low, switcch moves that chat to your next account **and keeps the same conversation** — no logging out, no copying context, no second setup.

- **One setup.** Every account uses your one real `~/.claude`: same settings, skills, `CLAUDE.md`, memories, plugins and chat history.
- **Many chats at once.** Each chat runs under the account you chose when you started it, side by side with chats on other accounts.
- **Failover you configure once.** `switcch fallback work personal` means chats you start on `work` continue on `personal` when `work` runs out. An account with no fallback is pinned: its chats never move.
- **Nothing moves mid-thought.** Chats switch at the end of a turn or on your next message, never in the middle of one.

macOS only for now.

## Install

Requires Go 1.23 or newer, and Claude Code (`claude` on your `PATH`).

```sh
go install github.com/00mkp/switcch/cmd/switcch@latest
```

Or build from a clone:

```sh
git clone https://github.com/00mkp/switcch.git
cd switcch
go build -o ~/.local/bin/switcch ./cmd/switcch
```

Make sure the install directory (`$(go env GOPATH)/bin`, or `~/.local/bin`) is on your `PATH`.

## Quick start

Register each account once. switcch captures whichever account plain `claude` is logged in to:

```sh
claude            # /login as your first account, then exit
switcch add --alias personal
claude            # /login as your second account, then exit
switcch add --alias work
```

Say where chats go when an account runs out, and install the hooks that let switcch see limits:

```sh
switcch fallback work personal
switcch setup
```

Start chats:

```sh
switcch work                  # a chat on work; moves to personal if work runs out
switcch personal              # a chat on personal; pinned, never moves
switcch work -- --resume      # pick an existing chat (from any account) and continue it on work
switcch list                  # every account's usage, with its live chats underneath
```

Anything after `--` goes to `claude` unchanged.

## How failover works

- **Before the limit (default).** switcch checks the active account's usage about once a minute (readings are shared between chats, so one can be a few minutes old). Past 90% of its 5-hour or 7-day window, the chat is marked to move: a running turn finishes first, then the chat continues on the fallback account. An idle chat moves when you send your next message, and that message goes to the new account word for word.
- **At the limit.** If an account hits its limit anyway, the chat moves at once. Other chats on that account move on their next message.
- **Same conversation.** switcch restarts Claude with `--resume` on the new account, with a short note as the first message, e.g. `[switcch] work hit its usage limit — continuing on personal.`
- **Model caps.** If only one model is capped (say Opus), the chat first continues on Sonnet on the same account, and only switches accounts if that is capped too.
- **Nowhere to go.** If every account a chat may use is out, it behaves like plain Claude Code. Turn on `waitForReset` to have switcch continue the chat by itself when the first account resets.
- **Background work.** If a background task from the turn is still running when the turn ends, the chat waits for your next message instead of restarting under it.

One-off overrides when starting a chat: `--pin` (never move), `--fallback a,b` (use these instead), `--trigger`, `--threshold`, `--mode`.

## Configuration

`switcch config show` prints the current values; `switcch config set <key> <value>` changes one.

| Key | Default | Meaning |
|---|---|---|
| `trigger` | `proactive` | `proactive`: move chats before the limit, at `threshold`. `reactive`: only when a limit is actually hit. |
| `threshold` | `90` | Percent (1–100) of the 5-hour or 7-day window, whichever is higher. |
| `mode` | `auto` | `auto` moves chats; `advisory` only shows a macOS notification. |
| `idleMigrateMinutes` | `0` | Above 0, also move a marked chat that has been idle this long. |
| `modelFallback` | `sonnet` | Models to try on the same account when one model is capped. `""` turns it off. |
| `waitForReset` | `false` | When every account is out, wait for the first reset and continue the chat. |
| `eventCommand` | off | A command run when a chat hits a limit or runs out of accounts (see below). |
| `notice` | `[switcch] {from} hit its usage limit — continuing on {to}.` | First message after a move. |
| `modelNotice` | `[switcch] {account} hit its {from} limit — continuing with {to}.` | First message after a model fallback. |
| `resumeMessage` | `[switcch] The usage limit has reset — continue where you left off.` | Sent when a waiting chat resumes. |

Config lives in `~/.config/switcch/config.json`.

## For other tools

`switcch status --json` describes every account and its live chats:

```json
{
  "schema": 1,
  "accounts": [
    {
      "slot": 1, "email": "you@work.com", "alias": "work", "limited": false,
      "fiveHour": {"utilization": 42, "resetsAt": "2026-11-18T19:00:00Z"},
      "sevenDay": {"utilization": 63, "resetsAt": "2026-11-21T08:00:00Z"},
      "models": [{"model": "Opus", "percent": 80, "active": true}],
      "sessions": [
        {"pid": 4242, "cwd": "/Users/you/code", "started": "2026-11-18T15:02:11Z",
         "sessionId": "6f1c…", "pool": [1, 2]}
      ]
    }
  ]
}
```

`limited` is true when a chat on that account hit a hard limit in the last 30 minutes. `models` lists per-model caps (`[]` when there are none). `pool` lists the chat's accounts (by slot) in failover order. `accounts`, `models`, `sessions` and `pool` are always lists, never `null`.

Some fields can be missing: `alias` when the account has none; `fiveHour` and `sevenDay` when usage couldn't be read, in which case `usageError` says why; a window's `resetsAt` when no reset time is known; and a session's `sessionId` until Claude has reported it (after the first message).

With `eventCommand` set, switcch runs it through `sh -c` and writes one line of JSON to its standard input:

```json
{"session_id": "6f1c…", "account": "you@work.com", "reason": "limit_hit", "timestamp": "2026-11-18T15:40:00Z"}
```

`reason` is `limit_hit` (the chat's account hit a limit) or `pool_exhausted` (no account the chat may use can take it). The command's output is discarded, and it is stopped after 10 seconds. For example: `switcch config set eventCommand 'cat >> ~/switcch-events.jsonl'`.

## Uninstall

```sh
switcch uninstall-hooks         # removes exactly the hooks switcch added
switcch remove work             # repeat for each account: deletes switcch's copy of its login and its profile folder
rm -rf ~/.config/switcch ~/.local/share/switcch
rm "$(command -v switcch)"
```

A few keychain items remain: `switcch-master-key`, and one `Claude Code-credentials-…` item per account profile. Delete them in Keychain Access if you like.

## License

MIT — see [LICENSE](LICENSE).
