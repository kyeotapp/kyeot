#!/bin/sh
# Installs kyeot. Downloads the release assets over https.
#   set up a server (no arguments):  curl -fsSL https://kyeot.app/install | sh
#     registers it as a service, starts it, and shows the connection QR. With KYEOT_NO_SERVICE=1 it only installs
#   connect an agent on another computer: … | sh -s -- '<server address>' <6-digit code>
set -eu

MIN_VERSION=0.1.5 # first version with a signature (checksums.txt.sig)
DEFAULT_BASE=https://github.com/kyeotapp/kyeot/releases/download
# Public keys for the checksums.txt signature (scripts/release-key.sh)
# The usual key and the spare key (spare only if the usual key is lost; kept offline). A signature by either is accepted
RELEASE_PUBKEYS='ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILt18cm4BlLU7vnXLkrG9qQvvLbX+tJjDDV2lYNyFto2
ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPSO8iev2GSsKyIutJPHL9rHbejefUUC6G2ZTRLwsTHZ'

# Language: if the first set value of KYEOT_LANG > LC_ALL > LC_MESSAGES > LANG starts with ko, Korean (same as cmd/kyeot/i18n.go)
LANG_KO=""
case "${KYEOT_LANG:-${LC_ALL:-${LC_MESSAGES:-${LANG:-}}}}" in
	[Kk][Oo]*) LANG_KO=1 ;;
esac

# msg <Korean> <English>
msg() {
	if [ -n "$LANG_KO" ]; then printf '%s\n' "$1"; else printf '%s\n' "$2"; fi
}

usage() {
	msg "쓰는 법: sh install.sh                                      kyeot 를 깔고 서버를 서비스로 켠다" \
		"usage: sh install.sh                                        install kyeot and start the server as a service" >&2
	msg "        sh install.sh <서버 주소(http://호스트:포트)> <6자리 코드>  깔고 그 서버에 이 컴퓨터의 에이전트를 붙인다" \
		"       sh install.sh <server address (http://host:port)> <6-digit code>  install and connect this computer's agent to that server" >&2
	exit 2
}

die() {
	echo "$1" >&2
	exit 1
}

# Whether the server address is of the form scheme://host[:port]. Paths, quotes, and shell characters are rejected
valid_server() {
	case "$1" in
		*[!A-Za-z0-9.:/\[\]-]*) return 1 ;;
	esac
	printf '%s\n' "$1" | LC_ALL=C grep -Eqx 'https?://([A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*|[0-9]{1,3}(\.[0-9]{1,3}){3}|\[[0-9A-Fa-f:.]+\])(:[0-9]{1,5})?'
}

valid_version() {
	case "$1" in
		''|*[!0-9.]*) return 1 ;;
	esac
	printf '%s\n' "$1" | LC_ALL=C grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+'
}

