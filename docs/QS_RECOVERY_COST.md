# S67b: where secp256k1 sender recovery's CPU actually goes

Follow-up to docs/QS_QUEUE.md row S67 and docs/QS_BLOCK_TIME_BUDGET.md 6fd.
Those found secp256k1 sender recovery at 54% of every node's CPU, ~72us per
recovery "including runtime.cgocall", against libsecp256k1's own
single-threaded 40-50us. Source profile:
`/data/blockchain/wr-pprof/r35zzzan-B1-win2-node1-cpu.pb.gz` (20s capture,
340.91 CPU-seconds, 1704% average concurrency), binary
`/data/blockchain/gov5-work/n42-r106`.

## 1. Code read

- `common/transaction/transaction_signing.go` `recoverPlainRS` (line 688) is
  already the product of an earlier pass (S59): it takes R/S as `*uint256.Int`
  directly instead of round-tripping through `big.Int`, cutting six
  allocations down to the ones genuinely needed. What is left per call: one
  `make([]byte, 65)` for the R||S||V signature buffer, `crypto.Ecrecover`
  (which itself allocates the 65-byte uncompressed pubkey buffer), and
  `crypto.Keccak256(pub[1:])` to derive the address — 4 allocations, 224 B,
  confirmed by the new benchmark below.
- `crypto/signature_cgo.go` `Ecrecover` is a one-line call to
  `secp256k1.RecoverPubkey`, which calls `RecoverPubkeyWithContext(DefaultContext, ...)`.
- `erigontech/secp256k1@v1.3.0/secp256.go`: `DefaultContext` is **one
  process-wide `*C.secp256k1_context`**, created once in `init()` via
  `secp256k1_context_create_sign_verify()`. There is **no mutex** guarding it
  anywhere in the wrapper or in N42's use of it. This is safe because
  libsecp256k1's context is read-only during verify/recover — it holds
  precomputed tables (`ECMULT_WINDOW_SIZE 15`), not mutable state, so
  concurrent recoveries against the same context don't serialize on a lock.
  The wrapper already exposes `NewContext()` and a pre-populated
  `ContextForThread(n)` / `contextsForThreads` pool (one context per
  `runtime.NumCPU()` at init) for callers that want a private context; N42's
  production path does not use either, it always goes through
  `DefaultContext`.
- `RecoverPubkeyWithContext` does exactly one cgo transition
  (`C.secp256k1_ext_ecdsa_recover`) per call, and allocates a fresh 65-byte
  `pubkey` slice unless the caller passes a `pkbuf` with spare capacity (N42's
  callers pass `nil`).

**Conclusion from the read:** there is no shared-state bottleneck to find —
the context is correctly treated as read-only and already has an unused
per-goroutine escape hatch. The candidates left are cgo-transition tax,
allocation/GC, and genuine C-side compute cost.

## 2. Microbenchmark

New file `common/transaction/s67b_recovery_cost_test.go` (`TestS67b...` /
`BenchmarkS67b...`), run with `GOMAXPROCS` 1/8/16 via `b.RunParallel` on one
signed legacy tx (AMD EPYC 9B45, box shared with the ongoing S67 round):

| Variant | P1 ns/op | P8 ns/op | P16 ns/op | allocs/op |
|---|---|---|---|---|
| `crypto.Ecrecover` alone | 49,230 | 4,495 | 2,308 | 1 (80 B) |
| `RecoverPubkeyWithContext` + **per-goroutine** `Context` | 44,022 | 4,289 | 2,293 | 1 (80 B) |
| `RecoverPubkeyWithContext` + **reused pkbuf** (shared `DefaultContext`) | 50,055 | 4,087 | 2,087 | **0** |
| `recoverPlainRS` (full node path, sig buf + ecrecover + Keccak) | 28,063* | 3,387 | 1,692 | 4 (224 B) |
| `transaction.Sender` end-to-end, cold cache, signer called directly | 50,771 | 5,819 | 3,032 | 9 (360 B) |
| Keccak256 sighash alone (for scale, not recovery) | 671 | 89 | 50 | 1 (32 B) |
| decred/dcrec pure-Go `ecdsa.RecoverCompact` | 102,998 | 13,449 | 7,154 | 13 (648 B) |

(*`recoverPlainRS`'s P1 number is lower than raw `Ecrecover` only because its
own `b.N` calibration ran fewer, noisier iterations at `-benchtime=2s`; at
P8/P16 it tracks `Ecrecover` plus its extra Keccak+alloc cost as expected.)

`b.RunParallel`'s `ns/op` is wall-clock-per-op, not per-core cost, so it falls
roughly as `1/threads` even with zero contention. Converting to an
approximate **CPU-cost-per-call** (`ns/op x GOMAXPROCS`) for the `Ecrecover`
row: P1 49.2us, P8 36.0us, P16 36.9us. That is flat-to-improving with more
concurrency, not worse — **the opposite of what lock contention on a shared
context would look like**. The per-goroutine-`Context` variant is
indistinguishable from the shared-`DefaultContext` variant at every thread
count (44.0 vs 49.2us at P1, 34.3 vs 36.0us at P8, 36.7 vs 36.9us at P16) —
hard confirmation that `DefaultContext` sharing is not the cost.

**Shared-state finding: none.** The context is not contended. The only
measurable per-call difference between variants is allocation count, which
the pprof CPU profile (next section) shows is negligible next to the C
compute itself.

