// Command sayicon draws Say's icon and writes it for macOS and Windows:
//
//	go run ./cmd/sayicon -out assets/icon -gui gui/icon
//
// writes Say.icns (the app bundle's icon), say.ico (the Windows executable's,
// put into it by scripts/windows-icon.sh), icon.png (1024 px, to look at),
// the PNGs the window sets as its own icon on Windows (into -gui) and the
// tray's (tray.png: the bubble alone, black on clear, macOS's template).
//
// The icon is the interface's look: a dark plate, on it a speech bubble in
// the frames' neon orange, its corners cut as the frames' are, glowing, a
// block cursor in it, and along the bottom the hazard stripes of the window's
// border. It is drawn once at 1024 px and scaled down for every size.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/vector"
)

const size = 1024

var (
	orange = color.RGBA{255, 140, 0, 255} // hud.HazardOrange
	plate  = color.RGBA{12, 12, 16, 255}
	dark   = color.RGBA{20, 20, 20, 255} // hud.HazardDark
)

func main() {
	out := flag.String("out", "assets/icon", "where Say.icns, say.ico and icon.png go")
	guiDir := flag.String("gui", "gui/icon", "where the window's icon PNGs go")
	flag.Parse()

	img := drawIcon()
	must(os.MkdirAll(*out, 0o755))
	must(os.MkdirAll(*guiDir, 0o755))
	must(writePNG(filepath.Join(*out, "icon.png"), img))

	icns, err := encodeICNS(img)
	must(err)
	must(os.WriteFile(filepath.Join(*out, "Say.icns"), icns, 0o644))

	ico, err := encodeICO(img, []int{16, 20, 24, 32, 40, 48, 64, 96, 128, 256})
	must(err)
	must(os.WriteFile(filepath.Join(*out, "say.ico"), ico, 0o644))

	for _, s := range []int{16, 32, 48, 256} {
		must(writePNG(filepath.Join(*guiDir, fmt.Sprintf("icon-%d.png", s)), scaled(img, s)))
	}
	// The tray's (macOS: the menu bar's) at 36 px, 18 points at 2x.
	must(writePNG(filepath.Join(*guiDir, "tray.png"), scaled(drawTray(), 36)))
}

// drawIcon draws the icon at size x size.
func drawIcon() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// The plate: a rounded square on Apple's icon grid (824 px of 1024).
	const m, r = 100, 185
	x0, y0, x1, y1 := float32(m), float32(m), float32(size-m), float32(size-m)
	plateMask := fill(roundedRect(x0, y0, x1, y1, r))
	paint(img, plateMask, func(x, y int) color.RGBA {
		// A little lighter at the top.
		t := float64(y-m) / float64(size-2*m)
		k := 1.25 - 0.35*t
		return color.RGBA{clamp(float64(plate.R) * k), clamp(float64(plate.G) * k), clamp(float64(plate.B) * k), 255}
	})

	// The hazard stripes along the bottom of the plate, as the window's
	// border has them, clipped to the plate.
	stripeTop := float32(y1 - 120)
	stripes := fill([][]float32{{x0, stripeTop, x1, stripeTop, x1, y1, x0, y1}})
	for i := range stripes.Pix {
		stripes.Pix[i] = min(stripes.Pix[i], plateMask.Pix[i])
	}
	paint(img, stripes, func(x, y int) color.RGBA {
		if (x+y)/44%2 == 0 {
			return orange
		}
		return dark
	})
	// A thin dark line between the stripes and the plate, within the plate.
	sep := fill([][]float32{{x0, stripeTop - 14, x1, stripeTop - 14, x1, stripeTop, x0, stripeTop}})
	for i := range sep.Pix {
		sep.Pix[i] = min(sep.Pix[i], plateMask.Pix[i])
	}
	paint(img, sep, func(int, int) color.RGBA { return plate })

	// The bubble: corners cut as the frames' are, a tail down on the left.
	bx0, by0, bx1, by1 := float32(210), float32(190), float32(814), float32(590)
	const cut = 70
	outline := []float32{
		bx0 + cut, by0, bx1 - cut, by0, bx1, by0 + cut, bx1, by1 - cut, bx1 - cut, by1,
		bx0 + 250, by1, bx0 + 125, by1 + 105, bx0 + 140, by1, // the tail
		bx0 + cut, by1, bx0, by1 - cut, bx0, by0 + cut,
	}
	const w = 46
	line := fill(strokeLoop(outline, w))

	// The cursor in it, a terminal's block.
	cursor := fill([][]float32{{430, 310, 594, 310, 594, 480, 430, 480}})

	glyphs := image.NewAlpha(img.Bounds())
	for i := range glyphs.Pix {
		glyphs.Pix[i] = max(line.Pix[i], cursor.Pix[i])
	}
	// The glow first, then the lines over it.
	glow := blur(glyphs, 26, 3)
	add(img, glow, orange, 1.3, plateMask)
	paint(img, glyphs, func(int, int) color.RGBA { return color.RGBA{255, 170, 60, 255} })
	return img
}

