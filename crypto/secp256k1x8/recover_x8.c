// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Eight-way secp256k1 public key recovery with AVX-512 IFMA.
//
// Eight signatures are recovered side by side, one per 64-bit lane of a zmm
// register. A field element is five 52-bit limbs (radix 2^52), one zmm per
// limb, multiplied with vpmadd52luq/vpmadd52huq. Per lane the recovery is
// Q = u2*R + u1*G with u1 = -z/r and u2 = s/r (mod n), where R is the point
// whose x is r and whose y parity is the recovery id:
//
//   - R is decompressed with the (p+1)/4 square root.
//   - u2*R is a fixed-window (signed 5-bit digits) double-and-add over a
//     table of 1R..16R in Jacobian coordinates.
//   - u1*G is a signed 7-bit comb over a precomputed table of affine
//     multiples j * 2^(7i) * G: one mixed addition per window, no doublings.
//   - 1/r is one Montgomery batch inversion per call; 1/Z for the affine
//     result a Fermat inversion per lane.
//
// Nothing here is constant time: every input is a public signature.
// Exceptional cases are not handled but detected: an addition of equal or
// opposite points leaves Z = 0 for good, which the final check catches.
// Any lane that is invalid or unusual (recovery id 2 or 3, r or s out of
// range, r not on the curve, a degenerate addition, a result at infinity)
// is reported as not handled, and the caller recovers it with libsecp256k1,
// so results are those of libsecp256k1 for every input.

#include "recover_x8.h"

#include <immintrin.h>
#include <string.h>

#if defined(__clang__)
#pragma clang attribute push(__attribute__((target("avx512f,avx512ifma,avx512dq,avx512vl,avx512bw,bmi2"))), \
                             apply_to = function)
#elif defined(__GNUC__)
#pragma GCC push_options
#pragma GCC target("avx512f,avx512ifma,avx512dq,avx512vl,avx512bw,bmi2")
#endif

#define INLINE static inline __attribute__((always_inline))

typedef __m512i v8;
typedef unsigned __int128 u128;

// fe holds eight field elements, limb-major. "Weak" form: every limb below
// 2^52 (so the value is below 2^260), congruent to the element mod p.
// Every multiplication input must be weak; every multiplication output is.
typedef struct {
    v8 l[5];
} fe;

#define M52 0xFFFFFFFFFFFFFULL
#define M48 0xFFFFFFFFFFFFULL
#define KP 0x1000003D1ULL     // 2^256 mod p
#define KP4 0x1000003D10ULL   // 2^260 mod p

// 64p in limbs, each limb at least 2^54 - 64, so a + P64 - b stays
// non-negative limb by limb for any b with limbs below 2^54 - 64.
static const uint64_t P64L[5] = {
    0x3ffffbfffff0bc0ULL, 0x3ffffffffffffc0ULL, 0x3ffffffffffffc0ULL, 0x3ffffffffffffc0ULL, 0x3fffffffffffc0ULL,
};

INLINE v8 bc(uint64_t x) { return _mm512_set1_epi64((long long)x); }
INLINE v8 vadd(v8 a, v8 b) { return _mm512_add_epi64(a, b); }
INLINE v8 vsub(v8 a, v8 b) { return _mm512_sub_epi64(a, b); }
INLINE v8 vand(v8 a, v8 b) { return _mm512_and_si512(a, b); }
INLINE v8 vsrl(v8 a, int s) { return _mm512_srli_epi64(a, s); }
INLINE v8 vsll(v8 a, int s) { return _mm512_slli_epi64(a, s); }
INLINE v8 mlo(v8 acc, v8 a, v8 b) { return _mm512_madd52lo_epu64(acc, a, b); }
INLINE v8 mhi(v8 acc, v8 a, v8 b) { return _mm512_madd52hi_epu64(acc, a, b); }

// fe_reduce reduces ten product columns (each below 2^57) to weak form.
INLINE void fe_reduce(fe *r, v8 *c) {
    const v8 M = bc(M52), MM48 = bc(M48), R4 = bc(KP4), K = bc(KP);
    // Columns 5..9 to 52-bit limbs. The high part is at most the product
    // over 2^260, below 2^260, so nothing carries out of column 9.
    c[6] = vadd(c[6], vsrl(c[5], 52));
    c[5] = vand(c[5], M);
    c[7] = vadd(c[7], vsrl(c[6], 52));
    c[6] = vand(c[6], M);
    c[8] = vadd(c[8], vsrl(c[7], 52));
    c[7] = vand(c[7], M);
    c[9] = vadd(c[9], vsrl(c[8], 52));
    c[8] = vand(c[8], M);
    // High limbs times 2^260 mod p into columns 0..4 and d5 (weight 2^260).
    v8 d5 = _mm512_setzero_si512();
    c[0] = mlo(c[0], c[5], R4);
    c[1] = mhi(c[1], c[5], R4);
    c[1] = mlo(c[1], c[6], R4);
    c[2] = mhi(c[2], c[6], R4);
    c[2] = mlo(c[2], c[7], R4);
    c[3] = mhi(c[3], c[7], R4);
    c[3] = mlo(c[3], c[8], R4);
    c[4] = mhi(c[4], c[8], R4);
    c[4] = mlo(c[4], c[9], R4);
    d5 = mhi(d5, c[9], R4);
    // Everything at or above 2^256 times 2^256 mod p into columns 0 and 1.
    v8 t = vadd(vsrl(c[4], 48), vsll(d5, 4));
    c[4] = vand(c[4], MM48);
    c[0] = mlo(c[0], t, K);
    c[1] = mhi(c[1], t, K);
    // One carry pass: limbs 0..3 below 2^52, limb 4 below 2^48 + 2^6.
    c[1] = vadd(c[1], vsrl(c[0], 52));
    r->l[0] = vand(c[0], M);
    c[2] = vadd(c[2], vsrl(c[1], 52));
    r->l[1] = vand(c[1], M);
    c[3] = vadd(c[3], vsrl(c[2], 52));
    r->l[2] = vand(c[2], M);
    r->l[4] = vadd(c[4], vsrl(c[3], 52));
    r->l[3] = vand(c[3], M);
}

