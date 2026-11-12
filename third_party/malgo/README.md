# Say's copy of malgo v0.11.27

github.com/gen2brain/malgo with one addition, InitDeviceRaw (raw.go, and
goSetDeviceConfigRaw in miniaudio.c and malgo.h): a device whose data
callback is C code, so that the audio thread never runs Go; and, on Windows
(miniaudio.h, marked "Say:"), streams of a call: the default device is the
one for calls (eCommunications) and the stream's category is
AudioCategory_Communications. Say's go.mod replaces the module with this
copy.

## malgo
[![Build Status](https://github.com/gen2brain/malgo/actions/workflows/build.yml/badge.svg)](https://github.com/gen2brain/malgo/actions)
[![Go Reference](https://pkg.go.dev/badge/github.com/gen2brain/malgo.svg)](https://pkg.go.dev/github.com/gen2brain/malgo)

Go bindings for [miniaudio](https://github.com/dr-soft/miniaudio) library.

Requires `cgo` but does not require linking to anything on the Windows/macOS and it links only `-ldl` on Linux/BSDs.

### Installation

    go get -u github.com/gen2brain/malgo

### Documentation

Documentation on [GoDoc](https://godoc.org/github.com/gen2brain/malgo). Also check [examples](https://github.com/gen2brain/malgo/tree/master/_examples).

### Platforms

* Windows (WASAPI, DirectSound, WinMM)
* Linux (PulseAudio, ALSA, JACK)
* FreeBSD/NetBSD/OpenBSD (OSS/audio(4)/sndio)
* macOS/iOS (CoreAudio)
* Android (OpenSL|ES, AAudio)
