//go:build darwin || windows

package gui

import (
	"image"
	"sync"
	_ "unsafe" // go:linkname

	"github.com/hajimehoshi/ebiten/v2"
)

// Ebiten draws antialiased triangles and those with a fill rule (the
// frames, the bricks, the corners cut) through three images of its own,
// twice the destination's size, kept in package variables: made on its
// atlases, which grow by doubling to hold them and are never freed, they
// kept some 400 MB of GPU memory for good at a 2556x1420 window. Say puts
// its own there instead, each on a texture of its own (newLayer), and lets
// them go when the window is hidden. Ebiten makes them again, larger, only
// when a draw needs more than they hold; a new version of Ebiten that
// renames them fails to link, not to draw.

//go:linkname ebitenStencilM github.com/hajimehoshi/ebiten/v2.stencilBufferM
var ebitenStencilM sync.Mutex

//go:linkname ebitenStencil github.com/hajimehoshi/ebiten/v2.stencilBufferImage
var ebitenStencil *ebiten.Image

//go:linkname ebitenOffscreen1 github.com/hajimehoshi/ebiten/v2.offscreenImage1
var ebitenOffscreen1 *ebiten.Image

//go:linkname ebitenOffscreen2 github.com/hajimehoshi/ebiten/v2.offscreenImage2
var ebitenOffscreen2 *ebiten.Image

// stencilFor makes Ebiten's three images ours, w x h (twice the window's
// size), unless they already hold that.
func stencilFor(w, h int) {
	ebitenStencilM.Lock()
	defer ebitenStencilM.Unlock()
	for _, im := range []**ebiten.Image{&ebitenStencil, &ebitenOffscreen1, &ebitenOffscreen2} {
		if *im != nil && image.Rect(0, 0, w, h).In((*im).Bounds()) {
			continue
		}
		if *im != nil {
			(*im).Deallocate()
		}
		*im = newLayer(w, h)
	}
}

// stencilRelease frees them (the window hidden).
func stencilRelease() {
	ebitenStencilM.Lock()
	defer ebitenStencilM.Unlock()
	for _, im := range []**ebiten.Image{&ebitenStencil, &ebitenOffscreen1, &ebitenOffscreen2} {
		if *im != nil {
			(*im).Deallocate()
			*im = nil
		}
	}
}
