# TODO

## Microphone mute in the system (macOS)

Mute the microphone in VoiceProcessingIO itself (`kAUVoiceIOProperty_MuteOutput`)
instead of only dropping frames in the sender. The sender keeps dropping them, so
nothing goes over the network.

- `kAUVoiceIOProperty_MutedSpeechActivityEventListener` (macOS 14+): the system
  reports speech while muted. Show "your microphone is off" when it does. It works
  only with `MuteOutput`.
- `AVAudioApplication.setInputMuted` / `setInputMuteStateChangeHandler` (macOS 14+):
  the system knows the call is muted; the AirPods stem press mutes and unmutes Say.

Windows and Linux have no per-application system mute that adds anything: the
endpoint mute silences the microphone for every program.

## Mic Mode in the log

Log the active Mic Mode (`AVCaptureDevice.activeMicrophoneMode`) at call start.
The menu on the self view opens the picker; the log does not say which mode was on.

## Always on: contacts, ringing, tray

Say runs and waits: anyone can call at any time, and the user can call anyone
from a contact list. No daemon: one process, as now. The code is split into a
core (Yggdrasil, calls, media, contacts, presence, history) and a screen, so a
daemon with clients attached over a socket can be added later without a rewrite.

### Start

- The system has a tray and Say has a window there (macOS, Windows): Say starts
  in the tray and shows its window. Closing the window hides it; Say keeps
  waiting. The tray menu: Open, Take calls on / off (off: Say is offline, calls
  do not get through), Settings, Quit.
- A window but no tray (a Linux desktop without one, if Say gets a window
  there): the window only; closing it quits.
- No window (Linux, BSD, Plan 9 today, or `-text`): the terminal, as now.
- The "GUI or TUI?" question at start goes away; `-gui` / `-text` choose.
- Started from a terminal into the tray, Say detaches from it, so closing the
  terminal does not end it.
- One Say per configuration (the lock): a second start asks the running one to
  show its window and exits (a small local socket; later it can also take
  `say call`, `say hangup`, `say events` from scripts).
- Autostart at login: LaunchAgent (macOS), Run key or Task Scheduler (Windows),
  systemd --user (Linux, terminal mode: opens a terminal with Say in it).
- The window is hidden and shown, never created again (Ebitengine opens a
  window once per process): `NSWindow orderOut` / `ShowWindow(SW_HIDE)`.

### Home screen

- Our address with Copy, online state, then the contacts: recent calls first,
  missed ones marked.
- One row per person with their devices and who is online:
  `Alice  ● home  ○ phone`. Enter calls a device that is online; Tab or Right
  picks another.
- The input line filters by name; a pasted address calls it.
- Add a contact (name, address, port, label); after a call with an unknown
  address, offer to save it.
- `say <name|address>` still calls at once; after the call Say goes to the home
  screen instead of exiting.

### Contacts: a folder of vCards

- Default `~/.config/say/contacts/`, `contacts_dir` / `-contacts` override. A
  folder of `.vcf` files is a vdir: vdirsyncer syncs it with any CardDAV
  (Nextcloud, iCloud, Google), and so with GNOME's and KDE's address books.
  iCloud Drive, Dropbox or Syncthing sync it between machines.
- Read vCard 2.1, 3.0 and 4.0; write 3.0, as macOS Contacts does. A file may
  hold several cards (a Contacts export).
- One card, several devices, each with a label, macOS style:

  ```
  BEGIN:VCARD
  VERSION:3.0
  FN:Alice
  PHOTO;ENCODING=b;TYPE=JPEG:/9j/4AAQ…
  item1.IMPP;X-SERVICE-TYPE=Say:say://[200:abcd::1]
  item1.X-ABLabel:home
  item2.IMPP;X-SERVICE-TYPE=Say:say://[201:…]:7000
  item2.X-ABLabel:phone
  END:VCARD
  ```

  The port is written only when it is not 7777. `TYPE=HOME/WORK` labels are
  read too.
- The old format (a folder per friend with a `say` file) is removed, without
  migration.
- No "send my card".
- Register the `say://` scheme (app bundle, Windows registry,
  `x-scheme-handler/say`): a click on such a link in Contacts, a browser or a
  messenger calls through Say.