static inline void fe_mul(fe *r, const fe *a, const fe *b) {
    v8 lo[10], hi[10], c[10];
    const v8 z = _mm512_setzero_si512();
#pragma GCC unroll 10
    for (int k = 0; k < 10; k++) {
        lo[k] = z;
        hi[k] = z;
    }
#pragma GCC unroll 5
    for (int i = 0; i < 5; i++) {
#pragma GCC unroll 5
        for (int j = 0; j < 5; j++) {
            lo[i + j] = mlo(lo[i + j], a->l[i], b->l[j]);
            hi[i + j + 1] = mhi(hi[i + j + 1], a->l[i], b->l[j]);
        }
    }
#pragma GCC unroll 10
    for (int k = 0; k < 10; k++) c[k] = vadd(lo[k], hi[k]);
    fe_reduce(r, c);
}

static inline void fe_sqr(fe *r, const fe *a) {
    v8 lo[10], hi[10], c[10];
    const v8 z = _mm512_setzero_si512();
#pragma GCC unroll 10
    for (int k = 0; k < 10; k++) {
        lo[k] = z;
        hi[k] = z;
    }
#pragma GCC unroll 5
    for (int i = 0; i < 5; i++) {
#pragma GCC unroll 5
        for (int j = i + 1; j < 5; j++) {
            lo[i + j] = mlo(lo[i + j], a->l[i], a->l[j]);
            hi[i + j + 1] = mhi(hi[i + j + 1], a->l[i], a->l[j]);
        }
    }
#pragma GCC unroll 10
    for (int k = 0; k < 10; k++) c[k] = vsll(vadd(lo[k], hi[k]), 1);
#pragma GCC unroll 5
    for (int i = 0; i < 5; i++) {
        c[2 * i] = mlo(c[2 * i], a->l[i], a->l[i]);
        c[2 * i + 1] = mhi(c[2 * i + 1], a->l[i], a->l[i]);
    }
    fe_reduce(r, c);
}

static inline void fe_sqr_n(fe *r, const fe *a, int n) {
    fe_sqr(r, a);
    for (int i = 1; i < n; i++) fe_sqr(r, r);
}

// fe_norm brings limbs below 2^62 (limb 4 below 2^62) back to weak form.
INLINE void fe_norm(fe *r) {
    const v8 M = bc(M52);
    v8 t = vsrl(r->l[4], 48);
    v8 l4 = vand(r->l[4], bc(M48));
    v8 l0 = vadd(r->l[0], _mm512_mullo_epi64(t, bc(KP)));
    v8 l1 = vadd(r->l[1], vsrl(l0, 52));
    l0 = vand(l0, M);
    v8 l2 = vadd(r->l[2], vsrl(l1, 52));
    l1 = vand(l1, M);
    v8 l3 = vadd(r->l[3], vsrl(l2, 52));
    l2 = vand(l2, M);
    l4 = vadd(l4, vsrl(l3, 52));
    l3 = vand(l3, M);
    r->l[0] = l0;
    r->l[1] = l1;
    r->l[2] = l2;
    r->l[3] = l3;
    r->l[4] = l4;
}

// fe_add: r = a + b, limb by limb, not normalized.
INLINE void fe_add(fe *r, const fe *a, const fe *b) {
#pragma GCC unroll 5
    for (int i = 0; i < 5; i++) r->l[i] = vadd(a->l[i], b->l[i]);
}

// fe_subk: r = a + k*64p - b, not normalized; b's limbs below k*(2^54-64).
INLINE void fe_subk(fe *r, const fe *a, const fe *b, int k) {
#pragma GCC unroll 5
    for (int i = 0; i < 5; i++) r->l[i] = vsub(vadd(a->l[i], bc(P64L[i] * (uint64_t)k)), b->l[i]);
}

// fe_shl: r = a * 2^s, limb by limb, not normalized.
INLINE void fe_shl(fe *r, const fe *a, int s) {
#pragma GCC unroll 5
    for (int i = 0; i < 5; i++) r->l[i] = vsll(a->l[i], s);
}

// fe_full normalizes a weak element to its canonical value in [0, p).
static inline void fe_full(fe *r) {
    const v8 M = bc(M52);
    fe_norm(r);
    // A second fold: the value is now below 2^256 + 2^52, under 2p.
    fe_norm(r);
    // w = r + (2^256 - p); bit 256 of w set iff r >= p, and then r - p is
    // w without bit 256.
    v8 w0 = vadd(r->l[0], bc(KP));
    v8 w1 = vadd(r->l[1], vsrl(w0, 52));
    w0 = vand(w0, M);
    v8 w2 = vadd(r->l[2], vsrl(w1, 52));
    w1 = vand(w1, M);
    v8 w3 = vadd(r->l[3], vsrl(w2, 52));
    w2 = vand(w2, M);
    v8 w4 = vadd(r->l[4], vsrl(w3, 52));
    w3 = vand(w3, M);
    __mmask8 ge = _mm512_test_epi64_mask(w4, bc(~M48));
    r->l[0] = _mm512_mask_mov_epi64(r->l[0], ge, w0);
    r->l[1] = _mm512_mask_mov_epi64(r->l[1], ge, w1);
    r->l[2] = _mm512_mask_mov_epi64(r->l[2], ge, w2);
    r->l[3] = _mm512_mask_mov_epi64(r->l[3], ge, w3);
    r->l[4] = _mm512_mask_mov_epi64(r->l[4], ge, vand(w4, bc(M48)));
}

