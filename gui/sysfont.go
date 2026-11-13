//go:build darwin || windows

package gui

import (
	"bytes"
	_ "embed"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/image/font/gofont/goregular"
)

// Text is drawn in GNU Unifont, built in: a pixel font (8x16, twice as wide
// for Chinese, Japanese and Korean) with a glyph for almost every character,
// so every language looks the same. A character it lacks comes from the
// system's monospaced font, then Go's own font, the last resort. The cells
// are a whole number of Unifont pixels (see setMetrics), so the glyphs stay
// sharp.

//go:embed fonts/unifont.otf.zst
var unifontZst []byte

//go:embed fonts/unifont_upper.otf.zst
var unifontUpperZst []byte // beyond the BMP: emoji, old scripts

// unifontPixels is Unifont's cell: a character is 8x16 of its pixels.
const unifontPixels = 8

// unifontSources reads the built-in Unifont (both files), once.
var unifontSources = sync.OnceValue(readUnifont)

func readUnifont() []*text.GoTextFaceSource {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil
	}
	defer dec.Close()
	var out []*text.GoTextFaceSource
	for _, z := range [][]byte{unifontZst, unifontUpperZst} {
		data, err := dec.DecodeAll(z, nil)
		if err != nil {
			continue
		}
		if src, err := text.NewGoTextFaceSource(bytes.NewReader(data)); err == nil {
			out = append(out, src)
		}
	}
	return out
}

// systemFonts returns the system's fonts, for characters Unifont lacks.
func systemFonts() []string {
	var dir string
	var names []string
	switch runtime.GOOS {
	case "darwin":
		dir = "/System/Library/Fonts"
		names = []string{"SFNSMono.ttf", "Menlo.ttc", "Apple Symbols.ttf"}
	case "windows":
		dir = os.Getenv("WINDIR")
		if dir == "" {
			dir = `C:\Windows`
		}
		dir = filepath.Join(dir, "Fonts")
		names = []string{"CascadiaMono.ttf", "consola.ttf", "seguisym.ttf"}
	}
	for i := range names {
		names[i] = filepath.Join(dir, names[i])
	}
	return names
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
	size  float64 // device pixels
	pixel float64 // a Unifont pixel, in device pixels
	face  text.Face

	metricsOnce sync.Once
	metrics     text.Metrics
}

// newFonts loads the fonts at the size where a character is cellWidth device
// pixels wide.
func newFonts(cellWidth float64) *fonts {
	sources := append(slices.Clone(unifontSources()), loadSources(systemFonts())...)
	if src, err := text.NewGoTextFaceSource(bytes.NewReader(goregular.TTF)); err == nil {
		sources = append(sources, src)
	}
	f := &fonts{pixel: cellWidth / unifontPixels, size: 2 * cellWidth}
	if len(sources) > 0 {
		if adv := text.Advance("M", &text.GoTextFace{Source: sources[0], Size: 100}); adv > 0 {
			f.size = 100 * cellWidth / adv
		}
	}
	faces := make([]text.Face, 0, len(sources))
	for _, src := range sources {
		faces = append(faces, &text.GoTextFace{Source: src, Size: f.size})
	}
	if mf, err := text.NewMultiFace(faces...); err == nil {
		f.face = mf
	}
	return f
}

// Face is the face to draw text with.
func (f *fonts) Face() text.Face { return f.face }

// BoldShift is how far to the right a character is drawn a second time to
// look bold (Unifont has no bold): one of its pixels.
func (f *fonts) BoldShift() float64 { return f.pixel }

// Metrics of the line (of the main font, measured once).
func (f *fonts) Metrics() text.Metrics {
	f.metricsOnce.Do(func() { f.metrics = f.face.Metrics() })
	return f.metrics
}
