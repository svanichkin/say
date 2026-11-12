package gui

import "testing"

func TestScreenCells(t *testing.T) {
	s := NewScreen(20, 5)
	_, _ = s.Write([]byte("\x1b[2;3Hhi \x1b[38;2;255;0;0mthere\x1b[0m│你x"))
	s.View(func(cols, _ int, cells []Cell, _ []Pane, _ uint64) {
		at := func(c int) Cell { return cells[1*cols+c] }
		if at(2).Text != "h" || at(4).Text != "" || at(5).Text != "t" || at(5).Fg.R != 255 {
			t.Fatalf("text cells: %+v %+v %+v", at(2), at(4), at(5))
		}
		if !at(10).Shape {
			t.Fatalf("box drawing not on the grid: %+v", at(10))
		}
		if at(11).Text != "你" || !at(11).Wide || at(13).Text != "x" {
			t.Fatalf("wide character: %+v %+v", at(11), at(13))
		}
	})
	_, _ = s.Write([]byte("\x1b[2;1H\x1b[2K"))
	s.View(func(cols, _ int, cells []Cell, _ []Pane, _ uint64) {
		for c := 0; c < cols; c++ {
			if cells[cols+c].Text != "" || cells[cols+c].Shape {
				t.Fatal("erased line keeps content")
			}
		}
	})
}

func TestScreenPaneMark(t *testing.T) {
	s := NewScreen(20, 6)
	_, _ = s.Write([]byte("\x1b[2;2Hold text"))
	_, _ = s.Write([]byte("\x1b]5577;2;2;10;3;42\x07\x1b[4;3Hcap"))
	s.View(func(cols, _ int, cells []Cell, panes []Pane, _ uint64) {
		if len(panes) != 1 || panes[0] != (Pane{Row: 1, Col: 1, Cols: 10, Rows: 3, Frame: 42}) {
			t.Fatalf("panes %+v", panes)
		}
		if cells[cols+2].Text != "" || !cells[cols+2].Video {
			t.Fatal("text under a pane stays")
		}
		if cells[3*cols+2].Video || cells[3*cols+2].Text != "c" {
			t.Fatal("caption over the pane")
		}
	})
	_, _ = s.Write([]byte("\x1b]5577;2;2;10;3;43\x07"))
	s.View(func(_, _ int, _ []Cell, panes []Pane, _ uint64) {
		if len(panes) != 1 || panes[0].Frame != 43 {
			t.Fatalf("panes %+v", panes)
		}
	})
}

func TestBracketMark(t *testing.T) {
	s := NewScreen(40, 10)
	s.Write([]byte("\x1b]5580;5;3;30;3;10;14;14;18;27\x07"))
	want := Bracket{Row: 4, Col: 2, Cols: 30, Spans: [][2]int{{3, 10}, {14, 14}, {18, 27}}}
	if got := s.brackets[[2]int{4, 2}]; !got.equal(want) {
		t.Fatalf("bracket %+v, want %+v", got, want)
	}
	s.Write([]byte("\x1b[2J"))
	if len(s.brackets) != 0 {
		t.Fatalf("brackets kept after a clear: %v", s.brackets)
	}
}
