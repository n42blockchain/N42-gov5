# N42 eth-el Staged Catch-up — Design

**Date:** 2026-05-27
**Status:** Design + implementation
**Goal:** Catch up eth-el (reth-2.2 hashed-canonical state) from a migrated head
toward mainnet tip via a STAGED pipeline (reth/erigon model), not per-block.
12s live-follow is already done — this is only the bulk historical catch-up.

---

## 1. What reth & erigon actually do (source-verified 2026-05-27)

Both clients separate EXECUTION from the STATE-ROOT (Merkle/commitment) stage, and
both compute the root by reading intermediate trie nodes **on demand via cursor seeks,
skipping untouched subtrees** — so peak memory is O(touched keys), never O(state size).

### reth (Rust, `C:/N42/reth`)
- Pipeline (`crates/stages/stages/src/sets.rs:60-83`): Headers → Bodies →
  SenderRecovery → **Execution** → (Merkle::Unwind → AccountHashing → StorageHashing →
  **Merkle::Execute**) → ...
- **Execution** (`config.rs:289-294`) batches up to `max_blocks=500_000` /
  `max_changes=5_000_000` / `max_gas=1.5e12` / `max_duration=10min`, writes BundleState
  + changesets, computes **NO root**. In hashed-canonical mode the hashing stages are
  no-ops (`hashing_account.rs:166-172`) — reth's own arch matches our no-PlainState setup.
- **Merkle** (`merkle.rs`): `rebuild_threshold=100_000` (:49). gap>threshold → full
  rebuild; else **incremental in 7_000-block chunks** (:314) via `incremental_root_with_updates`,
  building **prefix-sets from changesets** (`prefix_set.rs:22-70`).
- Memory bound: `TrieWalker` + `can_skip_current_node` (`walker.rs:172-202`) — cached
  branch hash + path∉prefix-set ⇒ emit stored hash, skip subtree. Full rebuild streams
  via `StateRootProgress`: flush trie updates to DB + resumable checkpoint every 100k
  updates (`merkle.rs:259-301`, `trie.rs:25,297`). Peak = O(depth + 100k buffer).

### erigon2.7 (Go, `C:/N42/erigon2.7`) — N42's FlatDBTrieLoader lineage
- Stages (`eth/stagedsync/default_stages.go:31-253`): Headers → Bodies → Senders →
  **Execution** → **HashState** → **IntermediateHashes** → ...
- **Execution** (`stage_execute.go:567`) commits when `batch.BatchSize() >= cfg.batchSize`
  (a `datasize.ByteSize`), no root.
- **HashState** (`stage_hashstate.go:78-86`): clean (ETL external-sort from PlainState)
  vs incremental (from changesets). N/A for us — execution writes HashedAccounts directly.
- **IntermediateHashes** (`stage_interhashes.go`) — THE model we copy:
  - `tooBigJump := to - from > 100_000` (:108). Comment: *"RetainList is in-memory and
    will OOM if jump is too big."* jump>100k ⇒ `RegenerateIntermediateHashes` (full, ETL);
    else `IncrementIntermediateHashes` (:553-626).
  - `IncrementIntermediateHashes`: `rl := trie.NewRetainList(0)`; walk the changesets for
    [from,to], `rl.AddKeyWithMarker(keccak(key), isDelete)` per touched key; then
    `trie.NewFlatDBTrieLoader(prefix, rl, accTrieCol, stTrieCol).CalcTrieRoot(db)`. The
    loader reads existing `TrieOfAccounts`/`TrieOfStorage` via cursor `Seek`, uses
    `canUse`/`SkipState` to emit cached hashes for untouched subtrees, recomputes only
    touched paths, writes updated nodes back. Peak state-proportional memory = the
    RetainList itself.

### erigon3 (Go, `C:/N42/erigon`)
- No separate HashState/Merkle stages — commitment folded into **Execution** via
  `SharedDomains.ComputeCommitment` (`exec3_serial.go:194`), HPH on a fixed 128×16 grid
  reading branches on demand (`hex_patricia_hashed.go:2755 Process`, `ctx.Branch`). This
  is the long-term [[commitment-domain-plan]] direction; out of scope for this catch-up.

## 2. Root cause of the 25M OOM (corrected)

`internal/ethel/hashstate.go CalcStateRoot` = erigon's `RegenerateIntermediateHashes`
(empty RetainList, clears TrieOf*, full descent). Running it per-checkpoint at a 25M
jump is the OOM. **It is the wrong tool for catch-up.** The right tool is the
changeset-driven incremental loader, sub-batch-sized under the 100k cap.

## 3. N42 staged catch-up design

