# reth MPT vs Erigon 3.4.1 HPH — Side-by-side Commitment Comparison

**Date:** 2026-05-20
**Purpose:** Inform N42's choice of commitment design. Both reth's
classic MPT and Erigon 3.4.1's HexPatriciaHashed (HPH) are mature
production implementations; this document captures the structural
differences and N42-specific implications.

---

## 1. Data Structure

| Aspect | reth MPT | Erigon HPH 3.4.1 |
|---|---|---|
| Tree type | 16-ary Patricia Merkle Trie | Same logical tree, different in-memory form |
| Node types | Branch, Extension, Leaf | Single grid representation (no distinct node types in memory) |
| In-memory layout | Standard pointer-based nodes loaded on demand | `grid [128][16]cell` — first 64 rows account layer, next 64 storage layer |
| Account/storage separation | Two distinct trie tables (`AccountsTrie`, `StoragesTrie`) | Single grid; depth ≥ 64 switches to storage hash path |
| Sparse encoding | Yes (no empty subtree storage) | Yes (cell `fieldBits` 5-bit mask) |

**Key difference:** reth uses traditional MPT node objects; Erigon's grid is a flat 128×16 cell array reused across operations (no GC churn, no pointer chasing). This is a performance optimisation, not a structural one — both encode the same MPT.

---

## 2. Branch Encoding (Wire Format)

### reth `BranchNodeCompact`

```
state_mask  u16 LE  (which of 16 nibbles have a child)
tree_mask   u16 LE  (which children are themselves trie roots)
hash_mask   u16 LE  (which children we stored explicit hashes for)
hashes      [B256; popcount(hash_mask)]
root_hash   Option<B256>  (Some if leaf-only branch)
```

Measured against 200K real samples (commit `7d1ff171`):
- Avg value bytes: 159.22
- Theoretical floor (masks + only required hashes): 159.22
- **Encoding overhead: 0.00 bytes (0.00%)**
- 4.79 hashes/branch average

### Erigon HPH branch encoding

```
bitmap   u16 BE      (which nibbles have a child)
fieldBits u8 [per cell]   (5-bit mask: extension/accountAddr/storageAddr/hash/stateHash)
cells:
  extension      var bytes (Patricia path compression)
  accountAddr    20 B  (account branch)  OR
  storageAddr    52 B  (storage branch: addr20 + slot32)
  hash           32 B  (child subtree hash)
  stateHash      32 B  (cached leaf hash, avoids re-keccaking unchanged subtrees)
```

Roughly 95-127 bytes per cell × ~3-4 cells/branch = **~300-500 B per branch encoding** in the canonical form.

**ReplaceKeysInValues optimisation** (at segment merge time): plain keys (20/52 bytes) get replaced with 8-byte file offsets into Account.kv / Storage.kv segment files. Saves ~12-44 bytes per cell with key.

| Format | Avg branch size | Notes |
|---|---|---|
| reth BranchNodeCompact | **~150 B** | No plain keys; values separate in PlainState |
| Erigon HPH raw | ~300-500 B | Includes plain account addresses + storage slot keys |
| Erigon HPH + RKV (post-merge) | ~150-250 B | Plain keys replaced with 8B offsets |

**Critical insight:** reth's design AVOIDS storing plain keys in branches in the first place (they live in PlainState). Erigon needs RKV because plain keys ARE in HPH branches by default. Once RKV is applied, the two formats converge. **There is no further encoding compression beyond this point.**

---

## 3. Persistence Layout

### reth

```
chain/
  AccountsTrie    MDBX table  key = Nibbles (path)        value = BranchNodeCompact   5.4 GB
  StoragesTrie    MDBX table  key = hashed_addr ‖ Nibbles  value = BranchNodeCompact  31.4 GB
  PlainAccountState  MDBX     key = addr                    value = account RLP        23.1 GB
  PlainStorageState  MDBX     key = addr ‖ slot             value = U256              129.5 GB
  HashedAccount      MDBX     (often empty in reth, computed lazily during proof)
```

### Erigon 3.4.1

