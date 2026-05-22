# MPT Commitment Compression Architecture Plan

**Date:** 2026-05-22
**Decision:** Erigon HexPatriciaHashed (HPH) style — do NOT persist
internal trie nodes; rebuild on demand via HPH grid fold/unfold.

## Why we got here

### Current state (post-G1d / post-G2 Path 2 partial)

| Table | Entries | Size |
|---|---|---|
| AccountsTrie (compact) | 28.9 M | 5.29 GB |
| StoragesTrie (compact) | 147 M | 30.48 GB |
| AccountsDense V1 | 28.9 M | 9.48 GB |
| StoragesDense V1 | 147 M | 51.47 GB |
| AccountsDenseV2 | 28.9 M | 1.79 GB |
| StoragesDenseV2 | 2 (stub) | ~0 |
| **Total** | | **98.52 GB** |

### What's wrong

1. **Triple-stored**: compact + dense V1 + dense V2 all persist the
   SAME branch nodes in three different encodings — no single
   format won outright.
2. **Architectural gap for USDC**: heavy contracts' storage
   subtrees collapse into one extension below the deepest persisted
   branch. Neither compact nor dense table stores the under-
   threshold subtree (`internal/mptproof/dense_extended_probe_test.go`
   empirically confirms depth 7 = MISS for USDC slot 0). Proof gen
   must enumerate the contract's leaves — fails with our 200K cap.
3. **No incremental update story**: every block rebuilds from
   scratch; no per-block delta layer.

### Survey: how erigon and reth solve this

| | Erigon | Reth |
|---|---|---|
| Storage trie | Unified composite `keccak(addr)‖keccak(slot)`, 128-row HPH grid | Per-contract trie, `StoragesTrie` keyed by `(addr-hash, nibbles)` |
| Internal nodes | **NOT persisted**; HPH rebuilds on demand. Only `BranchData` updates per block persist in `.kv` segments. | Fully persisted in `AccountsTrie`/`StoragesTrie` tables. |
| Proof gen | `HexPatriciaHashed.GenerateWitness()` lazy fold/unfold + `trie.Trie.Prove()` walks the materialized witness. | Cursor walks the persisted `(addr, nibbles)` range. |
| Compression | `seg.CompressKeys` dictionary on the .kv segments + step-based merge & prune. | `reth_codecs::Compact` per-entry + packed nibbles (33B keys). |
| ~Total at 14.5 M blocks | **~3-5 GB** | ~38 GB |

Erigon achieves ~10× compression over reth by not storing internal
nodes at all and using step-merge to amortize the per-block delta
overhead.

## Why we chose A (Erigon HPH style)

**Both** "extreme compression" **and** "USDC sub-second storage
proof" are the same architectural problem:

- Storing every internal node is wasteful **and** still doesn't
  cover under-threshold collapsed subtrees.
- HPH's grid model rebuilds any path on demand from a much smaller
  per-block delta — solves both at once.

We **already have**:

- `lib/commitment/HexPatriciaHashed` — full grid implementation
  (rows 0-63 accounts, 64-127 storage), unfold/fold/touch all done.
- `lib/commitment/hex_patricia_hashed.go:2465 GenerateWitness()` —
  builds a `*trie.Trie` for any set of touched keys.
- `lib/commitment/hex_patricia_hashed.go:1446 toWitnessTrie()` —
  converts the current grid state into the witness trie.
- `lib/commitment/trie/stub.go` — `Node`, `FullNode`, `ShortNode`,
  `HashNode`, `AccountNode`, `ValueNode` types + Trie + lookup
  walker + MergeTries.

We **still need**:

- `lib/commitment/trie/proof.go` — port erigon's `Prove(key,
  fromLevel, storage)` (50 LOC core algorithm; reference
  `C:/N42/erigon/execution/commitment/trie/proof.go`).
- `lib/commitment/trie/hasher.go` — port erigon's `hashChildren()`
  to RLP-encode FullNode/ShortNode/HashNode/AccountNode/ValueNode
  (~300 LOC; reference `C:/N42/erigon/execution/commitment/trie/hasher.go`).
- Single-key `GenerateWitnessForKey()` convenience that doesn't
  require an `Updates` set (small wrapper on existing API).

## Phases

### HA-1 — HPH `Prove()` port (2-3 days)
- Port `trie/hasher.go` and `trie/proof.go` from erigon.
- Adapt to our simplified Node types (no `DuoNode`).
- Synthetic-trie unit test that builds a small trie, generates a
  proof, verifies via standard EIP-1186 verifier.
- Output: `(*HexPatriciaHashed).ProveAccount(addr)` and
  `ProveStorage(addr, slot)` returning `[][]byte`.

Tracked as task #72.

### HA-2 — HPH-based eth_getProof end-to-end (1 week)
- Replace `RethHashedLeafSource` + `subLeavesByPrefix` enumeration
  with `HexPatriciaHashed.GenerateWitness()` + `trie.Trie.Prove()`.
- Validate USDC account + storage slot 0/1 proofs match expected
  root and complete sub-second.

Tracked as task #73.

### HA-3 — Commitment domain persistence (2-3 weeks)
- Design `BranchData` encoding: `(nibblePath, touchMap, afterMap,
  changedChildren[])` per modified branch per block.
- Build `.kv` segment writer + reader with `seg.CompressKeys`
  dictionary compression (mirror erigon's `db/state/files/`).
- Step merge + prune scheduler (mirror erigon's `merge.go`).
- Hot in-memory cache for the recent step (warmup-style).

Tracked as task #74.

### HA-4 — Drop legacy compact/dense tables (1 week)
- Migrate `Generator` to commitment-domain-only mode.
- Delete `AccountsTrie`, `StoragesTrie`, `AccountsDense*`,
  `StoragesDense*` tables.
- Reclaim ~95 GB on D:\n42-chaindata.

Tracked as task #75.

## Risk register

| Risk | Mitigation |
|---|---|
| HPH `GenerateWitness` is currently `Updates`-set oriented; single-key extraction may require state copying | Use the `Updates` shape with one entry; profile to confirm cost. If excessive, add direct fold-to-key + capture sibling helper. |
| `trie.Trie.Prove` requires RLP encoder for all Node types — that's where the LOC budget is | Erigon's hasher.go is the reference; our Node types are a strict subset, so we can drop ~30% of cases. |
| BranchData persistence schema needs to roundtrip exactly for resume after crash | Mirror erigon's encoding bit-for-bit; reuse its merge/prune semantics. |
| Storage savings depend on real-world branch churn; we should validate before HA-4 by running HA-3 in parallel with the old tables | Keep old tables until HA-3 stable for 1+ week of new blocks. |

## Decision log

- **2026-05-22**: Picked HPH over per-contract trie because we
  already have the HPH port; ~3 GB target vs reth's ~38 GB makes
  it worth the extra implementation work.
- **NOT chosen**: P1+P2+P3 incremental compression of current
  format — would optimize the existing 98 GB to ~6 GB but still
  fail on USDC storage proofs. Wasted work.
