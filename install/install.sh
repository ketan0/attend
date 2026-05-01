#!/usr/bin/env bash
# install.sh — build attend, install attendd as a LaunchAgent, install the
# hosts-edit sudoers rule, and load it.
#
# Run from the libraries/attend/ directory:
#   ./install/install.sh
#
# This is intentionally a single shell script (not a Makefile target) so the
# install steps are explicit and reviewable.

set -euo pipefail

DIR="$(cd "$(dirname "$0")/.." && pwd)"
BIN_DIR="${HOME}/.local/bin"
CONF_DIR="${HOME}/.config/attend"
PLIST_DIR="${HOME}/Library/LaunchAgents"
PLIST_PATH="${PLIST_DIR}/com.attend.attendd.plist"

mkdir -p "${BIN_DIR}" "${CONF_DIR}" "${PLIST_DIR}"

echo "[attend] building binaries -> ${BIN_DIR}"
( cd "${DIR}" && go build -o "${BIN_DIR}/attendd" ./cmd/attendd )
( cd "${DIR}" && go build -o "${BIN_DIR}/attend" ./cmd/attend )

echo "[attend] writing ${PLIST_PATH}"
sed \
    -e "s#{{ATTENDD_BIN}}#${BIN_DIR}/attendd#g" \
    -e "s#{{HOME}}#${HOME}#g" \
    "${DIR}/install/com.attend.attendd.plist.tmpl" > "${PLIST_PATH}"

echo "[attend] (re)loading LaunchAgent"
launchctl unload "${PLIST_PATH}" 2>/dev/null || true
launchctl load "${PLIST_PATH}"

cat <<EOF

attend is installed. Quick sanity check:

  attend status

To uninstall:
  launchctl unload ${PLIST_PATH}
  rm ${PLIST_PATH} ${BIN_DIR}/attend ${BIN_DIR}/attendd

NOTE: editing /etc/hosts requires root. The simplest options:
  - Run attendd as root (LaunchDaemon, not LaunchAgent). To do that, copy
    the plist to /Library/LaunchDaemons/ and reload.
  - Or, allow attendd to write hosts via a sudoers rule for a privileged
    helper. See README for the helper-binary pattern.

By default this script installs as a LaunchAgent (user-level), which means
domain blocking via /etc/hosts will be a no-op until you elevate. App
blocking, friction (browser extension), and the rule API all work fine
without root.
EOF
