// Command videostats measures what motion and copying could save on
// recorded frames (cmd/recordframes): over each frame's cells (as the
// encoder draws them) against the frame before, the cells that changed,
// those a global shift of whole cells would bring back, those a 4x4 group
// copied from within +-4 cells of the frame before would, and those found
// elsewhere in the same or the previous frame (not only left or above).
//
//	go run ./cmd/videostats -in call.frames
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"

	"github.com/svanichkin/say/codec"
)

type cell [7]byte // glyph, fg Y Cb Cr, bg Y Cb Cr

func main() {
	in := flag.String("in", "", "recorded frames")
	cols := flag.Int("cols", 80, "cells across")
	rows := flag.Int("rows", 40, "cells down")
	reach := flag.Int("reach", 4, "the largest shift in cells")
	flag.Parse()
	frames, err := load(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	enc := codec.NewEncoder(false, 0, 0, false)
	enc.SetShapeHold(10)
	W, H, R := *cols, *rows, *reach
	var prev []cell
	var changed, byShift, byBlock, byDict, byLeftUp, total int
	shiftHist := map[[2]int]int{}
	for _, fr := range frames {
		tv, err := enc.SelfView(fr, W, H)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		cur := make([]cell, W*H)
		for i := range cur {
			c := &cur[i]
			c[0] = tv.Glyphs[i]
			copy(c[1:4], tv.FgYCbCr[3*i:3*i+3])
			copy(c[4:7], tv.BgYCbCr[3*i:3*i+3])
		}
		if prev != nil {
			at := func(f []cell, x, y int) (cell, bool) {
				if x < 0 || y < 0 || x >= W || y >= H {
					return cell{}, false
				}
				return f[y*W+x], true
			}
			ch := make([]bool, W*H)
			n := 0
			for i := range cur {
				if cur[i] != prev[i] {
					ch[i] = true
					n++
				}
			}
			total += W * H
			changed += n
			// Global shift: the one that brings back most changed cells.
			best, bd := 0, [2]int{}
			for dy := -R; dy <= R; dy++ {
				for dx := -R; dx <= R; dx++ {
					if dx == 0 && dy == 0 {
						continue
					}
					k := 0
					for y := range H {
						for x := range W {
							if !ch[y*W+x] {
								continue
							}
							if p, ok := at(prev, x-dx, y-dy); ok && p == cur[y*W+x] {
								k++
							}
						}
					}
					if k > best {
						best, bd = k, [2]int{dx, dy}
					}
				}
			}
			byShift += best
			if best > 0 {
				shiftHist[bd]++
			}
			// 4x4 groups: a changed group found whole in the frame before
			// within +-R cells.
			for gy := 0; gy+4 <= H; gy += 4 {
				for gx := 0; gx+4 <= W; gx += 4 {
					k := 0
					for y := gy; y < gy+4; y++ {
						for x := gx; x < gx+4; x++ {
							if ch[y*W+x] {
								k++
							}
						}
					}
					if k == 0 {
						continue
					}
				search:
					for dy := -R; dy <= R; dy++ {
						for dx := -R; dx <= R; dx++ {
							if dx == 0 && dy == 0 {
								continue
							}
							for y := gy; y < gy+4; y++ {
								for x := gx; x < gx+4; x++ {
									if p, ok := at(prev, x-dx, y-dy); !ok || p != cur[y*W+x] {
										continue search
									}
								}
							}
							byBlock += k
							break search
						}
					}
				}
			}
			// Dictionary: a changed cell equal to one earlier in this frame
			// or anywhere in the frame before; and how many are just as the
			// cell left or above (what the coder already uses).
			seen := map[cell]bool{}
			for _, c := range prev {
				seen[c] = true
			}
			for y := range H {
				for x := range W {
					i := y*W + x
					c := cur[i]
					if ch[i] {
						l, okL := at(cur, x-1, y)
						u, okU := at(cur, x, y-1)
						if okL && l == c || okU && u == c {
							byLeftUp++
						} else if seen[c] {
							byDict++
						}
					}
					seen[c] = true
				}
			}
		}
		prev = cur
	}
	pc := func(n int) float64 { return 100 * float64(n) / float64(max(1, changed)) }
	fmt.Printf("%d frames, %dx%d cells: %.1f%% of cells change a frame\n", len(frames), W, H, 100*float64(changed)/float64(max(1, total)))
	fmt.Printf("  of the changed cells: global shift brings back %.1f%%, 4x4 copies within %d cells %.1f%%\n", pc(byShift), R, pc(byBlock))
	fmt.Printf("  as the cell left or above (coded already) %.1f%%, else found elsewhere in this or the last frame %.1f%%\n", pc(byLeftUp), pc(byDict))
	fmt.Printf("  shifts chosen: %v\n", shiftHist)
}

func load(path string) ([]codec.Frame, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var frames []codec.Frame
	for len(data) >= 4 {
		w, h := int(binary.LittleEndian.Uint16(data)), int(binary.LittleEndian.Uint16(data[2:]))
		n := w * h * 3
		if len(data) < 4+n {
			break
		}
		frames = append(frames, codec.Frame{Data: data[4 : 4+n], Width: w, Height: h})
		data = data[4+n:]
	}
	return frames, nil
}
