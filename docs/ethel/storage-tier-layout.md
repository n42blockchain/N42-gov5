# N42 Archive — Storage Tier Layout

**Date:** 2026-05-21
**Predecessors:**
- [`archive-commitment-final-design.md`](archive-commitment-final-design.md) — Phase A/B/C decisions
- [`commitment-compression-evidence.md`](commitment-compression-evidence.md) — A1-A4 investigation

This document captures the final placement rules for every category of
archive data, addressing the four user-supplied constraints:

1. AccountsTrie + StoragesTrie + (live) state + history must share a
   **single MDBX file** so per-block updates are atomic.
2. Chain-data (headers / bodies / receipts) lives in the freezer.
3. Execution data (witness, sender) also lives in the freezer.
4. Fast-sync nodes don't materialize historical-only files.

---

## 1. Three storage tiers

```
┌──────────────────────────────────────────────────────────────────┐
│ TIER A — chaindata (single MDBX env, atomic per-block writes)    │
│ ──────────────────────────────────────────────────────────────── │
│   PlainAccountState     latest by addr (point lookup)            │
│   PlainStorageState     latest by addr+slot (DupSort)            │
│   AccountsTrie          Phase A MPT (BranchNodeCompact)          │
│   StoragesTrie          Phase A MPT (BranchNodeCompact)          │
│   AccountChangeSets     last 256 blocks (reorg-safe diff layers) │
│   StorageChangeSets     same                                     │
│   AccountsHistory       inverted index (block lookup)            │
│   StoragesHistory       same                                     │
│   Bytecodes             contract code by keccak                  │
│   Meta                  state roots, build metadata              │
│                                                                  │
│   Per-block tx: BeginRw → update {state,trie,changesets,history} │
│                 → Commit() — single atomic transaction.          │
│   MDBX MVCC keeps the latest 256-block snapshot alive for reorg. │
└──────────────────────────────────────────────────────────────────┘
                                ↓ (block ages past 256)
┌──────────────────────────────────────────────────────────────────┐
│ TIER B — freezer (immutable cdat / cidx files, append-only)      │
│ ──────────────────────────────────────────────────────────────── │
│   chain data (per-block, written once at finalization):          │
│     headers          ~13 GB at 25M blocks                        │
│     bodies (txs)     ~567 GB                                     │
│     receipts         ~167 GB                                     │
│   execution data:                                                │
│     witnesses        ~167 GB (block-state witnesses; opt-in)     │
│     senders          ~38 GB (recovered sender addresses)         │
│   coldstore (after MDBX changeset ages out):                     │
│     accthist segments     accumulated account ChangeSet history  │
│     storhist segments     accumulated storage ChangeSet history  │
│   codes (after deploy block finalizes):                          │
│     codes segments        deployed-contract code, immutable      │
└──────────────────────────────────────────────────────────────────┘
                                ↓ (rebuilt from freezer, on demand)
┌──────────────────────────────────────────────────────────────────┐
│ TIER C — derived caches (any time, content-addressed)            │
│ ──────────────────────────────────────────────────────────────── │
│   n42-snapshot/       (existing) latest state via MPHF+codedict  │
│   n42-history-full/   (existing) compressed history MPHF+fp      │
└──────────────────────────────────────────────────────────────────┘
```

---

## 2. Why a single MDBX for tier A

The catch-up + 12-second-sync update pattern is:

```
on_block_commit(block N):
  tx = chaindata.BeginRw()
  apply_state_changes(tx, block.changes)         → PlainAccountState, PlainStorageState
  recompute_trie_paths(tx, block.dirty)          → AccountsTrie, StoragesTrie
  write_changeset(tx, N, block.old_values)       → AccountChangeSets, StorageChangeSets
  write_history_index(tx, N, block.touched_keys) → AccountsHistory, StoragesHistory
  tx.Commit()
  // if Commit fails: nothing persisted. If Commit succeeds: all 6 tables updated atomically.
```

If state and trie were in SEPARATE MDBX envs, a crash between their
commits would leave the trie root inconsistent with the plain state
(state shows balance X but trie root commits balance Y). Recovery
would require replaying from changesets — slow and complex.

MDBX MVCC additionally provides the **256-block reorg buffer for free**:
- Each Commit creates a new snapshot
- Old snapshots remain readable until pruned
- A reorg target up to 256 blocks back: open RoTx at that snapshot's
  txnum, apply alternative chain → no extra storage layer needed

→ Phase B refactor (post-Phase-C): merge `accounts-mptcache` and
`storage-mptcache` MDBX directories into one `chaindata` MDBX env with
multiple buckets. **mptbuild already supports shared env via
MDBXTarget(DBPath, Table) — just point both targets at the same DBPath
and they'll share the env.**

### Pre-Phase-D migration

Current state (from Phase A):
```
D:\n42-mpt\accounts-mptcache\mdbx.dat     5.29 GB MDBX env
D:\n42-mpt\storage-mptcache\mdbx.dat     30.48 GB MDBX env
```

