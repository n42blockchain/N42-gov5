# N42 eth-el Execution Performance — Analysis & Plan

**Date:** 2026-05-27
**Goal:** floor = match erigon (far exceed geth); target = exceed all (reth 2.0).
**Measure in Mgas/s, NOT blk/s** — blk/s is meaningless across heights (gas limit
at block 25.14M is **60M**; blocks avg ~35M gas, so 7.8 blk/s = ~270 Mgas/s).

## 1. Where we stand (measured 2026-05-27, block 25.14M)

| client | execution throughput | source |
|--------|---------------------|--------|
| geth full sync | **~27-29 Mgas/s** | geth logs (issue #27747) |
| erigon mainnet | **~120-280 Mgas/s** | erigon issues #8828/#3835 |
| reth 1.x live | ~100-200 Mgas/s (incl. per-block trie) | Paradigm 2024 |
| reth 1.x historical | ~520-620 Mgas/s | reth docs |
| **reth 2.0** | **~1.7 Ggas/s (~28 blk/s)** | Paradigm 2026 |
| **N42 eth-el (staged)** | **~270 Mgas/s** (7.8 blk/s × 35M gas) | our run |

**Verdict: we are already erigon-class (~270 Mgas/s ≈ erigon top end), ~9-10× geth,
above reth-1.x live.** The floor is MET. The "abnormally slow" impression came from
reading blk/s at a 60M-gas-limit height. To "exceed all" we must chase reth 2.0's
1.7 Ggas/s (~6× from here).

## 2. Bottleneck breakdown — theirs vs ours

reth's published breakdown (Paradigm 2024): **state root = >75% of per-block sealing**,
**EVM interpreter = ~50% of execution**, ~80% of storage slots are independent
(parallel EVM up to 5×), JIT/AOT EVM ~2×, parallel/pipelined state root 2-3×.

**We already neutralised reth's #1 cost (state root).** Staged catch-up moved the root
off the per-block path: dRoot 82ms → ~6ms, the Merkle runs once per sub-batch
(~7-15ms/block amortized, validated). So our warm per-block profile (tExec ~110ms,
~35M gas) is now:

| component | ~time/block | share | nature |
|-----------|------------|-------|--------|
| **dEVM** (interpreter + ecrecover) | ~62-76ms | **~60%** | CPU, opcode-bound |
| per-block overhead (wire decode, IBS/trc setup, ProcessBlockStart, rawdb Write{Header,CanonicalHash,RawBody}, receipt/bloom) | ~30ms | ~27% | CPU + small I/O |
| dCS (changeset MDBX writes) | ~7-15ms | ~10% | persistence |
| dRoot (Phase 1/2 hashed writes) | ~6ms | ~5% | persistence |
| dCommit | ~1ms | ~1% | — |
| Merkle (sub-batch, amortized) | ~7-15ms | separate | CPU+I/O |

**Our dominant cost is now dEVM (the interpreter) — exactly reth's ~50% figure.**

reth 2.0's 1.7 Ggas/s recipe (Paradigm 2026): **sparse trie cache** (in-memory trie →
state root **1-2ms/block**), **tiered storage** (hashed-only MDBX + changesets in static
files → block persistence **8.4s→40ms, 20×**), parallel EVM (already shipped), and
AOT/JIT EVM (revmc) is **still future** — so 2.0 got to 1.7 Ggas/s WITHOUT a faster
interpreter, via parallelism + cheap state-root + cheap persistence.

## 3. Plan (phased, each measured in Mgas/s)

### P1 — Parallel EVM (Block-STM) on the staged path  ★ biggest lever
dEVM is ~60% and ~80% of slots are independent → 2-4× realistic on 32 cores.
We HAVE the PoC (`internal/ethel/parallel_evm.go` RealParallelEVM + internal/parallel
Block-STM, validated genesis→3.87M on plain state). Blockers to clear:
- **post-Prague receipts**: `executeBlockParallel` errors on `rules.IsPrague` (needs
  ProcessExecutionBlockEnd receipts built from parallel outputs). Build them.
- **parallel-aware changeset writer**: the PoC uses a NoHistory writer; staged Merkle
  needs AccountChangeSet/StorageChangeSet. Add a parallel ChangeSetWriter (accumulate
  per-tx write-sets → ordered changeset on commit).
- **hashed-canonical base reader**: the MV overlay's fall-through base must be
  HashedStateReader (keccak-keyed), not PlainState. Wire MVBaseFromStateReader to it.
