package hud

import (
	"fmt"
	"reflect"
	"testing"
)

func TestFrameGeneratorStableAndVaried(t *testing.T) {
	shapes := map[string]bool{}
	for seed := uint64(0); seed < 100; seed++ {
		a, b := Generate(seed, false), Generate(seed, false)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("seed %d changes between draws", seed)
		}
		shapes[fmt.Sprint(a.cuts, a.steps)] = true
	}
	if len(shapes) < 95 {
		t.Fatalf("only %d silhouettes for 100 seeds", len(shapes))
	}
	// The high bit of a launch seed must not be discarded by the cache key.
	for _, seed := range []uint64{5, 5 | 1<<63} {
		if !reflect.DeepEqual(StyleOf(seed, false), Generate(seed, false)) {
			t.Fatalf("style cache aliases seed %x", seed)
		}
	}
}

// A divider goes across the inside from the inner line's left side to its
// right one, a cut corner kept out; a row past the inside has none.
func TestDivider(t *testing.T) {
	gr := Grid{CW: 10, CH: 20}
	p := Rect{Row: 1, Col: 2, Cols: 10, Rows: 5}
	inner := chamfered(20, 20, 120, 120, [4]float64{0, 0, 30, 30})
	a, b, ok := Divider(gr, p, 1, inner) // y 40, the top of row 1: clear of the cuts
	if !ok || a != (Pt{20, 40}) || b != (Pt{120, 40}) {
		t.Fatalf("row 1: %v %v %v", a, b, ok)
	}
	a, b, ok = Divider(gr, p, 4, inner) // y 100: in the bottom cuts
	if !ok || a[0] <= 20 || b[0] >= 120 {
		t.Fatalf("row 4 not kept out of the cut corners: %v %v", a, b)
	}
	if _, _, ok = Divider(gr, p, 7, inner); ok {
		t.Fatal("a row past the inside has a divider")
	}
}
