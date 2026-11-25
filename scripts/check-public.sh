#!/bin/sh
# Looks in tracked files for values that must not be public. Exits 1 if any are found.
#   sh scripts/check-public.sh
#   KYEOT_PUBLIC_DENY=<file> sh scripts/check-public.sh   one extra regex (ERE) per line to search for
# For a legitimate example, append public-ok to the end of that line (# public-ok, // public-ok).
set -eu
cd "$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"

patterns=$(mktemp)
trap 'rm -f "$patterns"' EXIT
cat >"$patterns" <<'EOF'
home path	/(Users|home)/[a-z][A-Za-z0-9._-]*
launchd label	(^|[^A-Za-z0-9_.])local\.[a-z][a-z0-9_-]*\.[a-z]
email	[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}
private ip	(^|[^0-9.])(10\.[0-9]{1,3}|192\.168|172\.(1[6-9]|2[0-9]|3[01]))\.[0-9]{1,3}\.[0-9]{1,3}
tailscale ip	(^|[^0-9.])100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.[0-9]{1,3}\.[0-9]{1,3}
anthropic key	sk-ant-
private key	-----BEGIN [A-Z ]*PRIVATE KEY
github token	gh[pousr]_[A-Za-z0-9]{20,}
EOF
if [ -n "${KYEOT_PUBLIC_DENY:-}" ]; then
  grep -vE '^[[:space:]]*(#|$)' "$KYEOT_PUBLIC_DENY" | sed 's/^/deny list	/' >>"$patterns"
fi

self=scripts/check-public.sh
found=0
while IFS='	' read -r name re; do
  hits=$(git ls-files -z -- . ":!$self" | xargs -0 grep -nIE -- "$re" 2>/dev/null |
    grep -vE 'public-ok[[:space:]]*(-->|\*/)?[[:space:]]*$' |
    { [ "$name" = email ] && grep -viE 'noreply|@([A-Za-z0-9-]+\.)*example\.(com|org|net)' || cat; } || true)
  [ -z "$hits" ] && continue
  found=1
  printf '%s\n' "$hits" | sed "s/^/[$name] /"
done <"$patterns"

if [ $found -ne 0 ]; then
  echo "public check failed: remove the value, or if it is a legitimate example append public-ok to the end of the line" >&2
  exit 1
fi
echo "public check passed"
