# G2 compression frontier analysis

**Date:** 2026-05-21
**Status:** Analysis + one additional optimization landed
**Predecessor:** [`g2-plain-key-referencing.md`](g2-plain-key-referencing.md)

This document walks through each remaining bit of V2's dense encoding,
asks "can we drop it?", and quantifies what stays. Goal: ensure we're
at or near the information-theoretic floor without giving up sub-ms
proof speed.

## 1. Per-byte audit of V2 encoding

Per branch:

```
+0  state_mask        2 B           bitmap of present children
+2  tree_mask         2 B           bitmap of children that are deeper branches
+4  per set state bit:
      slot bytes      1, 33, or N+1 bytes
```

Per slot:

| Slot form | Bytes | When |
|---|---|---|
| `0x01` LeafMarker | **1 B** | HasTree=0 + hashed (reconstruct via base prefix scan) |
| `0xa0‖hash` | **33 B** | HasTree=1 + hashed (deeper branch) |
| `0xc0..0xfe‖RLP` | **1+N B (N ≤ 30)** | inline child (small leaf RLP) |

## 2. Estimated V2 size on mainnet

Working from G1's data (after the actual G1.d bootstrap measures real,
this will be sharpened):

| Category | Count | × Avg size | Total |
|---|---|---|---|
| Account branches × 13 child slots | 376 M | | |
| ├─ Leaf markers (75%) | 282 M × 1 B | | 282 MB |
| ├─ Branch hashes (25%) | 94 M × 33 B | | 3.1 GB |
| └─ Per-branch header (4 B) | 28.9 M × 4 B | | 116 MB |
| **Accounts subtotal** | | | **≈ 3.5 GB** |
| Storage branches × 13 child slots | 1.91 B | | |
| ├─ Leaf markers (75%) | 1.43 B × 1 B | | 1.43 GB |
| ├─ Branch hashes (25%) | 478 M × 33 B | | 15.8 GB |
| └─ Per-branch header (4 B) | 147 M × 4 B | | 590 MB |
| **Storage subtotal** | | | **≈ 17.8 GB** |
| **Combined V2 estimate** | | | **≈ 21.3 GB** |

vs G1 V1 estimate ~76 GB → **72% smaller**.

vs reth's MPT (38.8 GB) + HashedAccounts/HashedStorages (157 GB) = 196 GB → **89% smaller**.

## 3. Can we cut more?

### 3.1 Drop `tree_mask` (✅ LANDED in this commit)

Read-side never uses tree_mask:
- `fullProofBytesDense` iterates `DenseBranch.Slots[]` directly; doesn't branch on tree
- Walk runs on the compact `AccountsTrie`/`StoragesTrie` table, not dense — tree info comes from there
- LeafMarker / 33B / inline are unambiguously distinguishable by first byte

Save: 2 B per branch × 176 M = **~350 MB**. Trivial code change, zero
runtime cost.

V3 layout:

```
+0  state_mask        2 B
+2  per set state bit slot bytes (same as V2)
```

### 3.2 Replace `0xa0` prefix in 33B hash slots (rejected — too small)

The hash slot is `0xa0‖32B keccak`. The `0xa0` is RLP signaling. We
could drop it if we recompute on read — but we'd need to know the slot
type (hash vs marker vs inline), which requires the type array. Saving
1 B × 478M storage hash slots = ~470 MB. Adding 2 bits/slot type
array: 2/8 × popcount(state) per branch ≈ +600 MB. **Net loss.**

### 3.3 Replace branch hashes with markers (rejected — proof becomes O(subtree))

A `0xa0‖hash` branch slot's 32B hash = `keccak(branch RLP at child path)`.
We could replace with a 1B marker too — but at proof time we'd have
to RECURSIVELY compute the child branch's hash. For root-level
siblings, that's a full subtree traversal (millions of leaves), taking
seconds per proof.

Quantified:
- Storage saved: 32 B × 572 M branch slots = **~18 GB**
- Proof latency added: ~30 s per USDC-shape proof (versus current ms)
- **Trade-off rejected.** Branch hashes are mandatory cache.

If a 30 s proof for a 18 GB save is ever acceptable (rare RPC node,
huge disk-pressure deployment), revisit as G3.

### 3.4 zstd dictionary for inline RLPs (rejected — too few inline children)

V2's inline children are leaves with RLP < 32 B. For accounts, most
leaves have RLP ≥ 68 B (storage root + code hash dominate) → all
hashed, none inline. For storage, more inline candidates but the
distribution is hash-uniform — small wins.

Saving estimate: ~5% on the inline subset, ~0.5 GB total. Adds
streaming dict to read path. **Not worth.**

### 3.5 Common-prefix compression for nibble keys (deferred)

MDBX cursor iteration returns adjacent branch keys that share a long
common prefix (sorted nibble paths). A custom encoding could prefix-
delta-compress keys. Saving on key storage might be ~1-2 GB. Requires
custom container — defer to a future "G4 sparse archive" phase if
storage pressure ever matters.

### 3.6 Block-aware deduplication (deferred to G4)

For snapshot rotation: most branches between adjacent snapshots are
unchanged. Erigon's step/merge model deduplicates via append-only
deltas. For our weekly snapshot model the FULL chaindata regenerates
each rotation — incremental dedupe across rotations would save
bandwidth on snapshot distribution but not local storage. Future work.

## 4. The floor

After landing §3.1 (drop tree_mask), V2 is essentially at the floor
for "store all interior branch hashes + recover leaf hashes":

| Cost item | Bytes |
|---|---|
| Per-branch header (state_mask only) | 2 B × 176 M = 350 MB |
| Branch hash slots (32 B each, 0xa0 prefix removed only saves 470 MB but adds type array of ~600 MB → net negative) | 32 B × 572 M = 18 GB + 0xa0: 572 MB → **~18.3 GB** |
| Leaf markers (1 B each) | 1 B × 1.7 B = 1.7 GB |
| Inline RLP (rare, <32 B avg) | est. ~1 GB |
| **Total V2 (after tree_mask drop)** | **≈ 21 GB** |

That's where compression bottoms out without giving up the sub-ms
proof guarantee. ~21 GB for full mainnet archive commitment is
already 10× better than reth's combined (38.8 + 157 = 196 GB) and
~80× smaller than geth's full archive.

## 5. Per-byte attribution

To make the trade-off concrete: in V2, the cost-per-byte breakdown is:

- 85% of bytes go to **branch hashes** — non-compressible without
  recursive read-time compute that destroys proof speed
- 8% to **leaf markers** — already maximally compressed (1 byte)
- 5% to **inline RLPs** — already small
- 2% to **state masks** — already minimal

Each pillar is at its individual floor. No "free lunch" lurking.

## 6. Code changes in this commit

```
lib/trie/trie_root.go
  MarshalTrieNodeDenseV2 — output no longer includes tree_mask
  UnmarshalTrieNodeDenseV2 — parser updated, returns dummy treeMask=0
                              (callers that need HasTree info read it
                              from the compact AccountsTrie/StoragesTrie
                              instead — only walk needs HasTree, and
                              walk runs on compact)

internal/mptbuild/dense_v2_test.go
  Updated synthetic expectation: header is 2 B (state) not 4 B
  (state+tree). New synthetic 100-acct V2 size: ~390 B (was 553 B,
  saving 29% additional — synthetic is dense-branch-heavy so the
  per-branch 2 B drop is significant; mainnet will see ~2% additional)
```

(The change is intentionally light: drop 2 B from the header. The
slot encoding is unchanged.)
