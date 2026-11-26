# Command line

[Documentation](README.md) › Command line

```sh
say                                 # wait for calls (the home screen)
say 200:abcd:1234:…                 # call an address
say "[200:abcd:1234:…]:7001"        # with a port other than 7777
say 7001 200:abcd:1234:…            # the port as its own argument, in any order
say "Jordan Lee"                    # call a contact: the device called last, else the first
```

## Options

### Window and language

| Option | |
| --- | --- |
| `-gui`, `-text` | The window or the terminal (see [Window, tray and terminal](window.md)) |
| `-lang CODE` | The language: en, ru, uk, be, kk, uz, az, ka, hy, de, fr, es, pt, it, nl, pl, cs, sk, sl, hr, sr, bg, ro, hu, fi, sv, nb, da, et, lv, lt, el, tr, id, ms, vi, zh, zh-Hant, ja, ko. Without it, the first of the system's languages Say knows, else English |
| `-colors 16\|truecolor` | Terminal colours; `16` on Plan 9 by default, `truecolor` elsewhere |
| `-nowt` | Windows: stay in the classic console |
| `-contacts DIR` | The contacts folder (kept for next time) |

### Video

| Option | |
| --- | --- |
| `-video-fx NAME`, `-video-light`, `-video-sharp N` | The window's video look (see [Window](window.md#video-in-the-window)) |
| `-red` … `-gray`, `-bw` | A monochrome filter on both panes |
| `-fps N` | A cap on the frame rate; without it every camera frame is coded and the rate control sends what the path allows |
| `-video-codec say` | Both peers: Say's video of cells (as protocol 8). By default the video goes in pixels by Babe's coder; the terminal turns them into cells |
| `-video-q N`, `-video-qmax N` | Babe's quality on a troubled path (default 40) and the most it rises to on a clean one (default 50) |
| `-video-hold N` | Say's cell video: a cell keeps its look while it changes by at most N (default 13, 0 off), so a still picture does not shimmer. `-video-hold-debug` tints the held cells |
| `-video-band N` | Cut the picture into bands of N rows, each its own stream, so a loss breaks only its band (default 0: one band). More traffic and delay |
| `-camera DEVICE` | Linux: use this camera only (`/dev/video2`) |
| `-camera FILE` | A `.y4m` or `.frames` file as the camera, in a loop: `ffmpeg -i clip.mp4 -vf scale=352:288 -pix_fmt yuv420p clip.y4m`, or `go run ./cmd/recordframes` |

### Audio

| Option | |
| --- | --- |
| `-audio-codec amrnb\|opus\|g722` | Your voice's codec: AMR-NB 4.75 kbit/s (default), Opus 12 kbit/s, G.722 48 kbit/s. Each side may choose its own |
| `-sysaudio on\|off` | The system's voice processing (echo cancellation, noise suppression, gain), on by default on macOS and Windows; `off` uses miniaudio and Say's own echo canceller |
| `-nomic` | Listen only: no microphone, no audio sent |

### Network

| Option | |
| --- | --- |
| `-port N` | The TCP/UDP port, 7777 by default |
| `-transport ygg\|plain` | The embedded Yggdrasil node (default), or ordinary TCP/UDP over IP: `say -transport plain -port 7001 127.0.0.1:7000` |
| `-nolan` | No Yggdrasil discovery in the local network |
| `-nodirect` | No direct calls: the call stays on its Yggdrasil route, and the IP addresses stay hidden (see [How it works](network.md#direct-calls)) |
| `-netsim[=NAME]` | Testing: this side's outgoing media through a simulated bad path: `loss`, `narrow`, `switch`, `mixed`, `heavy` (bare) |

### Configuration and logs

| Option | |
| --- | --- |
| `-config NAME\|PATH` | The configuration profile: `-config work` is `work.json` in Say's folder |
| `-log`, `-log debug` | Also write `<config-name>.log` beside the executable; `debug` the most detail |
| `-v` | Verbose logging |
| `-version`, `-donate`, `-support` | Print and exit |

## Files

The configuration is under your system's config folder: `~/Library/Application
Support/say/` on macOS, `$XDG_CONFIG_HOME/say/` (or `~/.config/say/`) on Linux.
It is a regular Yggdrasil node configuration: `config.json`, or the profile
named by `-config`. Beside it:

| File | |
| --- | --- |
| `config-contacts.json` | The contacts' settings: the source, the folder, how names show, who was called last |
| `config-contacts-pending/` | Cards received in calls from unknown addresses |
| `config-window.json` | The window's place and size |
| `config-tray.json` | Dock or tray (macOS) |
| `say-crash.log` | What the Go runtime printed when Say crashed |
