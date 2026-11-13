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
