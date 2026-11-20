//go:build amd64 && !purego

#include "textflag.h"

// func boxRowsSSE(sum *uint16, src *byte, stride, rows, n int)
// sum[i] = src[i] + src[stride+i] + ... (rows rows), i < n (a multiple of
// 16, rows >= 1): sixteen bytes a step, widened and added in two halves.
TEXT ·boxRowsSSE(SB), NOSPLIT, $0-40
	MOVQ	sum+0(FP), DI
	MOVQ	src+8(FP), SI
	MOVQ	stride+16(FP), DX
	MOVQ	rows+24(FP), CX
	MOVQ	n+32(FP), BX
	PXOR	X7, X7
cols:
	MOVQ	SI, R8
	MOVQ	CX, R9
	PXOR	X0, X0
	PXOR	X1, X1
rows:
	MOVOU	(R8), X2
	MOVO	X2, X3
	PUNPCKLBW	X7, X2
	PUNPCKHBW	X7, X3
	PADDW	X2, X0
	PADDW	X3, X1
	ADDQ	DX, R8
	DECQ	R9
	JNZ	rows
	MOVOU	X0, (DI)
	MOVOU	X1, 16(DI)
	ADDQ	$32, DI
	ADDQ	$16, SI
	SUBQ	$16, BX
	JNZ	cols
	RET
