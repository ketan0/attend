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

## Injections — persistent page modifications

`attend inject` lets you register persistent userscript-style JS/CSS that the
browser extension applies to every page load matching a URL pattern. This is
the agent-driven equivalent of Tampermonkey: write a script once, have it run
on every reload.

**Two one-time Chrome toggles** are required for JS injections. CSS works
without them. If a user reports "the JS injection didn't fire," the toggles
are the cause 99% of the time:

1. **Developer Mode** — `chrome://extensions/`, top-right toggle.
2. **Allow User Scripts** — `chrome://extensions/` → attend → Details →
   scroll to the toggle. This is a per-extension permission added in Chrome
   ~125; older guides only mention #1.

Without these, `typeof chrome.userScripts === "undefined"` in the
extension's service worker and JS registrations silently no-op. Diagnose by
opening the SW console (Details → "service worker" link) and running
`typeof chrome.userScripts` — if `"undefined"`, the user is missing toggle
#2.

### Match patterns

Patterns use Chrome's match-pattern syntax (the same thing extension
manifests use):

| Pattern | Matches |
|---|---|
| `<all_urls>` | every page |
| `https://github.com/*` | exact host, any path |
| `https://*.github.com/*` | host *and* any subdomain |
| `*://example.com/*` | http or https |
| `https://example.com/api/*/users` | path glob (`*` = any chars in one segment) |
| `file:///*` | local files |

The daemon validates patterns at submission. Malformed patterns → 400.

### Commands

```
attend inject add --match <pattern> [--match <pattern>...] [--exclude <pattern>...]
                  (--js <code> | --js-file <path>) [(--css <code> | --css-file <path>)]
                  [--name <label>] [--run-at document_start|document_end|document_idle]
                  [--world MAIN|ISOLATED] [--all-frames] [--id <stable-id>]
attend inject ls
attend inject get <id>
attend inject rm <id>
```

- `--js` / `--css`: inline payload (string).
- `--js-file` / `--css-file`: file path. Use `-` to read stdin.
- `--id`: pass a stable ID to upsert (overwrites the existing injection with
  the same ID instead of creating a new one).
- `--run-at`: defaults to `document_idle`. Use `document_start` for true
  pre-render injection (CSS dispatch is best-effort early; JS via
  `chrome.userScripts` is native).
- `--world`: defaults to `MAIN` (access to page globals like
  `window.React`). `ISOLATED` runs in a separate JS realm.

### Examples

```bash
# Hide a distracting sidebar on GitHub
attend inject add \
  --match 'https://github.com/*' \
  --css '.js-feed-item-component { display: none !important; }' \
  --name "github calmer"

# Auto-skip YouTube ads
attend inject add \
  --match 'https://*.youtube.com/*' \
  --js-file ./yt-skipper.js \
  --run-at document_end

# Inject from stdin (handy for one-shot agent edits)
echo 'document.title = "Focus";' | attend inject add \
  --match 'https://example.com/*' --js-file -

# Update by re-using the same id
attend inject add --id inj_my_script --match 'https://example.com/*' --js 'v2'
```

### Don'ts

- **Don't use injections to enforce blocking.** That's what `block` rules
  are for. Injections are page-modifications.
- **Don't paste untrusted code.** Injections run with full page access in
  MAIN world.
- **Don't assume run_at: document_start is instantaneous for CSS.** CSS
  goes through a dispatcher on navigation commit; JS is registered natively
  with chrome.userScripts and is truly synchronous.

## Page RPC — inspecting & poking live tabs

`attend page` lets you reach into a live browser tab from the CLI:

| Command | Purpose |
|---|---|
| `attend page tabs` | List every open tab as JSON: `[{tab_id, url, title, active, window_id}]`. |
| `attend page dump [selector]` | Dump the page's outerHTML to a temp file. Returns `{tab_id, url, title, file, bytes}`. |
| `attend page exec [selector] (--js S \| --js-file F)` | Run a one-shot JS expression/function body in the tab and print its JSON-serialized return value. |

