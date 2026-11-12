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

## Menu on the self view

A click on the self view opens a menu instead of toggling the microphone at once:

- Microphone off / on
- Camera off / on (does not exist yet: today only the sound is muted)
- Microphone mode / privacy: opens the system's own settings
  - macOS: the Mic Mode picker (`AVCaptureDevice.showSystemUserInterface(.microphoneModes)`):
    Standard, Voice Isolation, Wide Spectrum
  - Windows: `ms-settings:privacy-microphone` (or `ms-settings:sound`)
  - Linux, Plan 9: no such item

Log the active Mic Mode (`AVCaptureDevice.activeMicrophoneMode`) at call start.
