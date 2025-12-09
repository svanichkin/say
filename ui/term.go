package ui

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"github.com/svanichkin/say/codec"
	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/mediactrl"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Terminal cell aspect: one character cell is ~0.38 width of its height.
// We use it to compare "visual" width vs height.
const termCellWidthToHeight = 0.38
const termBorder = 1
const termSeparator = 1
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

const (
	statusCopyAckMessage    = "Copied to clipboard"
	statusCopyFeedbackDelay = 2 * time.Second
)

var statusClickState struct {
	sync.Mutex
	active      bool
	baseMessage string
	ackMessage  string
	clipboard   string
	revertAfter time.Duration
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
	return fmt.Sprintf("%s FPS", counter.display)
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

// SetCopyableStatus shows the provided message and makes it clickable so the text is copied via OSC-52.
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
	statusClickState.ackMessage = statusCopyAckMessage
	statusClickState.clipboard = trimmedClip
	statusClickState.revertAfter = statusCopyFeedbackDelay
	statusClickState.areas = nil
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
	statusClickState.ackMessage = ""
	statusClickState.clipboard = ""
	statusClickState.areas = nil
}

func statusClickMessageMatches(msg string) bool {
	trimmed := strings.TrimSpace(msg)
	statusClickState.Lock()
	defer statusClickState.Unlock()
	if !statusClickState.active || trimmed == "" {
		return false
	}
	return trimmed == statusClickState.baseMessage || trimmed == statusClickState.ackMessage
}

func statusClickUpdateAreas(msg string, rects []statusClickRect) {
	trimmed := strings.TrimSpace(msg)
	statusClickState.Lock()
	defer statusClickState.Unlock()
	if !statusClickState.active {
		statusClickState.areas = nil
		return
	}
	if trimmed != statusClickState.baseMessage && trimmed != statusClickState.ackMessage {
		statusClickState.areas = nil
		return
	}
	statusClickState.areas = statusClickState.areas[:0]
	statusClickState.areas = append(statusClickState.areas, rects...)
}

func statusClickClearAreas() {
	statusClickState.Lock()
	statusClickState.areas = nil
	statusClickState.Unlock()
}

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
	ack := statusClickState.ackMessage
	base := statusClickState.baseMessage
	delay := statusClickState.revertAfter
	if delay <= 0 {
		delay = statusCopyFeedbackDelay
	}
	if statusClickState.timer != nil {
		statusClickState.timer.Stop()
	}
	statusClickState.timer = time.AfterFunc(delay, func() {
		setStatusMessageInternal(base, true)
		statusClickState.Lock()
		statusClickState.timer = nil
		statusClickState.Unlock()
	})
	statusClickState.Unlock()

	if clipboard != "" {
		device.CopyToClipboard(clipboard)
	}
	setStatusMessageInternal(ack, true)
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
		default:
		}
		select {
		case ch <- tf:
		default:
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
		startMouseTracker(ctx)
		if _, err := startTermImgRenderer(ctx); err != nil {
			logs.LogV("[term] ensure renderer failed: %v", err)
		}
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
				initial = ViewportSize{Cols: slots.peerSlot.cols, Rows: slots.peerSlot.rows}
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
		logs.LogV("[term] self window %dx%d, %s slot %dx%d cells (~%dx%d px)",
			cols, rows, layout,
			selfViewport.selfSlot.cols, selfViewport.selfSlot.rows,
			selfViewport.selfSlot.cols*4, selfViewport.selfSlot.rows*8)
		return selfViewport.peerSlot.cols, selfViewport.peerSlot.rows, true
	}
	logs.LogV("[term] self window %dx%d", cols, rows)
	return 0, 0, false
}