```
CommitmentDomain {
  TblCommitmentVals        latest branch nodes by prefix       MDBX
  TblCommitmentHistoryKeys history values (if --opt-in)         MDBX
  TblCommitmentHistoryVals same                                  MDBX
  TblCommitmentIdx         EliasFano inverted index             MDBX
}

Snapshots (segment files when history opted in):
  <step>.commitment.kv     latest values (zstd-paged)
  <step>.commitment.kvi    hashmap accessor (not btree)
  <step>.commitment.v      historical values
  <step>.commitment.vi     hist-value index
  <step>.commitment.ef     inverted index (EliasFano)
  <step>.commitment.efi    index of inverted index
```

| Storage | reth | Erigon |
|---|---|---|
| Latest commitment | 37 GB (AccountsTrie + StoragesTrie) | ~40-50 GB (CommitmentDomain.kv, similar magnitude) |
| Latest plain state | 153 GB (PlainState) | similar magnitude (AccountsDomain + StorageDomain) |
| Commitment history | 0 (not stored) | ~1.5-2 TB if `--prune.include-commitment-history` |
| ChangeSets | 874 GB (AccountChangeSets + StorageChangeSets) | similar (in History layer) |

**Per-node storage cost is roughly equivalent.** The big difference is the commitment-history layer in Erigon (an opt-in 1.5-2 TB cost for fast historical proofs).

---

## 4. Update Path (per-block commit)

### reth

```
on_block_commit(changes):
  for (key, new_value) in changes:
    PlainState.put(key, new_value)
    AccountChangeSets[block].put(key, old_value)
  // Trie update:
  hashed_changes = changes.map(|(k,v)| (keccak(k), v))
  HashBuilder.update(AccountsTrie, hashed_changes)
  // Single-threaded incremental: walk old MPT, replace modified paths
  new_root = HashBuilder.compute_root()
```

Complexity: O(dirty_keys × log_16 N) hash operations, single-threaded.

### Erigon

```
on_block_commit(changes):
  for (key, new_value) in changes:
    sdCtx.TouchKey(domain, key, value)
    // routes to Domain layer which buffers + writes to History
  // Commitment:
  patriciaTrie.Process(updates):
    updates.HashSort(...)             // ETL collector sort by hashed_key
    for hashedKey in updates:
      fold to common prefix
      unfold from DB                  // load branch nodes lazily
      followAndUpdate                 // write cell
    final fold to root
  branchEncoder.ApplyDeferredUpdates(NumCPU)  // parallel encode + write
```

Optimisations:
- **stateHash memoisation**: cell-level cache avoids re-keccaking unchanged subtrees
- **Deferred branch updates**: writes batched and parallelised across CPUs
- **Warmup pipeline**: separate RO tx + multi-worker prefetches branches into MDBX page cache before main commit
- **Concurrent HPH** (DRAFT): splits trie at first divergent nibble, parallelises across subtrees

| Aspect | reth | Erigon |
|---|---|---|
| Threading | Single (HashBuilder linear) | Single grid + parallel encode |
| State hash cache | Per-walk only | Persistent `stateHash` cell field |
| Branch write batching | Per-update | Deferred + NumCPU parallel |
| Prefetch / warmup | None | Yes (separate RO workers) |
| Commit complexity | O(dirty × log_16 N) hashes | Same, with smaller constants |

**Erigon HPH is engineered for higher block throughput.** For per-block commit, Erigon is ~2-5× faster than reth in benchmarks. For N42's archive-write phase this matters; for read-only archive it does not.

---

## 5. Proof Generation

### reth (latest block)

```
eth_getProof(addr, slots, "latest"):
  hashed_addr = keccak(addr)
  walk(AccountsTrie, hashed_addr):
    collect sibling hashes at each level → account_proof
  account_value = PlainAccountState.get(addr)
  for slot in slots:
    hashed_slot = keccak(slot)
    walk(StoragesTrie[hashed_addr], hashed_slot):
      collect siblings → storage_proof
    storage_value = PlainStorageState.get(addr ‖ slot)
  return AccountProof{...}
```

Latency: ~100 µs (direct MDBX reads from persisted trie nodes).

### reth (historical block)