```
 coordinator round (sub-batch of N blocks, N ≤ ~16k, well under 100k OOM cap):

   1. HEADERS   fetch [from, from+N) skeleton (concurrent, buffered)
   2. BODIES    fetch bodies (concurrent, reordered)
   3. SENDERS   parallel ecrecover all sub-batch txs (Phase 2: recoverSenders)
   4. EXECUTE   for each block: ApplyTransactions → write HashedAccounts/HashedStorage
                + AccountChangeSet/StorageChangeSet; trc.SetWriteOnly(true) ⇒ NO per-block
                root. Head marker written IN the batch tx (atomic, [[ethel-head-state-split]]).
   5. MERKLE    once per sub-batch: build RetainList from this sub-batch's changesets
                ([from,to] AccountChangeSet/StorageChangeSet → keccak keys →
                AddKeyWithMarker); FlatDBTrieLoader.CalcTrieRoot(rl) reads existing
                TrieOf*, recomputes touched subtrees, writes updated TrieOf* (block-160
                delete-before-put DupSort fix preserved). Compare root to block[to].wire
                header.Root.
   6. COMMIT    root matches ⇒ commit tx (state + TrieOf* + head atomic). mismatch ⇒
                rollback the sub-batch tx (nothing committed) + fail loud / bisect.
```

- **Memory**: bounded by the sub-batch RetainList (~N×150 touched keys; N=16k ⇒ ~2.4M
  keys ⇒ ~100 MB). Never loads the full trie. Safe on 128 GB.
- **Verification granularity**: per sub-batch (the last block's wire root), matching
  reth/erigon's per-Merkle-stage verify. This RELAXES [[feedback_verify_every_block]] —
  a conscious move to the staged model per user direction 2026-05-27. A mismatch unwinds
  the whole sub-batch (changeset unwind) and bisects.
- **Throughput thesis**: execution runs at dEVM+dCS without dRoot interleaving; the
  Merkle pass is one sorted cursor walk over the sub-batch's deduped touched keys
  (cache-friendly sequential I/O) vs N separate per-block walks. Expected dRoot
  amortization from (a) hot-path dedup, (b) I/O locality, (c) removed per-block setup.
  Prior per-batch-root experiment showed only ~6% — that test merged per-block
  RetainLists without the changeset-driven single-pass; this design must be re-measured.

## 4. Implementation

- `modules/state/commitment/trie_root_computer.go`: re-add `writeOnly` (skip flushTrieRoot
  during execution). [reverted earlier; re-introduce, now paired with sub-batch Merkle].
- `internal/ethel/merkle_stage.go` (new): `MerkleStageIncremental(tx, from, to) (root, error)`
  — changeset → RetainList → incremental FlatDBTrieLoader → write TrieOf* → return root.
  Mirrors erigon2.7 `IncrementIntermediateHashes`.
- `internal/ethel/eldevp2p/downloader.go`: coordinator sub-batch loop = execute N blocks
  writeOnly into one tx → MerkleStageIncremental → compare to wire root → commit/rollback.
  Sub-batch size configurable (`--catchup.subbatch`, default 8192).
- Keep the per-block incremental path as the default/fallback (don't break what works at
  3.3 blk/s); staged path behind a flag until measured faster.

## 4b. Validation (2026-05-27)

- **Micro-benchmark** (`cmd/bench-merkle`, RO single Merkle pass over real ranges on
  the 25.1M datadir): per-block-equiv dRoot **12.6ms @1k / 9.0ms @5k / 7.2ms @10k**
  blocks → **6.5–11.5× vs the 82ms per-block path**. Memory bounded (~900MB RetainList
  @10k). Overturns the earlier "~6%" experiment (which deferred only the commit, not
  CalcTrieRoot).
- **Startup reconcile** (`MerkleStageIncremental(1, 25139797)`, full write path):
  3.14M acct + 10.69M storage unique keys, root `0x2838…`→ computed `0x8cc1fc6f…`
  **matched the wire header**, TrieOf* persisted. 8m39s incl. write + cold cache.
- **Warm steady-state staged execution** (EXECPROF): **dRoot 82ms → ~6ms** (writeOnly
  skips flushTrieRoot; the ~6ms is just Phase 1/2 hashed writes), dEVM ~65ms, dCS ~7ms.
  **tExec ~120ms/block vs per-block ~300ms → ~2.5×.** Execution is no longer the
  bottleneck — body download (tBody ~3-5s/round, gappy at low peer count) now dominates,
  exactly reth's lesson. NEXT lever = bulk concurrent body download (reth BodyStage:
  5-100 concurrent reqs, 200 body/req, BinaryHeap reorder so gaps don't stall).

## 5. Fallback / pragmatic note

If the Merkle sub-batch still doesn't amortize dRoot meaningfully (re-measure vs the ~6%
prior result), the architectural ceiling stands (~6.7 blk/s, dRoot+dCS serial) and the
fast path for the 2.66M gap remains re-migration from an updated reth snapshot
([[project_hashed_backup_stale_remigrate]]), executing only the tail with this pipeline.
