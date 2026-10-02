#!/bin/sh
# vrok installer for macOS, Linux and FreeBSD.
#
#   curl -fsSL https://raw.githubusercontent.com/AliJabbar034/vrok/main/install.sh | sh
#
# Environment:
#   VROK_VERSION       version to install, e.g. v0.1.0 (default: latest)
#   VROK_INSTALL_DIR   where to put the binary (default: /usr/local/bin, or
#                      ~/.local/bin when that is not writable)
#   VROK_NO_SUDO       set to 1 to never escalate
#   VROK_DOWNLOAD_BASE serve the archives from somewhere else, for a mirror or
#                      an air-gapped network
#
# POSIX sh on purpose: this has to run on a minimal container, a BSD and a Mac
# without assuming bash exists.

set -eu

REPO="AliJabbar034/vrok"
BIN="vrok"

# ---------------------------------------------------------------- output ----

if [ -t 2 ] && [ -z "${NO_COLOR:-}" ]; then
	C_RESET=$(printf '\033[0m'); C_DIM=$(printf '\033[2m')
	C_RED=$(printf '\033[31m'); C_GREEN=$(printf '\033[32m')
	C_CYAN=$(printf '\033[36m'); C_YELLOW=$(printf '\033[33m')
else
	C_RESET=''; C_DIM=''; C_RED=''; C_GREEN=''; C_CYAN=''; C_YELLOW=''
fi

info() { printf '%s\n' "$*" >&2; }
step() { printf '%s==>%s %s\n' "$C_CYAN" "$C_RESET" "$*" >&2; }
warn() { printf '%s!%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
ok()   { printf '%s✓%s %s\n' "$C_GREEN" "$C_RESET" "$*" >&2; }

die() {
	printf '%s✗%s %s\n' "$C_RED" "$C_RESET" "$1" >&2
	exit 1
}

# ------------------------------------------------------------ discovery ----

need() {
	command -v "$1" >/dev/null 2>&1 || die "this installer needs \`$1\`, which is not on your PATH."
}

detect_platform() {
	os=$(uname -s)
	case "$os" in
		Darwin)  OS=darwin ;;
		Linux)   OS=linux ;;
		FreeBSD) OS=freebsd ;;
		MINGW*|MSYS*|CYGWIN*)
			die "this installer is for Unix shells. On Windows run the PowerShell installer:
    irm https://raw.githubusercontent.com/$REPO/main/install.ps1 | iex" ;;
		*) die "unsupported operating system: $os" ;;
	esac

	arch=$(uname -m)
	case "$arch" in
		x86_64|amd64)  ARCH=amd64 ;;
		arm64|aarch64) ARCH=arm64 ;;
		armv7l|armv6l|arm) ARCH=arm ;;
		*) die "unsupported architecture: $arch" ;;
	esac

	# Only amd64 is built for FreeBSD, and arm only for Linux.
	case "$OS/$ARCH" in
		freebsd/arm64|freebsd/arm) die "no $OS/$ARCH build is published. Install with: go install github.com/$REPO/cmd/vrok@latest" ;;
		darwin/arm)               die "no $OS/$ARCH build is published." ;;
	esac

	PLATFORM="${OS}_${ARCH}"
}

# download writes a URL to a file, using whichever fetcher exists.
download() {
	if [ "$FETCHER" = curl ]; then
		curl -fsSL --retry 3 --retry-delay 1 -o "$2" "$1"
	else
		wget -q -O "$2" "$1"
	fi
}

# download_stdout fetches to stdout, for small metadata requests.
download_stdout() {
	if [ "$FETCHER" = curl ]; then
		curl -fsSL --retry 3 "$1"
	else
		wget -qO- "$1"
	fi
}

resolve_version() {
	if [ -n "${VROK_VERSION:-}" ]; then
		VERSION="$VROK_VERSION"
		return
	fi
	step "Finding the latest release"
	# The redirect from /releases/latest carries the tag, which avoids both the
	# JSON API's rate limit and needing a JSON parser.
	if [ "$FETCHER" = curl ]; then
		location=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
			"https://github.com/$REPO/releases/latest" 2>/dev/null || true)
	else
		location=$(wget -q --max-redirect=5 -S --spider \
			"https://github.com/$REPO/releases/latest" 2>&1 \
			| sed -n 's|^ *Location: *\(.*\)|\1|p' | tail -1 || true)
	fi
	VERSION=$(printf '%s' "$location" | sed -n 's|.*/tag/\(.*\)$|\1|p' | tr -d '\r')

	[ -n "$VERSION" ] || die "could not determine the latest version.
Set it explicitly:  VROK_VERSION=v0.1.0 sh install.sh"
}

# ------------------------------------------------------------- install ----

