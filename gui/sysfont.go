//go:build darwin || windows

package gui

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"golang.org/x/image/font/gofont/goregular"
)

// Text is drawn in the system's own fonts, read from disk: its monospaced
// font first (a character a cell, as in a terminal), then fonts for the
// scripts it lacks. The fonts for Chinese, Japanese and Korean are large
// (tens of MB), so they are loaded the first time such text shows up. Go's
// own font, built in, is the last resort.

// systemFonts returns the fonts loaded at once and those for CJK text, in
// the order they are asked for a character.
func systemFonts() (main, cjk []string) {
	switch runtime.GOOS {
	case "darwin":
		dir := "/System/Library/Fonts"
		main = []string{"SFNSMono.ttf", "Menlo.ttc", "SFArmenian.ttf", "SFGeorgian.ttf", "SFHebrew.ttf", "SFArabic.ttf", "Apple Symbols.ttf"}
		cjk = []string{"ヒラギノ角ゴシック W3.ttc", "Supplemental/Arial Unicode.ttf"}
		for i := range main {
			main[i] = filepath.Join(dir, main[i])
		}
		for i := range cjk {
			cjk[i] = filepath.Join(dir, cjk[i])
		}
	case "windows":
		dir := os.Getenv("WINDIR")
		if dir == "" {
			dir = `C:\Windows`
		}
		dir = filepath.Join(dir, "Fonts")
		main = []string{"CascadiaMono.ttf", "consola.ttf", "segoeui.ttf", "seguisym.ttf"}
		cjk = []string{"msyh.ttc", "YuGothR.ttc", "meiryo.ttc", "malgun.ttf"}
		for i := range main {
			main[i] = filepath.Join(dir, main[i])
		}
		for i := range cjk {
			cjk[i] = filepath.Join(dir, cjk[i])
		}
	}
	return main, cjk
}

// loadSources reads font files (a collection gives its first font); the
// ones that are missing or unreadable are skipped.
func loadSources(paths []string) []*text.GoTextFaceSource {
	var out []*text.GoTextFaceSource
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.HasSuffix(strings.ToLower(p), ".ttc") {
			if srcs, err := text.NewGoTextFaceSourcesFromCollection(bytes.NewReader(data)); err == nil && len(srcs) > 0 {
				out = append(out, srcs[0])
			}
			continue
		}
		if src, err := text.NewGoTextFaceSource(bytes.NewReader(data)); err == nil {
			out = append(out, src)
		}
	}
	return out
}

// fonts is the text font of the window at one size.
type fonts struct {
	size float64 // device pixels

	mu      sync.Mutex
	sources []*text.GoTextFaceSource
	cjkDone atomic.Bool
	face    atomic.Pointer[text.MultiFace]
	bold    atomic.Pointer[text.MultiFace] // digits: the same fonts, weight 700

	metricsOnce sync.Once
	metrics     text.Metrics
}

// newFonts loads the fonts at the size where a character of the monospaced
// font is cellWidth device pixels wide.
func newFonts(cellWidth float64) *fonts {
	f := &fonts{}
	main, _ := systemFonts()
	f.sources = loadSources(main)
	if src, err := text.NewGoTextFaceSource(bytes.NewReader(goregular.TTF)); err == nil {
		f.sources = append(f.sources, src)
	}
	f.size = 100
	if len(f.sources) > 0 {
		if adv := text.Advance("M", &text.GoTextFace{Source: f.sources[0], Size: 100}); adv > 0 {
			f.size = 100 * cellWidth / adv
		}
	}
	f.rebuild()
	return f
}

func (f *fonts) rebuild() {
	faces := make([]text.Face, 0, len(f.sources))
	bold := make([]text.Face, 0, len(f.sources))
	for _, src := range f.sources {
		faces = append(faces, &text.GoTextFace{Source: src, Size: f.size})
		b := &text.GoTextFace{Source: src, Size: f.size}
		b.SetVariation(text.MustParseTag("wght"), 700) // variable fonts (SF Mono)
		bold = append(bold, b)
	}
	if mf, err := text.NewMultiFace(faces...); err == nil {
		f.face.Store(mf)
	}
	if mf, err := text.NewMultiFace(bold...); err == nil {
		f.bold.Store(mf)
	}
}

// Bold is the face for digits.
func (f *fonts) Bold() text.Face { return f.bold.Load() }

// needsCJK reports whether s has Chinese, Japanese or Korean characters.
func needsCJK(s string) bool {
	for _, r := range s {
		if r >= 0x2e80 && (r <= 0x9fff || (r >= 0xac00 && r <= 0xd7af) || (r >= 0xf900 && r <= 0xfaff) || (r >= 0xff00 && r <= 0xffef) || r >= 0x20000) {
			return true
		}
	}
	return false
}

// Face returns the face to draw s with, loading the CJK fonts first if s
// needs them.
func (f *fonts) Face(s string) text.Face {
	if !f.cjkDone.Load() && needsCJK(s) {
		f.mu.Lock()
		if !f.cjkDone.Load() {
			_, cjk := systemFonts()
			// Before Go's font, which is the last resort.
			n := len(f.sources) - 1
			f.sources = append(f.sources[:n:n], append(loadSources(cjk), f.sources[n])...)
			f.rebuild()
			f.cjkDone.Store(true)
		}
		f.mu.Unlock()
	}
	return f.face.Load()
}

// Metrics of the line (of the main font, measured once).
func (f *fonts) Metrics() text.Metrics {
	f.metricsOnce.Do(func() { f.metrics = f.face.Load().Metrics() })
	return f.metrics
}