// drawTray draws the tray's icon at size x size: the bubble and its cursor
// alone, black on clear (macOS colours it as its menu bar: a template),
// its line thicker to read at 18 points, the bubble filling the square.
func drawTray() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	// The icon's bubble, made to fill the square (its tail at the bottom).
	const k, dx, dy = 1.45, -210, -190
	at := func(x, y float32) (float32, float32) { return (x+dx)*k + 74, (y+dy)*k + 140 }
	bx0, by0, bx1, by1 := float32(210), float32(190), float32(814), float32(590)
	const cut = 70
	raw := []float32{
		bx0 + cut, by0, bx1 - cut, by0, bx1, by0 + cut, bx1, by1 - cut, bx1 - cut, by1,
		bx0 + 250, by1, bx0 + 125, by1 + 105, bx0 + 140, by1,
		bx0 + cut, by1, bx0, by1 - cut, bx0, by0 + cut,
	}
	outline := make([]float32, len(raw))
	for i := 0; i < len(raw); i += 2 {
		outline[i], outline[i+1] = at(raw[i], raw[i+1])
	}
	line := fill(strokeLoop(outline, 100))
	cx0, cy0 := at(430, 310)
	cx1, cy1 := at(594, 480)
	cursor := fill([][]float32{{cx0, cy0, cx1, cy0, cx1, cy1, cx0, cy1}})
	for i := range line.Pix {
		img.Pix[4*i+3] = max(line.Pix[i], cursor.Pix[i]) // black, its alpha the shape
	}
	return img
}

// roundedRect is a rounded rectangle's outline (one polygon).
func roundedRect(x0, y0, x1, y1, r float32) [][]float32 {
	var p []float32
	corner := func(cx, cy float32, a0 float64) {
		for i := 0; i <= 12; i++ {
			a := a0 + float64(i)/12*math.Pi/2
			p = append(p, cx+r*float32(math.Cos(a)), cy+r*float32(math.Sin(a)))
		}
	}
	corner(x1-r, y0+r, -math.Pi/2)
	corner(x1-r, y1-r, 0)
	corner(x0+r, y1-r, math.Pi/2)
	corner(x0+r, y0+r, math.Pi)
	return [][]float32{p}
}

// strokeLoop is a closed polyline w wide, as two polygons the even-odd
// fill of which leaves the line: the outline pushed out and in by w/2.
func strokeLoop(pts []float32, w float32) [][]float32 {
	n := len(pts) / 2
	offset := func(d float32) []float32 {
		out := make([]float32, 0, len(pts))
		for i := 0; i < n; i++ {
			px, py := pts[2*((i+n-1)%n)], pts[2*((i+n-1)%n)+1]
			cx, cy := pts[2*i], pts[2*i+1]
			nx, ny := pts[2*((i+1)%n)], pts[2*((i+1)%n)+1]
			n1x, n1y := normal(px, py, cx, cy)
			n2x, n2y := normal(cx, cy, nx, ny)
			mx, my := n1x+n2x, n1y+n2y
			l := float32(math.Hypot(float64(mx), float64(my)))
			if l < 1e-3 {
				mx, my, l = n1x, n1y, 1
			}
			mx, my = mx/l, my/l
			cos := mx*n1x + my*n1y
			k := d / max(cos, 0.3) // a sharp turn is not pushed out forever
			out = append(out, cx+mx*k, cy+my*k)
		}
		return out
	}
	return [][]float32{offset(w / 2), offset(-w / 2)}
}

// normal is the unit normal of the segment a→b, on its left in screen
// coordinates (outward for a clockwise outline).
func normal(ax, ay, bx, by float32) (float32, float32) {
	dx, dy := bx-ax, by-ay
	l := float32(math.Hypot(float64(dx), float64(dy)))
	return dy / l, -dx / l
}

// fill rasterises polygons, even-odd, into an alpha mask of the icon's size.
func fill(polys [][]float32) *image.Alpha {
	ras := vector.NewRasterizer(size, size)
	ras.DrawOp = xdraw.Src
	for _, p := range polys {
		ras.MoveTo(p[0], p[1])
		for i := 2; i+1 < len(p); i += 2 {
			ras.LineTo(p[i], p[i+1])
		}
		ras.ClosePath()
	}
	// vector fills nonzero; an even-odd fill of the stroke's two rings is
	// the nonzero fill of one ring reversed: draw the inner ring backwards.
	mask := image.NewAlpha(image.Rect(0, 0, size, size))
	if len(polys) == 2 {
		ras = vector.NewRasterizer(size, size)
		outer, inner := polys[0], polys[1]
		ras.MoveTo(outer[0], outer[1])
		for i := 2; i+1 < len(outer); i += 2 {
			ras.LineTo(outer[i], outer[i+1])
		}
		ras.ClosePath()
		n := len(inner) / 2
		ras.MoveTo(inner[2*(n-1)], inner[2*(n-1)+1])
		for i := n - 2; i >= 0; i-- {
			ras.LineTo(inner[2*i], inner[2*i+1])
		}
		ras.ClosePath()
	}
	ras.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	return mask
}