- macOS Contacts (Contacts framework): read people with a "Say" IM service and
  their photos; with iCloud they come from the iPhone too. Needs the Contacts
  permission, so an `.app` bundle. Check whether Contacts lets one type a custom
  IM service; if not, Say writes the address. Windows: the folder only (the
  system address book is for Store apps).

### Presence

- At start Say tells its contacts it is online; every few minutes (with jitter)
  and when the list is opened it asks each device "are you there?".
- Online, in a call (busy), offline; per device.
- Presence is answered to contacts only; anyone can still call.

### Calling with answer

- A new phase before the media: calling → ringing → answered / busy /
  declined / cancelled / no answer (45 s) / hung up. The states and their
  codes follow SIP (INVITE, 180, 200, 486, 603, CANCEL, BYE); the transport
  stays ours. Media (camera, microphone) starts only after the answer.
- A new protocol version: an older caller gets PROTOCOL MISMATCH; an older
  listener answers at once, as now.
- Redial only after a drop, never after a hang-up or a decline.
- Hang up: an item in the self view's menu and a key; both sides go back to the
  home screen.
- Incoming call: the photo from the vCard, else the caller's frame colour; in
  the window and in kitty/Ghostty as an image, elsewhere encoded as cells by the
  video codec. The photo can also fill the peer's pane while its camera is off.
- Raise the call: the window comes to the front. In a terminal: activate the
  terminal app on macOS (`TERM_PROGRAM`), bring the console forward on Windows
  (else flash it), xterm's raise sequence on X11, a notification on Wayland. A
  system notification and BEL everywhere.

### Sounds (an audio change: on its own, with consent)

- Ringback for the caller: an original "frog" tone in the style of XM keygen
  music, synthesised. The incoming ring: a short tune in the same style.
- Played locally by a plain output of their own, not through the call's audio.

### Buttons and shortcuts

Every button alternates its label and its shortcut every 2 s:
`[Copy]` ↔ `[⌘C]`. Shortcuts follow Teams, which people know:

| Action | macOS window | Windows window | Terminal |
|---|---|---|---|
| Answer | ⌘⇧A | Ctrl+Shift+A | Enter |
| Decline / cancel | ⌘⇧D | Ctrl+Shift+D | Esc |
| Hang up | ⌘⇧H | Ctrl+Shift+H | Ctrl+Shift+H or Alt+H |
| Microphone | ⌘⇧M | Ctrl+Shift+M | Ctrl+Shift+M or Alt+M |
| Camera | ⌘⇧O | Ctrl+Shift+O | Ctrl+Shift+O or Alt+O |
| Copy address | ⌘C | Ctrl+C | Alt+C |

A terminal passes no ⌘, Ctrl+C quits and Ctrl+M is Enter: Ctrl+Shift works
where the terminal speaks the kitty keyboard protocol (kitty, Ghostty, WezTerm,
foot), else Alt (macOS Terminal: "Use Option as Meta"). The label shows what
works in this terminal; on Plan 9, what vt passes.

### Security

- Call history and missed calls are stored encrypted: the core seals them with
  a public key, the private key is in the system's keychain (Keychain, DPAPI /
  Windows Hello, Secret Service, factotum), so Say records a missed call
  without being able to read the history.
- Opening Say asks for biometrics (Touch ID, Windows Hello; a keyring password
  on Linux), can be turned off. Answering a call needs none, as on a phone.
- Move the Yggdrasil private key from `config.json` to the keychain.

### Later, if wanted

- A daemon with several screens attached (`ssh home say`, two windows), the
  socket above grown into it.
- A browser view: the same screen stream in xterm.js.
- A SIP gateway: SIP phones (Linphone) call Say over Yggdrasil, voice only
  (Opus and G.722 are standard).

### Order

1. Core / screen split, home screen, vCard contacts, hang up and back home.
2. Calling with answer, missed calls.
3. Sounds.
4. Presence.
5. Buttons with shortcuts.
6. Photos.
7. Tray, hidden window, single start, autostart, `say://`.
8. Encrypted history, keychain, biometrics.
9. macOS Contacts.