```
eth_getProof(addr, slots, blockN):
  Build HistoricalStateProvider(blockN):
    for each key in account_proof_path:
      use ChangeSets[blockN+1..latest] overlay PlainState
  Rebuild trie subset for accessed paths:
    expensive — needs hashed iteration through subtree
  return proof
```

Latency: ~ms-seconds. Implementation incomplete in some reth versions.

### Erigon (latest block)

```
eth_getProof(addr, slots, "latest"):
  SharedDomainsCommitmentContext.SetReader(latest)
  sdCtx.TouchKey(addr)
  sdCtx.Witness(...) // generates RLP MPT subset
  trie.Trie.Prove(keccak(addr), 0, false)
  return proof
```

Latency: ~100 µs - 1 ms (depends on grid state warmth).

### Erigon (historical block)

```
eth_getProof(addr, slots, blockN):
  HistoryStartTx := tx.Debug().HistoryStartFrom(kv.CommitmentDomain)
  if blockN's lastTxn < HistoryStartTx: return PrunedError
  sdCtx.SetHistoryStateReader(roTx, lastTxnInBlock)
  domains.SeekCommitment(...)  // restore grid state at blockN
  sdCtx.TouchKey(addr); sdCtx.Witness(...)
  proofTrie.Prove(...)
```

Latency: ~ms (direct query via commitment-history domain + inverted index lookup).

**Key difference for historical proofs:**
- reth: **on-demand recomputation** (uses ChangeSets + PlainState; slow but free in storage)
- Erigon: **direct query** (uses persisted commitment history; fast but 1.5-2 TB storage)

| Latency | reth | Erigon |
|---|---|---|
| Latest proof | ~100 µs | ~100 µs - 1 ms |
| Recent historical (last 100k blocks) | ~10-100 ms | ~1-5 ms |
| Deep historical (early chain) | seconds-minutes | ~1-10 ms |

---

## 6. Software-engineering complexity

| Dimension | reth | Erigon HPH |
|---|---|---|
| Lines of code (commitment-related) | ~5 K | ~25 K |
| Number of file abstractions | 2 (Account/Storages Trie + HashBuilder) | 10+ (HPH, Domain, History, InvertedIndex, Snapshots, Witness) |
| Concurrency model | Single-threaded | Grid + deferred + warmup + concurrent variant |
| Dependencies | Self-contained | MDBX + Snapshots + ETL + RecSplit + EliasFano |
| Memory footprint | Small (load nodes as needed) | Grid pool + caches (≥ hundreds of MB at warmup) |
| Maturity | Stable, simple | Stable but multi-year refactor history (E1→E2→E3→3.4) |
| Recent bugs (2026) | Few | PR #21044 segfault (slice aliasing in TrieContext.Branch); #19044 commitment-history root mismatch |

**For N42 porting:** reth-MPT is **5× less code** and significantly simpler to reason about. Erigon HPH brings performance perks (parallel commit, stateHash cache) at the cost of architectural complexity.

---

## 7. N42-specific Implications

Recall N42's archive positioning (decided earlier):

> reth-style: small storage + on-demand historical proof (slow but free)

What N42 already has:

| Component | Already exists | Maps to |
|---|---|---|
| Latest plain state | `D:\n42-snapshot` (52 GB, MPHF+codedict+zstd) | reth PlainState (153 GB) → 3× more compact |
| Per-block changes | `D:\n42-history-full` (137 GB, MPHF+fp) | reth ChangeSets + History (1274 GB) → 9× more compact |
| Historical state-as-of API | `internal/historicalstate` (commit `cd5f8f72`) | reth's HistoricalStateProvider equivalent |
| Latest commitment | ❌ missing | reth's AccountsTrie+StoragesTrie (37 GB) |

**The missing piece is the latest commitment trie.** Both reth-MPT and Erigon HPH are candidates.

### Comparison vs N42 requirements

