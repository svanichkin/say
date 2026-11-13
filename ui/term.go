package ui

import (
	"context"
	"fmt"
	"github.com/svanichkin/say/codec"
	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/lang"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/mediactrl"
	"log"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Terminal cell aspect: one character cell is ~0.38 width of its height.
// We use it to compare "visual" width vs height.
// termCellWidthToHeight is the assumed width/height ratio of a terminal cell.
// It must match the codec, which maps every cell to a 4×8 pixel block.
const termCellWidthToHeight = 0.5

// termBorder and termSeparator are the margins round the canvas and
// between the video panes, in cells. The window (SetPixelMode) widens them:
// its pane frames stick out with plates, brackets and circuit lines.
var (
	termBorder    = 1
	termSeparator = 1
)

// termSide and termGap are the same margins across: left and right of the
// canvas and between the panes side by side, in columns. The terminal keeps
// them equal to the ones above; the window gives them more air.
var (
	termSide = 1
	termGap  = 1
)

const frameFreezeThreshold = 1200 * time.Millisecond
const testCardDelay = 3 * time.Second
const (
	localFrameType  = "local"
	remoteFrameType = "remote"
)

var termRendererOnce sync.Once

// UI invalidation channel: request re-render when frames or size change.
var redrawCh = make(chan struct{}, 1)

type colorFilterSpec struct {
	key      string
	tint     [3]int
	strength float64
	contrast float64
}

var colorFilterPresets = map[string]*colorFilterSpec{
	"red": {
		key:      "red",
		tint:     [3]int{255, 0, 0},
		strength: 1.0,
		contrast: 1.3,
	},
	"orange": {
		key:      "orange",
		tint:     [3]int{255, 128, 0},
		strength: 1.0,
		contrast: 1.3,
	},
	"yellow": {
		key:      "yellow",
		tint:     [3]int{255, 255, 0},
		strength: 1.0,
		contrast: 1.3,
	},
	"green": {
		key:      "green",
		tint:     [3]int{0, 255, 0},
		strength: 1.0,
		contrast: 1.3,
	},
	"teal": {
		key:      "teal",
		tint:     [3]int{0, 255, 255},
		strength: 1.0,
		contrast: 1.3,
	},
	"blue": {
		key:      "blue",
		tint:     [3]int{0, 0, 255},
		strength: 1.0,
		contrast: 1.3,
	},
	"purple": {
		key:      "purple",
		tint:     [3]int{255, 0, 255},
		strength: 1.0,
		contrast: 1.3,
	},
	"pink": {
		key:      "pink",
		tint:     [3]int{255, 64, 160},
		strength: 1.0,
		contrast: 1.3,
	},
	"gray": {
		key:      "gray",
		tint:     [3]int{255, 255, 255},
		strength: 1.0,
		contrast: 1.3,
	},
	"bw": {
		key:      "bw",
		tint:     [3]int{255, 255, 255},
		strength: 1.0,
		contrast: 1.3,
	},
}

var activeColorFilter atomic.Pointer[colorFilterSpec]

var (
	frameFeedMu sync.RWMutex
	frameFeed   chan *device.TerminalFrame

	lastDrawMu sync.Mutex
	lastDraw   struct {
		w, h   int
		orient byte
	}

	glyphCodepoints        = codec.GlyphCodepoints()
	noiseGlyphSet          []byte
	glyphIndexLookup       map[int]int
	patternGlyphBlank      byte
	patternGlyphHorizontal byte
	patternGlyphVertical   byte

	fpsMu       sync.Mutex
	fpsCounters = map[string]*fpsCounter{
		localFrameType:  {},
		remoteFrameType: {},
	}

	statusMu      sync.RWMutex
	statusMessage string
)

type statusClickRect struct {
	row      int
	startCol int
	endCol   int
}

// A copyable status has a button under it; once pressed it says so for a
// while.
const (
	statusCopyFeedbackDelay = 2 * time.Second
)

var statusClickState struct {
	sync.Mutex
	active      bool
	baseMessage string
	copied      bool
	clipboard   string
	areas       []statusClickRect
	timer       *time.Timer
}

var (
	noiseRandSeed uint64

	testCardTopBars = [][3]byte{
		rgbToYCbCr(255, 255, 255), // white
		rgbToYCbCr(255, 255, 0),   // yellow
		rgbToYCbCr(0, 255, 255),   // cyan
		rgbToYCbCr(0, 255, 0),     // green
		rgbToYCbCr(255, 0, 255),   // magenta
		rgbToYCbCr(255, 0, 0),     // red
		rgbToYCbCr(0, 0, 255),     // blue
	}
	testCardMiddleBars = [][3]byte{
		rgbToYCbCr(64, 64, 64),   // dark gray
		rgbToYCbCr(235, 235, 16), // yellow highlight
		rgbToYCbCr(32, 32, 32),   // near black
		rgbToYCbCr(16, 16, 16),   // black
		rgbToYCbCr(235, 16, 16),  // red
		rgbToYCbCr(16, 235, 16),  // green
		rgbToYCbCr(16, 16, 235),  // blue
	}
	testCardBottomBars = [][3]byte{
		rgbToYCbCr(16, 16, 16),
		rgbToYCbCr(64, 64, 64),
		rgbToYCbCr(96, 96, 96),
		rgbToYCbCr(128, 128, 128),
		rgbToYCbCr(160, 160, 160),
		rgbToYCbCr(192, 192, 192),
		rgbToYCbCr(224, 224, 224),
	}
	testCardSeparator  = rgbToYCbCr(8, 8, 8)
	testCardAccent     = rgbToYCbCr(235, 235, 235)
	testCardBackground = rgbToYCbCr(12, 12, 12)
)

type noiseSlotState struct {
	active  bool
	since   time.Time
	pattern bool
}

type noiseSlotStore struct {
	sync.Mutex
	local  noiseSlotState
	remote noiseSlotState
}

var noiseSlots noiseSlotStore

var noiseAnimator struct {
	sync.Mutex
	cancel context.CancelFunc
}

func init() {
	limit := len(glyphCodepoints)
	if limit > 16 {
		limit = 16
	}
	if limit <= 0 {
		return
	}
	noiseGlyphSet = make([]byte, limit)
	for i := 0; i < limit; i++ {
		noiseGlyphSet[i] = byte(i)
	}

	glyphIndexLookup = make(map[int]int, len(glyphCodepoints))
	for i, cp := range glyphCodepoints {
		glyphIndexLookup[cp] = i
	}
	patternGlyphBlank = glyphIndexForRune('\u00a0')
	patternGlyphHorizontal = glyphIndexForRune('\u2501')
	patternGlyphVertical = glyphIndexForRune('\u2503')
}

type fpsCounter struct {
	lastTick time.Time
	frames   int
	display  string
}

func noteFrameArrival(frameType string) {
	fpsMu.Lock()
	defer fpsMu.Unlock()
	counter := ensureFpsCounter(frameType)
	counter.recordFrame(time.Now())
}

func currentFPSLabel(frameType string) string {
	fpsMu.Lock()
	defer fpsMu.Unlock()
	counter := ensureFpsCounter(frameType)
	if counter.display == "" {
		counter.display = formatFancyNumber(0)
	}
	return fmt.Sprintf("%sfps", counter.display)
}

func ensureFpsCounter(frameType string) *fpsCounter {
	counter, ok := fpsCounters[frameType]
	if !ok || counter == nil {
		counter = &fpsCounter{}
		fpsCounters[frameType] = counter
	}
	return counter
}

// SetStatusMessage updates the fallback text rendered when no frames are available.
// The message is trimmed; pass an empty string to clear it.
func SetStatusMessage(msg string) {
	setStatusMessageInternal(msg, false)
}

// SetCopyableStatus shows the provided message with a button under it that
// copies clipboard (OSC 52).
func SetCopyableStatus(msg, clipboard string) {
	trimmedMsg := strings.TrimSpace(msg)
	if trimmedMsg == "" || strings.TrimSpace(clipboard) == "" {
		SetStatusMessage(msg)
		return
	}
	setStatusMessageInternal(trimmedMsg, false)
	enableStatusClick(trimmedMsg, clipboard)
}

func setStatusMessageInternal(msg string, preserveClick bool) {
	trimmed := strings.TrimSpace(msg)
	statusMu.Lock()
	statusMessage = trimmed
	statusMu.Unlock()
	if !preserveClick {
		resetStatusClickState()
	}
	RequestRedraw()
}

func currentStatusMessage() string {
	statusMu.RLock()
	defer statusMu.RUnlock()
	return statusMessage
}

func enableStatusClick(msg, clipboard string) {
	trimmedClip := strings.TrimSpace(clipboard)
	if msg == "" || trimmedClip == "" {
		return
	}
	statusClickState.Lock()
	defer statusClickState.Unlock()
	if statusClickState.timer != nil {
		statusClickState.timer.Stop()
		statusClickState.timer = nil
	}
	statusClickState.active = true
	statusClickState.baseMessage = msg
	statusClickState.copied = false
	statusClickState.clipboard = trimmedClip
	statusClickState.areas = nil
	RequestRedraw()
}

func resetStatusClickState() {
	statusClickState.Lock()
	defer statusClickState.Unlock()
	if statusClickState.timer != nil {
		statusClickState.timer.Stop()
		statusClickState.timer = nil
	}
	statusClickState.active = false
	statusClickState.baseMessage = ""
	statusClickState.copied = false
	statusClickState.clipboard = ""
	statusClickState.areas = nil
}

// statusCopyButton is the button's label when msg is the copyable status
// ("" when it is not).
func statusCopyButton(msg string) string {
	trimmed := strings.TrimSpace(msg)
	statusClickState.Lock()
	defer statusClickState.Unlock()
	if !statusClickState.active || trimmed == "" || trimmed != statusClickState.baseMessage {
		return ""
	}
	if statusClickState.copied {
		return lang.T(lang.Copied)
	}
	return lang.T(lang.Copy)
}

func statusClickUpdateAreas(rects []statusClickRect) {
	statusClickState.Lock()
	defer statusClickState.Unlock()
	if !statusClickState.active {
		statusClickState.areas = nil
		return
	}
	statusClickState.areas = append(statusClickState.areas[:0], rects...)
}

func statusClickClearAreas() {
	statusClickState.Lock()
	statusClickState.areas = nil
	statusClickState.Unlock()
}

// statusClickHandlePoint copies the status when the click is on its button,
// and the button says Copied for a while.
func statusClickHandlePoint(col, row int) bool {
	statusClickState.Lock()
	if !statusClickState.active || len(statusClickState.areas) == 0 {
		statusClickState.Unlock()
		return false
	}
	hit := false
	for _, rect := range statusClickState.areas {
		if row == rect.row && col >= rect.startCol && col <= rect.endCol {
			hit = true
			break
		}
	}
	if !hit {
		statusClickState.Unlock()
		return false
	}
	clipboard := statusClickState.clipboard
	statusClickState.copied = true
	if statusClickState.timer != nil {
		statusClickState.timer.Stop()
	}
	statusClickState.timer = time.AfterFunc(statusCopyFeedbackDelay, func() {
		statusClickState.Lock()
		statusClickState.copied = false
		statusClickState.timer = nil
		statusClickState.Unlock()
		RequestRedraw()
	})
	statusClickState.Unlock()

	if clipboard != "" {
		device.CopyToClipboard(clipboard)
	}
	RequestRedraw()
	return true
}

func (fc *fpsCounter) recordFrame(now time.Time) {
	if fc == nil {
		return
	}
	if fc.display == "" {
		fc.display = formatFancyNumber(0)
	}
	if fc.lastTick.IsZero() {
		fc.lastTick = now
	}
	fc.frames++
	elapsed := now.Sub(fc.lastTick)
	if elapsed >= time.Second {
		fps := int(float64(fc.frames) / elapsed.Seconds())
		if fps < 0 {
			fps = 0
		}
		fc.display = formatFancyNumber(fps)
		fc.frames = 0
		fc.lastTick = now
	}
}

func setFrameFeed(ch chan *device.TerminalFrame) {
	frameFeedMu.Lock()
	frameFeed = ch
	frameFeedMu.Unlock()
}

func enqueueFrame(tf *device.TerminalFrame) {
	if tf == nil {
		return
	}
	frameFeedMu.RLock()
	ch := frameFeed
	frameFeedMu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case ch <- tf:
	default:
		select {
		case <-ch:
			// The dropped frame's cells never reached the screen.
			screenDirty.Store(true)
		default:
		}
		select {
		case ch <- tf:
		default:
			screenDirty.Store(true)
		}
	}
}

