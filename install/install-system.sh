#!/usr/bin/env bash
# install-system.sh — install attendd as a root LaunchDaemon so it can write
# /etc/hosts. Use this when you want true system-wide network blocking.
#
# Run from libraries/attend/:   sudo ./install/install-system.sh
#
# Idempotent: safe to re-run after `git pull`.

set -euo pipefail

if [[ $EUID -ne 0 ]]; then
    echo "this script writes to /Library/LaunchDaemons and /usr/local/bin"
    echo "please re-run with sudo:  sudo $0"
    exit 1
fi

DIR="$(cd "$(dirname "$0")/.." && pwd)"
BIN_DIR="/usr/local/bin"
STATE_DIR="/var/lib/attend"
PLIST_PATH="/Library/LaunchDaemons/com.attend.attendd.plist"
LOG_PATH="/var/log/attendd.log"
USER_PLIST_PATH="${SUDO_USER:+/Users/${SUDO_USER}}/Library/LaunchAgents/com.attend.attendd.plist"
USER_STORE_PATH="${SUDO_USER:+/Users/${SUDO_USER}}/.config/attend/rules.json"

REAL_USER="${SUDO_USER:-$(whoami)}"

# 1. Unload any pre-existing user LaunchAgent so the two don't fight over the
#    same TCP port.
if [[ -f "$USER_PLIST_PATH" ]]; then
    echo "[attend] unloading existing user LaunchAgent for ${REAL_USER}"
    sudo -u "$REAL_USER" launchctl unload "$USER_PLIST_PATH" 2>/dev/null || true
    rm -f "$USER_PLIST_PATH"
fi

# 2. Build binaries. Build as the real user (so Go's module/build cache lives
#    in their home, not root's), then move into root-owned ${BIN_DIR} as root.
#    sudo -H is critical: without it, HOME stays as /var/root and Go can't
#    use the user's GOPATH/build cache under /var/folders.
echo "[attend] building binaries"
mkdir -p "$BIN_DIR"
TMPBUILD="$(sudo -u "$REAL_USER" -H mktemp -d)"
sudo -u "$REAL_USER" -H bash -c "cd '${DIR}' && go build -o '${TMPBUILD}/attendd' ./cmd/attendd"
sudo -u "$REAL_USER" -H bash -c "cd '${DIR}' && go build -o '${TMPBUILD}/attend' ./cmd/attend"
install -m 0755 "${TMPBUILD}/attendd" "${BIN_DIR}/attendd"
install -m 0755 "${TMPBUILD}/attend" "${BIN_DIR}/attend"

# AttendFriction (SwiftUI native friction screen).
echo "[attend] building AttendFriction"
sudo -u "$REAL_USER" -H bash -c "cd '${DIR}/swift/AttendFriction' && ./build.sh"
install -m 0755 "${DIR}/swift/AttendFriction/AttendFriction" "${BIN_DIR}/AttendFriction"

rm -rf "$TMPBUILD"
echo "[attend] installed -> ${BIN_DIR}/{attend,attendd,AttendFriction}"

# 3. Set up state directory (root-owned, since the daemon runs as root).
mkdir -p "$STATE_DIR"
chmod 0755 "$STATE_DIR"
touch "$LOG_PATH"
chmod 0644 "$LOG_PATH"

# 4. Migrate any existing user-level rule store so the user's existing rules
#    survive the transition.
if [[ -n "${SUDO_USER:-}" && -f "$USER_STORE_PATH" && ! -f "${STATE_DIR}/rules.json" ]]; then
    echo "[attend] migrating ${USER_STORE_PATH} -> ${STATE_DIR}/rules.json"
    cp "$USER_STORE_PATH" "${STATE_DIR}/rules.json"
    chmod 0644 "${STATE_DIR}/rules.json"
fi

# 5. Install plist. LaunchDaemons must be owned by root:wheel mode 0644.
echo "[attend] writing ${PLIST_PATH}"
cp "${DIR}/install/com.attend.attendd.system.plist.tmpl" "$PLIST_PATH"
chown root:wheel "$PLIST_PATH"
chmod 0644 "$PLIST_PATH"

# 6. (Re)load.
echo "[attend] (re)loading LaunchDaemon"
launchctl bootout system "$PLIST_PATH" 2>/dev/null || true
launchctl bootstrap system "$PLIST_PATH"
launchctl kickstart -k system/com.attend.attendd 2>/dev/null || true

cat <<EOF

attend installed as a system LaunchDaemon.

binaries:   ${BIN_DIR}/attend, ${BIN_DIR}/attendd
state:      ${STATE_DIR}/rules.json
log:        ${LOG_PATH}
plist:      ${PLIST_PATH}

Quick sanity check (no sudo needed for the CLI):
  attend status

To uninstall:
  sudo launchctl bootout system ${PLIST_PATH}
  sudo rm ${PLIST_PATH}
  sudo rm -rf ${STATE_DIR}
  sudo rm ${BIN_DIR}/attend ${BIN_DIR}/attendd

EOF