| Requirement | reth-MPT | Erigon HPH |
|---|---|---|
| Latest proof support | ✅ 100 µs | ✅ 100 µs |
| Historical proof support | ✅ via historicalstate overlay | ✅ direct query (but needs +1.5 TB) |
| Storage cost | 37 GB | 40-50 GB latest (or +1.5 TB for full historical) |
| Engineering effort to port | **2-3 weeks** (simple linear encoding) | 5-8 weeks (grid + Domain abstraction) |
| Aligns with N42's "minimal" archive philosophy | ✅ Yes | ⚠️ Heavy if commitment-history enabled |
| Reuses N42's existing snapshot format | ⚠️ Need to map MPHF-ordinal to nibble order | Same problem |
| Compatible with N42's `internal/historicalstate` | ✅ Direct | ✅ But Erigon's own history would be redundant |

### Decision matrix

| Criterion | Weight | reth-MPT | Erigon HPH |
|---|---|---|---|
| Storage cost (latest only) | high | ✅ 37 GB | ⚠️ 40-50 GB |
| Engineering complexity | high | ✅ Low | ❌ High |
| N42 alignment ("reth-style") | high | ✅ Native fit | ❌ Different philosophy |
| Write throughput | medium | ⚠️ Lower | ✅ Higher |
| Latest proof latency | medium | ✅ 100 µs | ✅ 100 µs |
| Historical proof latency | low (rare query) | ⚠️ ms-s | ✅ ms |
| Code maturity / bug history | medium | ✅ Stable | ⚠️ Recent commitment-history bugs |

**Score:** reth-MPT wins on storage, engineering, alignment. Erigon wins on write throughput and historical proof latency (which N42 deprioritises).

---

## 8. Recommendation

**Choose reth-MPT.** Reasons:

1. **Structural fit with N42's archive philosophy.** N42's snapshot + history are already "reth-style minimal" (already 7× more compact than reth itself). Adding reth-MPT to top gives a complete archive with consistent design.

2. **Engineering cost is real.** Erigon HPH is ~5× more code and would require porting the full Domain/History/InvertedIndex abstraction. We don't gain back the cost in any concrete N42 use case.

3. **Erigon's main differentiator (commitment-history fast queries) costs +1.5-2 TB.** N42 explicitly traded this away by choosing the reth model. Without commitment-history, Erigon's complexity offers no real advantage.

4. **N42's `internal/historicalstate` already mirrors reth's HistoricalStateProvider.** Direct compatibility — no integration friction.

5. **Future flexibility preserved.** If we later need fast historical proofs, we can revisit Erigon-style commitment-history as an opt-in feature without disrupting the reth-MPT-based latest-commitment layer.

### Caveats

- **Write throughput** will be lower than Erigon. For archive-only nodes this is fine; for validator/full-sync nodes we'd revisit.
- **MPT builder** still needs effort. We can either:
  - **Path A**: import reth's `AccountsTrie`/`StoragesTrie` tables verbatim from a reth instance (37 GB one-time copy + ongoing maintenance overhead)
  - **Path B**: build our own MPT from `D:\n42-snapshot` using N42's existing `lib/commitment` HPH writer (independence + maintainability, ~hours of one-time build)

Path B is preferred — it makes N42 self-contained.

---

## 9. Open Questions for Implementation

1. **MPT format compatibility**: should N42's MPT match reth's `BranchNodeCompact` (exact byte format) or use N42's own encoding? Trade: cross-checkable (reth format) vs slightly more compact (custom).

2. **Storage backend**: separate MDBX dir or integrate with existing freezer? Separate dir is simpler and reversible.

3. **Build pipeline**: one-shot rebuild from snapshot at startup vs incremental updates per block? One-shot is simpler but loses commit-time freshness; incremental matches reth.

4. **Hashed-key iteration over snapshot**: N42's `D:\n42-snapshot` is MPHF-ordinal sorted (not hashed-key sorted). MPT building needs hashed-key iteration. Options:
   - External sort during build (memory-bounded, ETL collector — already in lib/etl)
   - Add a one-time hashed-key index alongside snapshot (+12 GB acct + 50 GB stor = 62 GB extra → defeats purpose)
   - Just rebuild from reth's data once and maintain via update path
   
   → Recommend external sort during build. ~hours one-time cost.

These are scoping questions for Phase A, not blockers.
