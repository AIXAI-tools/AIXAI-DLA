//go:build windows

#include "textflag.h"

// Vectored continue handler, Windows x64 ABI: CX = EXCEPTION_POINTERS*.
// Returns EXCEPTION_CONTINUE_EXECUTION (-1) when the exception address is
// outside this executable, otherwise EXCEPTION_CONTINUE_SEARCH (0).
TEXT ·foreignContinueHandler(SB),NOSPLIT|NOFRAME,$0
	MOVQ 0(CX), AX          // ExceptionRecord
	MOVQ 16(AX), AX         // ExceptionRecord.ExceptionAddress
	MOVQ ·goImageLo(SB), DX
	CMPQ AX, DX
	JB foreign
	MOVQ ·goImageHi(SB), DX
	CMPQ AX, DX
	JAE foreign
	XORL AX, AX
	RET
foreign:
	MOVL $-1, AX
	RET

// func foreignContinueHandlerAddr() uintptr
TEXT ·foreignContinueHandlerAddr(SB),NOSPLIT,$0-8
	LEAQ ·foreignContinueHandler(SB), AX
	MOVQ AX, ret+0(FP)
	RET