// fe_is_zero reports the lanes of a weak element that are 0 mod p.
static inline __mmask8 fe_is_zero(const fe *a) {
    fe t = *a;
    fe_full(&t);
    v8 o = _mm512_or_si512(_mm512_or_si512(t.l[0], t.l[1]), _mm512_or_si512(t.l[2], t.l[3]));
    o = _mm512_or_si512(o, t.l[4]);
    return _mm512_cmpeq_epi64_mask(o, _mm512_setzero_si512());
}

// fe_eq reports the lanes where weak elements a and b are equal mod p.
static inline __mmask8 fe_eq(const fe *a, const fe *b) {
    fe d;
    fe_subk(&d, a, b, 1);
    fe_norm(&d);
    return fe_is_zero(&d);
}

INLINE void fe_blend(fe *r, __mmask8 m, const fe *a) {
#pragma GCC unroll 5
    for (int i = 0; i < 5; i++) r->l[i] = _mm512_mask_mov_epi64(r->l[i], m, a->l[i]);
}

// fe_pow_chain computes x2, x3, x22 and x223 = a^(2^k - 1) for the k in
// their names: the shared head of the inversion and square root chains.
static void fe_pow_chain(const fe *a, fe *x2, fe *x3, fe *x22, fe *x223) {
    fe x6, x9, x11, x44, x88, x176, x220;
    fe_sqr(x2, a);
    fe_mul(x2, x2, a);
    fe_sqr(x3, x2);
    fe_mul(x3, x3, a);
    fe_sqr_n(&x6, x3, 3);
    fe_mul(&x6, &x6, x3);
    fe_sqr_n(&x9, &x6, 3);
    fe_mul(&x9, &x9, x3);
    fe_sqr_n(&x11, &x9, 2);
    fe_mul(&x11, &x11, x2);
    fe_sqr_n(x22, &x11, 11);
    fe_mul(x22, x22, &x11);
    fe_sqr_n(&x44, x22, 22);
    fe_mul(&x44, &x44, x22);
    fe_sqr_n(&x88, &x44, 44);
    fe_mul(&x88, &x88, &x44);
    fe_sqr_n(&x176, &x88, 88);
    fe_mul(&x176, &x176, &x88);
    fe_sqr_n(&x220, &x176, 44);
    fe_mul(&x220, &x220, &x44);
    fe_sqr_n(x223, &x220, 3);
    fe_mul(x223, x223, x3);
}

// fe_inv: r = a^(p-2) = 1/a (0 for a = 0).
static void fe_inv(fe *r, const fe *a) {
    fe x2, x3, x22, t;
    fe_pow_chain(a, &x2, &x3, &x22, &t);
    fe_sqr_n(&t, &t, 23);
    fe_mul(&t, &t, &x22);
    fe_sqr_n(&t, &t, 5);
    fe_mul(&t, &t, a);
    fe_sqr_n(&t, &t, 3);
    fe_mul(&t, &t, &x2);
    fe_sqr_n(&t, &t, 2);
    fe_mul(r, &t, a);
}

// fe_sqrt: r = a^((p+1)/4); returns the lanes where r^2 = a.
static __mmask8 fe_sqrt(fe *r, const fe *a) {
    fe x2, x3, x22, t;
    fe_pow_chain(a, &x2, &x3, &x22, &t);
    fe_sqr_n(&t, &t, 23);
    fe_mul(&t, &t, &x22);
    fe_sqr_n(&t, &t, 6);
    fe_mul(&t, &t, &x2);
    fe_sqr_n(r, &t, 2);
    fe_sqr(&t, r);
    return fe_eq(&t, a);
}

// ---------------------------------------------------------------------------
// Points: Jacobian (X, Y, Z) with x = X/Z^2, y = Y/Z^3, and affine (x, y).

typedef struct {
    fe x, y, z;
} gej;

typedef struct {
    fe x, y;
} ge;

// gej_dbl: r = 2a (dbl-2009-l). Z = 0 stays 0.
static inline void gej_dbl(gej *r, const gej *a) {
    fe A, B, C, D, E, F, t, x3, y3, z3;
    fe_sqr(&A, &a->x);
    fe_sqr(&B, &a->y);
    fe_sqr(&C, &B);
    fe_add(&t, &a->x, &B);
    fe_norm(&t);
    fe_sqr(&t, &t);
    // D = 2((X+B)^2 - A - C)
    fe_subk(&D, &t, &A, 1);
    fe_subk(&D, &D, &C, 1);
    fe_shl(&D, &D, 1);
    fe_norm(&D);
    // E = 3A
    fe_add(&E, &A, &A);
    fe_add(&E, &E, &A);
    fe_norm(&E);
    fe_sqr(&F, &E);
    // X3 = F - 2D
    fe_shl(&t, &D, 1);
    fe_subk(&x3, &F, &t, 1);
    fe_norm(&x3);
    // Y3 = E(D - X3) - 8C
    fe_subk(&t, &D, &x3, 1);
    fe_norm(&t);
    fe_mul(&y3, &E, &t);
    fe_shl(&t, &C, 3);
    fe_subk(&y3, &y3, &t, 8);
    fe_norm(&y3);
    // Z3 = 2YZ
    fe_mul(&z3, &a->y, &a->z);
    fe_shl(&z3, &z3, 1);
    fe_norm(&z3);
    r->x = x3;
    r->y = y3;
    r->z = z3;
}