pick_dir() {
	if [ -n "${VROK_INSTALL_DIR:-}" ]; then
		DEST="$VROK_INSTALL_DIR"
		return
	fi
	for candidate in /usr/local/bin "$HOME/.local/bin"; do
		if [ -d "$candidate" ] && [ -w "$candidate" ]; then
			DEST="$candidate"
			return
		fi
	done
	# Nothing writable. Prefer sudo into /usr/local/bin, since it is already on
	# PATH; otherwise create a user directory and say so.
	if [ -z "${VROK_NO_SUDO:-}" ] && command -v sudo >/dev/null 2>&1 && [ -d /usr/local/bin ]; then
		DEST=/usr/local/bin
		SUDO=sudo
		return
	fi
	DEST="$HOME/.local/bin"
	mkdir -p "$DEST"
}

on_path() {
	case ":$PATH:" in
		*":$1:"*) return 0 ;;
		*) return 1 ;;
	esac
}

main() {
	if command -v curl >/dev/null 2>&1; then FETCHER=curl
	elif command -v wget >/dev/null 2>&1; then FETCHER=wget
	else die "this installer needs \`curl\` or \`wget\`."; fi
	need tar
	need uname

	detect_platform
	resolve_version

	SUDO=''
	pick_dir

	archive="vrok_${VERSION}_${PLATFORM}.tar.gz"
	base="${VROK_DOWNLOAD_BASE:-https://github.com/$REPO/releases/download/$VERSION}"

	tmp=$(mktemp -d 2>/dev/null || mktemp -d -t vrok)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	step "Downloading vrok $VERSION for $OS/$ARCH"
	download "$base/$archive" "$tmp/$archive" \
		|| die "could not download $base/$archive
Check that $VERSION exists and publishes a $PLATFORM build."

	# Verify the download. A corrupted or substituted archive is worth
	# catching before it becomes an executable on your PATH.
	if download "$base/checksums.txt" "$tmp/checksums.txt" 2>/dev/null; then
		step "Verifying checksum"
		expected=$(grep " $archive\$" "$tmp/checksums.txt" | awk '{print $1}')
		if [ -z "$expected" ]; then
			warn "no checksum listed for $archive; skipping verification"
		else
			if command -v shasum >/dev/null 2>&1; then
				actual=$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')
			elif command -v sha256sum >/dev/null 2>&1; then
				actual=$(sha256sum "$tmp/$archive" | awk '{print $1}')
			else
				actual=''
				warn "no sha256 tool found; skipping verification"
			fi
			if [ -n "$actual" ] && [ "$actual" != "$expected" ]; then
				die "checksum mismatch for $archive.
  expected $expected
  got      $actual
Not installing. Please report this."
			fi
		fi
	else
		warn "could not fetch checksums.txt; skipping verification"
	fi

	step "Extracting"
	mkdir -p "$tmp/x"
	tar xzf "$tmp/$archive" -C "$tmp/x"
	# Archives are flat, but search anyway so an archive that gains a wrapping
	# directory does not break installs.
	extracted="$tmp/x/$BIN"
	[ -f "$extracted" ] || extracted=$(find "$tmp/x" -type f -name "$BIN" | head -1)
	[ -f "$extracted" ] || die "the archive did not contain a \`$BIN\` binary."

	# Only the CLI is installed. vrok-relay is a server that belongs on a host
	# with a wildcard DNS record, not on a laptop's PATH; it ships in the
	# archive and as a container image for anyone who wants it.
	step "Installing to $DEST"
	$SUDO mkdir -p "$DEST"
	$SUDO install -m 0755 "$extracted" "$DEST/$BIN" 2>/dev/null || {
		$SUDO cp "$extracted" "$DEST/$BIN"
		$SUDO chmod 0755 "$DEST/$BIN"
	}

	# macOS quarantines anything downloaded by a browser or curl. Clearing the
	# attribute here avoids a Gatekeeper dialog on first run.
	if [ "$OS" = darwin ] && command -v xattr >/dev/null 2>&1; then
		$SUDO xattr -d com.apple.quarantine "$DEST/$BIN" 2>/dev/null || true
	fi

	installed=$("$DEST/$BIN" --version 2>/dev/null || echo "$VERSION")
	ok "Installed $installed"
	info ""

	if on_path "$DEST"; then
		info "  Try it:  ${C_DIM}vrok ./some-file${C_RESET}"
	else
		warn "$DEST is not on your PATH."
		info ""
		info "  Add it by running one of these, then restarting your shell:"
		info ""
		info "    ${C_DIM}echo 'export PATH=\"$DEST:\$PATH\"' >> ~/.zshrc${C_RESET}"
		info "    ${C_DIM}echo 'export PATH=\"$DEST:\$PATH\"' >> ~/.bashrc${C_RESET}"
		info ""
		info "  Or run it directly:  ${C_DIM}$DEST/vrok ./some-file${C_RESET}"
	fi
}

main "$@"
