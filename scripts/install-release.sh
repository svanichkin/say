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
		msys*|mingw*|cygwin*) os="windows" ;;
		*) echo "Unsupported OS: $raw_os" >&2; exit 1 ;;
	esac

	raw_arch="$(uname -m)"
	case "$raw_arch" in
		x86_64|amd64) arch="amd64" ;;
		aarch64|arm64) arch="arm64" ;;
		armv7l|armv7) arch="armv7" ;;
		*) echo "Unsupported architecture: $raw_arch" >&2; exit 1 ;;
	esac

	echo "$os" "$arch"
}

get_latest_tag() {
	local latest_json
	latest_json="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest")"
	printf '%s\n' "$latest_json" | awk -F'"' '/"tag_name"/ { print $4; exit }'
}

ensure_dir() {
	if [ ! -d "$INSTALL_DIR" ]; then
		mkdir -p "$INSTALL_DIR"
	fi
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
			# Install into MacPorts-style path
			default_dir="/opt/local/bin"
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
	if [ "$os" = "darwin" ] && [ "$INSTALL_DIR" = "/opt/local/bin" ] && [ "$(id -u)" -ne 0 ]; then
		SUDO="sudo"
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

	archive_name="say-${os}-${arch}.${archive_ext}"
	download_url="https://github.com/${REPO}/releases/download/${tag}/${archive_name}"

	tmpdir="$(mktemp -d)"
	trap 'rm -rf "${tmpdir:-}"' EXIT

	echo "Downloading ${download_url}"
	curl -fL "$download_url" -o "${tmpdir}/${archive_name}"

	if [ "$archive_ext" = "zip" ]; then
		command -v unzip >/dev/null 2>&1 || { echo "unzip is required to extract Windows archives"; exit 1; }
		unzip -q "${tmpdir}/${archive_name}" -d "$tmpdir"
	else
		tar -xzf "${tmpdir}/${archive_name}" -C "$tmpdir"
	fi

	ensure_dir
	${SUDO:-} install -m 755 "${tmpdir}/${binary_name}" "${INSTALL_DIR}/${binary_name}"

	echo "Binary installed to ${INSTALL_DIR}/${binary_name}"
}

main "$@"