// gej_add: r = a + b (add-2007-bl, Z3 = 2 Z1 Z2 H). Equal or opposite
// inputs give Z3 = 0; so does a Z = 0 input.
static inline void gej_add(gej *r, const gej *a, const gej *b) {
    fe z1z1, z2z2, u1, u2, s1, s2, h, h2, i, j, rr, v, t, x3, y3, z3;
    fe_sqr(&z1z1, &a->z);
    fe_sqr(&z2z2, &b->z);
    fe_mul(&u1, &a->x, &z2z2);
    fe_mul(&u2, &b->x, &z1z1);
    fe_mul(&s1, &a->y, &b->z);
    fe_mul(&s1, &s1, &z2z2);
    fe_mul(&s2, &b->y, &a->z);
    fe_mul(&s2, &s2, &z1z1);
    fe_subk(&h, &u2, &u1, 1);
    fe_norm(&h);
    fe_shl(&h2, &h, 1);
    fe_norm(&h2);
    fe_sqr(&i, &h2);
    fe_mul(&j, &h, &i);
    fe_subk(&rr, &s2, &s1, 1);
    fe_shl(&rr, &rr, 1);
    fe_norm(&rr);
    fe_mul(&v, &u1, &i);
    // X3 = r^2 - J - 2V
    fe_sqr(&x3, &rr);
    fe_subk(&x3, &x3, &j, 1);
    fe_shl(&t, &v, 1);
    fe_subk(&x3, &x3, &t, 1);
    fe_norm(&x3);
    // Y3 = r(V - X3) - 2 S1 J
    fe_subk(&t, &v, &x3, 1);
    fe_norm(&t);
    fe_mul(&y3, &rr, &t);
    fe_mul(&t, &s1, &j);
    fe_shl(&t, &t, 1);
    fe_subk(&y3, &y3, &t, 1);
    fe_norm(&y3);
    // Z3 = Z1 Z2 2H
    fe_mul(&z3, &a->z, &b->z);
    fe_mul(&z3, &z3, &h2);
    r->x = x3;
    r->y = y3;
    r->z = z3;
}

// gej_add_ge: r = a + b with b affine (madd-2007-bl, Z3 = 2 Z1 H). Equal
// or opposite inputs give Z3 = 0; so does Z1 = 0.
static inline void gej_add_ge(gej *r, const gej *a, const ge *b) {
    fe z1z1, u2, s2, h, h2, i, j, rr, v, t, x3, y3, z3;
    fe_sqr(&z1z1, &a->z);
    fe_mul(&u2, &b->x, &z1z1);
    fe_mul(&t, &a->z, &z1z1);
    fe_mul(&s2, &b->y, &t);
    fe_subk(&h, &u2, &a->x, 1);
    fe_norm(&h);
    fe_shl(&h2, &h, 1);
    fe_norm(&h2);
    fe_sqr(&i, &h2);
    fe_mul(&j, &h, &i);
    fe_subk(&rr, &s2, &a->y, 1);
    fe_shl(&rr, &rr, 1);
    fe_norm(&rr);
    fe_mul(&v, &a->x, &i);
    fe_sqr(&x3, &rr);
    fe_subk(&x3, &x3, &j, 1);
    fe_shl(&t, &v, 1);
    fe_subk(&x3, &x3, &t, 1);
    fe_norm(&x3);
    fe_subk(&t, &v, &x3, 1);
    fe_norm(&t);
    fe_mul(&y3, &rr, &t);
    fe_mul(&t, &a->y, &j);
    fe_shl(&t, &t, 1);
    fe_subk(&y3, &y3, &t, 1);
    fe_norm(&y3);
    fe_mul(&z3, &a->z, &h2);
    r->x = x3;
    r->y = y3;
    r->z = z3;
}

INLINE void gej_blend(gej *r, __mmask8 m, const gej *a) {
    fe_blend(&r->x, m, &a->x);
    fe_blend(&r->y, m, &a->y);
    fe_blend(&r->z, m, &a->z);
}

// fe_neg_blend negates y (to its weak form of p - y) in the lanes of m.
INLINE void fe_neg_blend(fe *y, __mmask8 m) {
    fe n, zero;
    memset(&zero, 0, sizeof zero);
    fe_subk(&n, &zero, y, 1);
    fe_norm(&n);
    fe_blend(y, m, &n);
}

// ---------------------------------------------------------------------------
// Lane packing.

static inline uint64_t be64(const uint8_t *b) {
    uint64_t x;
    memcpy(&x, b, 8);
    return __builtin_bswap64(x);
}

static inline void put_be64(uint8_t *b, uint64_t x) {
    x = __builtin_bswap64(x);
    memcpy(b, &x, 8);
}

// b32_to_w reads a 32-byte big-endian number into little-endian words.
static inline void b32_to_w(uint64_t w[4], const uint8_t *b) {
    w[3] = be64(b);
    w[2] = be64(b + 8);
    w[1] = be64(b + 16);
    w[0] = be64(b + 24);
}

// w_to_l52 splits four 64-bit words into five 52-bit limbs.
static inline void w_to_l52(uint64_t l[5], const uint64_t w[4]) {
    l[0] = w[0] & M52;
    l[1] = ((w[0] >> 52) | (w[1] << 12)) & M52;
    l[2] = ((w[1] >> 40) | (w[2] << 24)) & M52;
    l[3] = ((w[2] >> 28) | (w[3] << 36)) & M52;
    l[4] = w[3] >> 16;
}

