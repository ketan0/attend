---
name: attend
description: Shape and guide computer attention by creating block / friction / nudge rules on domains, paths, and apps. Use when the user wants to manage focus, add intentional friction to distracting sites, set focus-window schedules, block apps during certain hours, or pause/resume their existing rules.
---

# attend

`attend` is a CLI for "attention design" — block, friction, and nudge rules
applied to domains, paths, and macOS apps. The CLI is a thin client over a
local daemon (`attendd`) that persists rules and enforces them.

You (the agent) are the primary caller. The CLI returns JSON on stdout for
every successful command and structured JSON errors on stderr with non-zero
exit codes.

## Always check status first

Before adding or changing rules, run:

```
attend status
```

Returns the current rules, which are active right now, and whether enforcement
is paused. This prevents:
- Creating duplicate / conflicting rules (the daemon will reject them with a
  409 unless you pass `--replace`).
- Adding a rule when an existing one already covers the case.
- Acting while paused (your rule will be saved but not enforced until resume).

If `attend status` exits non-zero, the daemon is unreachable. Tell the user;
do not retry-loop.

## Actions

| Action | When to use | Bypassable? |
|---|---|---|
| `block` | "block X", "I want X off", "make X impossible during deep work" | No — only `attend rm <id>` ends it. |
| `friction` | "make me think before X", "add friction to X", "interrupt me when I open X" | Yes — user passes a challenge, gets a cooldown window. |
| `nudge` | "remind me when I open X", "notify me about X", "I want to notice when..." | N/A — no enforcement, just a notification overlay. |
| `allow` | carve out an exception under a broader block/friction rule | N/A — suppresses other rules. |

Decision rule when the user is ambiguous: **default to `friction --level intent`**.
It surfaces the intent without trapping them behind a hard block.

### Precedence

When multiple rules match the same target/URL, **allow wins**. Otherwise:
`block > friction > nudge`. So `block reddit.com` + `allow reddit.com/r/LocalLLaMA`
means "reddit is blocked except /r/LocalLLaMA." Allow is browser-only for path
carve-outs — `/etc/hosts` is host-level and can't carve out paths.

## Targets

CLI auto-detects target kind from syntax. You can also force it with prefixes.

| Form | Kind | Example |
|---|---|---|
| `twitter.com` | `domain` (system-wide via /etc/hosts + browser) | `attend block twitter.com` |
| `reddit.com/r/all` | `path` (browser only — needs the extension) | `attend friction reddit.com/r/all` |
| `app:Slack` | `app` (macOS app polled and quit on match) | `attend block app:Slack --schedule-json '...'` |
| `domain:foo.com` | force-domain | rarely needed |
| `path:foo.com/x` | force-path | rarely needed |

Apps match macOS's app name **case-sensitively**. "Slack" not "slack".

## Schedule (when the rule is active)

Pick **at most one** of the four scope flags. Omit all four → always active.

| Flag | Format | Example |
|---|---|---|
| `--for` | Go duration | `--for 2h`, `--for 90m`, `--for 45s` |
| `--until` | RFC 3339 timestamp | `--until 2026-05-01T18:00:00-07:00` |
| `--schedule-json` | inline JSON (see schema below) | `--schedule-json '{"tz":"America/Los_Angeles","windows":[...]}'` |
| `--schedule-file` | path to a JSON file with the same schema | `--schedule-file /tmp/sched.json` |

### Recurring schedule JSON schema

```json
{
  "tz": "America/Los_Angeles",
  "windows": [
    {
      "days": ["mon", "tue", "wed", "thu", "fri"],
      "start": "09:00",
      "end": "17:00"
    }
  ]
}
```

- `tz`: IANA timezone name. Required.
- `windows`: at least one. Multiple windows OR together (if any matches, the rule is active).
- `days`: lowercase 3-letter weekdays: `mon tue wed thu fri sat sun`.
- `start` / `end`: `"HH:MM"` 24-hour. `[start, end)` — start is inclusive, end is exclusive.
- If `end <= start`, the window crosses midnight (e.g. `start: "22:00", end: "06:00"` on `["fri"]` runs Friday 22:00 → Saturday 06:00).

## Friction levels

