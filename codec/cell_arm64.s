//go:build arm64 && !purego

#include "textflag.h"

// A cell's 32 pixels are words 0x00YYBBRR (Y, Cb, Cr) in raster order; two
// VLD4s split them into Cr (V0, V4), Cb (V1, V5) and Y (V2, V6), pixels 0-15
// and 16-31. A pixel's bit is 31 minus its number.

// The bit of each byte's pixel within its group of eight, top first.
DATA cellBit<>+0(SB)/8, $0x0102040810204080
DATA cellBit<>+8(SB)/8, $0x0102040810204080
GLOBL cellBit<>(SB), RODATA|NOPTR, $16

// The byte of the pattern (pixels 0-7 first) for each of pixels 0-15 and
// 16-31.
DATA cellByte<>+0(SB)/8, $0x0000000000000000
DATA cellByte<>+8(SB)/8, $0x0101010101010101
DATA cellByte<>+16(SB)/8, $0x0202020202020202
DATA cellByte<>+24(SB)/8, $0x0303030303030303
GLOBL cellByte<>(SB), RODATA|NOPTR, $32

// RANGE puts the minimum of P and Q's bytes in LO and the maximum in HI
// (general registers).
#define RANGE(P, Q, LO, HI) \
	VUMIN	Q.B16, P.B16, V16.B16; \
	VUMAX	Q.B16, P.B16, V17.B16; \
	VUMINV	V16.B16, V18; \
	VUMAXV	V17.B16, V19; \
	VMOV	V18.B[0], LO; \
	VMOV	V19.B[0], HI

// func cellMaskNEON(block *cellBlock) uint32
TEXT ·cellMaskNEON(SB), NOSPLIT, $0-12
	MOVD	block+0(FP), R0
	VLD4.P	64(R0), [V0.B16, V1.B16, V2.B16, V3.B16]
	VLD4	(R0), [V4.B16, V5.B16, V6.B16, V7.B16]
	RANGE(V2, V6, R1, R2) // Y
	RANGE(V1, V5, R3, R4) // Cb
	RANGE(V0, V4, R5, R6) // Cr
	SUB	R1, R2, R2 // the spans
	SUB	R3, R4, R4
	SUB	R5, R6, R6
	// The widest channel, the first on a tie: Y, then Cb, then Cr.
	VORR	V2.B16, V2.B16, V8.B16
	VORR	V6.B16, V6.B16, V9.B16
	MOVD	R1, R7 // its minimum
	MOVD	R2, R8 // its span
	CMP	R8, R4
	BLS	notcb
	VORR	V1.B16, V1.B16, V8.B16
	VORR	V5.B16, V5.B16, V9.B16
	MOVD	R3, R7
	MOVD	R4, R8
notcb:
	CMP	R8, R6
	BLS	notcr
	VORR	V0.B16, V0.B16, V8.B16
	VORR	V4.B16, V4.B16, V9.B16
	MOVD	R5, R7
	MOVD	R6, R8
notcr:
	ADD	R8>>1, R7, R7 // the split
	VDUP	R7, V10.B16
	VCMHI	V10.B16, V8.B16, V11.B16 // above the split: 0xff
	VCMHI	V10.B16, V9.B16, V12.B16
	MOVD	$cellBit<>(SB), R9
	VLD1	(R9), [V30.B16]
	VAND	V30.B16, V11.B16, V11.B16
	VAND	V30.B16, V12.B16, V12.B16
	// Each group of eight bytes summed into one: pixels 0-7, 8-15, 16-23,
	// 24-31 in bytes 0-3.
	VADDP	V12.B16, V11.B16, V13.B16
	VADDP	V13.B16, V13.B16, V13.B16
	VADDP	V13.B16, V13.B16, V13.B16
	VMOV	V13.S[0], R10
	REVW	R10, R10
	MOVW	R10, ret+8(FP)
	RET

// SUMS adds the bytes of P and Q into DST (general register) as uint32.
#define SUMS(P, Q, DST) \
	VUADDLV	P.B16, V20; \
	VUADDLV	Q.B16, V21; \
	VMOV	V20.H[0], DST; \
	VMOV	V21.H[0], R11; \
	ADD	R11, DST, DST

// func cellSumsNEON(block *cellBlock, pattern uint32, sums *[6]uint32)
TEXT ·cellSumsNEON(SB), NOSPLIT, $0-24
	MOVD	block+0(FP), R0
	MOVWU	pattern+8(FP), R1
	MOVD	sums+16(FP), R2
	VLD4.P	64(R0), [V0.B16, V1.B16, V2.B16, V3.B16]
	VLD4	(R0), [V4.B16, V5.B16, V6.B16, V7.B16]
	// The pattern's bits as byte masks, pixels 0-15 (V11) and 16-31 (V12).
	REVW	R1, R1
	VMOV	R1, V10.S[0]
	MOVD	$cellByte<>(SB), R9
	VLD1	(R9), [V26.B16, V27.B16]
	VTBL	V26.B16, [V10.B16], V11.B16
	VTBL	V27.B16, [V10.B16], V12.B16
	MOVD	$cellBit<>(SB), R9
	VLD1	(R9), [V30.B16]
	VCMTST	V30.B16, V11.B16, V11.B16
	VCMTST	V30.B16, V12.B16, V12.B16
	// The whole cell's sums.
	SUMS(V2, V6, R3)
	SUMS(V1, V5, R4)
	SUMS(V0, V4, R5)
	MOVW	R3, 12(R2)
	MOVW	R4, 16(R2)
	MOVW	R5, 20(R2)
	// Under the pattern.
	VAND	V11.B16, V2.B16, V2.B16
	VAND	V12.B16, V6.B16, V6.B16
	VAND	V11.B16, V1.B16, V1.B16
	VAND	V12.B16, V5.B16, V5.B16
	VAND	V11.B16, V0.B16, V0.B16
	VAND	V12.B16, V4.B16, V4.B16
	SUMS(V2, V6, R3)
	SUMS(V1, V5, R4)
	SUMS(V0, V4, R5)
	MOVW	R3, 0(R2)
	MOVW	R4, 4(R2)
	MOVW	R5, 8(R2)
	RET