// l52_to_b32 writes canonical limbs as 32 big-endian bytes.
static inline void l52_to_b32(uint8_t *b, const uint64_t l[5]) {
    put_be64(b + 24, l[0] | (l[1] << 52));
    put_be64(b + 16, (l[1] >> 12) | (l[2] << 40));
    put_be64(b + 8, (l[2] >> 24) | (l[3] << 28));
    put_be64(b, (l[3] >> 36) | (l[4] << 16));
}

// fe_from_lanes loads per-lane limbs l[lane][5].
static inline void fe_from_lanes(fe *r, const uint64_t l[8][5]) {
    uint64_t t[5][8] __attribute__((aligned(64)));
    for (int i = 0; i < 8; i++)
        for (int k = 0; k < 5; k++) t[k][i] = l[i][k];
    for (int k = 0; k < 5; k++) r->l[k] = _mm512_load_si512((const void *)t[k]);
}

static inline void fe_to_lanes(uint64_t l[8][5], const fe *a) {
    uint64_t t[5][8] __attribute__((aligned(64)));
    for (int k = 0; k < 5; k++) _mm512_store_si512((void *)t[k], a->l[k]);
    for (int i = 0; i < 8; i++)
        for (int k = 0; k < 5; k++) l[i][k] = t[k][i];
}

// ---------------------------------------------------------------------------
// Scalars mod n (scalar code, little-endian 64-bit words, Montgomery form
// for multiplication).

static const uint64_t N[4] = {0xBFD25E8CD0364141ULL, 0xBAAEDCE6AF48A03BULL, 0xFFFFFFFFFFFFFFFEULL,
                              0xFFFFFFFFFFFFFFFFULL};
static const uint64_t N0INV = 0x4b0dff665588b13fULL;  // -1/n mod 2^64
static const uint64_t NR2[4] = {0x896cf21467d7d140ULL, 0x741496c20e7cf878ULL, 0xe697f5e45bcd07c6ULL,
                                0x9d671cd581c69bc5ULL};  // 2^512 mod n

static inline int sc_is_zero(const uint64_t a[4]) { return (a[0] | a[1] | a[2] | a[3]) == 0; }

// sc_geq_n reports a >= n.
static inline int sc_geq_n(const uint64_t a[4]) {
    for (int i = 3; i >= 0; i--) {
        if (a[i] != N[i]) return a[i] > N[i];
    }
    return 1;
}

// sc_sub_n: a -= n (wrapping).
static inline void sc_sub_n(uint64_t a[4]) {
    unsigned char b = 0;
    unsigned long long t;
    b = _subborrow_u64(b, a[0], N[0], &t);
    a[0] = t;
    b = _subborrow_u64(b, a[1], N[1], &t);
    a[1] = t;
    b = _subborrow_u64(b, a[2], N[2], &t);
    a[2] = t;
    (void)_subborrow_u64(b, a[3], N[3], &t);
    a[3] = t;
}

// sc_neg: r = n - a for a in (0, n); r = 0 for a = 0.
static inline void sc_neg(uint64_t r[4], const uint64_t a[4]) {
    if (sc_is_zero(a)) {
        r[0] = r[1] = r[2] = r[3] = 0;
        return;
    }
    unsigned char b = 0;
    unsigned long long t;
    b = _subborrow_u64(b, N[0], a[0], &t);
    r[0] = t;
    b = _subborrow_u64(b, N[1], a[1], &t);
    r[1] = t;
    b = _subborrow_u64(b, N[2], a[2], &t);
    r[2] = t;
    (void)_subborrow_u64(b, N[3], a[3], &t);
    r[3] = t;
}

// sc_mont_mul: r = a b / 2^256 mod n for a, b < n.
static void sc_mont_mul(uint64_t r[4], const uint64_t a[4], const uint64_t b[4]) {
    uint64_t t[6] = {0, 0, 0, 0, 0, 0};
    for (int i = 0; i < 4; i++) {
        u128 c = 0;
        for (int j = 0; j < 4; j++) {
            c += (u128)a[j] * b[i] + t[j];
            t[j] = (uint64_t)c;
            c >>= 64;
        }
        c += t[4];
        t[4] = (uint64_t)c;
        t[5] = (uint64_t)(c >> 64);
        uint64_t m = t[0] * N0INV;
        c = (u128)m * N[0] + t[0];
        c >>= 64;
        for (int j = 1; j < 4; j++) {
            c += (u128)m * N[j] + t[j];
            t[j - 1] = (uint64_t)c;
            c >>= 64;
        }
        c += t[4];
        t[3] = (uint64_t)c;
        t[4] = t[5] + (uint64_t)(c >> 64);
    }
    r[0] = t[0];
    r[1] = t[1];
    r[2] = t[2];
    r[3] = t[3];
    if (t[4] || sc_geq_n(r)) sc_sub_n(r);
}

// sc_mont_inv: r = 1/a in Montgomery form (a in Montgomery form, nonzero),
// as a^(n-2) with a 4-bit fixed window.
static void sc_mont_inv(uint64_t r[4], const uint64_t a[4]) {
    static const uint64_t E[4] = {0xBFD25E8CD036413FULL, 0xBAAEDCE6AF48A03BULL, 0xFFFFFFFFFFFFFFFEULL,
                                  0xFFFFFFFFFFFFFFFFULL};  // n - 2
    uint64_t tab[16][4];
    // tab[0] = 1 in Montgomery form = 2^256 mod n.
    tab[0][0] = ~N[0] + 1;
    tab[0][1] = ~N[1];
    tab[0][2] = ~N[2];
    tab[0][3] = ~N[3];
    memcpy(tab[1], a, 32);
    for (int i = 2; i < 16; i++) sc_mont_mul(tab[i], tab[i - 1], a);
    uint64_t acc[4];
    memcpy(acc, tab[(E[3] >> 60) & 15], 32);
    for (int pos = 252 - 4; pos >= 0; pos -= 4) {
        sc_mont_mul(acc, acc, acc);
        sc_mont_mul(acc, acc, acc);
        sc_mont_mul(acc, acc, acc);
        sc_mont_mul(acc, acc, acc);
        unsigned d = (unsigned)(E[pos >> 6] >> (pos & 63)) & 15;
        if (d) sc_mont_mul(acc, acc, tab[d]);
    }
    memcpy(r, acc, 32);
}