// RequestRedraw enqueues a redraw event for the terminal renderer.
func RequestRedraw() {
	select {
	case redrawCh <- struct{}{}:
	default:
	}
}

// ColorFilterActive reports whether frames are drawn through a colour filter,
// which keeps only their brightness.
func ColorFilterActive() bool {
	return activeColorFilter.Load() != nil
}

// SetColorFilter applies a named tint to subsequent frame renders. Empty key clears the filter.
func SetColorFilter(key string) {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "" {
		activeColorFilter.Store(nil)
		RequestRedraw()
		return
	}
	if spec, ok := colorFilterPresets[normalized]; ok {
		activeColorFilter.Store(spec)
	} else {
		activeColorFilter.Store(nil)
	}
	RequestRedraw()
}

// EnsureRenderer starts the terminal renderer loop once. Subsequent calls are no-ops.
func EnsureRenderer(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	termRendererOnce.Do(func() {
		if !pixelMode {
			startMouseTracker(ctx)
		}
		startTermImgRenderer(ctx)
	})
}

// TermSize represents the terminal size in character cells.
type TermSize struct {
	Cols int
	Rows int
}

type ViewportSize struct {
	Cols int
	Rows int
}

// slotSize describes the size and orientation of a single viewport slot used
// to render a video pane inside the terminal.
type slotSize struct {
	cols   int
	rows   int
	orient byte
}

type frameOverlay struct {
	fps         string
	rate        string
	latency     string
	centerLabel string
}

var (
	termSizeMu      sync.Mutex
	termSizeSubs    = make(map[int]chan TermSize)
	termSizeNextID  int
	termSizeLast    TermSize
	termSizeLastSet bool

	peerSlotMu sync.RWMutex
	peerSlot   slotSize

	selfSlotMu   sync.RWMutex
	selfViewport viewportSlots

	viewportMu      sync.Mutex
	viewportSubs    = make(map[int]chan ViewportSize)
	viewportNextID  int
	viewportLast    ViewportSize
	viewportLastSet bool
)

// subscribeTermSize registers a subscriber for terminal size updates.
// It returns a subscriber ID, a channel on which updates are delivered, the last
// known terminal size (if any), and a flag indicating whether an initial value is available.
func subscribeTermSize() (int, chan TermSize, TermSize, bool) {
	ch := make(chan TermSize, 4)
	termSizeMu.Lock()
	id := termSizeNextID
	termSizeNextID++
	termSizeSubs[id] = ch
	initial := termSizeLast
	hasInitial := termSizeLastSet
	termSizeMu.Unlock()
	return id, ch, initial, hasInitial
}

// unsubscribeTermSize removes a previously registered terminal size subscriber.
func unsubscribeTermSize(id int) {
	termSizeMu.Lock()
	delete(termSizeSubs, id)
	termSizeMu.Unlock()
}

// TermSizeFeed exposes a stream of terminal size events along with the last known size.
// Call Stop when the consumer no longer needs updates.
type TermSizeFeed struct {
	Updates <-chan TermSize
	Initial *TermSize
	Stop    func()
}

// NewTermSizeFeed creates a TermSizeFeed bound to ctx. When ctx is canceled or Stop is
// invoked, the feed stops forwarding updates and the Updates channel is closed.
func NewTermSizeFeed(ctx context.Context) TermSizeFeed {
	if ctx == nil {
		ctx = context.Background()
	}
	id, ch, initial, hasInitial := subscribeTermSize()
	if !hasInitial {
		if cols, rows, err := device.GetTermSize(); err == nil && cols > 0 && rows > 0 {
			initial = TermSize{Cols: cols, Rows: rows}
			hasInitial = true
		}
	}

	out := make(chan TermSize, 4)
	done := make(chan struct{})

	stopOnce := sync.Once{}
	stop := func() {
		stopOnce.Do(func() {
			close(done)
			unsubscribeTermSize(id)
		})
	}

	forward := func(ts TermSize) bool {
		select {
		case out <- ts:
			return true
		default:
			// drop oldest buffered value to keep the latest geometry
			select {
			case <-out:
			default:
			}
			select {
			case out <- ts:
				return true
			case <-ctx.Done():
			case <-done:
			}
		}
		return false
	}

	go func() {
		defer close(out)
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case ts := <-ch:
				if !forward(ts) {
					return
				}
			}
		}
	}()

	var initialPtr *TermSize
	if hasInitial {
		initialCopy := initial
		initialPtr = &initialCopy
	}

	return TermSizeFeed{
		Updates: out,
		Initial: initialPtr,
		Stop:    stop,
	}
}

// broadcastTermSize updates the cached terminal size and notifies all subscribers
// of the new dimensions, coalescing identical size updates.
func broadcastTermSize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	ts := TermSize{Cols: cols, Rows: rows}

	termSizeMu.Lock()
	if termSizeLastSet && termSizeLast.Cols == ts.Cols && termSizeLast.Rows == ts.Rows {
		termSizeMu.Unlock()
		return
	}
	termSizeLast = ts
	termSizeLastSet = true
	subs := make([]chan TermSize, 0, len(termSizeSubs))
	for _, ch := range termSizeSubs {
		subs = append(subs, ch)
	}
	termSizeMu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- ts:
		default:
		}
	}
}

func termSizeSnapshot() (TermSize, bool) {
	termSizeMu.Lock()
	defer termSizeMu.Unlock()
	if !termSizeLastSet {
		return TermSize{}, false
	}
	return termSizeLast, true
}

// SetPeerTermSize updates the cached viewport slot dimensions requested by the remote peer.
func SetPeerTermSize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	// The peer chooses the size we encode for; never exceed the codec limits.
	cols = min(cols, codec.MaxFrameCols)
	rows = min(rows, codec.MaxFrameRows)

	peerSlotMu.Lock()
	peerSlot.cols = cols
	peerSlot.rows = rows
	peerSlot.orient = 0
	peerSlotMu.Unlock()
	logs.LogV("[term] peer viewport request %dx%d cells (~%dx%d px)",
		cols, rows,
		cols*4, rows*8)
}

// ViewportSizeFeed exposes a stream of viewport size requests learned from the local layout.
type ViewportSizeFeed struct {
	Updates <-chan ViewportSize
	Initial *ViewportSize
	Stop    func()
}

func subscribeViewportSize() (int, chan ViewportSize, ViewportSize, bool) {
	ch := make(chan ViewportSize, 4)
	viewportMu.Lock()
	id := viewportNextID
	viewportNextID++
	viewportSubs[id] = ch
	initial := viewportLast
	hasInitial := viewportLastSet
	viewportMu.Unlock()
	return id, ch, initial, hasInitial
}

// CurrentViewport is the peer pane size for the terminal as it is now.
func CurrentViewport() (ViewportSize, bool) {
	if cols, rows, err := device.GetTermSize(); err == nil && cols > 0 && rows > 0 {
		if slots, ok := calcViewportSlots(cols, rows); ok {
			c, r := detailSize(slots.peerSlot.cols, slots.peerSlot.rows)
			return ViewportSize{Cols: c, Rows: r}, true
		}
	}
	viewportMu.Lock()
	defer viewportMu.Unlock()
	return viewportLast, viewportLastSet
}

func unsubscribeViewportSize(id int) {
	viewportMu.Lock()
	delete(viewportSubs, id)
	viewportMu.Unlock()
}