Migration plan (~1 day work):
1. Add `cmd/n42-mpt-migrate` that opens both source envs RO and
   writes both tables into a new unified env.
2. Switch consumers (mptproof.Generator) to single env.
3. Bench: unified env should be slightly faster than two-env open
   (one mmap, one transaction set).

---

## 3. Why headers/bodies/receipts in freezer

These are **append-only, immutable, sequential by block number**. The
freezer's cdat format is purpose-built for this:
- Random-access by block num via cidx
- zstd page compression (already integrated)
- Snapshot-friendly (whole files copyable for fast sync seeding)
- No mmap pressure on the chaindata env

**Block-roots (tx-root, receipts-root) embedded in header:**
- We DON'T persist a separate tx-trie or receipts-trie per block
- Each block's tx-root / receipts-root is computable on-demand from
  the freezer body / receipts in microseconds (small trees, often
  100-1000 leaves)
- For RPC `eth_getProof` of a transaction inclusion: rebuild that
  block's small tree on demand, generate proof. Fast.

→ No tier A involvement for chain-data proofs.

---

## 4. Execution data: witness + sender

| Data | Size at 25M | Tier | Why |
|---|---|---|---|
| **witness** | ~167 GB (opt-in) | freezer | Per-block immutable; used by stateless client verification, fraud proofs. Append-only fits cdat. |
| **sender** | ~38 GB | freezer | Recovered tx senders (from `(v, r, s)` ECDSA recovery). Pre-computed cache; immutable per-block. |

Both follow the same access pattern as receipts: rare random read by
block num, never updated after finalization. Same freezer layout, same
cidx index strategy.

---

## 5. Fast-sync configuration

A fast-sync node downloads:

```
✓ tier C derived caches (snapshot + history)     ~189 GB
✗ tier B historical-only freezer files            skip
✓ tier B chain data (headers + recent bodies)    only what's needed for sync
✓ tier A chaindata                                build from snapshot
```

Specifically the fast-sync node SKIPS:
- `accthist` / `storhist` segments (history was already compressed
  into n42-history-full; redundant)
- `witnesses` (only needed for stateless validation / fraud-proof use)
- Old `bodies` and `receipts` past the sync horizon (~last 100K blocks
  are kept; older ones can be fetched on demand from full nodes)

Result: fast-sync footprint = **~189 GB (snapshot+history) + ~50 GB
(recent chain data) + ~36 GB (latest MPT) ≈ ~275 GB** — half of a
full archive node. The full archive is ~448 GB (189 + 167 receipts +
38 senders + 13 headers + 567 bodies + 36 MPT + 167 witness, with
overlapping items counted once).

---

## 6. Summary table

| Category | Tier | Mutable? | Atomic with state? | Notes |
|---|---|---|---|---|
| PlainAccountState | A (MDBX) | Yes | Yes | latest values, point lookup |
| PlainStorageState | A (MDBX) | Yes | Yes | DupSort by addr |
| AccountsTrie | A (MDBX) | Yes | **Yes** | Phase A output, atomic with state writes |
| StoragesTrie | A (MDBX) | Yes | **Yes** | same |
| AccountChangeSets (recent) | A (MDBX) | Yes | Yes | last 256 blocks for reorg |
| AccountChangeSets (historical) | B (freezer) | No | — | aged out from A; → accthist segments |
| StorageChangeSets | A/B (split same) | partial | partial | as above |
| AccountsHistory inverted index | A (MDBX) | Yes | Yes | block→keys lookup |
| Bytecodes | A (MDBX) | append | Yes | rare appends on contract deploy |
| Headers | B (freezer) | No | — | immutable, sequential |
| Bodies (txs) | B (freezer) | No | — | immutable, sequential |
| Receipts | B (freezer) | No | — | immutable, sequential |
| Witnesses (opt-in) | B (freezer) | No | — | immutable, append-only |
| Senders | B (freezer) | No | — | precomputed cache |
| n42-snapshot | C (cache) | Rebuildable | — | derivable from A |
| n42-history-full | C (cache) | Rebuildable | — | derivable from changesets |

---

## 7. Action items derived from this layout

| Item | Status | Owner phase |
|---|---|---|
| Phase A unified MDBX target | API supports it (single MDBX, two buckets); migration tool pending | Phase D pre-work |
| `cmd/n42-mpt-migrate` (merge two existing MDBX dirs into one) | TODO | Phase D pre-work, ~1 day |
| Tier-A live updater per block | TODO; uses existing HashBuilder | Phase E |
| 256-block diff via MDBX MVCC | TODO; design proven, code pending | Phase E |
| Fast-sync mode selector | TODO; configuration only | Phase E |
| Freezer wire-up for witness + sender | Already in place (existing N42 code) | — |
| Tier B/C aging from tier A changesets | Existing cs-prune scheduler | — |
