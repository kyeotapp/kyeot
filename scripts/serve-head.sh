#!/bin/sh
# Replaces the live server (launchd app.kyeot.server) with the current commit (HEAD). Backs up the DB and executable first.
set -eu
SRC=$(cd "$(dirname "$0")/.." && pwd)
ROOT=${KYEOT_ROOT:-$SRC} # where data/ and bin/ are
PREFIX=$(git -C "$SRC" rev-parse --show-prefix) # server/ when inside the app repo
TS=$(date +%Y%m%d-%H%M)
W=$(mktemp -d)/w
trap 'git -C "$SRC" worktree remove --force "$W" 2>/dev/null || true' EXIT
git -C "$SRC" worktree add -q --detach "$W" HEAD
(cd "$W/$PREFIX" && go build -ldflags "-X main.version=$(cat VERSION)" -o "$W/kyeot-new" ./cmd/kyeot && go test -count=1 ./internal/store/ ./internal/server/ >/dev/null)
DB="$ROOT/data/kyeot.db"
[ -f "$DB" ] && sqlite3 "$DB" ".backup $ROOT/data/backup-$TS.db" && [ "$(sqlite3 "$ROOT/data/backup-$TS.db" 'PRAGMA integrity_check;')" = ok ]
cp "$ROOT/bin/kyeot" "$ROOT/bin/kyeot.bak-$TS"
cp "$W/kyeot-new" "$ROOT/bin/kyeot"
# if launchd reports re-entry avoidance (141), stop the process and let KeepAlive restart it
launchctl kickstart -k "gui/$(id -u)/app.kyeot.server" 2>/dev/null || pkill -TERM -f "^$ROOT/bin/kyeot$" || true
sleep 6
curl -fsS -m 5 http://127.0.0.1:7700/version >/dev/null && echo "server is back up. backups: data/backup-$TS.db, bin/kyeot.bak-$TS"