// paint lays colour c(x, y) over img through mask.
func paint(img *image.RGBA, mask *image.Alpha, c func(x, y int) color.RGBA) {
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			a := float64(mask.Pix[y*mask.Stride+x]) / 255
			if a == 0 {
				continue
			}
			col := c(x, y)
			o := y*img.Stride + 4*x
			for k, v := range []uint8{col.R, col.G, col.B} {
				img.Pix[o+k] = uint8(float64(img.Pix[o+k])*(1-a) + float64(v)*a)
			}
			img.Pix[o+3] = uint8(math.Max(float64(img.Pix[o+3]), a*255))
		}
	}
}

// add adds colour c times glow (0..1) times strength, within clip.
func add(img *image.RGBA, glow []float64, c color.RGBA, strength float64, clip *image.Alpha) {
	for i, g := range glow {
		if clip.Pix[i] == 0 {
			continue
		}
		o := 4 * i
		f := g * strength * float64(clip.Pix[i]) / 255
		img.Pix[o] = clamp(float64(img.Pix[o]) + float64(c.R)*f)
		img.Pix[o+1] = clamp(float64(img.Pix[o+1]) + float64(c.G)*f)
		img.Pix[o+2] = clamp(float64(img.Pix[o+2]) + float64(c.B)*f)
	}
}

// blur is mask blurred by passes box blurs of radius r (about a Gaussian).
func blur(mask *image.Alpha, r, passes int) []float64 {
	a := make([]float64, size*size)
	for i, v := range mask.Pix {
		a[i] = float64(v) / 255
	}
	tmp := make([]float64, len(a))
	for p := 0; p < passes; p++ {
		boxH(a, tmp, r)
		boxV(tmp, a, r)
	}
	return a
}

func boxH(src, dst []float64, r int) {
	for y := 0; y < size; y++ {
		row := src[y*size : (y+1)*size]
		var sum float64
		for x := -r; x <= r; x++ {
			sum += row[min(max(x, 0), size-1)]
		}
		for x := 0; x < size; x++ {
			dst[y*size+x] = sum / float64(2*r+1)
			sum += row[min(x+r+1, size-1)] - row[max(x-r, 0)]
		}
	}
}

func boxV(src, dst []float64, r int) {
	for x := 0; x < size; x++ {
		var sum float64
		for y := -r; y <= r; y++ {
			sum += src[min(max(y, 0), size-1)*size+x]
		}
		for y := 0; y < size; y++ {
			dst[y*size+x] = sum / float64(2*r+1)
			sum += src[min(y+r+1, size-1)*size+x] - src[max(y-r, 0)*size+x]
		}
	}
}

func clamp(v float64) uint8 { return uint8(math.Max(0, math.Min(255, math.Round(v)))) }

// scaled is img at s x s, filtered so that small sizes stay clean.
func scaled(img image.Image, s int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, s, s))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), xdraw.Src, nil)
	return dst
}

func pngBytes(img image.Image) ([]byte, error) {
	var b bytes.Buffer
	err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&b, img)
	return b.Bytes(), err
}

func writePNG(path string, img image.Image) error {
	data, err := pngBytes(img)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// encodeICNS writes the macOS icon file: PNG entries for every size the
// system looks for (1x and 2x), as iconutil makes them.
func encodeICNS(img image.Image) ([]byte, error) {
	entries := []struct {
		kind string
		size int
	}{
		{"icp4", 16}, {"icp5", 32}, {"icp6", 64}, {"ic07", 128}, {"ic08", 256}, {"ic09", 512}, {"ic10", 1024},
		{"ic11", 32}, {"ic12", 64}, {"ic13", 256}, {"ic14", 512},
	}
	var body bytes.Buffer
	for _, e := range entries {
		data, err := pngBytes(scaled(img, e.size))
		if err != nil {
			return nil, err
		}
		body.WriteString(e.kind)
		binary.Write(&body, binary.BigEndian, uint32(8+len(data)))
		body.Write(data)
	}
	var out bytes.Buffer
	out.WriteString("icns")
	binary.Write(&out, binary.BigEndian, uint32(8+body.Len()))
	out.Write(body.Bytes())
	return out.Bytes(), nil
}

// encodeICO writes the Windows icon file with a PNG image for each size
// (Windows reads PNG entries since Vista).
func encodeICO(img image.Image, sizes []int) ([]byte, error) {
	var images [][]byte
	for _, s := range sizes {
		data, err := pngBytes(scaled(img, s))
		if err != nil {
			return nil, err
		}
		images = append(images, data)
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := uint8(s)
		if s >= 256 {
			dim = 0 // 0 means 256
		}
		out.Write([]byte{dim, dim, 0, 0})
		binary.Write(&out, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(images[i])), uint32(offset)})
		offset += len(images[i])
	}
	for _, data := range images {
		out.Write(data)
	}
	return out.Bytes(), nil
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