// NewViewportFeed creates a ViewportSizeFeed bound to ctx.
func NewViewportFeed(ctx context.Context) ViewportSizeFeed {
	if ctx == nil {
		ctx = context.Background()
	}
	id, ch, initial, hasInitial := subscribeViewportSize()
	if !hasInitial {
		if cols, rows, err := device.GetTermSize(); err == nil && cols > 0 && rows > 0 {
			if slots, ok := calcViewportSlots(cols, rows); ok {
				c, r := detailSize(slots.peerSlot.cols, slots.peerSlot.rows)
				initial = ViewportSize{Cols: c, Rows: r}
				hasInitial = true
			}
		}
	}

	out := make(chan ViewportSize, 4)
	done := make(chan struct{})

	stopOnce := sync.Once{}
	stop := func() {
		stopOnce.Do(func() {
			close(done)
			unsubscribeViewportSize(id)
		})
	}

	forward := func(vs ViewportSize) bool {
		select {
		case out <- vs:
			return true
		default:
			select {
			case <-out:
			default:
			}
			select {
			case out <- vs:
				return true
			case <-ctx.Done():
			case <-done:
			}
		}
		return false
	}

	go func() {
		defer close(out)
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case vs := <-ch:
				if !forward(vs) {
					return
				}
			}
		}
	}()

	var initialPtr *ViewportSize
	if hasInitial {
		initialCopy := initial
		initialPtr = &initialCopy
	}

	return ViewportSizeFeed{
		Updates: out,
		Initial: initialPtr,
		Stop:    stop,
	}
}

func broadcastViewportSize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	vs := ViewportSize{Cols: cols, Rows: rows}

	viewportMu.Lock()
	if viewportLastSet && viewportLast.Cols == vs.Cols && viewportLast.Rows == vs.Rows {
		viewportMu.Unlock()
		return
	}
	viewportLast = vs
	viewportLastSet = true
	subs := make([]chan ViewportSize, 0, len(viewportSubs))
	for _, ch := range viewportSubs {
		subs = append(subs, ch)
	}
	viewportMu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- vs:
		default:
		}
	}
}

// setSelfTermSize updates the cached viewport slot dimensions for the local terminal
// and returns the peer viewport area derived from the current layout math.
func setSelfTermSize(cols, rows int) (peerCols, peerRows int, ok bool) {
	if cols <= 0 || rows <= 0 {
		return 0, 0, false
	}

	if viewportSlots, slotsOK := calcViewportSlots(cols, rows); slotsOK {
		selfSlotMu.Lock()
		selfViewport = viewportSlots
		selfSlotMu.Unlock()
		layout := "horizontal"
		if selfViewport.orient == 'V' {
			layout = "vertical"
		}
		// Logged when it changes: it is asked for on every frame drawn.
		if key := [4]int{cols, rows, selfViewport.selfSlot.cols, selfViewport.selfSlot.rows}; key != lastSelfLogged.Swap(key) {
			logs.LogV("[term] self window %dx%d, %s slot %dx%d cells (~%dx%d px)",
				cols, rows, layout,
				selfViewport.selfSlot.cols, selfViewport.selfSlot.rows,
				selfViewport.selfSlot.cols*4, selfViewport.selfSlot.rows*8)
		}
		peerCols, peerRows := detailSize(selfViewport.peerSlot.cols, selfViewport.peerSlot.rows)
		return peerCols, peerRows, true
	}
	if key := [4]int{cols, rows}; key != lastSelfLogged.Swap(key) {
		logs.LogV("[term] self window %dx%d", cols, rows)
	}
	return 0, 0, false
}

// lastScreenLogged is what the screen showed when last logged (DEBUG-TEMP).
var lastScreenLogged atomic.Value

// lastSelfLogged is the window size and slot last logged.
var lastSelfLogged swapKey

type swapKey struct {
	sync.Mutex
	v [4]int
}

// Swap stores v and returns what was there.
func (k *swapKey) Swap(v [4]int) [4]int {
	k.Lock()
	defer k.Unlock()
	old := k.v
	k.v = v
	return old
}

// SelfSlot returns the current viewport slot dimensions for the local terminal.
func SelfSlot() (cols, rows int, ok bool) {
	selfSlotMu.RLock()
	defer selfSlotMu.RUnlock()
	if selfViewport.selfSlot.cols <= 0 || selfViewport.selfSlot.rows <= 0 {
		return 0, 0, false
	}
	cols, rows = detailSize(selfViewport.selfSlot.cols, selfViewport.selfSlot.rows)
	return cols, rows, true
}

// PeerSlot returns the cached viewport slot dimensions learned from the remote peer.
func PeerSlot() (cols, rows int, ok bool) {
	peerSlotMu.RLock()
	defer peerSlotMu.RUnlock()
	if peerSlot.cols <= 0 || peerSlot.rows <= 0 {
		return 0, 0, false
	}
	return peerSlot.cols, peerSlot.rows, true
}

// startTermImgRenderer запускает рендер луп и возвращает stop-функцию и done-канал.
func startTermImgRenderer(parent context.Context) (func(), <-chan struct{}) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	frameCh := make(chan *device.TerminalFrame, 2)
	setFrameFeed(frameCh)

	terminal, err := device.StartTerminal(frameCh, ctx.Done(), nil)
	if err != nil {
		setFrameFeed(nil)
		close(frameCh)
		close(done)
		return func() {}, done
	}

	go func() {
		defer close(done)
		defer func() {
			setFrameFeed(nil)
			close(frameCh)
			if terminal != nil {
				<-terminal.Done()
			}
		}()

		var lastCols, lastRows int
		var lastBlink bool
		tick := time.NewTicker(50 * time.Millisecond) // periodic poll to react quickly to terminal resizes
		defer tick.Stop()

		// Frames of both panes arrive independently; a request shortly after
		// a render waits for the rest of renderMinInterval, so one render
		// shows both panes' news instead of each pane triggering its own.
		var lastRender time.Time
		var deferred <-chan time.Time
		// While the terminal has not processed the previous frame, rendering
		// waits for its answer (see device.TerminalBusy) and then draws the
		// latest state once, instead of queueing frames inside the terminal.
		var waiting atomic.Bool
		var renders, waits int
		statsAt := time.Now()
		device.SetTerminalAckHandler(func() {
			if waiting.Swap(false) {
				RequestRedraw()
			}
		})
		defer device.SetTerminalAckHandler(nil)
		render := func() {
			deferred = nil
			if device.TerminalBusy() {
				waits++
				waiting.Store(true)
				// The answer may have arrived just before waiting was set.
				if device.TerminalBusy() || !waiting.Swap(false) {
					return
				}
			}
			renderTermFrames()
			lastRender = time.Now()
			renders++
			if now := lastRender; now.Sub(statsAt) >= time.Second {
				acks, mean := device.TerminalAckStats()
				logs.LogV("[term] %d frames/s to the terminal, %d answered, mean answer %v, waited %d times",
					renders, acks, mean.Round(time.Millisecond), waits)
				renders, waits, statsAt = 0, 0, now
			}
		}

		// Initial draw so the UI is not blank until the first event arrives.
		render()

		for {
			select {
			case <-ctx.Done():
				return

			case <-redrawCh:
				// Коалесцируем несколько запросов в один.
				for {
					select {
					case <-redrawCh:
						continue
					default:
						break
					}
					break
				}
				if deferred != nil {
					continue // a render is already scheduled
				}
				if wait := renderMinInterval - time.Since(lastRender); wait > 0 {
					deferred = time.After(wait)
					continue
				}
				render()

			case <-deferred:
				render()

			case <-tick.C:
				cols, rows, err := device.GetTermSize()
				if err != nil {
					continue
				}
				// Redraw when the cursor blinks, even if no video frame arrives.
				blink := ChatConnected() && inputCursorVisible(time.Now())
				if cols != lastCols || rows != lastRows || blink != lastBlink {
					lastCols, lastRows, lastBlink = cols, rows, blink
					render()
				}
			}
		}
	}()

	return cancel, done
}

// renderMinInterval is the shortest time between two renders of the call
// screen (at most 40 per second).
const renderMinInterval = 25 * time.Millisecond

func renderTermFrames() {
	cols, rows, err := device.GetTermSize()
	if err != nil {
		return
	}
	peerCols, peerRows, havePeerViewport := setSelfTermSize(cols, rows)
	broadcastTermSize(cols, rows)
	if havePeerViewport {
		broadcastViewportSize(peerCols, peerRows)
	}
	var data string
	kind := "call"
	if ChatConnected() {
		data = renderChatScreen(cols, rows)
		if data == "" {
			// The call does not fit: say so, rather than leave whatever was
			// on the screen before (the call goes on, its sound too).
			kind = "call (too small)"
			data = renderConnectionScreen(cols, rows, lang.T(lang.LinkActive)+"\n"+lang.T(lang.TooSmall))
		}
	} else {
		kind = "status: " + currentStatusMessage()
		data = renderConnectionScreen(cols, rows, currentStatusMessage())
	}
	if lastScreenLogged.Swap(kind) != kind { // DEBUG-TEMP: what the screen shows, when it changes
		log.Printf("[debug] screen %dx%d: %s", cols, rows, kind)
	}
	if data != "" {
		if termHUD {
			data = onBlack(data)
		}
		data += gfxOutput(cols, rows, strings.Contains(data, "\x1b[2J"))
		enqueueFrame(&device.TerminalFrame{Data: data})
	}
}

const (
	screenStatus byte = 'S'
	screenChat   byte = 'C'
)

// beginScreen reports whether the terminal must be fully cleared before
// drawing the given screen kind at the given size. A full clear is only
// needed when the layout changes; regular frames overwrite cells in place,
// which avoids the whole screen flashing on every video frame.
func beginScreen(kind byte, cols, rows int) bool {
	lastDrawMu.Lock()
	defer lastDrawMu.Unlock()
	changed := lastDraw.w != cols || lastDraw.h != rows || lastDraw.orient != kind
	lastDraw.w, lastDraw.h, lastDraw.orient = cols, rows, kind
	return changed
}

// renderConnectionScreen keeps the pre-call UI focused on connection progress.
// The video and chat layout is shown only after the TCP session handshake.
func renderConnectionScreen(cols, rows int, status string) string {
	if strings.TrimSpace(status) == "" {
		status = lang.T(lang.Standby) + "\n" + lang.T(lang.AwaitSignal)
	}
	beginScreen(screenStatus, cols, rows)
	paneCache.reset()
	return buildStatusScreen(cols, rows, status)
}

