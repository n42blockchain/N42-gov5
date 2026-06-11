// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

#include "textflag.h"

// func prefetcht0(p unsafe.Pointer)
TEXT ·prefetcht0(SB), NOSPLIT, $0-8
	MOVQ p+0(FP), AX
	PREFETCHT0 (AX)
	RET