// sc_bits returns w bits of k from bit b (bits past 255 are 0).
static inline unsigned sc_bits(const uint64_t k[4], int b, int w) {
    int limb = b >> 6, sh = b & 63;
    if (limb >= 4) return 0;
    uint64_t v = k[limb] >> sh;
    if (sh + w > 64 && limb + 1 < 4) v |= k[limb + 1] << (64 - sh);
    return (unsigned)(v & ((1u << w) - 1));
}

// sc_recode writes m signed base-2^w digits of k, each in
// [-(2^(w-1) - 1), 2^(w-1)], with sum d[i] 2^(wi) = k, to d[i*8].
static inline void sc_recode(int8_t *d, const uint64_t k[4], int w, int m) {
    int carry = 0;
    for (int i = 0; i < m; i++) {
        int v = (int)sc_bits(k, i * w, w) + carry;
        if (v > (1 << (w - 1))) {
            v -= 1 << w;
            carry = 1;
        } else {
            carry = 0;
        }
        d[i * 8] = (int8_t)v;
    }
}

// ---------------------------------------------------------------------------
// Tables and the recovery kernel.

#define WR 5                       // u2*R window
#define NR ((257 + WR - 1) / WR)   // u2*R digits
#define TR (1 << (WR - 1))         // u2*R table entries: 1R..TR R
#define WG 7                       // u1*G window
#define NG ((257 + WG - 1) / WG)   // u1*G digits (windows)
#define TG (1 << (WG - 1))         // u1*G entries per window

// gtab[i][j] = (j+1) 2^(WG i) G, affine, canonical 52-bit limbs x then y.
static uint64_t gtab[NG][TG][10] __attribute__((aligned(64)));

static const uint8_t GX[32] = {0x79, 0xBE, 0x66, 0x7E, 0xF9, 0xDC, 0xBB, 0xAC, 0x55, 0xA0, 0x62,
                               0x95, 0xCE, 0x87, 0x0B, 0x07, 0x02, 0x9B, 0xFC, 0xDB, 0x2D, 0xCE,
                               0x28, 0xD9, 0x59, 0xF2, 0x81, 0x5B, 0x16, 0xF8, 0x17, 0x98};
static const uint8_t GY[32] = {0x48, 0x3A, 0xDA, 0x77, 0x26, 0xA3, 0xC4, 0x65, 0x5D, 0xA4, 0xFB,
                               0xFC, 0x0E, 0x11, 0x08, 0xA8, 0xFD, 0x17, 0xB4, 0x48, 0xA6, 0x85,
                               0x54, 0x19, 0x9C, 0x47, 0xD0, 0x8F, 0xFB, 0x10, 0xD4, 0xB8};

static inline void fe_set1(fe *r) {
    r->l[0] = bc(1);
    for (int i = 1; i < 5; i++) r->l[i] = _mm512_setzero_si512();
}

// gej_to_ge converts to affine (canonical limbs). Z must be nonzero.
static void gej_to_ge(ge *r, const gej *a) {
    fe zi, zi2, zi3;
    fe_inv(&zi, &a->z);
    fe_sqr(&zi2, &zi);
    fe_mul(&zi3, &zi2, &zi);
    fe_mul(&r->x, &a->x, &zi2);
    fe_mul(&r->y, &a->y, &zi3);
    fe_full(&r->x);
    fe_full(&r->y);
}

void n42_x8_init(void) {
    // Bases B_i = 2^(WG i) G, computed in lane 0 of a vector (all lanes the
    // same), kept affine.
    static ge bases[NG];
    uint64_t gw[4], gl[8][5];
    b32_to_w(gw, GX);
    w_to_l52(gl[0], gw);
    for (int i = 1; i < 8; i++) memcpy(gl[i], gl[0], sizeof gl[0]);
    fe_from_lanes(&bases[0].x, (const uint64_t(*)[5])gl);
    b32_to_w(gw, GY);
    w_to_l52(gl[0], gw);
    for (int i = 1; i < 8; i++) memcpy(gl[i], gl[0], sizeof gl[0]);
    fe_from_lanes(&bases[0].y, (const uint64_t(*)[5])gl);
    for (int i = 1; i < NG; i++) {
        gej t;
        t.x = bases[i - 1].x;
        t.y = bases[i - 1].y;
        fe_set1(&t.z);
        for (int k = 0; k < WG; k++) gej_dbl(&t, &t);
        gej_to_ge(&bases[i], &t);
    }
    // Eight windows per vector: lane k holds window i0 + k.
    for (int i0 = 0; i0 < NG; i0 += 8) {
        ge b;
        uint64_t lx[8][5], ly[8][5];
        for (int k = 0; k < 8; k++) {
            int i = i0 + k < NG ? i0 + k : NG - 1;
            uint64_t tx[8][5], ty[8][5];
            fe_to_lanes(tx, &bases[i].x);
            fe_to_lanes(ty, &bases[i].y);
            memcpy(lx[k], tx[0], sizeof lx[k]);
            memcpy(ly[k], ty[0], sizeof ly[k]);
        }
        fe_from_lanes(&b.x, (const uint64_t(*)[5])lx);
        fe_from_lanes(&b.y, (const uint64_t(*)[5])ly);
        gej acc;
        acc.x = b.x;
        acc.y = b.y;
        fe_set1(&acc.z);
        for (int j = 0; j < TG; j++) {
            if (j == 1) {
                gej_dbl(&acc, &acc);
            } else if (j > 1) {
                gej_add_ge(&acc, &acc, &b);
            }
            ge a;
            gej_to_ge(&a, &acc);
            uint64_t ax[8][5], ay[8][5];
            fe_to_lanes(ax, &a.x);
            fe_to_lanes(ay, &a.y);
            for (int k = 0; k < 8 && i0 + k < NG; k++) {
                memcpy(&gtab[i0 + k][j][0], ax[k], 40);
                memcpy(&gtab[i0 + k][j][5], ay[k], 40);
            }
        }
    }
}