// renderChatScreen draws the call's video panes, conversation, and input line.
// Every visible cell is rewritten on each call, so the screen is cleared only
// when the layout changes.
func renderChatScreen(cols, rows int) string {
	local, _, localAt := localFrameSnapshot()
	remote, _, remoteAt := remoteFrameSnapshot()
	vs, ok := calcViewportSlots(cols, rows)
	if !ok {
		return ""
	}
	canvasLeft, canvasTop := termSide+1, termBorder+1
	canvasCols := cols - 2*termSide
	videoGroupCols := vs.selfSlot.cols + termGap + vs.peerSlot.cols
	videoLeft := canvasLeft + max(0, (canvasCols-videoGroupCols)/2)
	videoCaptionRow := canvasTop + vs.selfSlot.rows
	if framed() {
		videoCaptionRow++ // a row for the bottom of the panes' frames
	}
	chatTop := videoCaptionRow + 1 + termSeparator
	inputRow := rows - termBorder
	if framed() {
		inputRow-- // room under the chat's frame for its plates
	}
	gap := termSeparator
	if framed() {
		gap = 1 // the chat and its input share a frame in the window
	}
	chatBottom := inputRow - 1 - gap
	chatRows := chatBottom - chatTop + 1
	// The conversation's columns: in the window, inside its frame.
	chatLeft, chatCols := canvasLeft, canvasCols
	if framed() {
		chatLeft, chatCols = canvasLeft+2, canvasCols-4
	}
	if chatRows < 1 {
		return ""
	}
	statusClickClearAreas()

	var out strings.Builder
	fullClear := beginScreen(screenChat, cols, rows)
	paneCache.prepare(cols, rows, time.Now())
	if fullClear {
		paneCache.reset()
	}
	writeFramePrefix(&out, fullClear)

	// Borders, separators, captions, chat and input: collected per row and
	// sent only when a row changed (see screenLines).
	lines := newScreenLines(rows)
	blankCols := strings.Repeat(" ", cols)
	lineAt := func(row, col int) *strings.Builder {
		b := lines.at(row)
		writeCursorTo(b, row, col)
		return b
	}
	lineAt(1, 1).WriteString(blankCols)
	lineAt(rows, 1).WriteString(blankCols)
	for r := 2; r < rows; r++ {
		lineAt(r, 1).WriteByte(' ')
		lineAt(r, cols).WriteByte(' ')
	}

	// Margins left and right of the centered video group (panes + captions).
	videoRight := videoLeft + videoGroupCols
	for r := canvasTop; r <= videoCaptionRow; r++ {
		if videoLeft > canvasLeft {
			lineAt(r, canvasLeft).WriteString(strings.Repeat(" ", videoLeft-canvasLeft))
		}
		if end := canvasLeft + canvasCols; videoRight < end {
			lineAt(r, videoRight).WriteString(strings.Repeat(" ", end-videoRight))
		}
	}

	sepCol := videoLeft + vs.selfSlot.cols
	peerLeft := sepCol + termGap
	for r := canvasTop; r <= canvasTop+vs.selfSlot.rows; r++ {
		lineAt(r, sepCol).WriteString(strings.Repeat(" ", termGap))
	}
	canvasBlank := strings.Repeat(" ", canvasCols)
	for i := 0; i < termSeparator; i++ {
		lineAt(chatTop-1+i, canvasLeft).WriteString(canvasBlank)
		lineAt(inputRow-1+i, canvasLeft).WriteString(canvasBlank)
	}

	// The terminal's frames: known before the video, which leaves their
	// cells (the panes' cut corners) to them. What the screen showed there
	// is forgotten, so the video never takes them back by itself.
	var hud *hudCanvas
	if termHUD {
		hud = newHUDCanvas()
		selfSeed, peerSeed, _ := FrameSeeds()
		self, peer := Accents()
		hudBox(hud, canvasTop-1, videoLeft-1, vs.selfSlot.cols+2, vs.selfSlot.rows+2, self, selfSeed, false, false)
		hudBox(hud, canvasTop-1, peerLeft-1, vs.peerSlot.cols+2, vs.peerSlot.rows+2, peer, peerSeed, true, false)
		hudWindowBorder(hud, cols, rows)
		if gfxHUD {
			// The frames go as an image: their cells stay blank, and
			// so do the video's cells outside the frames' inner line.
			gfxBegin()
			gfxPane(canvasTop, videoLeft, vs.selfSlot.cols, vs.selfSlot.rows, false)
			gfxPane(canvasTop, peerLeft, vs.peerSlot.cols, vs.peerSlot.rows, true)
			gfxBorder()
			hud.blank()
			for k, c := range gfxVideoMask(gfxGrid(cols, rows)).cells {
				hud.cells[k] = c
			}
		}
		for k := range hud.cells {
			paneCache.invalidate(k[0], k[1], 1, 1)
		}
		hudVideoMask = hud
	}

	now := time.Now()
	localIdle := writeChatPane(&out, local, localAt, localFrameType, noSignalLabel()+" CAM", now, canvasTop, videoLeft, vs.selfSlot)
	remoteIdle := writeChatPane(&out, remote, remoteAt, remoteFrameType, noSignalLabel()+" NET", now, canvasTop, peerLeft, vs.peerSlot)
	hudVideoMask = nil
	// Animate the noise while a pane has no signal.
	updateNoiseAnimation(localIdle || remoteIdle)

	txAudio, txVideo, rxAudio, rxVideo := NetRateSnapshot()
	latencyLabel := buildLatencyLabel(PeerLatencySnapshot())
	pathLabel := PeerPathSnapshot()
	localCaption := currentFPSLabel(localFrameType) + " " + buildRateLabel("TX", '↑', txAudio, txVideo)
	remoteCaption := currentFPSLabel(remoteFrameType) + " " + buildRateLabel("RX", '↓', rxAudio, rxVideo)
	if latencyLabel != "" && !framed() { // the window shows it between the panes
		remoteCaption += " " + joinLabels(pathLabel, latencyLabel)
	}
	// A click mutes the sound only (mediactrl); the video goes on.
	if !mediactrl.CaptureEnabled() {
		localCaption = "MUTED " + localCaption
	}
	if !mediactrl.PlaybackEnabled() {
		remoteCaption = "MUTED " + remoteCaption
	}
	selfText := writeVideoCaption(lines.at(videoCaptionRow), videoCaptionRow, videoLeft, vs.selfSlot.cols, localCaption, false)
	peerText := writeVideoCaption(lines.at(videoCaptionRow), videoCaptionRow, peerLeft, vs.peerSlot.cols, remoteCaption, true)
	if framed() {
		// One bracketed line under both panes: a block at each outer end,
		// the line running from caption to ping to caption.
		// The ping may spread past the gap, up to a column short of each
		// caption; when the route's name does not fit, the ping goes alone.
		spread := max(0, min(sepCol-selfText[1]-1, peerText[0]-1-(sepCol+termGap)))
		if selfText[1] == 0 || peerText[0] == 0 {
			spread = 0
		}
		ping := latencyLabel
		if latencyLabel != "" && pathLabel != "" {
			if full := joinLabels(pathLabel, latencyLabel); displayWidth(full) <= termGap+2*spread {
				ping = full
			}
		}
		extra := min(spread, max(0, (displayWidth(ping)-termGap+1)/2))
		pingText := writeGapPing(lines.at(videoCaptionRow), videoCaptionRow, sepCol-extra, termGap+2*extra, ping)
		rel := func(sp [2]int) [2]int { return [2]int{sp[0] - videoLeft, sp[1] - videoLeft} }
		writePixelBracket(lines.at(videoCaptionRow), videoCaptionRow, videoLeft, videoGroupCols, rel(selfText), rel(pingText), rel(peerText))
	}

	chatState.RLock()
	messages := append([]ChatMessage(nil), chatState.messages...)
	input := chatState.input
	chatState.RUnlock()
	// Our messages sit on the left, the peer's on the right; long messages wrap.
	type chatLine struct {
		text  string
		local bool
	}
	var chat []chatLine
	for i := len(messages) - 1; i >= 0 && len(chat) < chatRows; i-- {
		wrapped := wrapWidth(sanitizeLine(messages[i].Text), chatCols)
		block := make([]chatLine, 0, len(wrapped))
		for _, l := range wrapped {
			block = append(block, chatLine{text: l, local: messages[i].Local})
		}
		chat = append(block, chat...)
	}
	if len(chat) > chatRows {
		chat = chat[len(chat)-chatRows:]
	}
	for i := 0; i < chatRows; i++ {
		row := chatTop + i
		b := lineAt(row, 1)
		b.WriteString("\x1b[2K")
		if i >= len(chat) {
			continue
		}
		l := chat[i]
		// Default terminal colours: fixed light colours were unreadable on a
		// light terminal background. Sides tell the speakers apart.
		col := chatLeft
		if !l.local {
			col += max(0, chatCols-displayWidth(l.text))
		}
		writeCursorTo(b, row, col)
		if framed() { // the window colours the speakers
			self, peer := Accents()
			if l.local {
				b.WriteString(accentSGR(self))
			} else {
				b.WriteString(accentSGR(peer))
			}
			b.WriteString(l.text + "\x1b[0m")
		} else {
			b.WriteString(l.text)
		}
	}

	cursor := " "
	if inputCursorVisible(time.Now()) {
		cursor = "▏"
		if framed() {
			cursor = "█"
		}
	}
	// Keep the end of a long input visible, with room for the cursor.
	line := tailWidth(sanitizeLine(input), chatCols-1) + cursor
	b := lineAt(inputRow, 1)
	b.WriteString("\x1b[2K")
	writeCursorTo(b, inputRow, chatLeft)
	if framed() {
		self, _ := Accents()
		line = accentSGR(self) + line + "\x1b[0m"
	}
	b.WriteString(line)

	// The version, right under the input: in the window the row under the
	// chat's frame (whose bottom right keeps clear of its decoration),
	// otherwise the bottom margin. It ends short of the frame's cut corner.
	if v := shownVersion(); v != "" && displayWidth(v) <= canvasCols/2 {
		row, end := rows, canvasLeft+canvasCols-1
		if framed() {
			row, end = inputRow+2, canvasLeft+canvasCols-3
			lineAt(row, canvasLeft).WriteString(canvasBlank)
		}
		lineAt(row, end-displayWidth(v)+1).WriteString(versionSGR() + v + "\x1b[0m")
	}

	if framed() {
		// The frame takes a row above the chat and one below the input.
		writeChatFrame(&out, hud, chatTop-1, canvasLeft, canvasCols, inputRow-chatTop+3)
	}
	if hud != nil {
		// Written last, over the margins and the rows the chat clears.
		hud.draw(lineAt)
	}
	lines.flush(&out, &paneCache)
	return out.String()
}

