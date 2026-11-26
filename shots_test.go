//go:build darwin || windows

package main

import (
	"context"
	"flag"
	"image"
	_ "image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/svanichkin/say/contacts"
	"github.com/svanichkin/say/gui"
	"github.com/svanichkin/say/keys"
	"github.com/svanichkin/say/lang"
	"github.com/svanichkin/say/ui"
)

// The documentation's screenshots (docs/images): Say's screens as it draws
// them, in English, with made-up contacts and two pictures (-self, -peer;
// kept out of the repository) as the cameras, cut to the panes, never
// stretched. The window's by the
// software renderer, as on Plan 9 (the window's Unifont, its frames, its
// HUD); the terminal's its cells. make shots runs both:
//
//	make shots SELF=me.jpg PEER=them.jpg
var (
	shotsDir  = flag.String("shots", "", "write the documentation's screenshots into this folder")
	shotsGUI  = flag.Bool("gui", false, "with -shots: the window's, not the terminal's")
	shotsSelf = flag.String("self", "", "with -shots: the picture of our camera")
	shotsPeer = flag.String("peer", "", "with -shots: the picture of the peer's camera")
)

const (
	shotSelf  = "200:4f1e:9a20:3c7d:b815:62e0:d4a9:7c31"
	shotPeer  = "201:8c3a:15f2:e9b0:4d71:a6c8:3f05:92de"
	shotPhone = "201:8c3a:15f2:e9b0:4d71:a6c8:3f05:1a07"
	shotMaria = "202:6d19:aa04:7e3b:0c58:f1d2:39b6:e480"
	shotKenji = "203:11f7:c0de:5a2e:98b4:7d03:e6a1:2c9f"
)

func TestShots(t *testing.T) {
	if *shotsDir == "" {
		t.Skip("pass -shots <folder> to draw the documentation's screenshots")
	}
	if *shotsSelf == "" || *shotsPeer == "" {
		t.Fatal("-self and -peer: the pictures of the two cameras")
	}
	lang.Set("en")
	// The screens with video at the window's size (a terminal's of 100
	// columns), the menus and the statuses lower: less black round them.
	kind, cols, rows, low := "term", 100, 32, 18
	if *shotsGUI {
		ui.SetPixelMode()
		// The video as Say shows it by default: the hologram (its exposure,
		// its grid of dots), sharpened.
		if err := gui.SetVideoEffect("hologram"); err != nil {
			t.Fatal(err)
		}
		gui.SetVideoSharp(100)
		kind, cols, rows, low = "gui", gui.Cols, gui.Rows, 24
	} else {
		ui.SetColorMode("truecolor") // a terminal of today's: the frames in lines
	}
	ui.ShotSetup()
	if err := os.MkdirAll(*shotsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	self, peer := shotImage(t, *shotsSelf), shotImage(t, *shotsPeer)

	dir := t.TempDir()
	card := func(file, uid, family, given string, devices ...string) {
		c := "BEGIN:VCARD\nVERSION:3.0\nUID:" + uid + "\nN:" + family + ";" + given + ";;;\nFN:" + given + " " + family + "\n"
		for i := 0; i+1 < len(devices); i += 2 {
			n := string(rune('1' + i/2))
			c += "item" + n + ".URL:say:[" + devices[i] + "]\nitem" + n + ".X-ABLabel:" + devices[i+1] + "\n"
		}
		writeCard(t, filepath.Join(dir, file), c+"END:VCARD\n")
	}
	card("me.vcf", "ME", "Carter", "Sam", shotSelf, "laptop")
	card("jordan.vcf", "JORDAN", "Lee", "Jordan", shotPeer, "laptop", shotPhone, "phone")
	card("maria.vcf", "MARIA", "Santos", "Maria", shotMaria, "desktop")
	card("kenji.vcf", "KENJI", "Watanabe", "Kenji", shotKenji, "laptop")
	a := newHomeApp(filepath.Join(t.TempDir(), "say.json"), dir, shotSelf, 7777)
	a.setDial(func(context.Context, contacts.Device) error { return nil }) // nothing is called
	a.askPresence(func(host string, _ int) bool { return host == shotPeer || host == shotMaria })
	ui.SetAccentAddresses(shotSelf, "")

	in := gui.Input{
		Frame: ui.PixelFrame, FrameRGB: ui.FrameRGB, FrameRGBRow: ui.FrameRGBRow, FrameY: ui.FrameY,
		Accents: ui.Accents, Chat: ui.ChatAccent, Brick: ui.BrickColour, Mono: ui.AccentMono,
		Seeds: ui.FrameSeeds, Latency: ui.PeerLatencySnapshot,
	}
	shot := func(name string, rows int) {
		t.Helper()
		img, err := gui.Shot(ui.Shot(cols, rows), cols, rows, 16, 32, in, *shotsGUI)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(*shotsDir, kind+"-"+name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
	}

	ui.SetHome(a.root, a.paste, func() {})
	ui.ShowHome(true)
	shot("home", low)
	pressKeys(t, '\r')
	shot("contacts", low)
	pressKeys(t, keys.Down, '\r')
	shot("contact", low)
	pressKeys(t, keys.Esc, keys.Esc, keys.Down) // the address field: typed in at once
	for _, r := range "201:8c3a:15f2" {
		ui.KeyInput(r)
	}
	shot("dial", low)
	pressKeys(t, keys.Down, keys.Down, '\r', keys.Down) // Say's icon
	shot("settings", low)
	ui.ShowHome(false)
	ui.SetHome(nil, nil, nil)

	ui.SetStatusMessage(lang.T(lang.Dialing) + "\nJordan Lee · laptop\n" + lang.T(lang.RingingThere))
	ui.SetDialCancel(func() {})
	shot("dialing", low)
	ui.SetDialCancel(nil)
	ui.Ringing("Jordan Lee · laptop\n"+shotPeer, func(bool) {})
	shot("incoming", low)
	ui.RingingOver()

	ui.SetAccentAddresses(shotSelf, shotPeer)
	ui.SetChatActions(func(string) {}, func(string) {})
	ui.Shot(cols, rows) // the panes laid out: their sizes
	ui.ShotVideo(self, peer, cols, rows)
	ui.UpdateTxAudioRate(1.6)
	ui.UpdateTxVideoRate(11.8)
	ui.UpdateRxAudioRate(1.6)
	ui.UpdateRxVideoRate(13.2)
	ui.UpdatePeerLatency(38 * time.Millisecond)
	ui.AddChatMessage("Hi Sam! Can you hear me?", false)
	ui.AddChatMessage("Loud and clear. No servers in between, right?", true)
	ui.AddChatMessage("Just the two of us over Yggdrasil.", false)
	ui.ShotVideo(self, peer, cols, rows)
	shot("call", rows)
	pressKeys(t, keys.Left)
	ui.ShotVideo(self, peer, cols, rows)
	shot("call-menu", rows)
	ui.SetChatActions(nil, nil)
}

func shotImage(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}
