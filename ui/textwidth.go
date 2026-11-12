package ui

import (
	"strings"

	"github.com/rivo/uniseg"
)

// Terminal text is measured in cells per grapheme cluster: wide CJK
// characters and most emoji take two cells, combining marks and ZWJ
// sequences stay attached to their base character.

// displayWidth returns the number of terminal cells s occupies.
func displayWidth(s string) int {
	return uniseg.StringWidth(s)
}

// truncateWidth shortens s to at most limit cells, ending with "…" when cut.
func truncateWidth(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if displayWidth(s) <= limit {
		return s
	}
	// The ellipsis takes a cell in a terminal, more in the pixel window.
	ellipsis := displayWidth("…")
	if limit < ellipsis {
		head, _ := splitAtWidth(s, limit)
		return head
	}
	head, _ := splitAtWidth(s, limit-ellipsis)
	return head + "…"
}

// tailWidth returns the longest suffix of s that fits in limit cells.
func tailWidth(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	var clusters []string
	var widths []int
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		clusters = append(clusters, g.Str())
		widths = append(widths, g.Width())
	}
	used, start := 0, len(clusters)
	for start > 0 && used+widths[start-1] <= limit {
		start--
		used += widths[start]
	}
	return strings.Join(clusters[start:], "")
}

// splitAtWidth splits s after the last grapheme cluster that fits in limit cells.
func splitAtWidth(s string, limit int) (head, rest string) {
	used := 0
	state := -1
	pos := 0
	remaining := s
	for len(remaining) > 0 {
		cluster, next, width, newState := uniseg.FirstGraphemeClusterInString(remaining, state)
		if used+width > limit {
			break
		}
		used += width
		pos += len(cluster)
		remaining, state = next, newState
	}
	return s[:pos], s[pos:]
}

// wrapWidth breaks s into lines of at most limit cells, preferring to break
// after spaces and splitting words only when a single word is too long.
func wrapWidth(s string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	var lines []string
	for _, paragraph := range strings.Split(s, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line, lineWidth := "", 0
		for _, word := range words {
			w := displayWidth(word)
			switch {
			case lineWidth == 0 && w <= limit:
				line, lineWidth = word, w
			case lineWidth > 0 && lineWidth+1+w <= limit:
				line += " " + word
				lineWidth += 1 + w
			default:
				if lineWidth > 0 {
					lines = append(lines, line)
				}
				for w > limit {
					head, rest := splitAtWidth(word, limit)
					if head == "" { // a single cluster wider than the line
						head, rest = word, ""
					}
					lines = append(lines, head)
					word = rest
					w = displayWidth(word)
				}
				line, lineWidth = word, w
			}
		}
		if lineWidth > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}
