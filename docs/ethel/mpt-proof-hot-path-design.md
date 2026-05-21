# MPT Proof Hot-Path Optimization — Hashed-Key Index Design

**Date:** 2026-05-21
**Status:** Design proposed, no code yet — awaiting decision on storage tradeoff
**Predecessor:** [`mpt-proof-system.md`](mpt-proof-system.md)

---

## 1. Problem

The current `FullAccountProofBytes` for USDC takes **15 minutes wall** against production data. Every inline sibling at the deepest branch (4× for USDC) triggers a full `ScanAccounts` over reth's 386M-row `PlainAccountState` table. The per-scan cost is ~3-4 min on NVMe; sequential, not concurrent.

Profiling-by-arithmetic: with 386M accounts and a 6-nibble prefix, the expected number of matching leaves is `386M / 16^6 ≈ 23`. We are doing a 386M-row scan to return 23 rows. **The bottleneck is exclusively missing index.**

---

## 2. Solution shape

Add a **hashed-key-sorted index** alongside the existing MPT cache, in the same `D:\n42-chaindata` MDBX env so updates are atomic with MPT updates:

```
chaindata.mdbx/
  AccountsTrie     ← existing (BranchNodeCompact, ~37 GB)
  StoragesTrie     ← existing
  Meta             ← existing
  HashedAccount    ← NEW: key = keccak256(addr) 32B  → accountRLP
  HashedStorage    ← NEW: key = keccak256(addr) || keccak256(slot) 64B → value
```

Then `collectAccountLeavesWithPrefix(prefix []byte)` becomes:

```go
seekKey := nibblesToBytesRoundDown(prefix)         // e.g. 6 nibbles → 3 bytes
c, _ := tx.Cursor("HashedAccount")
for k, v, err := c.Seek(seekKey); k != nil && err == nil; k, v, err = c.Next() {
    hn := nibblesOf(k)
    if !bytes.HasPrefix(hn, prefix) { break }
    out = append(out, subLeaf{ effectiveKey: stripPrefix(hn, prefix), value: v })
}
```

Cost: **O(matches + log N)** instead of **O(N)**.

---

## 3. Expected speedup

| Operation | Current (full scan) | With index | Speedup |
|---|---|---|---|
| `collectAccountLeavesWithPrefix(6-nibble)` | ~4 min | ~5 ms | ~50,000× |
| Full USDC account proof (4 inline siblings) | 15m26s | ~50–100 ms | ~10,000× |
| Single-slot storage proof | minutes | ~10–20 ms | similar |

The 5 ms estimate is conservative: MDBX `Seek + 23 × Next` is dominated by 4 KB page reads. ~6 pages touched, each ~50 µs cold NVMe + 0 µs warm. With page cache warm (production server), <1 ms.

---

## 4. Storage cost tradeoff

### Option A — Full hashed account index, value-by-reference storage index

| Table | Rows | Key size | Value | Total |
|---|---|---|---|---|
| HashedAccount | 386M | 32B | accountRLP (~70B avg) | ~38 GB |
| HashedStorage | ~2B | 64B | `addr20 ‖ slot32` (52B) → plain re-lookup | ~230 GB |

Storage proof lookups do one extra random read into reth's `PlainStorageState` per leaf to fetch the value. At ~24 storage leaves per prefix × ~10 µs random read = ~240 µs added — negligible.

### Option B — Full hashed indices (value embedded)

| Table | Rows | Total |
|---|---|---|
| HashedAccount | 386M | ~38 GB |
| HashedStorage | ~2B | ~290 GB |

Self-contained — no plain-state dependency at proof time.

### Option C — Per-account storage index (deferred until Phase A.5)

If/when we move to per-account storage tries (EIP-1186 strict mode), each account has its own storage hashed index keyed by `keccak(slot)` only. Total storage shrinks to ~135 GB (saves the redundant keccak(addr) prefix). But this requires committing Phase A.5 first.

**Recommendation: Option A.** Account index is small enough to be unconditional. Storage index pays a tiny per-leaf re-lookup cost in exchange for ~60 GB savings. Total archive grows from 226 GB → ~494 GB (Option A) or ~554 GB (Option B). Both well below the 2.14 TB reth-archive reference.

---

## 5. Build path

