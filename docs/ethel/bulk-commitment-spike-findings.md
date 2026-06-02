# Bulk-commitment spike — findings & direction

**Branch:** feat/bulk-commitment-spike (isolated worktree C:/N42/bulk-spike)
**Goal asked:** reth-style bulk commitment re-architecture to speed up anchor production.

## Finding 1 — bulk commitment domain ≠ bpp build speedup

The `docs/ethel/commitment-domain-plan.md` (8-11 weeks) targets **proof speed +
storage size + live per-block updates** — NOT the per-block trie BUILD speed that
bottlenecks `n42-stateless-blockproof-produce` (bpp).

- The reth-format bulk builder (`internal/mptbuild`, 3-pass scan→ETL→HashBuilder,
  ~2897 blk/s) produces ONE final-state trie. bpp needs the trie at EACH anchor's
  n-1 (sequential per-block intermediate states). Bulk does not produce intermediates.
- The plan's G3 "per-block live updater" still runs one HPH `ComputeCommitment` per
  block — same per-block cost bpp already pays.
- bpp's measured bottleneck (block ~12.4M): per K=10000 window ~1m20s, ~95% in
  `ComputeRoot(window net)` over ~2.4M dirty keys, single-threaded, ~0.44 core
  keccak + ~0.55 core MDBX random read-modify-write. After sorted leaf writes
  (committed), the remaining I/O is RANDOM READS of TrieOf* along scattered dirty
  paths (walk order is fixed by trie shape — cannot be sorted). Bulk sequential
  scan conflicts with the per-anchor-intermediate requirement.

Conclusion: a bulk/commitment-domain re-architecture does not accelerate bpp's
per-block-sequential build. The DB also outgrows RAM (~82 GB @ 12.5M → ~218 GB @
25M vs 126 GB RAM) around block ~16-18M, making the tail disk-bound regardless.

## Finding 2 — the real reth-style move: snapshot-direct, not per-block anchors

reth/erigon minimal clients do NOT produce single-block transition anchors. They:
download a recent state snapshot → verify it against a recent header.stateRoot →
follow forward with execution witnesses. The per-block transition anchor (bpp) is
an N42-specific design that is expensive precisely because it needs per-block tries.

## Recommended direction — recent anchors from the existing bulk snapshot

`D:/N42-hashed` is already a bulk-built trie at block 25,191,536 (218 GB,
mainnet-aligned). The minimal client's valuable anchors are the RECENT ones. So:

1. Base = D:/N42-hashed (no genesis rebuild).
2. Bounded UNWIND with reverse V2 changesets (ethexec rollback applies OLD values)
   from 25.19M back to, e.g., 24.0M (~1.19M blocks).
3. Forward-produce anchors 24.0M → 25.1M (~1.1M blocks), reusing bpp's now-fast
   batched+sorted path.

Total work ≈ 2.3M block-applies vs 25M from genesis — ~10× less for the
recent-anchor deliverable, and it reuses the existing bulk artifact. This is the
genuine "reth-style: snapshot as base, bounded incremental" win.

## Open decision

- (A) Keep the full 0→25.1M batched+sorted build running (~7×, ~35-45h, durable),
  deliver the complete historical anchor set.
- (B) Pivot the spike to "recent anchors from D:/N42-hashed via bounded unwind"
  (fast, matches the minimal-client use case, reuses the bulk snapshot).
- (C) Full commitment-domain plan (8-11 weeks) — only if proof-speed / live-update
  / storage-size are the actual goals, not anchor build speed.

Recommendation: (A) is already banked and running; (B) is the right reth-style
spike if recent-only anchors suffice; (C) is out of scope for anchor build speed.
