package udp

// resizePCM applies a small rate correction to a decoded frame. Both boundary
// samples are preserved: adjacent frames keep their original joins. Large
// changes are spread across many frames by the playout controller. This is
// bounded resampling, not pitch-preserving time stretching.
func resizePCM(src []int16, n int) []int16 {
	if n == len(src) {
		return src
	}
	out := make([]int16, n)
	if len(src) < 2 || n < 2 {
		copy(out, src)
		return out
	}
	den := int64(n - 1)
	for i := range out {
		pos := i * (len(src) - 1)
		j, frac := pos/(n-1), int64(pos%(n-1))
		if j+1 == len(src) {
			out[i] = src[j]
			continue
		}
		out[i] = int16((int64(src[j])*(den-frac) + int64(src[j+1])*frac) / den)
	}
	return out
}
