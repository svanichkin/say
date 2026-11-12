package ui

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFrameSeedsChangePerLaunchButNotOnRedraw(t *testing.T) {
	oldSalt, oldSeeds := frameLaunchSalt, seeds.Load()
	oldAccents, oldChat := accents.Load(), chatAccent.Load()
	defer func() {
		frameLaunchSalt = oldSalt
		seeds.Store(oldSeeds)
		accents.Store(oldAccents)
		chatAccent.Store(oldChat)
	}()
	frameLaunchSalt = 100
	SetAccentAddresses("200::1", "200::2")
	a, b, c := FrameSeeds()
	selfColor, peerColor := Accents()
	SetAccentAddresses("200::1", "200::2")
	x, y, z := FrameSeeds()
	if a != x || b != y || c != z {
		t.Fatal("same launch regenerated its frames")
	}
	frameLaunchSalt = 101
	SetAccentAddresses("200::1", "200::2")
	x, y, z = FrameSeeds()
	if a == x || b == y || c == z {
		t.Fatal("a new launch retained a frame seed")
	}
	newSelf, newPeer := Accents()
	if selfColor != newSelf || peerColor != newPeer {
		t.Fatal("new frame geometry changed the address colours")
	}
}

// Both sides of a call see the same pair: each person's colour follows from
// their address, and when the two collide the same one moves on.
func TestAccentsAgreeOnBothSides(t *testing.T) {
	addrs := []string{"200:6ee:1bff:16e5:e8e0:f254:369c:ddcd", "200:d2f1:d881:3cbb:e606:b61a:8bd:a06d", "201:8f8a::1", "202:1::5", "203::9"}
	for _, a := range addrs {
		for _, b := range addrs {
			if a == b {
				continue
			}
			SetAccentAddresses(a, b)
			aSelf, aPeer := Accents()
			SetAccentAddresses(b, a)
			bSelf, bPeer := Accents()
			if aSelf != bPeer || aPeer != bSelf {
				t.Fatalf("%s and %s see different colours: %v/%v vs %v/%v", a, b, aSelf, aPeer, bSelf, bPeer)
			}
			if aSelf == aPeer {
				t.Fatalf("%s and %s share a colour", a, b)
			}
			SetAccentAddresses(a, b)
			sa, sp, ca := FrameSeeds()
			SetAccentAddresses(b, a)
			sb, sq, cb := FrameSeeds()
			if sa != sq || sp != sb || ca != cb || sa == sp {
				t.Fatalf("%s and %s: frame seeds %x/%x/%x vs %x/%x/%x", a, b, sa, sp, ca, sb, sq, cb)
			}
		}
	}
}

