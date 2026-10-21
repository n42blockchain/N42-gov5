// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

#ifndef N42_SECP256K1_RECOVER_X8_H
#define N42_SECP256K1_RECOVER_X8_H

#include <stddef.h>
#include <stdint.h>

// n42_x8_init builds the fixed-base generator table. It must run once, and
// complete, before the first n42_x8_recover call. AVX-512 IFMA required.
void n42_x8_init(void);

// n42_x8_recover recovers the uncompressed public keys of n signatures.
// msgs holds n 32-byte digests, sigs n 65-byte [R || S || V] signatures and
// pubs receives n 65-byte keys (0x04 || X || Y). ok[i] is set to 1 when
// pubs[i] was written; 0 means the lane was not handled (any invalid or
// unusual input) and the caller must recover it on the reference path.
// AVX-512 IFMA required.
void n42_x8_recover(size_t n, const uint8_t *msgs, const uint8_t *sigs, uint8_t *pubs, uint8_t *ok);

#endif
