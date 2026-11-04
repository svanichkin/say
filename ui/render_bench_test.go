package ui

import (
	"math/rand"
	"strings"
	"testing"
)

// BenchmarkPaneFullRender draws a full 98x58 pane (a 200x60 terminal).
func BenchmarkPaneFullRender(b *testing.B) {
	frame := randomTextFrame(rand.New(rand.NewSource(1)), 98, 58)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var sb strings.Builder
		writeTextFrameRegion(&sb, frame, 2, 2, 0, 0, frame.Cols, frame.Rows, nil)
	}
}