// Per-lane inputs of one group of eight.
typedef struct {
    uint64_t x[8][5];                         // r as a field element
    uint8_t odd[8];                           // wanted y parity
    int8_t dr[NR][8] __attribute__((aligned(8)));  // u2 digits
    int8_t dg[NG][8] __attribute__((aligned(8)));  // u1 digits
    uint8_t active[8];
} group_in;

// Variable-base table: rt[e][coord][limb][lane], e = 0..TR-1 holds (e+1)R.
typedef struct {
    uint64_t q[TR][3][5][8] __attribute__((aligned(64)));
} rtable;

INLINE v8 load_digits(const int8_t d[8]) {
    return _mm512_cvtepi8_epi64(_mm_loadl_epi64((const __m128i *)d));
}

static void rt_store(rtable *t, int e, const gej *p) {
    for (int k = 0; k < 5; k++) {
        _mm512_store_si512((void *)t->q[e][0][k], p->x.l[k]);
        _mm512_store_si512((void *)t->q[e][1][k], p->y.l[k]);
        _mm512_store_si512((void *)t->q[e][2][k], p->z.l[k]);
    }
}

// rt_gather loads, per lane, entry idx[lane] of the table.
static inline void rt_gather(gej *p, const rtable *t, v8 idx) {
    const v8 lane = _mm512_set_epi64(7, 6, 5, 4, 3, 2, 1, 0);
    v8 vi = vadd(_mm512_mullo_epi64(idx, bc(3 * 5 * 8)), lane);
    const long long *base = (const long long *)t->q;
    for (int k = 0; k < 5; k++) {
        p->x.l[k] = _mm512_i64gather_epi64(vi, base + (0 * 5 + k) * 8, 8);
        p->y.l[k] = _mm512_i64gather_epi64(vi, base + (1 * 5 + k) * 8, 8);
        p->z.l[k] = _mm512_i64gather_epi64(vi, base + (2 * 5 + k) * 8, 8);
    }
}

// g_gather loads, per lane, entry idx[lane] of window w of gtab.
static inline void g_gather(ge *p, int w, v8 idx) {
    v8 vi = _mm512_mullo_epi64(idx, bc(10));
    const long long *base = (const long long *)gtab[w];
    for (int k = 0; k < 5; k++) {
        p->x.l[k] = _mm512_i64gather_epi64(vi, base + k, 8);
        p->y.l[k] = _mm512_i64gather_epi64(vi, base + 5 + k, 8);
    }
}

static void recover_group(const group_in *in, uint8_t pubs[8][65], uint8_t ok[8]) {
    const v8 one = bc(1);
    __mmask8 good = 0;
    for (int i = 0; i < 8; i++) good |= (__mmask8)((in->active[i] ? 1 : 0) << i);

    // R = (x, y): y^2 = x^3 + 7, y of the wanted parity.
    ge R;
    fe_from_lanes(&R.x, in->x);
    fe t;
    fe_sqr(&t, &R.x);
    fe_mul(&t, &t, &R.x);
    t.l[0] = vadd(t.l[0], bc(7));
    fe_norm(&t);
    good &= fe_sqrt(&R.y, &t);
    fe_full(&R.y);
    {
        uint64_t odd[8] __attribute__((aligned(64)));
        for (int i = 0; i < 8; i++) odd[i] = in->odd[i];
        __mmask8 flip =
            _mm512_cmpneq_epi64_mask(vand(R.y.l[0], one), _mm512_load_si512((const void *)odd));
        fe_neg_blend(&R.y, flip);
    }

    // Table 1R..TR R.
    rtable tbs;
    rtable *tb = &tbs;
    gej p;
    p.x = R.x;
    p.y = R.y;
    fe_set1(&p.z);
    rt_store(tb, 0, &p);
    gej_dbl(&p, &p);
    rt_store(tb, 1, &p);
    for (int e = 2; e < TR; e++) {
        gej_add_ge(&p, &p, &R);
        rt_store(tb, e, &p);
    }

    // acc = u2 R, top digit first.
    gej acc, q, s;
    __mmask8 inf = 0xFF;
    for (int i = NR - 1; i >= 0; i--) {
        if (i != NR - 1) {
            for (int k = 0; k < WR; k++) gej_dbl(&acc, &acc);
        }
        v8 d = load_digits(in->dr[i]);
        __mmask8 nz = _mm512_test_epi64_mask(d, d);
        if (!nz) continue;
        __mmask8 neg = _mm512_movepi64_mask(d);
        v8 a = _mm512_abs_epi64(d);
        v8 idx = _mm512_max_epi64(vsub(a, one), _mm512_setzero_si512());
        rt_gather(&q, tb, idx);
        fe_neg_blend(&q.y, neg);
        if (inf == 0xFF) {
            acc = q;
        } else {
            gej_add(&s, &acc, &q);
            gej_blend(&acc, nz & ~inf, &s);
            gej_blend(&acc, nz & inf, &q);
        }
        inf &= ~nz;
    }
    __mmask8 infR = inf;

    // accg = u1 G.
    gej accg;
    inf = 0xFF;
    for (int i = 0; i < NG; i++) {
        v8 d = load_digits(in->dg[i]);
        __mmask8 nz = _mm512_test_epi64_mask(d, d);
        if (!nz) continue;
        __mmask8 neg = _mm512_movepi64_mask(d);
        v8 a = _mm512_abs_epi64(d);
        v8 idx = _mm512_max_epi64(vsub(a, one), _mm512_setzero_si512());
        ge g;
        g_gather(&g, i, idx);
        fe_neg_blend(&g.y, neg);
        gej gj;
        gj.x = g.x;
        gj.y = g.y;
        fe_set1(&gj.z);
        if (inf == 0xFF) {
            accg = gj;
        } else {
            gej_add_ge(&s, &accg, &g);
            gej_blend(&accg, nz & ~inf, &s);
            gej_blend(&accg, nz & inf, &gj);
        }
        inf &= ~nz;
    }
    __mmask8 infG = inf;

    // Q = acc + accg.
    if (infG != 0xFF) {
        if (infR == 0xFF) {
            acc = accg;
        } else {
            gej_add(&s, &acc, &accg);
            gej_blend(&s, infG, &acc);
            gej_blend(&s, infR, &accg);
            acc = s;
        }
    }
    good &= ~(infR & infG);
    good &= ~fe_is_zero(&acc.z);
    if (!good) {
        memset(ok, 0, 8);
        return;
    }
    ge out;
    gej_to_ge(&out, &acc);
    uint64_t ox[8][5], oy[8][5];
    fe_to_lanes(ox, &out.x);
    fe_to_lanes(oy, &out.y);
    for (int i = 0; i < 8; i++) {
        if (!((good >> i) & 1)) {
            ok[i] = 0;
            continue;
        }
        pubs[i][0] = 4;
        l52_to_b32(&pubs[i][1], ox[i]);
        l52_to_b32(&pubs[i][33], oy[i]);
        ok[i] = 1;
    }
}