// SelfSlot returns the current viewport slot dimensions for the local terminal.
func SelfSlot() (cols, rows int, ok bool) {
	selfSlotMu.RLock()
	defer selfSlotMu.RUnlock()
	if selfViewport.selfSlot.cols <= 0 || selfViewport.selfSlot.rows <= 0 {
		return 0, 0, false
	}
	return selfViewport.selfSlot.cols, selfViewport.selfSlot.rows, true
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
		tick := time.NewTicker(50 * time.Millisecond) // periodic poll to react quickly to terminal resizes
		defer tick.Stop()

		// Initial draw so the UI is not blank until the first event arrives.
		renderTermFrames()

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
				renderTermFrames()

			case <-tick.C:
				cols, rows, err := device.GetTermSize()
				if err != nil {
					continue
				}
				if cols != lastCols || rows != lastRows {
					lastCols, lastRows = cols, rows
					renderTermFrames()
				}
			}
		}
	}()

	return cancel, done
}

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

	width := cols - 2*termBorder
	height := rows - 2*termBorder
	if width <= 4 || height <= 2 {
		return
	}

	local, _, localUpdated := localFrameSnapshot()
	remote, _, remoteUpdated := remoteFrameSnapshot()
	status := currentStatusMessage()
	if local == nil && remote == nil {
		updateNoiseAnimation(false)
		if status == "" {
			return
		}
		data := buildStatusScreen(cols, rows, status)
		if data == "" {
			return
		}
		lastDrawMu.Lock()
		lastDraw.w, lastDraw.h, lastDraw.orient = 0, 0, 0
		lastDrawMu.Unlock()
		enqueueFrame(&device.TerminalFrame{Data: data})
		return
	}
	statusClickClearAreas()

	var orient byte
	if local != nil && remote != nil {
		if float64(height) > float64(width)*termCellWidthToHeight {
			orient = 'V'
		} else {
			orient = 'H'
		}
	}

	localSlot := slotSize{cols: width, rows: height}
	remoteSlot := slotSize{cols: width, rows: height}
	if local != nil && remote != nil {
		if vs, ok := calcViewportSlots(cols, rows); ok {
			localSlot = vs.selfSlot
			remoteSlot = vs.peerSlot
		}
	}

	fullClear := false
	lastDrawMu.Lock()
	if lastDraw.orient != orient {
		fullClear = true
	}
	lastDraw.w, lastDraw.h, lastDraw.orient = width, height, orient
	lastDrawMu.Unlock()

	now := time.Now()
	localFrozen := local != nil && !localUpdated.IsZero() && now.Sub(localUpdated) > frameFreezeThreshold
	remoteFrozen := remote != nil && !remoteUpdated.IsZero() && now.Sub(remoteUpdated) > frameFreezeThreshold

	localPatternActive := false
	if localFrozen && local != nil {
		localPatternActive = updateIdlePatternSlot(localFrameType, true, now)
	} else {
		updateIdlePatternSlot(localFrameType, false, now)
	}
	remotePatternActive := false
	if remoteFrozen && remote != nil {
		remotePatternActive = updateIdlePatternSlot(remoteFrameType, true, now)
	} else {
		updateIdlePatternSlot(remoteFrameType, false, now)
	}

	localLabel := currentFPSLabel(localFrameType)
	remoteLabel := currentFPSLabel(remoteFrameType)
	txAudio, txVideo, rxAudio, rxVideo := NetRateSnapshot()
	latency := PeerLatencySnapshot()
	latencyLabel := buildLatencyLabel(latency)
	localRate := buildRateLabel("TX", '↑', txAudio, txVideo)
	remoteRate := buildRateLabel("RX", '↓', rxAudio, rxVideo)
	localOverlay := frameOverlay{fps: localLabel, rate: localRate}
	remoteOverlay := frameOverlay{fps: remoteLabel, rate: remoteRate, latency: latencyLabel}
	if !mediactrl.CaptureEnabled() {
		localOverlay.fps = "MUTED"
		localOverlay.rate = ""
	}
	if !mediactrl.PlaybackEnabled() {
		remoteOverlay.fps = "PAUSED"
		remoteOverlay.rate = ""
		remoteOverlay.latency = ""
	}

	updateNoiseAnimation(localFrozen || remoteFrozen)

	renderLocal := local
	renderRemote := remote

	if localFrozen && local != nil {
		if localOverlay.fps == localLabel {
			localOverlay.fps = "NO SIGNAL"
			localOverlay.rate = ""
		}
		noiseCols := localSlot.cols
		noiseRows := localSlot.rows
		if noiseCols <= 0 || noiseRows <= 0 {
			noiseCols = local.Cols
			noiseRows = local.Rows
		}
		if localPatternActive {
			if gf := testCardTextFrame(noiseCols, noiseRows); gf != nil {
				renderLocal = gf
				localOverlay.centerLabel = "NO SIGNAL"
			}
		} else if nf := noiseTextFrame(noiseCols, noiseRows); nf != nil {
			renderLocal = nf
			localOverlay.centerLabel = "NO SIGNAL"
		}
	}
	if remoteFrozen && remote != nil {
		if remoteOverlay.fps == remoteLabel {
			remoteOverlay.fps = "NO SIGNAL"
			remoteOverlay.rate = ""
		}
		noiseCols := remoteSlot.cols
		noiseRows := remoteSlot.rows
		if noiseCols <= 0 || noiseRows <= 0 {
			noiseCols = remote.Cols
			noiseRows = remote.Rows
		}
		if remotePatternActive {
			if gf := testCardTextFrame(noiseCols, noiseRows); gf != nil {
				renderRemote = gf
				remoteOverlay.centerLabel = "NO SIGNAL"
			}
		} else if nf := noiseTextFrame(noiseCols, noiseRows); nf != nil {
			renderRemote = nf
			remoteOverlay.centerLabel = "NO SIGNAL"
		}
	}

	var data string
	switch {
	case renderLocal != nil && renderRemote != nil:
		if orient == 'V' {
			data = buildVerticalFrame(cols, rows, renderLocal, renderRemote, localOverlay, remoteOverlay, fullClear)
		} else {
			data = buildHorizontalFrame(cols, rows, renderLocal, renderRemote, localOverlay, remoteOverlay, fullClear)
		}
	default:
		frame := renderLocal
		overlay := localOverlay
		if frame == nil {
			frame = renderRemote
			overlay = remoteOverlay
		}
		data = buildSingleFrame(cols, rows, frame, overlay, fullClear)
	}

	if data == "" {
		return
	}
	enqueueFrame(&device.TerminalFrame{Data: data})
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

	drawVerticalLine := func(x int, color [3]byte) {
		if x < 0 || x >= cols {
			return
		}
		for r := 0; r < rows; r++ {
			idx := r*cols + x
			glyphs[idx] = patternGlyphVertical
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
	drawVerticalLine(cols/2, testCardSeparator)

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

	lines := strings.Split(msg, "\n")
	for i := range lines {
		lines[i] = truncateRunes(strings.TrimSpace(lines[i]), canvasCols)
	}

	startRow := termBorder + 1 + max(0, (canvasRows-len(lines))/2)
	if startRow < 1 {
		startRow = 1
	}

	var out strings.Builder
	writeFramePrefix(&out, true)
	appendBorderClear(&out, cols, rows)

	trackClicks := statusClickMessageMatches(msg)
	var clickRects []statusClickRect

	for i, line := range lines {
		if line == "" {
			continue
		}
		row := startRow + i
		if row > rows-termBorder {
			break
		}
		width := runeCount(line)
		if width > canvasCols {
			line = truncateRunes(line, canvasCols)
			width = runeCount(line)
		}
		startCol := termBorder + 1 + max(0, (canvasCols-width)/2)
		out.WriteString(fmt.Sprintf("\x1b[%d;%dH%s", row, startCol, line))
		if trackClicks {
			clickRects = append(clickRects, statusClickRect{
				row:      row,
				startCol: startCol,
				endCol:   startCol + width - 1,
			})
		}
	}
	out.WriteString("\x1b[0m")

	if trackClicks {
		statusClickUpdateAreas(msg, clickRects)
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
	cols := frame.Cols
	rows := frame.Rows
	total := cols * rows
	if cols <= 0 || rows <= 0 {
		return
	}
	if len(frame.Glyphs) < total || len(frame.FgYCbCr) < total*3 || len(frame.BgYCbCr) < total*3 {
		return
	}

	for r := 0; r < rows; r++ {
		sb.WriteString(fmt.Sprintf("\x1b[%d;%dH", startRow+r, startCol))
		var lastFg, lastBg [3]int
		first := true
		for c := 0; c < cols; c++ {
			idx := r*cols + c
			fg := frame.FgYCbCr[idx*3 : idx*3+3]
			bg := frame.BgYCbCr[idx*3 : idx*3+3]
			fr, fgCol, fb := ycbcrToRGB(fg[0], fg[1], fg[2])
			fr, fgCol, fb = applyColorFilter(fr, fgCol, fb)
			br, bgCol, bb := ycbcrToRGB(bg[0], bg[1], bg[2])
			br, bgCol, bb = applyColorFilter(br, bgCol, bb)

			if first || fr != lastFg[0] || fgCol != lastFg[1] || fb != lastFg[2] {
				sb.WriteString(fmt.Sprintf("\x1b[38;2;%d;%d;%dm", fr, fgCol, fb))
				lastFg = [3]int{fr, fgCol, fb}
			}
			if first || br != lastBg[0] || bgCol != lastBg[1] || bb != lastBg[2] {
				sb.WriteString(fmt.Sprintf("\x1b[48;2;%d;%d;%dm", br, bgCol, bb))
				lastBg = [3]int{br, bgCol, bb}
			}

			first = false

			glyphIndex := int(frame.Glyphs[idx])
			rn := ' '
			if glyphIndex >= 0 && glyphIndex < len(glyphCodepoints) {
				rn = rune(glyphCodepoints[glyphIndex])
			}
			sb.WriteRune(rn)
		}
		sb.WriteString("\x1b[0m")
	}
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

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit == 1 {
		return string(runes[:1])
	}
	return string(runes[:limit-1]) + "…"
}

func runeCount(s string) int {
	return len([]rune(s))
}

func formatFancyNumber(n int) string {
	return strconv.Itoa(n)
}

func buildRateLabel(prefix string, direction rune, audioKBps, videoKBps float64) string {
	arrow := string(direction)
	aVal := formatRateValue(audioKBps)
	vVal := formatRateValue(videoKBps)
	label := fmt.Sprintf("A%s%skB/s V%s%skB/s", arrow, aVal, arrow, vVal)
	if prefix != "" {
		return fmt.Sprintf("%s %s", prefix, label)
	}
	return label
}

func buildLatencyLabel(latency time.Duration) string {
	if latency <= 0 {
		return ""
	}
	ms := int(math.Round(float64(latency) / float64(time.Millisecond)))
	if ms <= 0 {
		ms = 1
	}
	return fmt.Sprintf("PING %sms", formatFancyNumber(ms))
}

func formatRateValue(v float64) string {
	if v <= 0 {
		return "0"
	}
	return formatFancyNumber(int(math.Round(v)))
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
	runes := []rune(label)
	if len(runes) > frameCols {
		runes = runes[:frameCols]
	}
	width := len(runes)
	pad := frameCols - width
	leftPad := pad / 2
	rightPad := pad - leftPad
	row := bottomRow
	col := startCol
	sb.WriteString(fmt.Sprintf("\x1b[%d;%dH\x1b[48;2;20;20;24m\x1b[38;2;245;245;245m", row, col))
	if leftPad > 0 {
		sb.WriteString(strings.Repeat(" ", leftPad))
	}
	sb.WriteString(string(runes))
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
	sb.WriteString("\x1b[48;2;20;20;24m\x1b[38;2;245;245;245m")
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
	border := termBorder
	separator := termSeparator

	width := cols - 2*border
	height := rows - 2*border
	if width <= 0 || height <= 0 {
		return
	}

	// Выбираем ориентацию так же, как в рендерере.
	if float64(height) > float64(width)*termCellWidthToHeight {
		vs.orient = 'V'
	} else {
		vs.orient = 'H'
	}

	switch vs.orient {
	case 'V':
		// Два слота друг над другом.
		availableRows := height - separator
		if availableRows < 2 {
			return
		}
		topRows := availableRows / 2
		bottomRows := availableRows - topRows

		vs.selfSlot = slotSize{
			cols:   width,
			rows:   topRows,
			orient: vs.orient,
		}
		vs.peerSlot = slotSize{
			cols:   width,
			rows:   bottomRows,
			orient: vs.orient,
		}

	default: // 'H'
		// Два слота рядом.
		availableCols := width - separator
		if availableCols < 4 {
			return
		}
		leftCols := availableCols / 2
		rightCols := availableCols - leftCols

		vs.selfSlot = slotSize{
			cols:   leftCols,
			rows:   height,
			orient: vs.orient,
		}
		vs.peerSlot = slotSize{
			cols:   rightCols,
			rows:   height,
			orient: vs.orient,
		}
	}

	if vs.selfSlot.cols <= 0 || vs.selfSlot.rows <= 0 ||
		vs.peerSlot.cols <= 0 || vs.peerSlot.rows <= 0 {
		return
	}
	ok = true
	return
}
