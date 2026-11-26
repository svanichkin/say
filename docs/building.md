# Building

[Documentation](README.md) › Building

## Requirements

- Go 1.27.3 or later, with cgo (a C compiler: Xcode's command line tools on
  macOS, gcc or clang elsewhere, mingw for Windows from another system).
- Linux: the ALSA or PulseAudio development packages. Cameras through V4L2:
  every `/dev/video*` capture node is tried (`-camera` picks one); SoC cameras
  with a media controller (PinePhone and similar) are set up automatically,
  `GOCAM_SENSOR=ov5640` picks a sensor. The user needs the `video` group, and
  for SoC cameras `/dev/media*` and `/dev/v4l-subdev*`. Raw Bayer sensors are
  not supported.
- Babe, the video coder (`github.com/svanichkin/Babe`), is a private module:
  building fetches it with `GOPRIVATE=github.com/svanichkin/Babe` and a git
  login that can read it. Working on both, a `go.work` beside `go.mod`
  (ignored by git) builds against a Babe checkout: `go work init . ../Babe`.

## Make

```sh
git clone https://github.com/svanichkin/say.git
cd say
make                       # bin/say, a release build
make app                   # bin/Say.app (macOS)
make debug                 # a debug build
make build-all             # every system make knows, into bin/<os>-<arch>/
make build-windows/amd64 CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++
bash scripts/check.sh      # gofmt, vet, the tests, the Windows, Plan 9 and Linux builds
```

Without make: `CGO_ENABLED=1 go build .` or `go install
github.com/svanichkin/say@latest` (on Windows, set `CGO_ENABLED=1` first).

A release build has no debugging information (`-trimpath -s -w`: a third of
the size; a crash still names its functions).

## Debug build

`make debug` (or `-tags debug`) traces calls in detail: `[debug]` and `[trace]`
lines with `-log debug`, the peer's log sent over the call to a caller started
with `-log debug` (saved beside ours as `*-peer.log`), and on Windows a debug
log without any flags. It keeps the debugging information for a debugger. It
is for chasing problems: the tracing takes some CPU, the log grows by tens of
MB an hour, and the log sent over a call takes bandwidth and shows the peer
your addresses.

## The macOS app

`make app` puts `Say.app` into `bin/`, signed ad hoc: macOS may ask for the
camera and the microphone again after each build.

`make notarize` signs it with the Developer ID and the hardened runtime (the
camera, the microphone and the contacts: `scripts/say.entitlements`), has
Apple notarize it, staples the ticket and packs `bin/Say-macos.zip`. Once, the
notary's API key into the keychain:

```sh
xcrun notarytool store-credentials say-notary --key AuthKey_<ID>.p8 --key-id <ID> --issuer <issuer ID>
```

## Releases

A tag `v*.*.*` runs `.github/workflows/go.yml`: the tests of `check.sh`, then
the builds for Linux (amd64, arm64), Windows, macOS (amd64, arm64: the bare
`say` and a `Say.app` of each, signed and notarized) and Plan 9 (amd64, 386,
arm), and a release with their checksums. Its secrets: `BABE_SSH_KEY` (the
private half of a read-only deploy key of Babe), `DEVELOPER_ID_P12` and
`DEVELOPER_ID_P12_PASSWORD` (the Developer ID certificate), `NOTARY_KEY`,
`NOTARY_KEY_ID` and `NOTARY_ISSUER_ID` (an App Store Connect API key). Without
the certificate the apps are signed ad hoc.

## The screenshots

`docs/images` is drawn by Say itself (`shots_test.go`): its screens in
English, with made-up contacts and two pictures as the cameras, the window's
by the software renderer with the default video effect, the terminal's its
cells. After the interface changes:

```sh
make shots SELF=me.png PEER=them.png
```

The two pictures are not kept in the repository.
