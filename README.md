# Say

Say is a terminal‑first voice and video call utility that builds an embedded
Yggdrasil node via [ygg](https://github.com/svanichkin/ygg) and speaks directly
to other peers over the Ygg overlay network.
Audio is captured with [malgo](https://github.com/gen2brain/malgo) (miniaudio),
encoded as G.722, and optionally sent via UDP for lower latency. Video frames are
sourced through [gocam](https://github.com/svanichkin/gocam), compressed then 
rendered in the current terminal session using Unicode glyphs.

![Screenshot](./sample.png)

The project is intended as a lightweight experiment: share your Yggdrasil
address, run `say` on both ends, and you have voice/video without deploying an
external Yggdrasil daemon or web stack.

## Filters

![Red](./red.webm)

![Pink](./pink.webm)

![Blue](./blue.webm)

## Features

- Embedded Yggdrasil node – no separate daemon required.
- G.722 voice (8 kHz / 48 kbps) with configurable frame size and UDP/TCP transports.
- Video streaming directly in the terminal two‑pane layout.
- Simple newline text bridge when media is disabled.
- Friend list bootstrap via the same `config.json` that holds the Yggdrasil
  settings.

## Requirements

- Go **1.24** (minimum) with cgo enabled.
- A working microphone for voice calls and, if you enable video, a webcam that
  is supported by your OS (V4L2 on Linux, AVFoundation on macOS, Media
  Foundation on Windows).
- System libraries required by miniaudio / Gocam (on Linux ensure ALSA/ Pulse
  dev packages are available; on macOS you just need Xcode command line tools).
- Permission to access camera and microphone for the terminal session.

## Installation

```sh
git clone https://github.com/svanichkin/say.git
cd Say
go build -o say ./...
```

You can also run the tool without building a binary:

```sh
go run .
```

## Configuration

Say stores its configuration under your OS config directory: on Linux this is
typically `$XDG_CONFIG_HOME/say/config.json` (or `~/.config/say/config.json` if
`XDG_CONFIG_HOME` is unset), while on macOS it resolves to
`~/Library/Application Support/say/config.json`. You can point to a different
path with `-config /path/to/config.json`.

The configuration file is a regular Yggdrasil node config plus an optional
`contacts_dir` key understood by Say:

```json
{
  "peers": [
    "tls://example.yggnode.net:443",
    "quic://203.0.113.10:8443"
  ],
  "contacts_dir": "/Users/me/Contacts"
}
```

Pass the same config file to Say – the embedded node uses it to bootstrap. When
`contacts_dir` is supplied (or overridden via `-contacts`), Say recursively
scans that directory. Each friend is represented as a folder that contains a
`say` subdirectory with text files holding IPv6 addresses:

```
~/Contacts/
  Alice G/
    say/
      laptop
  Bob D/
    say/
      phone
      console
```

- Если в `say` лежит единственный файл, его адрес добавляется как `Alice G`.
- Если файлов несколько, каждый превращается в отдельный контакт вида
  `Bob D/phone`, `Bob D/console`, где часть после слеша берётся из имени файла.

При клике друга по имени в CLI используйте те же подписи (`"Bob D/phone"`).
Если вы запускаете `say` с `-contacts /path/to/contacts`, этот путь сохраняется
в активный конфиг, поэтому повторный запуск может обойтись без флага.

The utility automatically updates the list of peers each time it starts.

## Usage

Run Say on one machine in listen mode:

```sh
./say
```

The program prints your Yggdrasil address. Share it with a friend and have them
dial you:

```sh
./say "[200:abcd:1234:...]"
```

Optionally pass the port as a separate argument, in any order:

```sh
./say "[200:abcd:1234:...]" 7777
./say 7777 "[200:abcd:1234:...]"
```

Select alternative configs explicitly via `-config work` (which maps to
`$XDG_CONFIG_HOME/say/work.json`). Positional arguments are interpreted as
either a dial target (`"[200:abcd:...]"`) or a friend name from contacts
(`"Alice G"`, `"Bob D/phone"`). To run the server with another profile, always
pass `-config`; bare tokens are no longer treated as profile names.

Audio and video are always enabled when hardware is present; no extra flags are
required. Relevant CLI switches:

- `-port` – listening TCP/UDP port (defaults to `7777`).
- `-config` – config profile or path; `-config work` maps to `$XDG_CONFIG_HOME/say/work.json`.
- `-contacts` – override the contacts directory defined in the config.
- `-v` – verbose logging.
- Color filters: `-red`, `-orange`, `-yellow`, `-green`, `-teal`, `-blue`, `-purple`, `-pink`, `-gray`, `-bw` – apply a tinted (or neutral) monochrome filter to the local/remote viewports (purely visual, codec stream stays untouched). Use at most one flag; if several are present the last one wins.

When media is disabled on both ends, Say falls back to a simple line‑based text
bridge over the established TCP session.

### Example call flow

1. Both peers ensure their configs contain working Yggdrasil peers.
2. Alice runs `./say`.
3. Bob runs `./say "[alice-ygg-addr]"`.
4. Speak! Use `Ctrl+C` to terminate; the app restores the terminal state.

## Development

Standard Go tooling works:

```sh
go test ./...
golangci-lint run       # if you use golangci-lint
```

Audio/video capture and terminal drawing depend on the current OS; on a headless
machine you’ll need to mock or stub out the camera/microphone layers.

## License

This project is distributed under the terms of the [MIT License](LICENSE).
