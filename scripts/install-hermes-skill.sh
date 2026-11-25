#!/bin/sh
# Installs the deck-ui skill and example files into Hermes. An existing SKILL.md with different content is backed up, then overwritten.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
src="$root/integrations/hermes/deck-ui/SKILL.md"
dest_dir="${HERMES_HOME:-$HOME/.hermes}/skills/productivity/deck-ui"
dest="$dest_dir/SKILL.md"

mkdir -p "$dest_dir"
# Also place the example files the skill points to (examples/netwatch etc.) under the skill folder
rm -rf "$dest_dir/examples"
cp -R "$root/integrations/hermes/examples" "$dest_dir/examples"
echo "examples: $dest_dir/examples"
if [ -f "$dest" ]; then
  if cmp -s "$src" "$dest"; then
    echo "already identical: $dest"
    exit 0
  fi
  backup="$dest.bak-$(date +%Y%m%d-%H%M%S)"
  cp "$dest" "$backup"
  echo "backup: $backup"
fi
cp "$src" "$dest"
echo "installed: $dest"