# True if $1 < $2. Decimal comparison (test doesn't read leading 0 as octal)
version_lt() {
	a1=${1%%.*}; r=${1#*.}; a2=${r%%.*}; a3=${r#*.}
	b1=${2%%.*}; r=${2#*.}; b2=${r%%.*}; b3=${r#*.}
	[ "$a1" -lt "$b1" ] && return 0
	[ "$a1" -gt "$b1" ] && return 1
	[ "$a2" -lt "$b2" ] && return 0
	[ "$a2" -gt "$b2" ] && return 1
	[ "$a3" -lt "$b3" ]
}

reach_hint() {
	host=${1#*://}
	host=${host%:[0-9]*}
	host=$(printf '%s' "$host" | tr 'A-Z' 'a-z')
	case "$host" in
		*.ts.net|100.6[4-9].*|100.[7-9][0-9].*|100.1[01][0-9].*|100.12[0-7].*)
			msg "이 컴퓨터도 같은 Tailscale 계정에 로그인돼 있는지 확인한다" "Check that this computer is signed in to the same Tailscale account" ;;
		*.local|10.*|192.168.*|169.254.*|172.1[6-9].*|172.2[0-9].*|172.3[01].*|\[fc*|\[fd*|\[fe80:*)
			msg "이 컴퓨터가 서버와 같은 와이파이(네트워크)에 있는지 확인한다" "Check that this computer is on the same Wi-Fi (network) as the server" ;;
	esac
}

sha256() {
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	elif command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		die "$(msg "shasum 이나 sha256sum 이 필요하다" "shasum or sha256sum is required")"
	fi
}

# Whether ssh-keygen -Y (OpenSSH 8.0+) works
check_ssh_keygen() {
	command -v ssh-keygen >/dev/null 2>&1 || die "$(msg "릴리스 서명을 확인할 ssh-keygen 이 없다. OpenSSH(8.0 이상)를 깔고 다시 실행한다" "ssh-keygen is needed to check the release signature. Install OpenSSH 8.0 or later and run again")"
	case "$(ssh-keygen -Y verify 2>&1 || true)" in
		*option*) die "$(msg "ssh-keygen 이 너무 옛 판이라 릴리스 서명을 확인할 수 없다. OpenSSH 를 8.0 이상으로 올린다" "ssh-keygen is too old to check the release signature. Upgrade OpenSSH to 8.0 or later")" ;;
	esac
}

# $1 = checksums.txt, $2 = signature. True only if signed with an embedded public key
verify_checksums() {
	KEYS=$RELEASE_PUBKEYS
	if [ "${KYEOT_INSECURE_TEST:-}" = 1 ] && [ -n "${KYEOT_TEST_RELEASE_PUBKEY:-}" ]; then
		KEYS=$KYEOT_TEST_RELEASE_PUBKEY
	fi
	printf '%s\n' "$KEYS" | while IFS= read -r k; do
		[ -n "$k" ] && printf 'kyeot-release namespaces="kyeot-release" %s\n' "$k"
	done > "$TMP/allowed_signers"
	ssh-keygen -Y verify -f "$TMP/allowed_signers" -I kyeot-release -n kyeot-release -s "$2" < "$1" >/dev/null 2>&1
}

# Starts the server as a service and shows the QR. Installing still succeeds if registration fails.
# An already registered service is only restarted; the token QR is shown on first setup only.
start_server() {
	if [ "${KYEOT_NO_SERVICE:-}" = 1 ]; then
		echo
		if [ -n "$UPDATED" ]; then
			msg "이미 떠 있는 서버는 옛 판이다. 서비스면 $BIN service restart, 터미널이면 끄고(Ctrl-C) 다시 띄운다" \
				"A server that is already running is still the old version. For a service: $BIN service restart. In a terminal: stop it (Ctrl-C) and start it again"
			return 0
		fi
		msg "서버를 시작하려면:   $BIN" "To start the server:   $BIN"
		msg "  처음이면 터미널에 연결 QR 이 뜬다. 곁 앱의 「QR 찍기」로 찍는다. 다시 보려면 $BIN pair" \
			"  The first time, a connection QR appears in the terminal. Scan it with “Scan QR” in the Kyeot app. To show it again: $BIN pair"
		msg "로그인할 때 저절로 뜨게 하려면: $BIN service install" "To start it automatically at login: $BIN service install"
		return 0
	fi
	if server_registered; then
		echo
		if "$BIN" service restart; then # keeps the settings saved in the service file
			msg "서버를 다시 켰다: $("$BIN" version 2>/dev/null || true). 폰 연결은 그대로다" \
				"Restarted the server: $("$BIN" version 2>/dev/null || true). Phone connections stay as they are"
		else
			STATUS=1
			msg "서버를 다시 켜지 못했다. 잠시 뒤 다시: $BIN service restart" "Couldn't restart the server. Try again in a moment: $BIN service restart"
		fi
		msg "QR 다시 보기:   $BIN pair" "Show the QR again:   $BIN pair"
		return 0
	fi
	if [ -n "$HAD_BIN" ]; then # installed before without a service: keep it that way
		echo
		if [ -n "$UPDATED" ]; then
			msg "서비스로 등록돼 있지 않아 새로 등록하지 않았다. 터미널에서 띄운 서버가 있으면 옛 판이다. 끄고(Ctrl-C) 다시 띄운다:   $BIN" \
				"No service is registered, so none was added. A server started in a terminal is still the old version. Stop it (Ctrl-C) and start it again:   $BIN"
		else
			msg "서비스로 등록돼 있지 않다. 서버를 시작하려면:   $BIN" "No service is registered. To start the server:   $BIN"
		fi
		msg "로그인할 때 저절로 뜨게 하려면: $BIN service install" "To start it automatically at login: $BIN service install"
		return 0
	fi
	if ! "$BIN" service install; then
		echo
		msg "서비스로 등록하지 못했다. 설치는 끝났다. 서버는 터미널에서 직접 띄운다:   $BIN" \
			"Couldn't register the service. Installation is done. Start the server in the terminal yourself:   $BIN"
		msg "  처음이면 터미널에 연결 QR 이 뜬다. 곁 앱의 「QR 찍기」로 찍는다" \
			"  The first time, a connection QR appears in the terminal. Scan it with “Scan QR” in the Kyeot app"
		return 0
	fi
	echo
	"$BIN" pair || msg "QR 을 띄우지 못했다. 잠시 뒤 $BIN pair 로 다시 띄운다" "Couldn't show the QR. Try again in a moment with $BIN pair"
	echo
	msg "QR 다시 보기:   $BIN pair" "Show the QR again:   $BIN pair"
	msg "서버 끄기:      $BIN service uninstall" "Stop the server:     $BIN service uninstall"
	msg "다른 컴퓨터의 에이전트를 붙이려면 앱의 「에이전트 추가」가 알려 주는 한 줄 명령을 그 컴퓨터에서 실행한다" \
		"To connect an agent on another computer, run the one-line command from “Add agent” in the app on that computer"
}

# Restarts a registered connect service in place (the service file is not rewritten, so its settings stay).
restart_connect() {
	if [ -f "$HOME/Library/LaunchAgents/app.kyeot.connect.plist" ]; then
		launchctl kickstart -k "gui/$(id -u)/app.kyeot.connect" >/dev/null 2>&1 || launchctl kickstart -k "user/$(id -u)/app.kyeot.connect" >/dev/null 2>&1 || RC=1
	elif [ -f "$HOME/.config/systemd/user/kyeot-connect.service" ]; then
		systemctl --user restart kyeot-connect >/dev/null 2>&1 || RC=1
	else
		return 0
	fi
	if [ "${RC:-0}" = 0 ]; then
		msg "에이전트 연결 서비스를 새 판으로 다시 켰다" "Restarted the agent connect service on the new version"
		return 0
	fi
	msg "에이전트 연결 서비스를 다시 켜지 못했다. 맥은 launchctl kickstart -k gui/$(id -u)/app.kyeot.connect, 리눅스는 systemctl --user restart kyeot-connect" \
		"Couldn't restart the agent connect service. On macOS: launchctl kickstart -k gui/$(id -u)/app.kyeot.connect. On Linux: systemctl --user restart kyeot-connect"
	return 1
}

# Registration files made by kyeot service install (cmd/kyeot/service.go)
server_registered() {
	[ -f "$HOME/Library/LaunchAgents/app.kyeot.server.plist" ] || [ -f "$HOME/.config/systemd/user/kyeot-server.service" ]
}

TMP=""
NEW=""
PREV=""
cleanup() {
	[ -z "$TMP" ] || rm -rf "$TMP"
	[ -z "$NEW" ] || rm -f "$NEW"
	[ -z "$PREV" ] || rm -f "$PREV"
}

main() {
	FG=""
	SERVER=""
	CODE=""
	case $# in
		0) ;;
		2) ;;
		3) [ "$3" = --foreground ] || usage; FG=--foreground ;;
		*) usage ;;
	esac
	if [ $# -gt 0 ]; then
		SERVER=${1%/}
		CODE=$2
		valid_server "$SERVER" || usage
		case "$CODE" in
			[0-9][0-9][0-9][0-9][0-9][0-9]) ;;
			*) usage ;;
		esac
	fi

	[ "$(id -u)" != 0 ] || die "$(msg "root 로는 설치하지 않는다. 보통 사용자로 실행한다" "Won't install as root. Run as a normal user")"

	BASE=${KYEOT_RELEASE_BASE:-$DEFAULT_BASE}
	BASE=${BASE%/}
	case "$BASE" in
		https://*) set -- --proto '=https' ;;
		http://*)
			[ "${KYEOT_INSECURE_TEST:-}" = 1 ] || die "$(msg "릴리스 주소는 https 여야 한다: $BASE" "The release address must be https: $BASE")"
			set -- ;;
		*) die "$(msg "릴리스 주소는 https 여야 한다: $BASE" "The release address must be https: $BASE")" ;;
	esac

	case "$(uname -s)" in
		Darwin) OS=darwin ;;
		Linux) OS=linux ;;
		MINGW*|MSYS*|CYGWIN*) die "$(msg "곁 서버는 지금 맥과 리눅스에서만 돈다. 윈도우는 아직 지원하지 않는다" "The Kyeot server runs only on macOS and Linux for now. Windows is not supported yet")" ;;
		*) die "$(msg "곁 서버는 지금 맥과 리눅스에서만 돈다" "The Kyeot server runs only on macOS and Linux for now")" ;;
	esac
	case "$(uname -m)" in
		arm64|aarch64) ARCH=arm64 ;;
		x86_64|amd64) ARCH=amd64 ;;
		*) die "$(msg "맥, 리눅스만 지원한다 (arm64, x86_64)" "Only macOS and Linux are supported (arm64, x86_64)")" ;;
	esac

	if [ -n "$SERVER" ]; then
		RC=0
		RES=$(curl -fsS --max-time 5 "$SERVER/version") || RC=$?
		if [ "$RC" = 22 ]; then
			die "$(msg "서버가 판을 알려 주지 않는다. 개발판 서버면 릴리스 판으로 다시 띄운다" "The server doesn't report its version. If it's a development build, restart it with a release build")"
		elif [ "$RC" != 0 ]; then
			msg "서버에 닿지 않는다: $SERVER" "Can't reach the server: $SERVER" >&2
			HINT=$(reach_hint "$SERVER")
			[ -z "$HINT" ] || echo "$HINT" >&2
			exit 1
		fi
		VERSION=$(printf '%s\n' "$RES" | sed -n 's/^ *{ *"version" *: *"\([0-9.]*\)" *} *$/\1/p')
		valid_version "$VERSION" || die "$(msg "서버가 알려 준 판이 올바르지 않다" "The server reported an invalid version")"
		! version_lt "$VERSION" "$MIN_VERSION" || die "$(msg "서버 판 $VERSION 은 너무 낮다 (최소 $MIN_VERSION). 서버를 올린다" "Server version $VERSION is too old (minimum $MIN_VERSION). Upgrade the server")"
		REL="$BASE/v$VERSION"
	else
		# Without a server, the latest release. The version is read from the VERSION line that comes with checksums.txt
		REL="${BASE%/download}/latest/download"
		VERSION=$(curl -fsSL "$@" --max-time 10 "$REL/VERSION" 2>/dev/null | tr -d '[:space:]') || die "$(msg "최신 판을 알 수 없다: $REL/VERSION" "Can't find the latest version: $REL/VERSION")"
		valid_version "$VERSION" || die "$(msg "릴리스의 VERSION 이 올바르지 않다: $VERSION" "The release VERSION is invalid: $VERSION")"
	fi

	DIR="$HOME/.local/bin"
	BIN="$DIR/kyeot"
	HAD_BIN=""
	UPDATED="" # an existing kyeot was replaced in this run
	[ ! -f "$BIN" ] || HAD_BIN=1
	NAME="kyeot_${OS}_${ARCH}"
	[ ! -d "$BIN" ] || die "$(msg "$BIN 이 폴더다. 치우고 다시 실행한다" "$BIN is a folder. Move it away and run again")"

	if [ -x "$BIN" ] && [ "$("$BIN" version 2>/dev/null || true)" = "$VERSION" ]; then
		msg "kyeot $VERSION 이 이미 있다: $BIN" "kyeot $VERSION is already installed: $BIN"
	else
		msg "kyeot $VERSION 을 받는다 ($OS/$ARCH)" "Downloading kyeot $VERSION ($OS/$ARCH)"
		check_ssh_keygen
		TMP=$(mktemp -d)
		curl -fsSL "$@" -o "$TMP/$NAME" "$REL/$NAME"
		curl -fsSL "$@" -o "$TMP/checksums.txt" "$REL/checksums.txt"
		curl -fsSL "$@" -o "$TMP/checksums.txt.sig" "$REL/checksums.txt.sig" 2>/dev/null || die "$(msg "릴리스에 서명(checksums.txt.sig)이 없다. 설치하지 않는다" "The release has no signature (checksums.txt.sig). Not installing")"
		verify_checksums "$TMP/checksums.txt" "$TMP/checksums.txt.sig" || die "$(msg "릴리스 서명이 맞지 않는다. 파일이 바꿔치기됐을 수 있다. 설치하지 않는다" "The release signature doesn't match. The files may have been swapped. Not installing")"
		WANT=$(awk -v f="$NAME" '$2 == f || $2 == "*" f {print $1}' "$TMP/checksums.txt")
		GOT=$(sha256 "$TMP/$NAME")
		if [ -z "$WANT" ] || [ "$WANT" != "$GOT" ]; then
			die "$(msg "파일이 변조됐거나 손상됐다. 설치하지 않는다" "The file was tampered with or is damaged. Not installing")"
		fi
		mkdir -p "$DIR"
		NEW=$(mktemp "$DIR/.kyeot.new.XXXXXX")
		cp "$TMP/$NAME" "$NEW"
		chmod 755 "$NEW"
		if [ "$OS" = darwin ]; then
			xattr -d com.apple.quarantine "$NEW" >/dev/null 2>&1 || true
		fi
		if [ -f "$BIN" ]; then # the one previous version, used for rollback
			PREV=$(mktemp "$DIR/.kyeot.prev.XXXXXX") || PREV=""
			if [ -n "$PREV" ] && [ ! -d "$DIR/kyeot.prev" ] && cp "$BIN" "$PREV" && chmod 755 "$PREV" && mv -f "$PREV" "$DIR/kyeot.prev"; then
				PREV=""
				msg "이전 판은 $DIR/kyeot.prev 에 남겼다" "The previous version is kept at $DIR/kyeot.prev"
			else
				msg "이전 판을 남기지 못했다. 설치는 계속한다" "Couldn't keep the previous version. Installing anyway"
			fi
		fi
		mv -f "$NEW" "$BIN"
		NEW=""
		[ -z "$HAD_BIN" ] || UPDATED=1
		msg "설치했다: $BIN" "Installed: $BIN"
	fi

	STATUS=0
	if [ -z "$SERVER" ]; then
		[ -z "$UPDATED" ] || restart_connect || STATUS=1
		start_server
	elif [ -n "$FG" ]; then
		KYEOT_CONNECT_YES=1 "$BIN" connect "$FG" "$SERVER" "$CODE" || STATUS=$?
	else
		KYEOT_CONNECT_YES=1 "$BIN" connect "$SERVER" "$CODE" || STATUS=$?
	fi

	case ":$PATH:" in
		*":$DIR:"*) ;;
		*) msg "다음부터 kyeot 를 바로 쓰려면 PATH 에 ~/.local/bin 을 넣는다" "To run kyeot directly next time, add ~/.local/bin to your PATH" ;;
	esac
	exit "$STATUS"
}

trap cleanup EXIT
trap 'exit 1' INT TERM HUP
main "$@"
