package ui

import "testing"

// Whatever is cut to a width must still fit it, ellipsis included.
func TestTruncateFits(t *testing.T) {
	texts := []string{
		"30fps TX↑ A0.7kBps V12.3kBps 4ms",
		"Привет, мир! Съешь же ещё этих мягких булок",
		"WWWWWWWWWWWWWWWWWWWWWW iiiiiiiiiiiiii",
		"你好世界 こんにちは 안녕하세요",
		"…………",
	}
	for _, s := range texts {
		for limit := 0; limit <= 60; limit++ {
			got := truncateWidth(s, limit)
			if w := displayWidth(got); w > limit {
				t.Fatalf("truncateWidth(%q, %d) = %q, %d cells", s, limit, got, w)
			}
			if w := displayWidth(tailWidth(s, limit)); w > limit {
				t.Fatalf("tailWidth(%q, %d): %d cells", s, limit, w)
			}
			for _, line := range wrapWidth(s, limit) {
				if w := displayWidth(line); limit > 2 && w > limit {
					t.Fatalf("wrapWidth(%q, %d): line %q is %d cells", s, limit, line, w)
				}
			}
		}
	}
}
