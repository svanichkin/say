# Window, tray and terminal

[Documentation](README.md) › Window, tray and terminal

Say draws the same interface in its own window and in a terminal. The window
shows the video as a picture with an effect over it, neon frames and a glow;
the terminal draws the video in text cells and the frames in lines.

## Which one opens

| Started as | Opens |
| --- | --- |
| `Say.app` (macOS) | the window |
| `say` in a terminal (macOS, Linux, BSD) | the terminal |
| a double click or a shortcut (Windows) | the window |
| `say` in cmd, PowerShell or Windows Terminal | the terminal |
| `say` in its own rio window (Plan 9, drawterm too) | the window |
| `say` in `vt` (Plan 9) | the terminal, in 16 colours |
| over SSH | always the terminal |

`-gui` and `-text` choose. The window opens where it was closed, at the same
size (kept in `config-window.json` beside the configuration).

## Hiding and quitting (window)

- `x` in the first column of the home menu, and the window's own close
  button, hide the window. Say keeps running and waits for calls. A call
  coming in brings the window back; so does a click on the Dock icon or
  **Open Say** in the tray.
- Cmd+Q (macOS), **Quit** in the Dock's or the tray's menu, or Ctrl+C quit.
- On Windows the window is minimised instead of hidden for now; Ctrl+C quits.
- In a terminal, `x` in the first column quits.

## Dock or tray (macOS)

**Settings → Say's icon**: **In the Dock** (as any app) or **In the tray**: an
icon in the menu bar and none in the Dock. The tray's menu lists the contacts,
a green or red dot before each, each opening to its devices to call, then
**Open Say** and **Quit**. The choice is kept in `config-tray.json`. Windows'
notification area is still to come.

## Video in the window

- `-video-fx NAME`: the effect over both video panes. `hologram` by default:
  its own exposure and a faint grid of dots, light on dark and dark on light.
  Also `neon`, `thermal`, `duotone`, `bladerunner`, `poster`, `aberration`,
  `crt`, `glitch`, or `none`.
- `-video-light`: shadows lifted and highlights held, each place by its own
  light, then the levels stretched (the hologram does this anyway).
- `-video-sharp N`: sharpness in percent, 100 by default, `0` off.
- Cmd+ and Cmd− (Ctrl on Windows) ask the peer for one row more or less of
  detail, up to twice the pane's size within the codec's 160×80 cells.
- `-red`, `-orange`, `-yellow`, `-green`, `-teal`, `-blue`, `-purple`, `-pink`,
  `-gray`, `-bw`: a tinted monochrome filter on both panes. In the window `-bw`
  shows each pane in its owner's colour.

All of these change only what you see; the stream sent is not touched.

The text windows (menus, the status, the chat) have the same faint grid of
dots behind their text, as an old monitor; fainter in inactive columns.

## Terminal

The terminal needs 24-bit colour for the frames (most terminals today).
`-colors 16` uses the 16 ANSI colours with dithering and a plain layout, for
terminals without it, such as Plan 9's `vt` (the default there). On Windows,
started by a double click, Say reopens itself in Windows Terminal when it is
installed (the classic console has no font fallback); `-nowt` stays in the
classic console.

## Plan 9

Started in a rio window of its own, Say draws the window through `/dev/draw`,
drawn by the CPU at Unifont's 8×16 cells, sending only what changed. Keys come
from `/dev/cons` (Del or Ctrl-C stop Say), clicks from `/dev/mouse`; the
window can be resized. Without a camera, `-camera clip.y4m` plays a file as
one.

## Linux and BSD desktops

`make install-desktop` puts Say into the desktop's menu for this user (a
launcher and the icon; `say` must be on the `PATH`). The launcher opens Say in
a terminal. Started without a terminal in a graphical session (a double
click in a file manager), Say reopens itself in one: `$TERMINAL`, then
`x-terminal-emulator`, then the common terminals (GNOME's, Konsole, Xfce's,
kitty, Ghostty, Alacritty, WezTerm, foot, xterm).
