#!/bin/sh
# Manual check against a real Hermes gateway: approve, deny, clarify, stop while waiting.
# Uses an in-memory deck server, never a running kyeot server. Each case is one short model run.
#   HERMES_URL      required, e.g. http://127.0.0.1:8642. Runs leave deck-<task> sessions in that Hermes
#   API_SERVER_KEY  default: read from ${HERMES_HOME:-~/.hermes}/.env
#   KYEOT_LIVE_PROMPT  replaces the task text that should make Hermes ask
# Hermes must actually ask: approvals.mode manual. In smart mode a helper model lets harmless
# commands through and the cases fail with "ended before asking"; use a test gateway
# (HOME=<temp dir> hermes gateway run) with manual approvals rather than changing your own.
set -eu
cd "$(dirname "$0")/.."
exec go test -tags hermeslive -count=1 -v -timeout 20m -run '^TestLive' ./internal/hermesbridge/ "$@"