// sanitizeLine replaces control characters so a line cannot move the cursor.
func sanitizeLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// writeVideoCaption draws the info bar under a video pane across its full width.
// writeGapPing writes the round trip to the peer in the gap between the
// panes' captions (the window), centred, in the brackets' orange; the gap is
// cleared when there is none yet. It returns the columns the text spans
// (1-based, [from, to)): empty without a ping.
func writeGapPing(out *strings.Builder, row, left, width int, ping string) (span [2]int) {
	if out == nil || row <= 0 || left <= 0 || width <= 0 {
		return span
	}
	ping = truncateWidth(sanitizeLine(ping), width)
	pad := width - displayWidth(ping)
	fmt.Fprintf(out, "\x1b[%d;%dH\x1b[49m\x1b[38;2;255;140;0m%s%s%s\x1b[0m", row, left,
		strings.Repeat(" ", pad/2), ping, strings.Repeat(" ", pad-pad/2))
	return [2]int{left + pad/2, left + pad/2 + displayWidth(ping)}
}

// writeVideoCaption writes a pane's caption under it, centred: on the
// panel's colours in a terminal; in the window in its owner's accent (the
// peer's for remote), on the window's black.
// It returns the columns the text spans (1-based, [from, to)).
func writeVideoCaption(out *strings.Builder, row, left, width int, caption string, remote bool) (span [2]int) {
	if out == nil || row <= 0 || left <= 0 || width <= 0 {
		return span
	}
	// In the window the caption is on a bracketed line (drawn there, from
	// a mark): a line up to the text, a block at the outer end. They need
	// three columns on each side.
	room := width
	if framed() && width >= 7 {
		room = width - 6
	}
	caption = truncateWidth(sanitizeLine(caption), room)
	pad := max(0, width-displayWidth(caption))
	leftPad := pad / 2
	colors := panelColors()
	if framed() {
		self, peer := Accents()
		colors = "\x1b[49m" + accentSGR(self)
		if remote {
			colors = "\x1b[49m" + accentSGR(peer)
		}
	}
	out.WriteString(fmt.Sprintf("\x1b[%d;%dH", row, left) + colors)
	out.WriteString(strings.Repeat(" ", leftPad))
	out.WriteString(caption)
	out.WriteString(strings.Repeat(" ", pad-leftPad))
	out.WriteString("\x1b[0m")
	return [2]int{left + leftPad, left + leftPad + displayWidth(caption)}
}

// writeChatPane fills a video slot: the frame when available (centered and
// clipped to the slot), otherwise a blank pane with a placeholder label.
// noSignalLabel marks a pane without live video, as in v0.1.3; the pane name
// (CAM or NET) follows it. The caption below keeps showing the link stats.
func noSignalLabel() string { return lang.T(lang.NoSignal) }

// writeChatPane fills a video slot with the frame (centered and clipped to
// the slot). Without a frame yet, or when frames stopped for longer than
// frameFreezeThreshold, it shows animated noise and, after testCardDelay, the
// TV test card, labelled NO SIGNAL. It reports whether the pane has no signal.
func writeChatPane(out *strings.Builder, frame *codec.TextFrame, updatedAt time.Time, frameType, label string, now time.Time, top, left int, slot slotSize) bool {
	idle := frame == nil || (!updatedAt.IsZero() && now.Sub(updatedAt) > frameFreezeThreshold)
	if idle {
		cols, rows := detailSize(slot.cols, slot.rows)
		if updateIdlePatternSlot(frameType, true, now) {
			frame = testCardTextFrame(cols, rows)
		} else {
			frame = noiseTextFrame(cols, rows)
		}
	} else {
		updateIdlePatternSlot(frameType, false, now)
	}
	if pixelMode {
		// The window draws the frame itself, fitted to the pane.
		writePixelPane(out, frame, top, left, slot.cols, slot.rows, frameType == remoteFrameType)
	} else if frame != nil && frame.Cols == slot.cols && frame.Rows == slot.rows {
		writeTextFrameRegion(out, frame, top, left, 0, 0, frame.Cols, frame.Rows, &paneCache)
	} else {
		// The peer may still be encoding for a previous slot size: blank the
		// pane and centre what fits. Rare, so drawn in full.
		paneCache.invalidate(top, left, slot.cols, slot.rows)
		blank := strings.Repeat(" ", slot.cols)
		for r := 0; r < slot.rows; r++ {
			out.WriteString(fmt.Sprintf("\x1b[%d;%dH%s", top+r, left, blank))
		}
		if frame != nil {
			visCols, visRows := min(frame.Cols, slot.cols), min(frame.Rows, slot.rows)
			offCol, offRow := max(0, (frame.Cols-slot.cols)/2), max(0, (frame.Rows-slot.rows)/2)
			writeTextFrameRegion(out, frame, top+(slot.rows-visRows)/2, left+(slot.cols-visCols)/2, offCol, offRow, visCols, visRows, nil)
		}
	}
	if idle {
		// The label covers a row of pane cells; redraw it under the label next time.
		writeCenterLabel(out, label, top, left, slot.cols, slot.rows)
		paneCache.invalidate(top+slot.rows/2, left, slot.cols, 1)
	}
	return idle
}

var noisePaletteYCbCr = [2][3]byte{
	{30, 128, 128},  // почти чёрный
	{235, 128, 128}, // почти белый
}

func noiseTextFrame(cols, rows int) *codec.TextFrame {
	if cols <= 0 || rows <= 0 || len(noiseGlyphSet) == 0 {
		return nil
	}

	total := cols * rows
	glyphs := make([]byte, total)
	fg := make([]byte, total*3)
	bg := make([]byte, total*3)

	colorBits := total * 2
	colorBytes := (colorBits + 7) / 8
	bits := make([]byte, colorBytes)

	rng := rand.New(rand.NewSource(nextNoiseSeed()))
	for i := 0; i < colorBytes; i++ {
		bits[i] = byte(rng.Intn(256))
	}

	for i := 0; i < total; i++ {
		glyphs[i] = noiseGlyphSet[rng.Intn(len(noiseGlyphSet))]

		fgColor := noisePaletteYCbCr[0]
		if bitIsSet(bits, i*2) {
			fgColor = noisePaletteYCbCr[1]
		}
		bgColor := noisePaletteYCbCr[0]
		if bitIsSet(bits, i*2+1) {
			bgColor = noisePaletteYCbCr[1]
		}

		base := i * 3
		bg[base], bg[base+1], bg[base+2] = bgColor[0], bgColor[1], bgColor[2]
		fg[base], fg[base+1], fg[base+2] = fgColor[0], fgColor[1], fgColor[2]
	}

	return &codec.TextFrame{
		Cols:    cols,
		Rows:    rows,
		Width:   cols * 4,
		Height:  rows * 8,
		Glyphs:  glyphs,
		FgYCbCr: fg,
		BgYCbCr: bg,
	}
}

func testCardTextFrame(cols, rows int) *codec.TextFrame {
	if cols <= 0 || rows <= 0 {
		return nil
	}

	total := cols * rows
	glyphs := make([]byte, total)
	fg := make([]byte, total*3)
	bg := make([]byte, total*3)

	for i := 0; i < total; i++ {
		glyphs[i] = patternGlyphBlank
		setCellColor(bg, i, testCardBackground)
		setCellColor(fg, i, testCardBackground)
	}

	clampRect := func(x0, y0, w, h int) (int, int, int, int, bool) {
		if w <= 0 || h <= 0 || x0 >= cols || y0 >= rows {
			return 0, 0, 0, 0, false
		}
		x1 := x0 + w
		y1 := y0 + h
		if x1 > cols {
			x1 = cols
		}
		if y1 > rows {
			y1 = rows
		}
		if x0 < 0 {
			x0 = 0
		}
		if y0 < 0 {
			y0 = 0
		}
		if x0 >= x1 || y0 >= y1 {
			return 0, 0, 0, 0, false
		}
		return x0, y0, x1, y1, true
	}

	fillRect := func(x0, y0, w, h int, color [3]byte) {
		left, top, right, bottom, ok := clampRect(x0, y0, w, h)
		if !ok {
			return
		}
		for r := top; r < bottom; r++ {
			rowBase := r * cols
			for c := left; c < right; c++ {
				idx := rowBase + c
				glyphs[idx] = patternGlyphBlank
				setCellColor(bg, idx, color)
				setCellColor(fg, idx, color)
			}
		}
	}

	drawHorizontalLine := func(y int, color [3]byte) {
		if y < 0 || y >= rows {
			return
		}
		for c := 0; c < cols; c++ {
			idx := y*cols + c
			glyphs[idx] = patternGlyphHorizontal
			setCellColor(bg, idx, testCardBackground)
			setCellColor(fg, idx, color)
		}
	}

	fillBars := func(y0, h int, palette [][3]byte) {
		if len(palette) == 0 || h <= 0 {
			return
		}
		barWidth := max(1, cols/len(palette))
		x := 0
		for i, color := range palette {
			width := barWidth
			if i == len(palette)-1 {
				width = cols - x
			}
			fillRect(x, y0, width, h, color)
			x += width
			if x >= cols {
				break
			}
		}
	}

	topHeight := max(1, (rows*2)/3)
	midHeight := max(1, rows/6)
	if topHeight+midHeight >= rows {
		topHeight = max(1, rows-2)
		midHeight = 1
	}
	bottomHeight := rows - topHeight - midHeight
	if bottomHeight < 1 {
		bottomHeight = 1
		if topHeight > midHeight {
			topHeight--
		} else if midHeight > 1 {
			midHeight--
		}
	}

	fillBars(0, topHeight, testCardTopBars)

	midStart := topHeight
	fillBars(midStart, midHeight, testCardMiddleBars)

	bottomStart := midStart + midHeight
	fillBars(bottomStart, bottomHeight, testCardBottomBars)

	drawHorizontalLine(midStart, testCardSeparator)
	drawHorizontalLine(bottomStart, testCardSeparator)

	centerBoxWidth := max(2, cols/6)
	centerBoxHeight := max(1, midHeight/2)
	centerX := (cols - centerBoxWidth) / 2
	centerY := midStart + (midHeight-centerBoxHeight)/2
	fillRect(centerX, centerY, centerBoxWidth, centerBoxHeight, adjustLuma(testCardAccent, -10))

	innerBoxWidth := max(1, centerBoxWidth/2)
	innerBoxHeight := max(1, centerBoxHeight/2)
	fillRect(centerX+(centerBoxWidth-innerBoxWidth)/2, centerY+(centerBoxHeight-innerBoxHeight)/2, innerBoxWidth, innerBoxHeight, testCardAccent)

	// Color burst stripes inside middle band.
	burstPalette := [][3]byte{
		rgbToYCbCr(255, 188, 0),
		rgbToYCbCr(0, 160, 255),
		rgbToYCbCr(255, 0, 140),
		rgbToYCbCr(0, 255, 188),
	}
	burstHeight := max(1, midHeight/3)
	fillBars(midStart+midHeight-burstHeight, burstHeight, burstPalette)

	// PLUGE bars at the bottom left to mimic calibration levels.
	plugeWidth := max(1, cols/24)
	plugeHeight := max(1, bottomHeight/2)
	plugeColors := [][3]byte{
		rgbToYCbCr(0, 0, 0),
		rgbToYCbCr(35, 35, 35),
		rgbToYCbCr(70, 70, 70),
	}
	for i, color := range plugeColors {
		fillRect(i*plugeWidth, rows-plugeHeight, plugeWidth, plugeHeight, color)
	}

	// Add right-side reference blocks.
	refWidth := max(1, cols/16)
	refColors := [][3]byte{
		rgbToYCbCr(255, 255, 255),
		rgbToYCbCr(0, 0, 0),
		rgbToYCbCr(255, 0, 0),
		rgbToYCbCr(0, 255, 0),
		rgbToYCbCr(0, 0, 255),
	}
	for i, color := range refColors {
		refX := cols - (i+1)*refWidth
		fillRect(refX, rows-bottomHeight, refWidth, bottomHeight, color)
	}

	return &codec.TextFrame{
		Cols:    cols,
		Rows:    rows,
		Width:   cols * 4,
		Height:  rows * 8,
		Glyphs:  glyphs,
		FgYCbCr: fg,
		BgYCbCr: bg,
	}
}

