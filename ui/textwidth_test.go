package ui

import (
	"strings"
	"testing"
)

func TestDisplayWidthCountsCells(t *testing.T) {
	cases := map[string]int{
		"hello":  5,
		"привет": 6,
		"你好":     4, // wide CJK
		"👍":      2,
		"👨‍👩‍👧":  2, // ZWJ family is one cluster
		"é":      1, // e + combining acute
	}
	for s, want := range cases {
		if got := displayWidth(s); got != want {
			t.Errorf("displayWidth(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestWrapWidthKeepsEveryCharacter(t *testing.T) {
	msg := "Это длинное сообщение с 你好世界 и 👨‍👩‍👧 эмодзи, которое не должно обрезаться"
	lines := wrapWidth(msg, 12)
	if len(lines) < 2 {
		t.Fatalf("expected wrapping, got %q", lines)
	}
	for _, l := range lines {
		if w := displayWidth(l); w > 12 {
			t.Errorf("line %q is %d cells wide, limit 12", l, w)
		}
	}
	strip := func(s string) string { return strings.ReplaceAll(s, " ", "") }
	if got := strip(strings.Join(lines, "")); got != strip(msg) {
		t.Errorf("text changed while wrapping:\n got %q\nwant %q", got, strip(msg))
	}
	if got := tailWidth("abc你好", 4); got != "你好" {
		t.Errorf("tailWidth = %q, want 你好", got)
	}
	if got := truncateWidth("你好世界", 5); got != "你好…" {
		t.Errorf("truncateWidth = %q, want 你好…", got)
	}
}
