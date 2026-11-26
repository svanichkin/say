//go:build darwin || windows

package gui

import "image"

// Shot draws a screen of Say's output (what the window or a terminal is
// sent), cols x rows cells of cw x ch pixels, into a picture by the
// software renderer (as on Plan 9; the same Unifont as the window): the
// documentation's screenshots (shots_test.go). pixel: as the window draws
// it (the video panes, their frames, the HUD over the cells); else as a
// terminal does (the cells alone).
func Shot(data string, cols, rows, cw, ch int, in Input, pixel bool) (*image.RGBA, error) {
	font, err := newSoftFont(ch, unifontZst, unifontUpperZst)
	if err != nil {
		return nil, err
	}
	s := NewScreen(cols, rows)
	s.Write([]byte(data))
	base := newSoftRenderer(font, cw, ch)
	var px *softPixels
	if pixel {
		px = newSoftPixels(in, font, cw, ch)
	}
	brackets := s.Brackets() // not under View: both take the screen's lock
	s.View(func(cols, rows int, cells []Cell, panes []Pane, _ uint64) {
		d := base.draw(cols, rows, cells)
		if px != nil {
			px.update(base, append([]Pane(nil), panes...), brackets, d)
		}
	})
	w, h, pix := base.w, base.h, base.pix
	if px != nil {
		w, h, pix = px.w, px.h, px.out
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i, o := 0, 0; i+softBytes <= len(pix) && o+4 <= len(img.Pix); i, o = i+softBytes, o+4 {
		img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = pix[i+2], pix[i+1], pix[i], 255
	}
	return img, nil
}