func clampByte(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

func nextNoiseSeed() int64 {
	now := uint64(time.Now().UnixNano())
	seed := atomic.AddUint64(&noiseRandSeed, now+1)
	return int64(seed ^ now)
}

func setCellColor(buf []byte, idx int, color [3]byte) {
	if buf == nil {
		return
	}
	base := idx * 3
	if base+2 >= len(buf) {
		return
	}
	buf[base] = color[0]
	buf[base+1] = color[1]
	buf[base+2] = color[2]
}

func glyphIndexForRune(r rune) byte {
	if glyphIndexLookup == nil {
		glyphIndexLookup = make(map[int]int, len(glyphCodepoints))
		for i, cp := range glyphCodepoints {
			glyphIndexLookup[cp] = i
		}
	}
	if idx, ok := glyphIndexLookup[int(r)]; ok && idx >= 0 {
		return byte(idx)
	}
	return 0
}

func adjustLuma(color [3]byte, delta int) [3]byte {
	y := int(color[0]) + delta
	if y < 0 {
		y = 0
	}
	if y > 255 {
		y = 255
	}
	return [3]byte{byte(y), color[1], color[2]}
}

func bitIsSet(bits []byte, bit int) bool {
	if bit < 0 {
		return false
	}
	idx := bit / 8
	off := uint(bit % 8)
	if idx < 0 || idx >= len(bits) {
		return false
	}
	return bits[idx]&(1<<off) != 0
}

func updateNoiseAnimation(active bool) {
	noiseAnimator.Lock()
	defer noiseAnimator.Unlock()
	if active {
		if noiseAnimator.cancel != nil {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		noiseAnimator.cancel = cancel
		go func() {
			const noiseFrameInterval = time.Second / 20
			ticker := time.NewTicker(noiseFrameInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					RequestRedraw()
				}
			}
		}()
	} else {
		if noiseAnimator.cancel != nil {
			noiseAnimator.cancel()
			noiseAnimator.cancel = nil
		}
	}
}

func updateIdlePatternSlot(frameType string, active bool, now time.Time) bool {
	noiseSlots.Lock()
	defer noiseSlots.Unlock()
	slot := noiseSlots.slot(frameType)
	if slot == nil {
		return false
	}
	if !active {
		*slot = noiseSlotState{}
		return false
	}
	if !slot.active {
		slot.active = true
		slot.since = now
		slot.pattern = false
	}
	if slot.pattern {
		return true
	}
	if !slot.since.IsZero() && now.Sub(slot.since) >= testCardDelay {
		slot.pattern = true
		return true
	}
	return false
}

func (s *noiseSlotStore) slot(frameType string) *noiseSlotState {
	switch frameType {
	case localFrameType:
		return &s.local
	case remoteFrameType:
		return &s.remote
	default:
		return nil
	}
}

func buildHorizontalFrame(cols, rows int, leftFrame, rightFrame *codec.TextFrame, leftOverlay, rightOverlay frameOverlay, fullClear bool) string {
	canvasCols := cols - 2*termBorder
	canvasRows := rows - 2*termBorder
	if canvasCols <= 0 || canvasRows <= 0 {
		return ""
	}

	canvasTop := termBorder + 1
	canvasLeft := termBorder + 1

	availableCols := canvasCols - termSeparator
	if availableCols < 4 {
		return ""
	}

	leftCols := availableCols / 2
	rightCols := availableCols - leftCols
	if leftCols < 2 || rightCols < 2 {
		return ""
	}

	var out strings.Builder
	writeFramePrefix(&out, fullClear)
	appendBorderClear(&out, cols, rows)

	separatorCol := canvasLeft + leftCols
	appendVerticalSeparator(&out, canvasTop, canvasRows, separatorCol)

	if leftFrame != nil {
		startCol := canvasLeft + max(0, (leftCols-leftFrame.Cols)/2)
		startRow := canvasTop + max(0, (canvasRows-leftFrame.Rows)/2)
		writeTextFrame(&out, leftFrame, startRow, startCol)
		writeCenterLabel(&out, leftOverlay.centerLabel, startRow, startCol, leftFrame.Cols, leftFrame.Rows)
		writeStatusOverlay(&out, startRow+leftFrame.Rows-1, startCol, leftFrame.Cols, leftFrame.Rows, leftOverlay)
	}

	if rightFrame != nil {
		slotLeft := separatorCol + termSeparator
		startCol := slotLeft + max(0, (rightCols-rightFrame.Cols)/2)
		startRow := canvasTop + max(0, (canvasRows-rightFrame.Rows)/2)
		writeTextFrame(&out, rightFrame, startRow, startCol)
		writeCenterLabel(&out, rightOverlay.centerLabel, startRow, startCol, rightFrame.Cols, rightFrame.Rows)
		writeStatusOverlay(&out, startRow+rightFrame.Rows-1, startCol, rightFrame.Cols, rightFrame.Rows, rightOverlay)
	}

	return out.String()
}

func buildVerticalFrame(cols, rows int, topFrame, bottomFrame *codec.TextFrame, topOverlay, bottomOverlay frameOverlay, fullClear bool) string {
	canvasCols := cols - 2*termBorder
	canvasRows := rows - 2*termBorder
	if canvasCols <= 0 || canvasRows <= 0 {
		return ""
	}

	canvasTop := termBorder + 1
	canvasLeft := termBorder + 1

	availableRows := canvasRows - termSeparator
	if availableRows < 2 {
		return ""
	}
	topRows := availableRows / 2
	bottomRows := availableRows - topRows
	if topRows < 1 || bottomRows < 1 {
		return ""
	}

	var out strings.Builder
	writeFramePrefix(&out, fullClear)
	appendBorderClear(&out, cols, rows)

	separatorRow := canvasTop + topRows
	appendHorizontalSeparator(&out, canvasLeft, canvasCols, separatorRow)

	if topFrame != nil {
		startCol := canvasLeft + max(0, (canvasCols-topFrame.Cols)/2)
		startRow := canvasTop + max(0, (topRows-topFrame.Rows)/2)
		writeTextFrame(&out, topFrame, startRow, startCol)
		writeCenterLabel(&out, topOverlay.centerLabel, startRow, startCol, topFrame.Cols, topFrame.Rows)
		writeStatusOverlay(&out, startRow+topFrame.Rows-1, startCol, topFrame.Cols, topFrame.Rows, topOverlay)
	}

	if bottomFrame != nil {
		slotTop := separatorRow + termSeparator
		startCol := canvasLeft + max(0, (canvasCols-bottomFrame.Cols)/2)
		startRow := slotTop + max(0, (bottomRows-bottomFrame.Rows)/2)
		writeTextFrame(&out, bottomFrame, startRow, startCol)
		writeCenterLabel(&out, bottomOverlay.centerLabel, startRow, startCol, bottomFrame.Cols, bottomFrame.Rows)
		writeStatusOverlay(&out, startRow+bottomFrame.Rows-1, startCol, bottomFrame.Cols, bottomFrame.Rows, bottomOverlay)
	}

	return out.String()
}

func buildSingleFrame(cols, rows int, frame *codec.TextFrame, overlay frameOverlay, fullClear bool) string {
	if frame == nil {
		return ""
	}
	canvasCols := cols - 2*termBorder
	canvasRows := rows - 2*termBorder
	if canvasCols <= 0 || canvasRows <= 0 {
		return ""
	}

	canvasTop := termBorder + 1
	canvasLeft := termBorder + 1

	var out strings.Builder
	writeFramePrefix(&out, fullClear)
	appendBorderClear(&out, cols, rows)

	startCol := canvasLeft + max(0, (canvasCols-frame.Cols)/2)
	startRow := canvasTop + max(0, (canvasRows-frame.Rows)/2)
	writeTextFrame(&out, frame, startRow, startCol)
	writeCenterLabel(&out, overlay.centerLabel, startRow, startCol, frame.Cols, frame.Rows)
	writeStatusOverlay(&out, startRow+frame.Rows-1, startCol, frame.Cols, frame.Rows, overlay)
	return out.String()
}

func buildStatusScreen(cols, rows int, message string) string {
	msg := strings.TrimSpace(message)
	if msg == "" || cols <= 0 || rows <= 0 {
		statusClickClearAreas()
		return ""
	}

	canvasCols := cols - 2*termBorder
	canvasRows := rows - 2*termBorder
	if canvasCols <= 0 {
		canvasCols = cols
	}
	if canvasRows <= 0 {
		canvasRows = rows
	}

	shown := msg
	if framed() {
		shown = statusHeading(msg)
		// The text fits inside its frame, which leaves the side margins
		// and its own padding.
		canvasCols = max(1, cols-2*termSide-2*statusPadCols)
	}
	// Too long a line is cut in the terminal and wrapped in the window.
	var lines []string
	for _, l := range strings.Split(shown, "\n") {
		l = strings.TrimSpace(l)
		if !framed() {
			lines = append(lines, truncateWidth(l, canvasCols))
			continue
		}
		wrapped := wrapWidth(l, canvasCols)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		lines = append(lines, wrapped...)
	}

	// A copyable status has its button a line below it: [ Copy ] in the
	// terminal, bracketed in the window, as wide for Copied as for Copy.
	label := statusCopyButton(msg)
	buttonLine, buttonCols := -1, 0
	if label != "" {
		inner := max(displayWidth(lang.T(lang.Copy)), displayWidth(lang.T(lang.Copied)))
		pad := (inner - displayWidth(label)) / 2
		label = strings.Repeat(" ", pad) + label + strings.Repeat(" ", inner-displayWidth(label)-pad)
		if framed() {
			label = "  " + label + "  "
		} else {
			label = "[ " + label + " ]"
		}
		buttonCols = displayWidth(label)
		lines = append(lines, "", label)
		buttonLine = len(lines) - 1
	}

	// The lines go one under another from the same column, the block of
	// them centred.
	blockCols := 0
	for _, l := range lines {
		blockCols = max(blockCols, min(displayWidth(l), canvasCols))
	}
	blockLeft := termBorder + 1 + max(0, (canvasCols-blockCols)/2)
	if framed() { // centred in the window, as its frame is
		blockLeft = 1 + max(0, (cols-blockCols)/2)
	}

	startRow := termBorder + 1 + max(0, (canvasRows-len(lines))/2)
	if startRow < 1 {
		startRow = 1
	}

	var out strings.Builder
	writeFramePrefix(&out, true)
	appendBorderClear(&out, cols, rows)
	if framed() {
		h := newHUDCanvas()
		if gfxHUD {
			gfxBegin()
			gfxBorder()
		}
		writePixelStatusFrame(&out, h, blockCols, len(lines), startRow, cols, rows)
		if termHUD {
			hudWindowBorder(h, cols, rows)
			if gfxHUD {
				h.blank()
			}
			h.draw(outAt(&out))
		}
	}

	var clickRects []statusClickRect
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		row := startRow + i
		if row > rows-termBorder {
			break
		}
		if displayWidth(line) > canvasCols {
			line = truncateWidth(line, canvasCols)
		}
		out.WriteString(fmt.Sprintf("\x1b[%d;%dH", row, blockLeft))
		if framed() {
			out.WriteString(accentSGR(BrickColour(ChatAccent()))) // its bricks' colour
		}
		out.WriteString(line)
		if i == buttonLine {
			if framed() {
				writePixelBracket(&out, row, blockLeft, buttonCols, [2]int{2, buttonCols - 2})
			}
			clickRects = append(clickRects, statusClickRect{
				row:      row,
				startCol: blockLeft,
				endCol:   blockLeft + buttonCols - 1,
			})
		}
	}
	out.WriteString("\x1b[0m")

	if len(clickRects) > 0 {
		statusClickUpdateAreas(clickRects)
	} else {
		statusClickClearAreas()
	}
	return out.String()
}

