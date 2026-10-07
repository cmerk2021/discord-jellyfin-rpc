#!/bin/sh
# jellyfin-rpc installer
#
#   curl -fsSL https://raw.githubusercontent.com/cmerk2021/discord-jellyfin-rpc/main/install.sh | sh
#
# Options (pass after `sh -s --`):
#   --version vX.Y.Z   install a specific release (default: latest)
#   --system           install to /usr/local/bin (uses sudo) instead of ~/.local/bin
#   --bin-dir DIR      install to DIR
#   --no-setup         don't run the interactive setup wizard
#   --uninstall        remove the service, binary and (optionally) config
#
# Environment: JELLYFIN_RPC_VERSION, JELLYFIN_RPC_BIN_DIR, JELLYFIN_RPC_NO_SETUP=1,
#              JELLYFIN_RPC_DOWNLOAD_URL (mirror serving the release assets)
set -eu

REPO="cmerk2021/discord-jellyfin-rpc"
NAME="jellyfin-rpc"
VERSION="${JELLYFIN_RPC_VERSION:-latest}"
BIN_DIR="${JELLYFIN_RPC_BIN_DIR:-}"
SYSTEM=0
RUN_SETUP=1
UNINSTALL=0
[ "${JELLYFIN_RPC_NO_SETUP:-0}" = "1" ] && RUN_SETUP=0

if [ -t 1 ]; then
	B="$(printf '\033[1m')"; G="$(printf '\033[32m')"; Y="$(printf '\033[33m')"; R="$(printf '\033[31m')"; N="$(printf '\033[0m')"
else
	B=""; G=""; Y=""; R=""; N=""
fi
info() { printf '%s==>%s %s\n' "$B" "$N" "$*"; }
ok() { printf '%s✔%s %s\n' "$G" "$N" "$*"; }
warn() { printf '%s!%s %s\n' "$Y" "$N" "$*" >&2; }
die() { printf '%s✘ %s%s\n' "$R" "$*" "$N" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

while [ $# -gt 0 ]; do
	case "$1" in
	--version) [ $# -ge 2 ] || die "--version needs a value"; VERSION="$2"; shift 2 ;;
	--version=*) VERSION="${1#*=}"; shift ;;
	--bin-dir) [ $# -ge 2 ] || die "--bin-dir needs a value"; BIN_DIR="$2"; shift 2 ;;
	--bin-dir=*) BIN_DIR="${1#*=}"; shift ;;
	--system) SYSTEM=1; shift ;;
	--no-setup) RUN_SETUP=0; shift ;;
	--uninstall) UNINSTALL=1; shift ;;
	-h | --help) sed -n '2,15p' "$0" 2>/dev/null || true; exit 0 ;;
	*) die "unknown option: $1" ;;
	esac
done

if [ -z "$BIN_DIR" ]; then
	if [ "$SYSTEM" = 1 ]; then BIN_DIR="/usr/local/bin"; else BIN_DIR="$HOME/.local/bin"; fi
fi

SUDO=""
if [ "$(id -u)" -ne 0 ]; then
	if has sudo; then SUDO="sudo"; elif has doas; then SUDO="doas"; fi
fi

# Interactive input must come from the terminal: stdin is the script when piped from curl.
TTY=""
if (exec </dev/tty) 2>/dev/null; then TTY="/dev/tty"; fi

ask_yn() { # ask_yn "question" default(y|n)
	[ -n "$TTY" ] || { [ "$2" = y ]; return; }
	if [ "$2" = y ]; then hint="[Y/n]"; else hint="[y/N]"; fi
	printf '%s %s ' "$1" "$hint" >"$TTY"
	read -r ans <"$TTY" || ans=""
	case "${ans:-$2}" in [yY]*) return 0 ;; *) return 1 ;; esac
}

# ---------- uninstall ----------
if [ "$UNINSTALL" = 1 ]; then
	BIN="$(command -v "$NAME" 2>/dev/null || true)"
	[ -n "$BIN" ] || BIN="$BIN_DIR/$NAME"
	if [ -x "$BIN" ]; then
		CFG="$("$BIN" config path 2>/dev/null || true)"
		"$BIN" service uninstall 2>/dev/null || true
		info "Removing $BIN"
		if [ -w "$(dirname "$BIN")" ]; then rm -f "$BIN"; else $SUDO rm -f "$BIN"; fi
		if [ -n "$CFG" ] && [ -f "$CFG" ] && ask_yn "Also delete config ($CFG)?" n; then
			rm -f "$CFG"
			rmdir "$(dirname "$CFG")" 2>/dev/null || true
		fi
		ok "jellyfin-rpc uninstalled"
	else
		warn "jellyfin-rpc is not installed"
	fi
	exit 0
fi

