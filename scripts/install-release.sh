#!/usr/bin/env bash
set -euo pipefail

REPO="${REPO:-svanichkin/say}"
INSTALL_DIR="${INSTALL_DIR:-}"
REQUESTED_VERSION="${1:-${VERSION:-latest}}"

command -v curl >/dev/null 2>&1 || { echo "curl is required"; exit 1; }

detect_platform() {
	local os arch raw_os raw_arch
	raw_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
	case "$raw_os" in
		linux*) os="linux" ;;
		darwin*) os="darwin" ;;
		freebsd*) os="freebsd" ;;
		openbsd*) os="openbsd" ;;
		netbsd*) os="netbsd" ;;
		msys*|mingw*|cygwin*) os="windows" ;;
		*) echo "Unsupported OS: $raw_os" >&2; exit 1 ;;
	esac

	raw_arch="$(uname -m)"
	case "$raw_arch" in
		x86_64|amd64) arch="amd64" ;;
		aarch64|arm64) arch="arm64" ;;
		armv7l|armv7|armv8l) arch="armv7" ;;
		*) echo "Unsupported architecture: $raw_arch" >&2; exit 1 ;;
	esac

	echo "$os" "$arch"
}

get_latest_tag() {
	local latest_json
	latest_json="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest")"
	printf '%s\n' "$latest_json" | awk -F'"' '/"tag_name"/ { print $4; exit }'
}

main() {
	read -r os arch <<<"$(detect_platform)"

	local default_dir
	case "$os" in
		windows)
			if [ -n "${HOME:-}" ]; then
				default_dir="$HOME/AppData/Local/Programs/say"
			else
				default_dir="./bin"
			fi
			;;
		darwin)
			# /usr/local/bin is in the default macOS PATH (/etc/paths) and comes
			# before /usr/bin, so it shadows the system text-to-speech /usr/bin/say.
			default_dir="/usr/local/bin"
			;;
		*)
			# Linux / BSD fallback logic unchanged
			if command -v id >/dev/null 2>&1 && [ "$(id -u)" -ne 0 ] && [ -n "${HOME:-}" ]; then
				default_dir="$HOME/.local/bin"
			else
				default_dir="/usr/local/bin"
			fi
			;;
	esac
	INSTALL_DIR="${INSTALL_DIR:-$default_dir}"

	SUDO=""
	if [ "$(id -u)" -ne 0 ]; then
		if { [ -d "$INSTALL_DIR" ] && [ ! -w "$INSTALL_DIR" ]; } ||
			{ [ ! -d "$INSTALL_DIR" ] && [ ! -w "$(dirname "$INSTALL_DIR")" ]; }; then
			SUDO="sudo"
		fi
	fi

	local tag version
	if [ "$REQUESTED_VERSION" = "latest" ]; then
		tag="$(get_latest_tag)"
	else
		tag="$REQUESTED_VERSION"
	fi
	[ -n "$tag" ] || { echo "Failed to determine release tag"; exit 1; }
	version="${tag#v}"

	local archive_ext archive_name binary_name download_url

	# All release archives are named like:
	#   say-darwin-amd64.tar.gz
	#   say-linux-amd64.tar.gz
	#   say-windows-amd64.tar.gz
	archive_ext="tar.gz"

	if [ "$os" = "windows" ]; then
		binary_name="say.exe"
	else
		binary_name="say"
	fi

	# Linux on x86-64 with AVX2 takes the build for it (x86-64-v3).
	if [ "$os" = "linux" ] && [ "$arch" = "amd64" ] && grep -qw avx2 /proc/cpuinfo 2>/dev/null; then
		arch="amd64-v3"
	fi
	archive_name="say-${os}-${arch}.${archive_ext}"
	download_url="https://github.com/${REPO}/releases/download/${tag}/${archive_name}"

	tmpdir="$(mktemp -d)"
	trap 'rm -rf "${tmpdir:-}"' EXIT

	echo "Downloading ${download_url}"
	curl -fL "$download_url" -o "${tmpdir}/${archive_name}"

	# The release keeps each archive's sha256 beside it: a broken or
	# substituted download is not installed.
	local want got
	want="$(curl -fsSL "${download_url}.sha256" | awk '{print $1}')"
	if command -v sha256sum >/dev/null 2>&1; then
		got="$(sha256sum "${tmpdir}/${archive_name}" | awk '{print $1}')"
	elif command -v shasum >/dev/null 2>&1; then
		got="$(shasum -a 256 "${tmpdir}/${archive_name}" | awk '{print $1}')"
	elif command -v sha256 >/dev/null 2>&1; then
		got="$(sha256 -q "${tmpdir}/${archive_name}")"
	fi
	if [ -z "$want" ] || [ -z "${got:-}" ]; then
		echo "Warning: the checksum could not be checked" >&2
	elif [ "$want" != "$got" ]; then
		echo "Checksum mismatch for ${archive_name}: expected ${want}, got ${got}" >&2
		exit 1
	else
		echo "Checksum OK"
	fi

	if [ "$archive_ext" = "zip" ]; then
		command -v unzip >/dev/null 2>&1 || { echo "unzip is required to extract Windows archives"; exit 1; }
		unzip -q "${tmpdir}/${archive_name}" -d "$tmpdir"
	else
		tar -xzf "${tmpdir}/${archive_name}" -C "$tmpdir"
	fi

	${SUDO:-} mkdir -p "$INSTALL_DIR"
	${SUDO:-} install -m 755 "${tmpdir}/${binary_name}" "${INSTALL_DIR}/${binary_name}"

	echo "Binary installed to ${INSTALL_DIR}/${binary_name}"

	# Warn when another "say" (e.g. macOS /usr/bin/say) comes first in PATH.
	local found
	found="$(command -v "$binary_name" 2>/dev/null || true)"
	if [ "$found" != "${INSTALL_DIR}/${binary_name}" ]; then
		echo
		echo "Warning: '${binary_name}' in your PATH resolves to: ${found:-nothing}"
		echo "Put ${INSTALL_DIR} before it in PATH, e.g. add to your shell profile:"
		echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
	fi
}

main "$@"
