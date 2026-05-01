# Working on attend

This file is for AI coding agents. Read it before making changes. The
human-facing entry point is `README.md`; the user-facing usage guide for
agents *consuming* attend is `SKILL.md`. This document is for agents
*building on* attend.

## Layout

```
cmd/
  attend/         CLI client (cobra). Emits JSON on stdout, structured
                  errors on stderr. Talks to attendd over HTTP.
  attendd/        Long-running daemon. main is thin — wiring lives in
                  internal/daemon.

internal/
  rules/          Pure types and operations: Rule, Schedule, Target,
                  conflict detection, the SystemBlocks helper that turns
                  a rule list into "what to enforce now," and the
                  PickEffective helper for in-browser precedence.
                  No I/O, no time.Now (clocks injected). High coverage.
  store/          JSON-backed rule store with atomic writes and an
                  optional change hook. Tests use t.TempDir.
  server/         HTTP API. New(store) returns a *Server; Handler() gives
                  you an http.Handler. Tests via httptest.
  client/         Go SDK over the HTTP API. Used by the CLI and tests.
  hosts/          /etc/hosts editor. Maintains a marker-delimited
                  attend-managed block; never touches user content.
                  FS interface so tests use an in-memory fake.
  appmon/         macOS app polling and quit. Lister/Quitter interfaces
                  with osascript-backed implementations and FakeLister/
                  FakeQuitter for tests.
  daemon/         Wires it all together. Run() blocks; enforce() is the
                  per-tick (and per-poke) reconciler.

extension/        Chromium MV3 extension. background.js polls
                  /v1/status into chrome.storage.local; content.js reads
                  storage at document_start and renders overlays.
                  match.js holds the URL→rule matcher used in both the
                  extension and node tests.

swift/AttendFriction/   Minimal SwiftUI native friction screen. Single
                        main.swift compiled with build.sh. Not yet
                        wired to the daemon.

install/          install.sh (user LaunchAgent) and install-system.sh
                  (root LaunchDaemon). Plist templates beside them.
```

## Conventions

- **Pure logic in `internal/rules/`**. No I/O, no `time.Now()` — clocks
  pass through as parameters. Anything new that's deterministic and
  hashable belongs here.
- **I/O behind interfaces**. The hosts editor takes an `FS`. The app
  monitor takes a `Lister` + `Quitter`. Tests inject fakes; production
  uses the OS variants.
- **JSON contract is load-bearing**. Agents parse the CLI's stdout and
  the daemon's HTTP responses. Don't change field names or add optional
  fields without updating SKILL.md and the CLI tests.
- **Errors are structured**. `apiError{code, message}` on the wire,
  `client.APIError` in Go. The CLI maps status to exit codes (1 user,
  2 system).
- **No emojis in code, comments, or docs.** Do not add emojis to files
  unless the user explicitly asks.
- **Comments narrate WHY, not WHAT.** No comments at all when the code
  is self-explanatory. No comments referring to the current task or
  PR — those rot.

## Build and test

```sh
go build ./...                              # everything
go test ./... -race                         # all Go tests with race detector
node --test extension/match.test.js         # extension matcher tests
swift/AttendFriction/build.sh               # native friction app
```

## Install

```sh
./install/install.sh                  # user LaunchAgent (no /etc/hosts)
sudo ./install/install-system.sh      # root LaunchDaemon (full enforcement)
```

The system installer migrates an existing user-level rule store
(`~/.config/attend/rules.json`) into `/var/lib/attend/rules.json` so
existing rules survive the transition.

## Hot-reloading changes

You don't need a full reinstall after every edit.

```sh
# Daemon code (cmd/attendd, internal/daemon, internal/server, internal/store,
# internal/hosts, internal/appmon, internal/rules):
go build -o /tmp/attendd-new ./cmd/attendd
sudo install -m 0755 /tmp/attendd-new /usr/local/bin/attendd
sudo launchctl kickstart -k system/com.attend.attendd

# CLI only (cmd/attend, internal/client):
go build -o /tmp/attend-new ./cmd/attend
sudo install -m 0755 /tmp/attend-new /usr/local/bin/attend
# (no daemon restart needed)

# Browser extension (extension/*.js, *.css, manifest.json):
# 1. chrome://extensions/ → click the reload arrow on attend
# 2. Cmd+Shift+R any tab where you want the new content script to apply
# (Existing tabs keep running the previously-injected content script
# until they navigate.)

# Swift friction app:
swift/AttendFriction/build.sh
# Output: swift/AttendFriction/AttendFriction
```

After kickstart, the daemon enforces immediately. Rule changes via the API
are picked up within ~30ms thanks to the store change hook → daemon poke
channel; the 5-second tick is just a safety net.

## Adding things

Add to all of these in lockstep — agents reading SKILL.md need to find the
new feature, and the CLI/extension need to support it.

| Adding... | Touch |
|---|---|
| A new action | `rules/types.go` (const + Valid + Validate), `cmd/attend/main.go` (new sub-command + tests), `extension/content.js` (precedence + render), `SKILL.md` (Actions table + examples). |
| A new target kind | `rules/types.go` (TargetKind + Valid + Canonical), `internal/hosts` or `appmon` (whichever enforces), `extension/match.js` `targetMatches`, `cmd/attend/main.go` `parseTarget`. |
| A new friction level | `rules/types.go` (FrictionLevel const + Valid), `swift/AttendFriction/main.swift` (render view), `extension/content.js` (renderXxx function), SKILL.md. |
| A new schedule mode | `rules/types.go` (ScheduleKind + Validate), `rules/schedule.go` (IsActive case), `cmd/attend/main.go` flag wiring, SKILL.md. |

## Testing approach

- Pure logic gets exhaustive table tests. The schedule evaluator is the
  best example.
- I/O packages get tests with injected fakes. See `appmon` for the Lister/
  Quitter pattern; see `hosts` for the FS pattern.
- The HTTP API is tested via `httptest.NewServer`. The client is tested
  the same way.
- The daemon's enforce loop has integration tests that drive it through
  state transitions (add rule → poke → /etc/hosts updated).
- The extension has node tests for `targetMatches` and `pickEffective`.
  Run with `node --test extension/match.test.js`.

## What NOT to do

- Don't add a comment that narrates *what* the next line does. Names tell
  you that. Comment only when there's a non-obvious *why*.
- Don't mock the rule store in tests when `t.TempDir() + store.Open` is
  cheap and exercises the real code.
- Don't break the JSON output contract for the CLI without updating
  `SKILL.md` and the CLI integration tests in the same change.
- Don't add backwards-compat shims for renames — rename and update all
  call sites.
- Don't shell out from Go for things the standard library handles.
  Do shell out for macOS-specific glue (`osascript`, `launchctl`).
- Don't run `sudo` in tests. The system installer is the only place that
  needs root, and it's a script, not a Go program.

## Privilege model

The system installer puts attendd in `/Library/LaunchDaemons/` running as
root. It owns `/var/lib/attend/rules.json` (root-writable) and listens on
`127.0.0.1:7723` without authentication — the threat model assumes
single-user macOS where any user-space process can already read your
files. If that assumption changes, swap the TCP listener for a Unix
socket with mode 0600.

## Releasing

There is no release process yet. Push to `main`; users `git pull` and
re-run `install-system.sh`.