- **integrate into eldevp2p executeRange** (replace the serial ApplyTransaction loop
  with ExecuteBlockParallel) behind a flag; verify per-sub-batch root unchanged.
Target: dEVM ~65→~20-30ms → **~500-700 Mgas/s** (beats erigon + reth-1.x decisively).

### P2 — In-memory state overlay + bundled/tiered persistence (reth BundleState + 2.0 tiered storage)
Execute the whole sub-batch against an in-memory overlay (the Block-STM MVHashMap /
stateBuf already is this): reads fall through overlay→DB (hot keys served from RAM, no
per-access MDBX/keccak), writes accumulate, flush hashed state ONCE per sub-batch.
Move changesets from per-block MDBX `Put` to an append-only **static file / freezer**
(reth 2.0's 20× persistence win). Removes the ~13-21ms/block persistence from the
critical path. Naturally falls out of P1's overlay.
Target: shave ~15-20ms/block + faster reads → **+20-40%**.

### P3 — Reduce per-block overhead (~27%, currently unmeasured-but-large)
Batch the per-block rawdb writes (Header/CanonicalHash/RawBody) into the sub-batch tx
write path; reuse decode buffers; hoist trc/reader/ibs setup out of the per-block loop
where possible. Diffuse but ~27% of tExec is here.
Target: **+15-25%**.

### P4 — Pipeline the stages (overlap download / execute / Merkle)
Download sub-batch N+1 and Merkle sub-batch N-1 while executing N (separate goroutines,
the buffer already decouples download). Hides the Merkle pause + download latency.
Target: **+15-30%** (toward the execution-bound max).

### P5 — Faster interpreter (AOT/JIT or revm)  — the last mile to ≥1.7 Ggas/s
Even reth hasn't shipped this. ~2× on the EVM. Options: a Go EVM JIT, or revm via
revmc-style AOT. Prior spike found CGo callback overhead (80ns vs 2.68ns witness SLOAD,
30×) kills naive revm-over-Go-state integration — would need native state too. Largest
effort, last priority. Target (with P1-P4): **~1.2-1.7+ Ggas/s — matches/beats reth 2.0.**

## 4. Realistic trajectory

| stage | Mgas/s | vs |
|-------|--------|-----|
| now (staged + buffer) | ~270 | = erigon, 10× geth |
| +P1 parallel EVM | ~500-700 | > erigon, > reth-1.x |
| +P2 overlay/tiered persistence | ~600-900 | |
| +P3 overhead + P4 pipeline | ~800-1100 | approaching reth-2.0 |
| +P5 interpreter | ~1.2-1.7 Ggas/s | = / > reth-2.0 |

## 4c. P1 implementation blueprint (code-grounded 2026-05-27)

Integration site = `internal/api/engine_state_adapter.go` executePayloadDetailed
(NOT executor_parallel.go — that's the plain-state Executor path). MV base =
`NewMVBaseFromStateReader(NewHashedStateReader(workerTx))` (confirmed: HashedStateReader
satisfies StateReader; code read uses zero-addr→codeHash table, address-independent).

- **P1-B DONE**: `state.ExecuteBlockParallelF(numTxs, numWorkers, factory MVBaseFactory,
  executor)` (modules/state/parallel_executor.go) — per-worker base, pre-flight fail-fast,
  shared `runWorker`. Caller builds `factory := func() (MVBaseReader, func()) { tx,_ :=
  db.BeginRo(ctx); return NewMVBaseFromStateReader(NewHashedStateReader(tx)), tx.Rollback }`.
- **Receipts (folded from P1-A)**: assemble `block.Receipts` IN THE CALLER from existing
  `BlockCommit.Receipts` (TxReceipt: Status, CumulativeGasUsed, First/LastLogIndex) +
  `BlockCommit.Logs` + the tx (TxHash, Type, To, Nonce) + recovered sender. ContractAddress
  = `crypto.CreateAddress(sender, nonce)` for create-txs (To==nil); Bloom = CreateBloom(logs);
  per-tx GasUsed = CumGas[i]-CumGas[i-1]. No TxOutput/modules-state change needed.
- **P1-D (IBS apply) — THE RISKY CORE**: prefer a DIRECT function
  `ApplyBlockCommitToIBS(bc *BlockCommit, ibs *IntraBlockState)` (NOT the ApplyTarget
  interface — its call order is key-sorted, but we need controlled order):
  1. Build `codeMap[codeHash]=code` from code-tagged Writes.
  2. Wipe-tagged entries → IBS selfdestruct/CreateContract. **HIGH RISK**: this is the
     SELFDESTRUCT/metamorphic-storage class ([[project_metamorphic_storage_bug]],
     [[project_sstore_15k_saga_resolved]], [[project_lru_empty_slot_leftover]]). Must
     reproduce the serial IBS wipe semantics exactly.
  3. Account-tagged → decode (DecodeAccountValue); SetBalance+SetNonce; if codeHash∈codeMap
     SetCode(addr, codeMap[codeHash]) (links new contract code so CommitBlock's
     UpdateAccountCode fires). nil value → delete.
  4. Storage-tagged → SetState(addr, slot, value).
  Then the EXISTING `ibs.IntermediateRoot()` (writeOnly trc Phase1/2) +
  `ibs.CommitBlock(HashedCanonicalWriter)` + `WriteChangeSets()` produce byte-identical
  HashedAccounts/HashedStorage + changesets (changeset pre-values read from HashedStateReader
  base = pre-block state; NET deltas suffice for block-level changesets).
- **P1-E (splice)**: behind a flag in executePayloadDetailed, replace the serial tx loop with
  ExecuteBlockParallelF → FinalizeBlock → ApplyBlockCommitToIBS(bc, ibs) → assemble receipts;
  keep pre/post hooks (ProcessExecutionBlockStart/End, Finalize) serial on the same ibs.
  Remove the post-Prague guard for THIS path. Per-sub-batch Merkle root is the correctness gate.
- **P1-C (lazy-coinbase) — REQUIRED for real speedup**: without it every tx writes the
  coinbase tip → Block-STM serializes on that key (PoC parallel_evm.go:167-181 defers this).
  Add a coinbase-skipping MVStateWriter wrapper + TxOutput.CoinbaseTip + FinalizeBlock
  aggregate + target AddBalance-once. Measure P1-F WITH and WITHOUT to quantify.

**Validation (P1-F)**: the per-sub-batch Merkle root MUST equal the wire root (catches any
divergence loud). Additionally diff parallel-vs-serial over a range containing
SELFDESTRUCT/CREATE2/7702 blocks before trusting. Measure in Mgas/s.

### ⚠ P1-E blocker discovered (2026-05-27): parallel READ visibility

The serial staged path executes many blocks into ONE batch RwTx (commitInterval=256)
and reads via `NewHashedStateReader(batchTx)` — an RwTx sees its OWN uncommitted writes,
so block N+1 sees block N's writes and the pre-block hooks (beacon root etc.). The
parallel workers CANNOT share that RwTx (mdbx-go cgo goroutine check), so each uses a
per-worker `BeginRo`. **An MDBX RoTx sees only the last COMMITTED meta — NOT the batch
RwTx's uncommitted writes.** So naive parallel workers would read STALE parent state
(missing same-batch prior blocks + this block's pre-block hooks) → wrong execution.

Three ways to fix (architecture decision):
1. **In-memory hashed-state overlay** (reth BundleState / the executor_parallel.go
   PlainStateBuffer analog, but hashed-keyed): workers read overlay→base RoTx; overlay
   holds the batch's writes + pre-block hooks; flush to MDBX per sub-batch. Cleanest +
   reth-aligned + serves hot reads from RAM (this IS P2). Biggest build (hashed overlay
   + reader + trc reads/writes through it).
2. **No-sync per-block commit + fresh per-worker RoTx**: commit each block to MDBX
   WITHOUT fsync (cheap) so a fresh RoTx sees the parent; one fsync per sub-batch.
   Same kill-safety as today (lose ≤ batch). Simpler; changes the commit model; pays
   BeginRo×workers per block.
3. **Per-block fsync commit**: correct but reintroduces per-block fsync — likely erases
   the parallel gain.

Recommendation: option 1 (overlay) is the right long-term architecture and merges P1+P2,
but option 2 is the faster path to MEASURE the parallel-EVM speedup (and whether
coinbase contention throttles it) before investing in the overlay. Decide before P1-E.

## 5. Sequencing recommendation

P1 (parallel EVM) is the centerpiece and brings P2's overlay for free — do it first.
P3/P4 are independent quick-ish wins that can interleave. P5 is a separate large track.
Validate every phase in **Mgas/s** on the live staged run, keeping per-sub-batch root
verification (correctness gate) intact throughout.
