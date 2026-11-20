//go:build amd64 && !purego

#include "textflag.h"

// A cell's 32 pixels are words 0x00YYBBRR (Y, Cb, Cr) in raster order
// (see cell_arm64.s); here SSE2: each channel's bytes for pixels 0-15 and
// 16-31 in two registers. A pixel's bit is 31 minus its number.

// The bit of each byte's pixel within its group of eight, top first.
DATA cellBitX<>+0(SB)/8, $0x0102040810204080
DATA cellBitX<>+8(SB)/8, $0x0102040810204080
GLOBL cellBitX<>(SB), RODATA|NOPTR, $16

// CHAN puts the channel at bit S of 16 pixels (64 bytes at OFF(SI)) in D,
// a byte each (X15 holds 0xff in each dword; X0-X3 scratch).
#define CHAN(S, OFF, D) \
	MOVOU	OFF(SI), X0; MOVOU OFF+16(SI), X1; MOVOU OFF+32(SI), X2; MOVOU OFF+48(SI), X3; \
	PSRLL	$S, X0; PSRLL $S, X1; PSRLL $S, X2; PSRLL $S, X3; \
	PAND	X15, X0; PAND X15, X1; PAND X15, X2; PAND X15, X3; \
	PACKSSLW	X1, X0; PACKSSLW X3, X2; PACKUSWB X2, X0; \
	MOVOA	X0, D

// HMIN, HMAX fold X's sixteen bytes into its lowest (X0 scratch).
#define HMIN(X) \
	MOVOA	X, X0; PSRLO $8, X0; PMINUB X0, X; \
	MOVOA	X, X0; PSRLO $4, X0; PMINUB X0, X; \
	MOVOA	X, X0; PSRLO $2, X0; PMINUB X0, X; \
	MOVOA	X, X0; PSRLO $1, X0; PMINUB X0, X
#define HMAX(X) \
	MOVOA	X, X0; PSRLO $8, X0; PMAXUB X0, X; \
	MOVOA	X, X0; PSRLO $4, X0; PMAXUB X0, X; \
	MOVOA	X, X0; PSRLO $2, X0; PMAXUB X0, X; \
	MOVOA	X, X0; PSRLO $1, X0; PMAXUB X0, X

// RANGE puts the least of P and Q's bytes in LO and the greatest in HI
// (general registers; X12, X13 scratch).
#define RANGE(P, Q, LO, HI) \
	MOVOA	P, X12; PMINUB Q, X12; HMIN(X12); MOVQ X12, LO; ANDQ $0xff, LO; \
	MOVOA	P, X13; PMAXUB Q, X13; HMAX(X13); MOVQ X13, HI; ANDQ $0xff, HI

// REV reverses X's sixteen bytes (X0 scratch).
#define REV(X) \
	PSHUFL	$0x4E, X, X; PSHUFLW $0x1B, X, X; PSHUFHW $0x1B, X, X; \
	MOVOA	X, X0; PSLLW $8, X; PSRLW $8, X0; POR X0, X

// func cellMaskSSE(block *cellBlock) uint32
TEXT ·cellMaskSSE(SB), NOSPLIT, $0-12
	MOVQ	block+0(FP), SI
	PCMPEQL	X15, X15
	PSRLL	$24, X15
	CHAN(16, 0, X4) // Y, pixels 0-15
	CHAN(16, 64, X5) // 16-31
	CHAN(8, 0, X6) // Cb
	CHAN(8, 64, X7)
	CHAN(0, 0, X8) // Cr
	CHAN(0, 64, X9)
	RANGE(X4, X5, R8, R9)
	RANGE(X6, X7, R10, R11)
	RANGE(X8, X9, R12, R13)
	SUBQ	R8, R9 // the spans
	SUBQ	R10, R11
	SUBQ	R12, R13
	// The widest channel, the first on a tie: Y, then Cb, then Cr.
	MOVOA	X4, X10
	MOVOA	X5, X11
	MOVQ	R8, AX // its least
	MOVQ	R9, BX // its span
	CMPQ	R11, BX
	JLS	notcb
	MOVOA	X6, X10
	MOVOA	X7, X11
	MOVQ	R10, AX
	MOVQ	R11, BX
notcb:
	CMPQ	R13, BX
	JLS	notcr
	MOVOA	X8, X10
	MOVOA	X9, X11
	MOVQ	R12, AX
	MOVQ	R13, BX
notcr:
	SHRQ	$1, BX
	ADDQ	BX, AX // the split
	XORL	DX, DX
	CMPQ	AX, $255
	JEQ	maskdone // none above it
	INCQ	AX
	MOVQ	AX, X12
	PUNPCKLBW	X12, X12
	PSHUFLW	$0, X12, X12
	PSHUFL	$0, X12, X12 // split+1 in every byte
	// above the split: max(x, split+1) == x
	MOVOA	X10, X13
	PMAXUB	X12, X13
	PCMPEQB	X10, X13
	MOVOA	X11, X14
	PMAXUB	X12, X14
	PCMPEQB	X11, X14
	REV(X13)
	REV(X14)
	PMOVMSKB	X13, AX
	PMOVMSKB	X14, DX
	SHLL	$16, AX
	ORL	AX, DX
maskdone:
	MOVL	DX, ret+8(FP)
	RET

// SUM is the sum of P and Q's bytes in DST (general register; X0, X1
// scratch, X14 zero).
#define SUM(P, Q, DST) \
	MOVOA	P, X0; PSADBW X14, X0; MOVOA Q, X1; PSADBW X14, X1; PADDQ X1, X0; \
	MOVOA	X0, X1; PSRLO $8, X1; PADDQ X1, X0; MOVQ X0, DST

// func cellSumsSSE(block *cellBlock, pattern uint32, sums *[6]uint32)
TEXT ·cellSumsSSE(SB), NOSPLIT, $0-24
	MOVQ	block+0(FP), SI
	MOVLQZX	pattern+8(FP), AX
	MOVQ	sums+16(FP), DI
	PCMPEQL	X15, X15
	PSRLL	$24, X15
	CHAN(16, 0, X4)
	CHAN(16, 64, X5)
	CHAN(8, 0, X6)
	CHAN(8, 64, X7)
	CHAN(0, 0, X8)
	CHAN(0, 64, X9)
	PXOR	X14, X14
	// The whole cell's sums.
	SUM(X4, X5, R8)
	SUM(X6, X7, R9)
	SUM(X8, X9, R10)
	MOVL	R8, 12(DI)
	MOVL	R9, 16(DI)
	MOVL	R10, 20(DI)
	// The pattern's bits as byte masks: pixels 0-15 from its bytes 3 and
	// 2 (X10), 16-31 from 1 and 0 (X11).
	MOVQ	AX, X12
	PUNPCKLBW	X12, X12
	PUNPCKLWL	X12, X12
	PSHUFL	$0xAF, X12, X10
	PSHUFL	$0x05, X12, X11
	MOVOU	cellBitX<>(SB), X13
	PAND	X13, X10
	PCMPEQB	X13, X10
	PAND	X13, X11
	PCMPEQB	X13, X11
	// Under the pattern.
	PAND	X10, X4
	PAND	X11, X5
	PAND	X10, X6
	PAND	X11, X7
	PAND	X10, X8
	PAND	X11, X9
	SUM(X4, X5, R8)
	SUM(X6, X7, R9)
	SUM(X8, X9, R10)
	MOVL	R8, 0(DI)
	MOVL	R9, 4(DI)
	MOVL	R10, 8(DI)
	RET
