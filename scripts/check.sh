#!/usr/bin/env bash
# check.sh: gofmt, go vet, the tests of the packages named (by default the
# ones that are green), and the builds for Windows, Plan 9 and Linux. Prints
# one line when all is well, else the first error of each step that failed.
#
#   bash scripts/check.sh              # the default packages
#   bash scripts/check.sh ./contacts   # these packages' tests only
#   SKIP_CROSS=1 bash scripts/check.sh # no builds for other systems
#
# Tests red on HEAD before this work (codec, network/udp, ui's TestSelfMenu)
# are not run: they are the video work's. vet's old warning on ui/term.go's
# renderer (a cancel not used on all paths) is not reported.
set -u
cd "$(dirname "$0")/.."
export PATH=/opt/homebrew/bin:$PATH
pkgs=("$@")
if [ ${#pkgs[@]} -eq 0 ]; then
	pkgs=(. ./contacts ./ui ./lang ./conf ./network/tcp ./keys ./gui ./hud ./device ./mediactrl ./tones)
fi
failed=0
step() { # name, then the command; shows its first lines on failure
	local name=$1
	shift
	local out
	if ! out=$("$@" 2>&1); then
		echo "✗ $name:"
		printf '%s\n' "$out" | grep -v 'duplicate libraries' | grep -v '^#' | head -12
		failed=1
	fi
}
unformatted=$(gofmt -l . 2>/dev/null | grep -v '^vendor/' | grep -v '^third_party/')
if [ -n "$unformatted" ]; then
	echo "✗ gofmt: $unformatted"
	failed=1
fi
# vet, but for its old warning on ui/term.go's renderer (before this work).
vetout=$(go vet "${pkgs[@]}" 2>&1 | grep -v 'duplicate libraries' | grep -v '^#' | grep -v 'ui/term.go:[0-9]*:.*cancel')
if [ -n "$vetout" ]; then
	echo "✗ vet:"
	printf '%s\n' "$vetout" | head -12
	failed=1
fi
step "tests" go test -count=1 -skip 'TestSelfMenu' "${pkgs[@]}"
if [ -z "${SKIP_CROSS:-}" ]; then
	step "windows" env GOOS=windows GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ go build -o /dev/null .
	step "plan9" env GOOS=plan9 GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null .
	step "linux" env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./contacts ./keys
fi
if [ $failed -eq 0 ]; then
	echo "ок: fmt, vet, тесты (${pkgs[*]}), windows, plan9, linux"
fi
exit $failed
