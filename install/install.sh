#!/bin/bash
# Builds tailpaste, installs it, and loads the launchd agent.
set -euo pipefail

cd "$(dirname "$0")/.."
LABEL=com.abdallah.tailpaste
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
CONFIG="$HOME/.config/tailpaste/config.json"

echo "==> building"
go build -o /tmp/tailpaste .
sudo install -m 0755 /tmp/tailpaste /usr/local/bin/tailpaste

echo "==> config"
# Creates the config with a fresh random token if this is the first run.
/usr/local/bin/tailpaste init

PORT=$(sed -n 's/.*"port"[[:space:]]*:[[:space:]]*\([0-9]*\).*/\1/p' "$CONFIG" 2>/dev/null || true)
PORT=${PORT:-8787}

echo "==> installing $PLIST"
mkdir -p "$HOME/Library/LaunchAgents"
# launchd does not expand ~, so bake the real home directory in.
sed "s|__HOME__|$HOME|g" "install/$LABEL.plist" > "$PLIST"

echo "==> (re)loading the agent"
launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$PLIST"

echo "==> waiting for the daemon on port $PORT"
for _ in $(seq 1 20); do
    if curl -sf "http://127.0.0.1:$PORT/health"; then
        echo "==> installed. Config: $CONFIG   Logs: ~/Library/Logs/tailpaste.log"
        exit 0
    fi
    sleep 0.5
done

echo "daemon did not come up; check ~/Library/Logs/tailpaste.log" >&2
exit 1