func TestStatusHeading(t *testing.T) {
	for in, want := range map[string]string{
		"Dial failed: timeout\nRetrying in 2s…": "Dial failed\ntimeout\nRetrying in 2s…",
		"Error: no camera":                      "Error\nno camera",
		`say "[200:db4b::1]:7011"`:              `say "[200:db4b::1]:7011"`,
		"Connecting to Ygg network…":            "Connecting to Ygg network…",
	} {
		if got := statusHeading(in); got != want {
			t.Errorf("statusHeading(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCursorBlinksBeforeTyping(t *testing.T) {
	chatState.Lock()
	chatState.typedAt = time.Time{}
	chatState.Unlock()
	now := time.Now()
	if inputCursorVisible(now) == inputCursorVisible(now.Add(cursorBlinkPeriod)) {
		t.Fatal("the cursor does not blink before anything is typed")
	}
}

func TestDetailBoost(t *testing.T) {
	was := pixelMode
	pixelMode = true
	defer func() { pixelMode = was; ResetDetailBoost(); SetVideoDetail(1) }()
	SetVideoDetail(1)
	ResetDetailBoost()
	start := time.Now()
	at := func(s float64) time.Time { return start.Add(time.Duration(s * float64(time.Second))) }
	for s := 0.0; s < 9; s++ {
		noteVideoLoss(0, at(s))
	}
	if detail() != 1 {
		t.Fatalf("boosted after 9 clean seconds: detail %d", detail())
	}
	noteVideoLoss(0.005, at(10))
	if detail() != 2 {
		t.Fatalf("not boosted after 10 clean seconds: detail %d", detail())
	}
	noteVideoLoss(0.02, at(11)) // some loss: kept
	if detail() != 2 {
		t.Fatalf("dropped on light loss: detail %d", detail())
	}
	noteVideoLoss(0.05, at(12))
	if detail() != 1 {
		t.Fatalf("not dropped on heavy loss: detail %d", detail())
	}
	for s := 13.0; s < 60; s++ { // clean, but still waiting to retry
		noteVideoLoss(0, at(s))
	}
	if detail() != 1 {
		t.Fatalf("retried before a minute: detail %d", detail())
	}
	noteVideoLoss(0, at(73))
	if detail() != 2 {
		t.Fatalf("no retry after a minute of clean video: detail %d", detail())
	}
	SetVideoDetail(4) // the most there is: no step beyond
	if detail() != 4 {
		t.Fatalf("detail %d beyond 4", detail())
	}
}

func TestStatusTextFitsItsFrame(t *testing.T) {
	was, wasSide, wasBorder := pixelMode, termSide, termBorder
	pixelMode, termSide, termBorder = true, 4, 2
	defer func() { pixelMode, termSide, termBorder = was, wasSide, wasBorder }()
	const cols = 40
	out := buildStatusScreen(cols, 20, "Re-dialing 201:6612:cb1f:d8f4:4cf:7a2e:91bb:1c0d (attempt 3)…")
	room := cols - 2*termSide - 2*statusPadCols
	// Wrapped, not cut: all of it is there.
	if squeeze := strings.NewReplacer(" ", "", "\n", ""); !strings.Contains(squeeze.Replace(stripEscapes(out)), squeeze.Replace("Re-dialing 201:6612:cb1f:d8f4:4cf:7a2e:91bb:1c0d (attempt 3)…")) {
		t.Fatalf("status cut instead of wrapped: %q", stripEscapes(out))
	}
	found := false
	for _, m := range regexp.MustCompile("\x1b\\[[0-9;]*m([^\x1b]*)").FindAllStringSubmatch(out, -1) {
		if strings.Contains(m[1], "Re-dialing") {
			found = true
			if w := displayWidth(m[1]); w > room {
				t.Fatalf("status text %d wide, the frame holds %d", w, room)
			}
		}
	}
	if !found {
		t.Fatal("no status text written")
	}
	// Centred as its frame is: both are centred in the window.
	short := buildStatusScreen(cols, 20, "Dialing")
	m := regexp.MustCompile("\x1b\\[(\\d+);(\\d+)H\x1b\\[[0-9;]*mDialing").FindStringSubmatch(short)
	if m == nil {
		t.Fatal("no status text in the short status")
	}
	if want := 1 + (cols-len("Dialing"))/2; m[2] != strconv.Itoa(want) {
		t.Fatalf("status text at column %s, want %d (centred)", m[2], want)
	}
}

func stripEscapes(s string) string {
	return regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]|\x1b\\][^\x07]*\x07").ReplaceAllString(s, " ")
}

func TestWrapWidth(t *testing.T) {
	got := wrapWidth("Re-dialing 201:6612:cb1f:d8f4:4cf:7a2e:91bb:1c0d (attempt 3)…", 20)
	want := []string{"Re-dialing", "201:6612:cb1f:d8f4:4", "cf:7a2e:91bb:1c0d", "(attempt 3)…"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("wrapWidth = %q, want %q", got, want)
	}
}

// Ctrl+C and the window's close button stop Say through the installed stop,
// not a signal (Windows cannot signal a process).
func TestInterruptCallsTheStop(t *testing.T) {
	called := false
	SetInterrupt(func() { called = true })
	defer SetInterrupt(nil)
	Interrupt()
	if !called {
		t.Fatal("Interrupt did not call the installed stop")
	}
}

// In the window the captions' row is one bracketed line: both captions and
// the ping between them, one mark with three spans across the panes.
func TestCaptionRowIsOneBracketedLine(t *testing.T) {
	was, ws, wg, wb, wsep := pixelMode, termSide, termGap, termBorder, termSeparator
	pixelMode, termSide, termGap, termBorder, termSeparator = true, 4, 6, 2, 3
	defer func() { pixelMode, termSide, termGap, termBorder, termSeparator = was, ws, wg, wb, wsep }()
	SetChatActions(func(string) {}, func(string) {})
	defer func() {
		SetChatActions(nil, nil)
		updateIdlePatternSlot(localFrameType, false, time.Now())
		updateIdlePatternSlot(remoteFrameType, false, time.Now())
		updateNoiseAnimation(false)
	}()
	const cols, rows = 120, 40
	out := renderChatScreen(cols, rows)
	m := regexp.MustCompile(`\x1b\]5580;(\d+);(\d+);(\d+)((?:;\d+;\d+)+)\x07`).FindAllStringSubmatch(out, -1)
	if len(m) != 1 {
		t.Fatalf("%d bracket marks, want one for the whole row", len(m))
	}
	vs, _ := calcViewportSlots(cols, rows)
	width, _ := strconv.Atoi(m[0][3])
	if want := vs.selfSlot.cols + termGap + vs.peerSlot.cols; width != want {
		t.Fatalf("the bracketed line is %d wide, want %d (both panes and the gap)", width, want)
	}
	spans := strings.Split(strings.TrimPrefix(m[0][4], ";"), ";")
	if len(spans) != 6 {
		t.Fatalf("spans %v, want three (caption, ping, caption)", spans)
	}
	from, _ := strconv.Atoi(spans[0])
	to, _ := strconv.Atoi(spans[5])
	if from < 1 || to > width-1 || from >= to {
		t.Fatalf("texts span %d..%d of %d: must leave the end blocks room", from, to, width)
	}
}

// The status lines start at one column, and a copyable status has its
// button under it, which alone is clicked.
func TestStatusLeftAlignedWithCopyButton(t *testing.T) {
	was, wasSide, wasBorder := pixelMode, termSide, termBorder
	pixelMode, termSide, termBorder = true, 4, 2
	defer func() { pixelMode, termSide, termBorder = was, wasSide, wasBorder; resetStatusClickState() }()
	const cols = 60
	starts := func(out string) map[string]int {
		got := map[string]int{}
		for _, m := range regexp.MustCompile("\x1b\\[(\\d+);(\\d+)H(?:\x1b\\[[0-9;]*m)*([^\x1b]+)").FindAllStringSubmatch(out, -1) {
			col, _ := strconv.Atoi(m[2])
			got[strings.TrimSpace(m[3])] = col
		}
		return got
	}

	got := starts(buildStatusScreen(cols, 20, "REDIAL\n201:6612:cb1f::1\nAttempt 3…"))
	if got["REDIAL"] == 0 || got["REDIAL"] != got["201:6612:cb1f::1"] || got["REDIAL"] != got["Attempt 3…"] {
		t.Fatalf("status lines do not start at one column: %v", got)
	}

	const hint = `say "201:6612:cb1f::1"`
	enableStatusClick(hint, hint)
	out := buildStatusScreen(cols, 20, hint)
	got = starts(out)
	if got["Copy"] == 0 {
		t.Fatalf("no copy button: %v", got)
	}
	if !strings.Contains(out, "\x1b]5580;") {
		t.Fatal("the button is not bracketed in the window")
	}
	statusClickState.Lock()
	areas := append([]statusClickRect(nil), statusClickState.areas...)
	statusClickState.copied = true
	statusClickState.Unlock()
	if len(areas) != 1 || areas[0].startCol != got[hint] {
		t.Fatalf("click areas %v, want the button's row only", areas)
	}
	after := starts(buildStatusScreen(cols, 20, hint))
	if after["Copied"] == 0 || after[hint] != got[hint] {
		t.Fatalf("after copying: %v (before %v)", after, got)
	}
}