### Bootstrap (one-time, ~5–8 h estimated)

Same 3-pass pipeline as Phase A's MPT builder, but a much simpler middle stage (no HashBuilder, just sort + AppendDup):

```
n42-mpt-hashedindex \
  --src D:\reth2k\db \
  --dst D:\n42-chaindata \
  --tables HashedAccount,HashedStorage
```

Per-table:
1. Cursor-scan source plain table → emit `keccak(plainKey)||originalKey` records into ETL collector.
2. ETL external-sort by hashed-key.
3. AppendDup load into destination table.

Expected: account ~1 h, storage ~5 h on this hardware. Same write pattern as Phase A so the timing should track.

### Catch-up & 12-sec sync

Each block-execution writer that touches the MPT cache **also** writes to the hashed index in the same MDBX tx. The increment is trivial — for each plain-state mutation `(plainKey, oldVal, newVal)`:

```go
hashed := keccak(plainKey)
if newVal == nil {
    tx.Delete("Hashed...", hashed)
} else {
    tx.Put("Hashed...", hashed, newVal)
}
```

Atomicity with the MPT update is automatic since they share the tx. No extra coordination.

### Validation

Cross-check by full scan after bootstrap: every account in `PlainAccountState` must have its `keccak(addr)` present in `HashedAccount` with byte-equal value. Same for storage. Bootstrap rejects on mismatch.

---

## 6. Migration / opt-in path

1. **Bootstrap tool** (`cmd/n42-mpt-hashedindex`) — builds the two new tables into an existing chaindata env. Idempotent: skips if already present and `--rebuild` not set.
2. **`mptproof.RethLeafSource` keeps the slow path** — used by `n42-proof-debug` for verification, and as a fallback when the hashed index is absent.
3. **`mptproof.HashedLeafSource`** (NEW) — implements the same `LeafSource` interface but reads from the hashed tables. Selected via `Config.HashedIndexDir` (typically same as `ChaindataDir`).
4. **No API change** — `Generator.FullAccountProofBytes` etc. work unchanged; only the leaf source under the hood is faster.

```go
g, _ := mptproof.New(mptproof.Config{
    ChaindataDir:    "D:\\n42-chaindata",  // contains MPT + hashed indices
    HashedIndexDir:  "D:\\n42-chaindata",  // same env; sets HashedLeafSource
    HistoryDir:      "D:\\n42-history-full",
    Leaves:          fallback,             // RethLeafSource for absent-index fallback
})
```

---

## 7. Open decisions (need answer before code)

1. **Option A vs B** — pay ~60 GB to embed storage values, or do a re-lookup per leaf?
2. **Bootstrap concurrency** — single-pass per table is the safe default. Concurrent account+storage bootstrap saturates disk I/O but doubles peak RAM (two ETL collectors). Acceptable on the build machine (128 GB)?
3. **Index lifetime** — does the hashed index get *snapshot-replicated* with the MPT cache to other nodes, or is it always locally rebuilt? Locally rebuilt is simpler; snapshot-replicated cuts new-node spin-up time by ~6 h.

---

## 8. Risks

- **Build-time disaster window**: bootstrap is single-pass and atomic per table, but a crash mid-bootstrap requires `--rebuild`. Mitigation: write a `Meta:HashedAccount.complete=true` marker only after the table-level COMMIT succeeds. On restart, missing marker → rebuild.
- **MDBX env size**: with hashed indices added, the chaindata env grows toward ~280 GB. Current Phase A defaults the env at 4 TB so we have headroom. Confirm.
- **PR slot order**: storage tables in MDBX have a max page fill bound; we'll need to validate AppendDup gives the same ~95% fill on a 64-byte key as on the 32-byte MPT keys.

---

## 9. Decision request

This document is the design surface. Next steps depend on your call:

- **Approve Option A + start bootstrap tool** → I'll write `cmd/n42-mpt-hashedindex` + `internal/mptproof/hashed.go` and run the account-only bootstrap as a first-pass smoke (~1 h) to confirm the speedup model before committing to the larger storage bootstrap.
- **Want Option B for proof-self-contained reasons** → same path, the value-embed change is a one-line difference.
- **Want different storage scoping (per-account tries)** → that's Phase A.5, much larger surface.
- **Defer** → no action, current 15-min slow path remains correct.