**Tab selector flags** (apply to `dump` and `exec`; default is `--active`):

| Flag | Picks |
|---|---|
| `--active` | The active tab in the focused window (default). |
| `--tab-id N` | A specific tab id from `attend page tabs`. |
| `--url-pattern P` | The first tab whose URL matches a Chrome match pattern, e.g. `https://github.com/*`. |

### Workflow: finding the right selector for an injection

The intended loop when you need to write an injection but don't know the
DOM:

```bash
# 1. Find the tab that has the page you want to modify.
attend page tabs | jq '.[] | select(.url|test("youtube.com"))'

# 2. Dump its HTML to a temp file.
attend page dump --url-pattern 'https://*.youtube.com/*'
# → {"tab_id": 42, "url": "...", "file": "/tmp/attend-page-42-1747...html", "bytes": 5283194}

# 3. Search the dump for the section you want to target. HTML is megabytes;
# stay in the file, don't slurp the whole thing into context.
grep -o 'ytd-[a-z-]*shorts[a-z-]*' /tmp/attend-page-42-1747...html | sort -u

# 4. Pick a selector, register the injection.
attend inject add \
  --match 'https://*.youtube.com/*' \
  --css 'ytd-rich-shelf-renderer[is-shorts] { display: none !important; }'

# 5. Iterate: refresh the page, run `attend page exec` to verify the
# selector hides exactly what you expected.
attend page exec --url-pattern 'https://*.youtube.com/*' \
  --js 'document.querySelectorAll("ytd-rich-shelf-renderer[is-shorts]").length'
```

### Exec specifics

- The string passed to `--js` is wrapped so a bare expression returns its
  value: `document.title` works, no need to write `return document.title`.
  Function bodies / multi-statement code also work — the wrapper falls back
  to executing as statements when the expression form fails to parse.
- Return values are JSON-serialized via `chrome.scripting.executeScript`.
  DOM nodes and functions are NOT serializable — project them first
  (`document.querySelectorAll(...).length`, `el.outerHTML`, `el.getAttribute(...)`).
- `--world` defaults to `MAIN` (page realm; sees `window.React` etc.). Use
  `ISOLATED` if you want a fresh JS context that can't see page globals.

### Don'ts

- **Don't `attend page exec` arbitrary string interpolation from untrusted
  input.** The code is `eval`'d in the page world; you're already trusting
  the daemon's host, but agents should not pipe user-supplied strings into
  `--js` without escaping.
- **Don't put HTML dumps into your context window.** Page HTML can be many
  MB. Use the file path and grep/sed/jq.
- **Don't expect dump/exec on `chrome://` or `about:` pages.** Chrome
  refuses extension scripting on privileged URLs; the command will error.
- **Don't assume the extension is reachable.** Both commands hang on the
  daemon → extension hop and time out (default 30s). Failure modes: 504
  from the daemon means the extension didn't pick the job up at all (SW
  asleep, no Chrome window, daemon → extension link broken).

### Blast radius note

Any process on this Mac that can reach `127.0.0.1:7723` can dump or exec
in any of your open tabs — including authenticated session state (your
Gmail inbox, your bank, etc.). This matches attend's existing trust model
(single-user macOS, localhost daemon, no auth) but is meaningfully bigger
than just "block a domain." If the threat model changes, the listener
needs auth or a Unix socket with mode 0600.

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
attend inject add --match P [--match P...] [--exclude P...] (--js S | --js-file F) [--css S | --css-file F] [--name N] [--run-at R] [--world W] [--all-frames] [--id ID]
attend inject ls
attend inject get <id>
attend inject rm <id>
attend page tabs [--timeout D]
attend page dump [--active | --tab-id N | --url-pattern P] [--out FILE] [--timeout D]
attend page exec [--active | --tab-id N | --url-pattern P] (--js S | --js-file F) [--world MAIN|ISOLATED] [--timeout D]
```

All commands accept `--url <baseURL>` if attendd is on a non-default port
(default: `http://127.0.0.1:7723`).