# ---------- platform ----------
OS="$(uname -s)"
case "$OS" in
Linux) OS=linux ;;
Darwin) OS=darwin ;;
*) die "unsupported OS: $OS (Linux and macOS are supported)" ;;
esac
ARCH="$(uname -m)"
case "$ARCH" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
armv7* | armv8l) ARCH=armv7 ;;
*) die "unsupported architecture: $ARCH" ;;
esac
# Rosetta: prefer the native binary on Apple silicon.
if [ "$OS" = darwin ] && [ "$ARCH" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
	ARCH=arm64
fi

# ---------- dependencies ----------
install_pkgs() {
	if has apt-get; then $SUDO apt-get update -qq && $SUDO apt-get install -y -qq "$@"
	elif has dnf; then $SUDO dnf install -y -q "$@"
	elif has yum; then $SUDO yum install -y -q "$@"
	elif has pacman; then $SUDO pacman -Sy --noconfirm --needed "$@"
	elif has zypper; then $SUDO zypper --non-interactive install "$@"
	elif has apk; then $SUDO apk add --no-cache "$@"
	elif has xbps-install; then $SUDO xbps-install -Sy "$@"
	elif has brew; then brew install "$@"
	else return 1
	fi
}
need=""
has curl || has wget || need="$need curl"
has tar || need="$need tar"
has gzip || need="$need gzip"
has sha256sum || has shasum || need="$need coreutils"
if [ -n "$need" ]; then
	info "Installing required packages:$need"
	# shellcheck disable=SC2086
	install_pkgs $need || die "please install:$need"
fi

fetch() { # fetch URL DEST
	if has curl; then curl -fsSL --retry 3 -o "$2" "$1"
	else wget -q -O "$2" "$1"
	fi
}
sha256() {
	if has sha256sum; then sha256sum "$1" | cut -d' ' -f1
	else shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

# ---------- download ----------
ASSET="${NAME}_${OS}_${ARCH}.tar.gz"
if [ -n "${JELLYFIN_RPC_DOWNLOAD_URL:-}" ]; then
	BASE="${JELLYFIN_RPC_DOWNLOAD_URL%/}"
elif [ "$VERSION" = latest ]; then
	BASE="https://github.com/$REPO/releases/latest/download"
else
	case "$VERSION" in v*) ;; *) VERSION="v$VERSION" ;; esac
	BASE="https://github.com/$REPO/releases/download/$VERSION"
fi

TMP="$(mktemp -d 2>/dev/null || mktemp -d -t jellyfin-rpc)"
trap 'rm -rf "$TMP"' EXIT INT TERM

info "Downloading $ASSET ($VERSION)"
fetch "$BASE/$ASSET" "$TMP/$ASSET" || die "download failed: $BASE/$ASSET"
if fetch "$BASE/checksums.txt" "$TMP/checksums.txt"; then
	want="$(grep " $ASSET\$" "$TMP/checksums.txt" | cut -d' ' -f1)"
	[ -n "$want" ] || die "$ASSET not listed in checksums.txt"
	[ "$(sha256 "$TMP/$ASSET")" = "$want" ] || die "checksum mismatch for $ASSET"
	ok "Checksum verified"
else
	die "could not download checksums.txt"
fi
tar -xzf "$TMP/$ASSET" -C "$TMP" "$NAME" || die "failed to extract archive"

# ---------- install ----------
WAS_RUNNING=0
if has "$NAME" && "$NAME" service status >/dev/null 2>&1; then WAS_RUNNING=1; fi

if [ ! -d "$BIN_DIR" ]; then
	mkdir -p "$BIN_DIR" 2>/dev/null || $SUDO mkdir -p "$BIN_DIR"
fi
if [ -w "$BIN_DIR" ]; then
	install -m 0755 "$TMP/$NAME" "$BIN_DIR/$NAME" 2>/dev/null || { cp "$TMP/$NAME" "$BIN_DIR/$NAME" && chmod 0755 "$BIN_DIR/$NAME"; }
else
	[ -n "$SUDO" ] || die "$BIN_DIR is not writable; re-run as root or choose --bin-dir"
	$SUDO install -m 0755 "$TMP/$NAME" "$BIN_DIR/$NAME"
fi
BIN="$BIN_DIR/$NAME"
ok "Installed $("$BIN" version 2>/dev/null | head -n1) to $BIN"

case ":$PATH:" in
*":$BIN_DIR:"*) ;;
*) warn "$BIN_DIR is not in your PATH. Add this to your shell profile:"
	# shellcheck disable=SC2016
	printf '    export PATH="%s:$PATH"\n' "$BIN_DIR" ;;
esac

# ---------- configure ----------
CFG="$("$BIN" config path)"
if [ "$WAS_RUNNING" = 1 ]; then
	info "Restarting background service"
	"$BIN" service restart || warn "restart failed; run: $NAME service restart"
fi

if [ "$RUN_SETUP" = 1 ]; then
	if [ -z "$TTY" ]; then
		warn "No terminal available; run '$NAME setup' to configure."
	elif [ -f "$CFG" ]; then
		if ask_yn "Existing config found at $CFG. Re-run the setup wizard?" n; then
			"$BIN" setup  # the wizard reads from /dev/tty itself
		fi
	else
		"$BIN" setup  # the wizard reads from /dev/tty itself
	fi
else
	[ -f "$CFG" ] || info "Run '$NAME setup' to configure."
fi

printf '\n%sDone!%s Useful commands:\n' "$B" "$N"
printf '  %s setup            re-run the configuration wizard\n' "$NAME"
printf '  %s check            show what would be displayed right now\n' "$NAME"
printf '  %s service status   background service status\n' "$NAME"
