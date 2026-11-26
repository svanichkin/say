#!/usr/bin/env bash
# notarize.sh [APP]: signs Say.app (bin/Say.app, from make app) with a
# Developer ID and the hardened runtime, has Apple notarize it, staples the
# ticket and packs Say-macos.zip beside the app: an app Gatekeeper opens on
# any Mac without "unidentified developer". Prints the zip's path.
#
#   SAY_SIGN_ID       the identity (a name or its SHA-1); by default the
#                     first "Developer ID Application" in the keychain
#   SAY_NOTARY        a keychain profile of notarytool (default say-notary),
#                     made once:
#                       xcrun notarytool store-credentials say-notary \
#                         --key AuthKey_<ID>.p8 --key-id <ID> --issuer <issuer ID>
#   SAY_NOTARY_KEY, SAY_NOTARY_KEY_ID, SAY_NOTARY_ISSUER
#                     or an App Store Connect API key itself (the CI's)
#   SAY_SIGN_ONLY=1   signed, not notarized (no credentials)
set -euo pipefail
cd "$(dirname "$0")/.."

app=${1:-bin/Say.app}
zip="$(cd "$(dirname "$app")" && pwd)/Say-macos.zip"
[ -d "$app" ] || { echo "no $app: make app first" >&2; exit 1; }

id=${SAY_SIGN_ID:-$(security find-identity -v -p codesigning | awk '/Developer ID Application/ { print $2; exit }')}
[ -n "$id" ] || { echo "no Developer ID Application identity in the keychain" >&2; exit 1; }

codesign --force --timestamp --options runtime \
	--entitlements scripts/say.entitlements \
	--identifier io.github.svanichkin.say \
	--sign "$id" "$app"
codesign --verify --strict --deep "$app"

rm -f "$zip"
if [ -z "${SAY_SIGN_ONLY:-}" ]; then
	if [ -n "${SAY_NOTARY_KEY:-}" ]; then
		auth=(--key "$SAY_NOTARY_KEY" --key-id "$SAY_NOTARY_KEY_ID" --issuer "$SAY_NOTARY_ISSUER")
	else
		auth=(--keychain-profile "${SAY_NOTARY:-say-notary}")
	fi
	# Sent as a zip (ditto keeps the bundle as it is); the ticket is stapled
	# to the app, then the zip made again with the ticket in it.
	ditto -c -k --keepParent "$app" "$zip"
	out=$(mktemp)
	xcrun notarytool submit "$zip" "${auth[@]}" --wait --timeout 30m --output-format json >"$out"
	status=$(plutil -extract status raw "$out")
	if [ "$status" != Accepted ]; then
		echo "notarization: $status" >&2
		xcrun notarytool log "$(plutil -extract id raw "$out")" "${auth[@]}" >&2 || true
		rm -f "$out" "$zip"
		exit 1
	fi
	rm -f "$out" "$zip"
	xcrun stapler staple -q "$app"
	spctl --assess --type execute "$app"
fi
ditto -c -k --keepParent "$app" "$zip"
echo "$zip"