`--level` selects the challenge type. Default: `intent`.

| Level | Challenge | Notes |
|---|---|---|
| `intent` | type 8+ chars of why you're opening this | best default — forces articulation |
| `timer` | wait N seconds (`--timer-seconds 30`) | passive, easiest to pass |
| `phrase` | type a specific phrase (`--phrase "I am opening Twitter on purpose"`) | high friction without a hard block |
| `math` | solve a × b | quick to pass once you start |
| `breath` | breathing countdown (`--timer-seconds 30`) | calmer alternative to `timer` |

Cooldown after passing: default **5 minutes**, set with `--cooldown 1h`.

## Common patterns

### Hard-block during deep work hours, recurring

```bash
attend block twitter.com \
  --schedule-json '{"tz":"America/Los_Angeles","windows":[{"days":["mon","tue","wed","thu","fri"],"start":"09:00","end":"12:00"}]}'
```

### Block reddit, but allow /r/LocalLLaMA

```bash
attend block reddit.com
attend allow reddit.com/r/LocalLLaMA
```

Note: the system-wide /etc/hosts block on `reddit.com` is host-level — it
black-holes ALL of reddit, including /r/LocalLLaMA, at the OS resolver. The
allow only takes effect inside the browser extension. So if the user wants
true path carve-outs, they need the extension installed and Chrome's
"Use Secure DNS" disabled (or the path target enforced from the extension
without relying on /etc/hosts at all).

### One-shot focus block

```bash
attend block youtube.com --for 2h
```

### Evening wind-down (everyday 22:00 → 06:00 next day)

```bash
attend block app:Slack \
  --schedule-json '{"tz":"America/Los_Angeles","windows":[{"days":["mon","tue","wed","thu","fri","sat","sun"],"start":"22:00","end":"06:00"}]}'
```

### Replace an existing rule

```bash
attend block twitter.com --replace --for 4h
# Same target as an existing rule? Without --replace you get a 409.
```

### Pause everything during a meeting, then resume

```bash
attend pause --for 1h
# ... later ...
attend resume
```

## Output handling

- **stdout**: JSON. Always parseable. For `ls`, an array of rules. For other writes, the rule that was just changed. For `status`, the full state envelope.
- **stderr**: human-readable error line + (for daemon errors) a JSON error envelope. Format: `{"error": {"code": "<code>", "message": "..."}}`.
- **exit codes**: `0` success, `1` validation / user error, `2` system / daemon-unreachable.

Common error codes you should handle:

| Code | Meaning | What to do |
|---|---|---|
| `conflict` | A rule already targets the same canonical target | retry with `--replace` only if user intent is replacement |
| `not_found` | No rule with that ID | re-fetch `attend ls` to find current IDs |
| `invalid_rule` | Validation failed | the message names the field; fix it |
| `bad_json` | Malformed payload | check your `--schedule-json` |

## Don'ts

- **Don't add a friction rule when the user wanted a block.** They are not the same. A friction rule still lets them through.
- **Don't create overlapping rules on the same target.** Use `attend update <id>` or `attend block <target> --replace`.
- **Don't pause globally when the user only wanted one rule disabled.** `attend rm <id>` (or `attend update <id> --until <past>` if they may want it back).
- **Don't quote the JSON schedule with newlines/tabs that your shell will mangle.** Pass real newlines via `--schedule-file` if it's complex.
- **Don't assume domain rules apply to in-browser path-level patterns.** They don't (only `path:` targets do). Domain rules block at the network layer, which is browser-agnostic but coarser.

## Reference: full command surface

```
attend block <target> [--for D | --until T | --schedule-json J | --schedule-file F] [--replace]
attend allow <target> [scope flags...] [--replace]
attend friction <target> [--level L] [--cooldown D] [--phrase S] [--timer-seconds N] [scope flags...] [--replace]
attend nudge <target> --message M [scope flags...] [--replace]
attend ls
attend get <id>
attend rm <id>
attend update <id> [--for D | --until T | --schedule-json J | --always | --message M]
attend pause [--for D | --until T]
attend resume
attend status
```

All commands accept `--url <baseURL>` if attendd is on a non-default port
(default: `http://127.0.0.1:7723`).