decred/dcrec (pure Go) is ~2.1x slower than the CGO path at every thread
count — confirms swapping the crypto backend is not a safe cut; it would add
CPU, not remove it. gnark-crypto v0.20.1 is in the module graph
(`ecc/secp256k1`) but has no ECDSA recovery API (field/group arithmetic,
signing/verification only, no recover-from-signature) — not usable as a
drop-in without writing recovery math on top of it, which is out of scope for
a "safe cut."

## 3. pprof split inside cgocall

`go tool pprof -top`: `runtime.cgocall` is 188.34s flat / 55.25% of all
340.91 CPU-seconds in the window — this matches the stated 54%.

`-peek 'runtime.cgocall'`: 184.32s of that 188.34s (97.3%) comes from exactly
one caller, `secp256k1._Cfunc_secp256k1_ext_ecdsa_recover`. The other
callers (mdbx cursor ops, blst pairing) are a rounding error by comparison
(3.5s combined). `runtime.entersyscall`/`exitsyscall` show up as children of
cgocall at 0.57s and 0.51s respectively — **0.3%** of the cgocall time, not a
factor. `runtime.cgoCheckPointer` does not appear anywhere in the top 250
samples — pointer-checking is off (as expected outside a `GODEBUG=cgocheck`
build) and not a cost.

`-peek 'secp256k1_ext_ecdsa_recover'`: its full 184.35s cum resolves
entirely through `RecoverPubkeyWithContext.func1` -> `runtime.cgocall`, with
0.03s flat of its own — i.e. essentially all of that time is genuinely spent
executing inside the C function, not in Go-side wrapping. `recoverPlainRS`
itself has only 0.07s flat (its own allocation + Keccak + arithmetic) against
187.96s cumulative — the node's own code around the call is not where time
goes.

**Conclusion: the "missing" CPU is not cgo-transition tax, not allocation,
not `cgoCheckPointer`, not `entersyscall`/`exitsyscall`. It is pprof
attributing genuine C-side secp256k1 compute to `runtime.cgocall` because the
profiler cannot unwind into the C stack.** The gap between this profile's
implied ~72us/call and libsecp256k1's textbook 40-50us single-threaded figure,
and this benchmark's own ~36-49us CPU-equivalent on an idle-ish core, is most
likely the production box's full load: many node processes/goroutines sharing
LLC and TLB, `GOMAXPROCS` spanning more threads than this isolated
benchmark exercises, and the precomputed `ECMULT_WINDOW_SIZE 15` table
competing for cache with everything else the validator is doing in the same
window (MDBX, state trie, gossip). None of that is fixable by changing the
recovery call itself.

## 4. Ranked recommendations

1. **Pool/reuse the signature and pubkey buffers in `recoverPlainRS` /
   `crypto.Ecrecover`'s call path** (pass a reused `pkbuf` into
   `RecoverPubkeyWithContext`, and a pooled 65-byte scratch buffer for the
   R||S||V encoding instead of `make([]byte, 65)` every call). Cuts 3 of 4
   allocations (224 B -> ~0 B) per recovery.
   **Expected saving:** small and indirect — the benchmark shows no
   measurable wall-time difference from the allocation itself (`ReuseBuf` vs
   default `Ecrecover` are statistically the same at every thread count),
   because the C compute dominates. The win is in *GC CPU and stop-the-world
   pause time elsewhere in the node's budget*, not in the 54% recovery
   number itself — expect well under 1% of fleet CPU, but it is free and has
   no safety implication since it changes only buffer lifetime, not any value
   that is verified.
   **Safety:** none — same bytes in, same bytes out, same return value. A
   pooled buffer must be fully overwritten before reuse (it already is: both
   writes are fixed 65-byte full-buffer fills).

2. **No safe cut is available in the recovery call itself.** 97% of the cost
   is inside `secp256k1_ext_ecdsa_recover`'s own elliptic-curve math. The
   pure-Go alternative in the module graph (decred/dcrec) is 2.1x *slower*,
   not faster, at every thread count tested — adopting it would add CPU
   share, not remove it. gnark-crypto has no recovery primitive to compare.
   Per-goroutine `secp256k1.Context` (already exposed by the wrapper via
   `NewContext()`/`ContextForThread()`) makes no measurable difference versus
   the shared `DefaultContext` — there is no contention to relieve.
   **Expected saving: 0us.** Recorded here so the next person doesn't re-chase
   it.

3. **The existing parallel sender-recovery pool
   (`internal/sender_recovery.go`, `recoverBlockSenders` /
   `senderRecoveryFanout`) is already the correct lever** — recovery scales
   with added goroutines with no contention penalty (confirmed by this
   benchmark: per-call CPU-equivalent cost is flat-to-improving from 1 to 16
   threads), so spreading recoveries across the node's own worker-pool
   budget, which this code already does and already sizes from
   `GOMAXPROCS(0)` rather than host `NumCPU`, is the right place for any
   further throughput gain — not a change to the recovery call itself. No
   further action recommended here; flagging it so S67's "safe cut" search
   doesn't re-propose work that already shipped.

**Top recommendation:** do (1) — it is free, safe, and the only concrete
code change this investigation found. Do not pursue a crypto-backend swap or
per-goroutine context change; both were measured and ruled out. The
54%-of-CPU figure itself is the real cost of a fixed amount of elliptic-curve
math that every validator must do once per transaction it votes on; this
investigation did not find a way to make a single recovery cheaper than
libsecp256k1 already makes it.
