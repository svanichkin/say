#!/bin/sh
# macos-app.sh BINARY VERSION OUT_DIR: puts a macOS build of Say into
# OUT_DIR/Say.app. Started from the bundle (a double click, Launchpad, open)
# Say has no terminal, so it opens its window. The bundle carries what macOS
# asks of an app using the camera and the microphone: the usage strings,
# without which it is stopped at the first access. It is signed ad hoc: macOS
# may ask for the camera and the microphone again after each new build.
set -eu

bin=$1
version=${2:-dev}
out=${3:-bin}

if [ "$(uname -s)" != Darwin ]; then
	echo "Say.app is built on macOS" >&2
	exit 1
fi

app="$out/Say.app"
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources/en.lproj" "$app/Contents/Resources/ru.lproj"
cp "$bin" "$app/Contents/MacOS/say"
cp "$(dirname "$0")/../assets/icon/Say.icns" "$app/Contents/Resources/Say.icns"

# v1.3.0-dirty -> 1.3.0 (what CFBundleShortVersionString takes)
short=$(printf '%s' "$version" | sed -E 's/^v//; s/[^0-9.].*$//')
[ -n "$short" ] || short=0.0.0

cat > "$app/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key>
	<string>en</string>
	<key>CFBundleDisplayName</key>
	<string>Say</string>
	<key>CFBundleExecutable</key>
	<string>say</string>
	<key>CFBundleIconFile</key>
	<string>Say</string>
	<key>CFBundleIdentifier</key>
	<string>io.github.svanichkin.say</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>CFBundleName</key>
	<string>Say</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>$short</string>
	<key>CFBundleVersion</key>
	<string>$version</string>
	<key>LSApplicationCategoryType</key>
	<string>public.app-category.social-networking</string>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>NSCameraUsageDescription</key>
	<string>Say shows your camera to the person you call.</string>
	<key>NSMicrophoneUsageDescription</key>
	<string>Say sends your voice to the person you call.</string>
</dict>
</plist>
EOF

cat > "$app/Contents/Resources/en.lproj/InfoPlist.strings" <<'EOF'
"NSCameraUsageDescription" = "Say shows your camera to the person you call.";
"NSMicrophoneUsageDescription" = "Say sends your voice to the person you call.";
EOF
cat > "$app/Contents/Resources/ru.lproj/InfoPlist.strings" <<'EOF'
"NSCameraUsageDescription" = "Say показывает вашу камеру тому, с кем вы говорите.";
"NSMicrophoneUsageDescription" = "Say передаёт ваш голос тому, с кем вы говорите.";
EOF

codesign --force --sign - --identifier io.github.svanichkin.say "$app"
echo "$app"