#define CHUNK 64

static void recover_chunk(size_t n, const uint8_t *msgs, const uint8_t *sigs, uint8_t *pubs, uint8_t *ok) {
    uint64_t r[CHUNK][4], s[CHUNK][4], z[CHUNK][4], rm[CHUNK][4], pre[CHUNK][4];
    uint8_t valid[CHUNK];
    for (size_t i = 0; i < n; i++) {
        const uint8_t *sig = sigs + 65 * i;
        b32_to_w(r[i], sig);
        b32_to_w(s[i], sig + 32);
        b32_to_w(z[i], msgs + 32 * i);
        if (sc_geq_n(z[i])) sc_sub_n(z[i]);
        valid[i] = sig[64] < 2 && !sc_is_zero(r[i]) && !sc_geq_n(r[i]) && !sc_is_zero(s[i]) && !sc_geq_n(s[i]);
    }
    // 1/r for every valid lane: one Montgomery batch inversion. pre[i] is
    // the product of the valid lanes before i.
    uint64_t accm[4];
    int first = -1;
    for (size_t i = 0; i < n; i++) {
        if (!valid[i]) continue;
        sc_mont_mul(rm[i], r[i], NR2);
        if (first < 0) {
            memcpy(accm, rm[i], 32);
            first = (int)i;
        } else {
            memcpy(pre[i], accm, 32);
            sc_mont_mul(accm, accm, rm[i]);
        }
    }
    if (first >= 0) {
        uint64_t inv[4];
        sc_mont_inv(inv, accm);
        for (size_t i = n - 1; i > (size_t)first; i--) {
            if (!valid[i]) continue;
            uint64_t ri[4];
            sc_mont_mul(ri, inv, pre[i]);
            sc_mont_mul(inv, inv, rm[i]);
            memcpy(rm[i], ri, 32);
        }
        memcpy(rm[first], inv, 32);
    }
    for (size_t g = 0; g < n; g += 8) {
        group_in in;
        memset(&in, 0, sizeof in);
        size_t m = n - g < 8 ? n - g : 8;
        for (size_t k = 0; k < 8; k++) {
            if (k >= m || !valid[g + k]) {
                in.x[k][0] = 1;
                continue;
            }
            size_t i = g + k;
            in.active[k] = 1;
            in.odd[k] = sigs[65 * i + 64] & 1;
            w_to_l52(in.x[k], r[i]);
            uint64_t u1[4], u2[4];
            sc_mont_mul(u1, z[i], rm[i]);  // z/r
            sc_neg(u1, u1);
            sc_mont_mul(u2, s[i], rm[i]);  // s/r
            sc_recode(&in.dr[0][k], u2, WR, NR);
            sc_recode(&in.dg[0][k], u1, WG, NG);
        }
        uint8_t gp[8][65], gok[8];
        recover_group(&in, gp, gok);
        for (size_t k = 0; k < m; k++) {
            ok[g + k] = gok[k];
            if (gok[k]) memcpy(pubs + 65 * (g + k), gp[k], 65);
        }
    }
}

void n42_x8_recover(size_t n, const uint8_t *msgs, const uint8_t *sigs, uint8_t *pubs, uint8_t *ok) {
    for (size_t off = 0; off < n; off += CHUNK) {
        size_t m = n - off < CHUNK ? n - off : CHUNK;
        recover_chunk(m, msgs + 32 * off, sigs + 65 * off, pubs + 65 * off, ok + off);
    }
}

#if defined(__clang__)
#pragma clang attribute pop
#elif defined(__GNUC__)
#pragma GCC pop_options
#endif
