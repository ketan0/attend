#!/usr/bin/env bash
# Build the AttendFriction macOS helper.
#
# Single-file SwiftUI app — no Xcode project required. Output: ./AttendFriction
# Install: cp AttendFriction ~/.local/bin/
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
swiftc -O \
    -framework SwiftUI \
    -framework AppKit \
    "${DIR}/main.swift" \
    -o "${DIR}/AttendFriction"
echo "built: ${DIR}/AttendFriction"