func writeFramePrefix(out *strings.Builder, fullClear bool) {
	if fullClear {
		out.WriteString("\x1b[2J\x1b[H")
	} else {
		out.WriteString("\x1b[H")
	}
}

func appendBorderClear(sb *strings.Builder, cols, rows int) {
	if cols < 1 || rows < 1 {
		return
	}
	spaceLine := strings.Repeat(" ", cols)
	sb.WriteString("\x1b[1;1H")
	sb.WriteString(spaceLine)

	if rows > 1 {
		sb.WriteString(fmt.Sprintf("\x1b[%d;1H", rows))
		sb.WriteString(spaceLine)
	}

	for r := 2; r < rows; r++ {
		sb.WriteString(fmt.Sprintf("\x1b[%d;1H ", r))
		if cols > 1 {
			sb.WriteString(fmt.Sprintf("\x1b[%d;%dH ", r, cols))
		}
	}
}

func appendVerticalSeparator(sb *strings.Builder, topRow, height, col int) {
	if height <= 0 || termSeparator <= 0 {
		return
	}
	for offset := 0; offset < termSeparator; offset++ {
		currentCol := col + offset
		for r := 0; r < height; r++ {
			sb.WriteString(fmt.Sprintf("\x1b[%d;%dH ", topRow+r, currentCol))
		}
	}
}

func appendHorizontalSeparator(sb *strings.Builder, leftCol, width, row int) {
	if width <= 0 || termSeparator <= 0 {
		return
	}
	line := strings.Repeat(" ", width)
	for offset := 0; offset < termSeparator; offset++ {
		currentRow := row + offset
		sb.WriteString(fmt.Sprintf("\x1b[%d;%dH", currentRow, leftCol))
		sb.WriteString(line)
	}
}

func writeTextFrame(sb *strings.Builder, frame *codec.TextFrame, startRow, startCol int) {
	if frame == nil {
		return
	}
	writeTextFrameRegion(sb, frame, startRow, startCol, 0, 0, frame.Cols, frame.Rows, nil)
}

// writeTextFrameRegion draws the visCols×visRows sub-rectangle of frame that
// starts at (offCol, offRow) with its top-left corner at (startRow, startCol).
// With a cache, cells the screen already shows are skipped.
func writeTextFrameRegion(sb *strings.Builder, frame *codec.TextFrame, startRow, startCol, offCol, offRow, visCols, visRows int, cache *screenCache) {
	if frame == nil {
		return
	}
	cols := frame.Cols
	rows := frame.Rows
	total := cols * rows
	if cols <= 0 || rows <= 0 {
		return
	}
	if len(frame.Glyphs) < total || len(frame.FgYCbCr) < total*3 || len(frame.BgYCbCr) < total*3 {
		return
	}
	if offCol < 0 || offRow < 0 || visCols <= 0 || visRows <= 0 {
		return
	}
	visCols = min(visCols, cols-offCol)
	visRows = min(visRows, rows-offRow)

	// Colours set by this call persist across cursor moves; nothing else
	// writes in between, so they are tracked for the whole region, as is
	// the cursor.
	var lastFg, lastBg [3]int
	fgSet, bgSet, wrote := false, false, false
	curRow, curCol := -1, -1 // cursor position, if known
	for vr := 0; vr < visRows; vr++ {
		r := offRow + vr
		row := startRow + vr
		for c := offCol; c < offCol+visCols; c++ {
			idx := r*cols + c
			col := startCol + c - offCol
			if hudVideoMask.has(row, col) {
				continue // a frame's cut corner
			}
			fg := frame.FgYCbCr[idx*3 : idx*3+3]
			bg := frame.BgYCbCr[idx*3 : idx*3+3]
			br, bgCol, bb := ycbcrToRGB(bg[0], bg[1], bg[2])
			br, bgCol, bb = applyColorFilter(br, bgCol, bb)
			cell := screenCell{bg: [3]int{br, bgCol, bb}, set: true}

			glyphIndex := int(frame.Glyphs[idx])
			cell.r = ' '
			if glyphIndex >= 0 && glyphIndex < len(glyphCodepoints) {
				cell.r = rune(glyphCodepoints[glyphIndex])
			}
			blank := isBlankGlyph(cell.r)
			if blank {
				cell.fg = cell.bg // the foreground of an empty glyph is invisible
			} else {
				fr, fgCol, fb := ycbcrToRGB(fg[0], fg[1], fg[2])
				fr, fgCol, fb = applyColorFilter(fr, fgCol, fb)
				cell.fg = [3]int{fr, fgCol, fb}
			}
			if ansiColors() {
				quantizeCell(&cell, blank, r, c)
				blank = isBlankGlyph(cell.r)
			}
			if cache != nil && !cache.update(row, col, cell) {
				continue
			}
			if curRow != row || curCol != col {
				moveCursor(sb, cache, row, col, curRow, curCol, lastFg, lastBg, fgSet && bgSet)
			}
			needFg := !blank && (!fgSet || cell.fg != lastFg)
			needBg := !bgSet || cell.bg != lastBg
			writeCellColors(sb, cell, needFg, needBg)
			if needFg {
				lastFg, fgSet = cell.fg, true
			}
			if needBg {
				lastBg, bgSet = cell.bg, true
			}
			wrote = true
			sb.WriteRune(cell.r)
			curRow, curCol = row, col+1
		}
	}
	if wrote {
		sb.WriteString("\x1b[0m")
	}
}

// isBlankGlyph reports whether a glyph shows only its background.
func isBlankGlyph(r rune) bool {
	return r == ' ' || r == '\u00a0'
}

