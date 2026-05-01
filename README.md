# attend

`attend`is a tool for shaping the quality and direction of your attention while using the computer.

rules can be applied to domains, paths, and apps. Designed to be
driven by AI agents as well as humans.

## Status

v0.1. Single-user, macOS-only. Working: block, friction, nudge, allow,
recurring schedules, browser-side path-level enforcement, /etc/hosts-based
domain enforcement, app polling/quit, native-app friction (SwiftUI overlay
launched into the user's GUI session, with daemon-side cooldowns).

## Components

- **`attend`** — CLI client (Go). Talks to the daemon over localhost HTTP.
  All output is JSON, all errors are structured.
- **`attendd`** — long-running daemon (Go). Owns the rule store; enforces
  via `/etc/hosts` and a macOS app-polling loop. Run as a root LaunchDaemon
  for system-wide effect.
- **Browser extension** — Chromium MV3 extension. Required for path-level
  rules and for enforcing block/friction at the page-load layer (which works
  even when Chrome's Secure DNS bypasses `/etc/hosts`).
- **`AttendFriction`** — minimal SwiftUI helper for native-app friction
  screens. Spawned by the daemon via `launchctl asuser` when a friction
  rule matches a running native app; posts the result back to the daemon
  to set a cooldown.

## Install

Two flavors: user-level (no sudo, but `/etc/hosts` writes will fail) and
system-wide (LaunchDaemon as root, full enforcement).

```sh
# Build + install as a user LaunchAgent. /etc/hosts blocks will be no-ops.
./install/install.sh

# Build + install as a root LaunchDaemon. /etc/hosts writes work.
sudo ./install/install-system.sh
```

After install, load the browser extension at `chrome://extensions/` →
Developer mode → Load unpacked → select the `extension/` directory.

If your browser ships with DNS-over-HTTPS on (Chrome does), turn it off at
`chrome://settings/security` so `/etc/hosts` rules apply. Or rely on the
extension and skip the system-wide daemon entirely.

## Quick start

```sh
# Block, hard
attend block twitter.com
attend block app:Slack --schedule-json '{"tz":"America/Los_Angeles","windows":[{"days":["mon","tue","wed","thu","fri"],"start":"09:00","end":"17:00"}]}'

# Friction (default level: intent — type why you're opening this)
attend friction reddit.com --cooldown 10m

# Nudge
attend nudge youtube.com --message "is this the move?"

# Carve-out: block reddit, allow one subreddit through the browser
attend block reddit.com
attend allow reddit.com/r/LocalLLaMA

# One-shot
attend block youtube.com --for 2h

# Pause everything for a meeting
attend pause --for 1h
attend resume

# Always check state first (especially from agents)
attend status
attend ls
attend rm <id>
```

## Architecture

```
attend (CLI) ──HTTP──► attendd ──┬──► /etc/hosts        (domain blocks)
                                 ├──► osascript quit    (app blocks)
                                 └──► state file        (rules + settings)
                                          ▲
                                          │
                              browser extension polls /v1/status,
                              renders block / friction overlays at
                              document_start time
```

Rule precedence: `allow > block > friction > nudge`. When a path-allow rule
falls under a domain block, the daemon drops the domain from `/etc/hosts`
so the browser can load the page and enforce the carve-out itself.

## Limitations

- macOS only. The daemon assumes `/etc/hosts`, `osascript`, and `launchd`.
- Only Chromium-family browsers tested. Firefox manifest would need a
  separate build.
- No HTTPS interception, so path-level rules require the browser extension.
  System-wide path-level blocking would require a local proxy with a
  trusted CA, which is intentionally out of scope.
- Block rules on `domain:reddit.com` cover `www.reddit.com` (subdomain),
  but `/etc/hosts` only sinkholes the bare host. Apps that hit
  `www.reddit.com` directly are not affected by a `reddit.com` rule.

## Development

See `CLAUDE.md` for layout, build/test, and hot-reload workflow. The
`SKILL.md` file is the agent-facing usage guide; symlink it into
`~/.claude/skills/attend/SKILL.md` (or your IDE's equivalent) to make it
discoverable.

## License

MIT. See `LICENSE`.
