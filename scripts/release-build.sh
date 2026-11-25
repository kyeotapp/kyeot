#!/bin/sh
# Builds kyeot executables for other computers into dist/<version>/. A person does the upload.
set -eu

VERSION="${1:-$(cat "$(dirname "$0")/../VERSION" 2>/dev/null)}"
if ! printf '%s\n' "$VERSION" | LC_ALL=C grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+' || [ "$(printf '%s\n' "$VERSION" | wc -l)" -ne 1 ]; then
	echo "usage: scripts/release-build.sh <version>  (e.g. 0.1.0)" >&2
	exit 2
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
KEY="${KYEOT_RELEASE_KEY:-$HOME/.config/kyeot/release-signing/id_ed25519}"
if [ "${KYEOT_RELEASE_UNSIGNED:-}" != 1 ] && [ ! -f "$KEY" ]; then
	echo "signing key not found: $KEY  (create it with scripts/release-key.sh or restore from backup)" >&2
	exit 1
fi
OUT="$ROOT/dist/$VERSION"
rm -rf "$OUT"
mkdir -p "$OUT"
cd "$ROOT"

for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
	os="${target%/*}"
	arch="${target#*/}"
	echo "build: $os/$arch"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
		-ldflags "-s -w -X main.version=$VERSION" \
		-o "$OUT/kyeot_${os}_${arch}" ./cmd/kyeot
done

cp scripts/install.sh "$OUT/install.sh"
printf '%s\n' "$VERSION" > "$OUT/VERSION"
cd "$OUT"
shasum -a 256 kyeot_* install.sh VERSION > checksums.txt
cat checksums.txt
SIG=""
if [ "${KYEOT_RELEASE_UNSIGNED:-}" = 1 ]; then
	echo "not signed (KYEOT_RELEASE_UNSIGNED=1). install.sh rejects this release" >&2
else
	ssh-keygen -q -Y sign -f "$KEY" -n kyeot-release checksums.txt
	SIG=" $OUT/checksums.txt.sig"
fi
echo "done: $OUT  → gh release create v$VERSION $OUT/kyeot_* $OUT/install.sh $OUT/VERSION $OUT/checksums.txt$SIG -R kyeotapp/kyeot"