// moveCursor moves the cursor from (curRow, curCol) to (row, col) with the
// shortest output: across a gap of one or two cells that the screen already
// shows in the current colours it reprints them, within a row it moves
// forward relatively, otherwise it positions absolutely.
func moveCursor(sb *strings.Builder, cache *screenCache, row, col, curRow, curCol int, fg, bg [3]int, colored bool) {
	if curRow == row && col > curCol {
		gap := col - curCol
		if gap <= 2 && colored && cache != nil {
			var cells [2]screenCell
			ok := true
			for i := 0; i < gap && ok; i++ {
				cell, known := cache.cell(row, curCol+i)
				ok = known && cell.bg == bg && (isBlankGlyph(cell.r) || cell.fg == fg)
				cells[i] = cell
			}
			if ok {
				for i := 0; i < gap; i++ {
					sb.WriteRune(cells[i].r)
				}
				return
			}
		}
		var buf [16]byte
		b := append(buf[:0], "\x1b["...)
		if gap > 1 {
			b = strconv.AppendInt(b, int64(gap), 10)
		}
		sb.Write(append(b, 'C'))
		return
	}
	writeCursorTo(sb, row, col)
}

// writeCellColors sets the colours a cell needs, in one escape sequence
// when both change.
func writeCellColors(sb *strings.Builder, cell screenCell, fg, bg bool) {
	if !fg && !bg {
		return
	}
	var buf [48]byte
	b := append(buf[:0], "\x1b["...)
	ansi := ansiColors()
	if fg {
		if ansi {
			b = appendANSIFg(b, cell.fg)
		} else {
			b = appendRGBParams(append(b, "38;2;"...), cell.fg)
		}
	}
	if bg {
		if fg {
			b = append(b, ';')
		}
		if ansi {
			b = appendANSIBg(b, cell.bg)
		} else {
			b = appendRGBParams(append(b, "48;2;"...), cell.bg)
		}
	}
	sb.Write(append(b, 'm'))
}

func appendRGBParams(b []byte, c [3]int) []byte {
	b = strconv.AppendInt(b, int64(c[0]), 10)
	b = append(b, ';')
	b = strconv.AppendInt(b, int64(c[1]), 10)
	b = append(b, ';')
	return strconv.AppendInt(b, int64(c[2]), 10)
}

// writeCursorTo moves the cursor to (row, col) without fmt: the escape codes
// of every changed video cell are built here, many thousands per second.
func writeCursorTo(sb *strings.Builder, row, col int) {
	var buf [24]byte
	b := append(buf[:0], "\x1b["...)
	b = strconv.AppendInt(b, int64(row), 10)
	b = append(b, ';')
	b = strconv.AppendInt(b, int64(col), 10)
	b = append(b, 'H')
	sb.Write(b)
}

func ycbcrToRGB(y, cb, cr byte) (int, int, int) {
	Y := float64(y)
	Cb := float64(cb) - 128.0
	Cr := float64(cr) - 128.0
	r := clampColor(Y + 1.402*Cr)
	g := clampColor(Y - 0.344136*Cb - 0.714136*Cr)
	b := clampColor(Y + 1.772*Cb)
	return r, g, b
}

func applyColorFilter(r, g, b int) (int, int, int) {
	spec := activeColorFilter.Load()
	if spec == nil {
		return r, g, b
	}
	return spec.apply(r, g, b)
}

func (cf *colorFilterSpec) apply(r, g, b int) (int, int, int) {
	if cf == nil {
		return r, g, b
	}
	luminance := 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
	contrast := cf.contrast
	if contrast <= 0 {
		contrast = 1
	}
	adj := clampColor((luminance-128)*contrast + 128)
	mono := float64(adj)
	strength := cf.strength
	if strength <= 0 {
		strength = 0.5
	}
	if strength > 1 {
		strength = 1
	}
	intensity := mono / 255.0
	colorMix := func(target int) int {
		colorComponent := float64(target) * intensity
		baseComponent := mono * (1 - strength)
		return clampColor(baseComponent + colorComponent*strength)
	}
	return colorMix(cf.tint[0]), colorMix(cf.tint[1]), colorMix(cf.tint[2])
}

func clampColor(v float64) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return int(v + 0.5)
}

func rgbToYCbCr(r, g, b byte) [3]byte {
	R := float64(r)
	G := float64(g)
	B := float64(b)
	y := 0.299*R + 0.587*G + 0.114*B
	cb := 128 - 0.168736*R - 0.331264*G + 0.5*B
	cr := 128 + 0.5*R - 0.418688*G - 0.081312*B
	return [3]byte{
		byte(clampColor(y)),
		byte(clampColor(cb)),
		byte(clampColor(cr)),
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func formatFancyNumber(n int) string {
	return strconv.Itoa(n)
}

func buildRateLabel(prefix string, direction rune, audioKBps, videoKBps float64) string {
	label := fmt.Sprintf("A%s V%s", formatRate(audioKBps), formatRate(videoKBps))
	if prefix != "" {
		return fmt.Sprintf("%s%c %s", prefix, direction, label)
	}
	return fmt.Sprintf("%c %s", direction, label)
}

// joinLabels joins the non-empty labels with a space.
func joinLabels(labels ...string) string {
	out := ""
	for _, l := range labels {
		if l == "" {
			continue
		}
		if out != "" {
			out += " "
		}
		out += l
	}
	return out
}

func buildLatencyLabel(latency time.Duration) string {
	if latency <= 0 {
		return ""
	}
	ms := int(math.Round(float64(latency) / float64(time.Millisecond)))
	if ms <= 0 {
		ms = 1
	}
	return fmt.Sprintf("%sms", formatFancyNumber(ms))
}

func formatRate(kBps float64) string {
	if kBps <= 0 {
		return "0Bps"
	}
	roundedKBps := int(math.Round(kBps))
	if roundedKBps > 0 {
		return formatFancyNumber(roundedKBps) + "kBps"
	}
	bytesPerSecond := int(math.Round(kBps * 1024))
	return formatFancyNumber(bytesPerSecond) + "Bps"
}

func composeStatusLabel(frameCols, frameRows int, overlay frameOverlay) string {
	if frameCols <= 0 || frameRows <= 0 {
		return ""
	}
	size := fmt.Sprintf("RES %s×%s", formatFancyNumber(frameCols), formatFancyNumber(frameRows))
	parts := []string{size}
	if overlay.fps != "" {
		parts = append(parts, overlay.fps)
	}
	if overlay.rate != "" {
		parts = append(parts, overlay.rate)
	}
	if overlay.latency != "" {
		parts = append(parts, overlay.latency)
	}
	return strings.Join(parts, " │ ")
}

func writeStatusOverlay(sb *strings.Builder, bottomRow, startCol, frameCols, frameRows int, overlay frameOverlay) {
	if sb == nil || bottomRow <= 0 || startCol <= 0 || frameCols <= 0 || frameRows <= 0 {
		return
	}
	label := composeStatusLabel(frameCols, frameRows, overlay)
	if label == "" {
		return
	}
	label = truncateWidth(label, frameCols)
	pad := max(0, frameCols-displayWidth(label))
	leftPad := pad / 2
	rightPad := pad - leftPad
	row := bottomRow
	col := startCol
	sb.WriteString(fmt.Sprintf("\x1b[%d;%dH", row, col) + panelColors())
	if leftPad > 0 {
		sb.WriteString(strings.Repeat(" ", leftPad))
	}
	sb.WriteString(label)
	if rightPad > 0 {
		sb.WriteString(strings.Repeat(" ", rightPad))
	}
	sb.WriteString("\x1b[0m")
}

func writeCenterLabel(sb *strings.Builder, label string, startRow, startCol, frameCols, frameRows int) {
	label = strings.TrimSpace(label)
	if sb == nil || label == "" || frameCols <= 0 || frameRows <= 0 {
		return
	}
	label = " " + label + " "
	runes := []rune(label)
	maxWidth := frameCols
	if maxWidth < 1 {
		return
	}
	if len(runes) > maxWidth {
		runes = runes[:maxWidth]
	}
	width := len(runes)
	if width == 0 {
		return
	}
	pad := maxWidth - width
	leftPad := pad / 2
	row := startRow + frameRows/2
	if row < startRow {
		row = startRow
	}
	col := startCol + leftPad
	if col < startCol {
		col = startCol
	}
	sb.WriteString(fmt.Sprintf("\x1b[%d;%dH", row, col))
	sb.WriteString(panelColors())
	sb.WriteString(string(runes))
	sb.WriteString("\x1b[0m")
}

type viewportSlots struct {
	selfSlot slotSize // наш слот (верхний или левый)
	peerSlot slotSize // слот собеседника (нижний или правый)
	orient   byte     // 'V' или 'H'
}

// calcViewportSlotSize replicates the layout math from the renderer and returns
// the max drawable area (in character cells) for a single view when two panes
// are shown side by side (horizontal) or stacked (vertical).
func calcViewportSlots(cols, rows int) (vs viewportSlots, ok bool) {
	width := cols - 2*termSide
	height := rows - 2*termBorder
	if width <= 0 || height <= 0 {
		return
	}

	vs.orient = 'H'
	availableCols := width - termGap
	if availableCols < 4 {
		return
	}
	leftCols := availableCols / 2
	rightCols := availableCols - leftCols
	// Panes are physically square (1:1): with cells twice as tall as wide,
	// a pane has twice as many columns as rows. Keep at least three rows for
	// chat history, plus the caption, separators and the input row.
	paneCols := min(min(leftCols, rightCols), codec.MaxFrameCols)
	// What the call screen puts under the panes (renderChatScreen): the
	// caption, the separator, three rows of chat, the input. The window has
	// a row more under the panes' frames, a wider separator and a row under
	// the chat's frame.
	reserved := 7
	if framed() {
		reserved += 1 + (termSeparator - 1) + 1
	}
	paneRows := min(height-reserved, codec.MaxFrameRows)
	if fromCols := int(math.Round(float64(paneCols) * termCellWidthToHeight)); fromCols < paneRows {
		paneRows = fromCols
	} else {
		// The terminal is too short: narrow the panes and center them.
		paneCols = min(paneCols, int(math.Round(float64(paneRows)/termCellWidthToHeight)))
	}
	if paneRows < 1 || paneCols < 1 {
		return
	}
	vs.selfSlot = slotSize{cols: paneCols, rows: paneRows, orient: vs.orient}
	vs.peerSlot = slotSize{cols: paneCols, rows: paneRows, orient: vs.orient}

	if vs.selfSlot.cols <= 0 || vs.selfSlot.rows <= 0 ||
		vs.peerSlot.cols <= 0 || vs.peerSlot.rows <= 0 {
		return
	}
	ok = true
	return
}
