//go:build arm64 && !purego

#include "textflag.h"

// func boxRowsNEON(sum *uint16, src *byte, stride, rows, n int)
// sum[i] = src[i] + src[stride+i] + ... (rows rows), i < n (a multiple of
// 16, rows >= 1): sixteen bytes a step, widened and added in two halves.
TEXT ·boxRowsNEON(SB), NOSPLIT, $0-40
	MOVD	sum+0(FP), R0
	MOVD	src+8(FP), R1
	MOVD	stride+16(FP), R2
	MOVD	rows+24(FP), R3
	MOVD	n+32(FP), R4
cols:
	MOVD	R1, R5
	MOVD	R3, R6
	VEOR	V0.B16, V0.B16, V0.B16
	VEOR	V1.B16, V1.B16, V1.B16
rows:
	VLD1	(R5), [V2.B16]
	VUADDW	V2.B8, V0.H8, V0.H8
	VUADDW2	V2.B16, V1.H8, V1.H8
	ADD	R2, R5
	SUBS	$1, R6
	BNE	rows
	VST1.P	[V0.H8, V1.H8], 32(R0)
	ADD	$16, R1
	SUBS	$16, R4
	BNE	cols
	RET

