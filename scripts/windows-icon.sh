#!/bin/sh
# windows-icon.sh: puts assets/icon/say.ico into the Windows executable, as
# a resource file Go links in by its name (rsrc_windows_amd64.syso, beside
# main.go). Needs mingw's windres. The .syso is kept in the repository, so
# builds without mingw (CI) have the icon too.
set -eu
cd "$(dirname "$0")/.."
windres=${WINDRES:-x86_64-w64-mingw32-windres}
rc=$(mktemp -t say-icon.XXXXXX)
trap 'rm -f "$rc"' EXIT
printf '1 ICON "%s"\n' "assets/icon/say.ico" > "$rc"
"$windres" --input-format=rc --output-format=coff --target=pe-x86-64 -i "$rc" -o rsrc_windows_amd64.syso
echo rsrc_windows_amd64.syso
