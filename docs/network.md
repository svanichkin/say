# How it works

[Documentation](README.md) › How it works

## Yggdrasil

Say carries its own [Yggdrasil](https://yggdrasil-network.github.io/) node
([ygg](https://github.com/svanichkin/ygg)); no separate daemon is needed. An
address comes from its owner's public key, and all traffic is encrypted end
to end by Yggdrasil. Peers in the same local network (or on the same machine)
find each other by multicast and connect directly (`-nolan` turns this off).

## Direct calls

By default a call goes straight between the two machines, without public
Yggdrasil peers in between: they connect through their NATs (UDP hole
punching, as WebRTC does), for lower latency and fewer drop-outs, and fall
back to the Yggdrasil route when the direct link does not get through or is
worse. Each side then sees the other's public IP address, which Yggdrasil
otherwise hides, and Say asks public STUN servers (Google, Cloudflare, Sipnet)
for its own. With `-nodirect` on either side, the call stays on Yggdrasil.

## A call

1. The caller connects over TCP and sends HELLO with its protocol and Say
   versions. A peer that cannot talk to ours is refused with a message naming
   both versions.
2. The callee rings (protocol 17): RING, then OK or DECLINED. The line is
   taken meanwhile: other callers get BUSY. Peers of 14–16 are answered OK at
   once.
3. Both sides send their cards (protocol 15).
4. Voice and video go over UDP; the chat and files over the TCP connection.

One call at a time; a peer that reconnects after a drop replaces its stale
call.

## Presence

Every 30 seconds while no call is on (every 5 seconds in the first minute),
Say sends each device in the contacts PING in place of HELLO (protocol 16). Say
there answers PONG; no call begins and the line is not taken. A device that
has not answered within 5 seconds is not online. A Say of protocol 14 or 15
answers as a peer too old; it counts as online. A mesh peer cannot announce
that it came up, so asking is the only way.

## Voice

- **Codecs**, all pure Go: AMR-NB at 4.75 kbit/s with silence suppression
  (default), Opus at 12 kbit/s, G.722 at 48 kbit/s. Each side announces its
  codec in the handshake and decodes the other's, so they may differ.
- **The system's voice processing** (`-sysaudio`, on by default on macOS and
  Windows): VoiceProcessingIO on macOS (as FaceTime; the Mic Mode chosen in
  Control Center applies), the Voice Capture DSP on Windows. The system then
  knows it is a call and lowers other sounds; on Linux the streams have the
  phone role (`media.role=phone`).
- **Say's own audio** elsewhere: miniaudio and Say's echo canceller, with a
  reference delay estimated from the speaker buffer.
- **Small fixed buffers**: a 10 ms × 2 speaker buffer and a 40 ms jitter
  buffer; clock drift between the two sound cards is corrected by at most one
  sample per 20 ms frame. The audio thread only moves samples through
  lock-free rings filled at the priority the system gives audio.

## Video

- The camera's frames are cut to 352×288 and coded by Babe's coder (blocks of
  an error-split tree that copy the frame before, moved or not, or are drawn
  anew); a frame the camera hardly changed is not sent. The window shows the
  pixels; the terminal turns them into its cells. `-video-codec say` sends
  Say's cell video instead, only the cells that changed after a full frame.
- **Rate control as WebRTC's**: the receiver reports every 100 ms what
  arrived, its loss and the standing queue; the sender cuts its rate below
  what arrived when a queue stands, gently on heavy loss, and raises it on a
  clean path. Quality, then frames left out, meet the rate.
- **Loss**: a frame recovers from the newest one the receiver decoded, and
  after a loss every frame is coded against one the receiver acknowledged,
  so a lost datagram is lost alone. `-video-band` splits the picture further.
- Video is synchronised with the same peer's audio.

## Files in a call

A file goes over the call's TCP connection as fast as the path allows, while
keeping the delay it adds small so voice and video stay smooth. The receiver
saves it to `~/Downloads` and confirms; the sender shows **Sent** only then.

## Protocol versions

The protocol's history is in `network/tcp/hello.go`; the main steps:

| Version | |
| --- | --- |
| 14 | Change-only video frames |
| 15 | Each side sends its own card after the handshake |
| 16 | PING / PONG: is Say running there |
| 17 | A call rings before it is answered: RING, OK, DECLINED |

## Audio diagnostics

To compare the raw microphone, the echo reference and the processed
microphone, start Say with:

```sh
SAY_AUDIO_TRACE=/tmp/say-audio say
```

Each call writes a private three-channel WAV (8 kHz, 16-bit): the microphone,
the exact reference given to the echo filter, and its output. These files hold
the call's audio; nothing is recorded without the variable.
