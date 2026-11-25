#!/bin/sh
# Creates the release signing key (ed25519) and prints the public key line. If it already exists, prints only the public key.
# --backup creates a spare key used only if the usual key is lost. Back it up, then delete it from this Mac.
set -eu

KEY="${KYEOT_RELEASE_KEY:-$HOME/.config/kyeot/release-signing/id_ed25519}"
BACKUP=""
if [ "${1:-}" = --backup ]; then
	BACKUP=1
	KEY="${KYEOT_RELEASE_BACKUP_KEY:-$(dirname "$KEY")/backup_ed25519}"
fi
DIR=$(dirname "$KEY")

command -v ssh-keygen >/dev/null 2>&1 || { echo "ssh-keygen is required" >&2; exit 1; }

if [ ! -f "$KEY" ]; then
	umask 077
	mkdir -p "$DIR"
	chmod 700 "$DIR"
	ssh-keygen -q -t ed25519 -N '' -C kyeot-release -f "$KEY" >/dev/null
	chmod 600 "$KEY"
	echo "created a new signing key: $KEY" >&2
	if [ -n "$BACKUP" ]; then
		echo "this is the spare key. Back it up in a password manager, then delete it from this Mac: rm $KEY" >&2
		echo "put the public key ($KEY.pub) into RELEASE_PUBKEYS in install.sh" >&2
	else
		echo "back up the private key file in a password manager. If it is lost, switch keys in a new release signed with the spare key" >&2
	fi
fi
[ -f "$KEY.pub" ] || ssh-keygen -y -f "$KEY" > "$KEY.pub"
cat "$KEY.pub"
