# Parallel EVM Integration Review (Task #148)

**Date:** 2026-05-28
**Verdict:** Keep Block-STM as shadow-bench only; do NOT replace the serial
EVM path in `executePayloadDetailed`.

## What's already done

1. **#140 P1-B** — `ExecuteBlockParallelF` accepts a per-worker `MVBaseFactory`
   so each worker holds its own MDBX `RoTx`. Required because `mdbx-go`'s
   cgo goroutine-safety check trips when multiple workers share a single
   `RoTx`. (`modules/state/parallel_executor.go`)

2. **#141 P1-D** — `ApplyBlockCommitToIBS(bc, ibs)` replays a parallel
   `BlockCommit` (codeMap, wipes, accounts, storage, coinbase tip) back
   into a fresh `IntraBlockState` so the existing `trc + CommitBlock +
   changeset` commit path produces byte-identical state to a serial run.
   (`modules/state/parallel_apply_ibs.go`)

3. **#142 P1-E** — `coinbaseSkipWriter` enabled only when `sender !=
   coinbase`. The MEV-builder pattern (sender IS coinbase, sending its
   own tx) requires the builder's nonce++/balance writes to reach the
   MV write-set so the builder's next tx sees the correct nonce; only
   pure tip credits from non-builder senders are commutative and can be
   safely dropped from the MV view. (`internal/ethel/parallel_evm.go`)

4. **#144 P1-C** — Lazy-coinbase productionized: when the wrapper drops
   the coinbase write it captures the exact balance delta into
   `TxOutput.CoinbaseTip`; `FinalizeBlock` aggregates `Σ tips` into
   `BlockCommit.CoinbaseDelta`; `ApplyBlockCommitToIBS` credits the
   coinbase exactly once via the normal account writer (no double-count).

5. **Shadow bench** — `N42_PEVM_BENCH=1` runs the parallel EVM alongside
   the serial path and prints `PEVMBENCH speedup=X.XXx`. Results
   discarded so production state is unaffected. Gates:
   - `N42_PEVM_LAZYCB=1` enables `SetSkipCoinbase(header.Coinbase)`
   - `N42_PEVM_WORKERS=N` overrides default 8

## Measured speedup (#143)

| Block range          | Workers | LazyCB | Speedup |
|----------------------|---------|--------|---------|
| 25M tx-dense blocks  | 8       | on     | ~1.15×  |
| Unit test (#144)     | 8       | on     | ~1.8×   |

The unit test isolates the EVM loop. The shadow bench measures it inside
the real `executePayloadDetailed` envelope, which includes:

- per-tx `ibs.Prepare` (alloc + clear)
- `ApplyTransaction` driving `NoopWriter` (serial path buffers via IBS
  staged map; parallel path goes through `MVStateView.Get` → MVHashMap
  lookup → base reader if miss → another alloc per read record)
- per-tx receipt assembly
- gas pool seeding per-worker

The dominant residual contention is **sender repetition**: a hot account
sending 6-10 sequential txs in the same block still serializes through
the scheduler (validation-fail → rewind). Lazy-coinbase removes the
universal coinbase conflict but leaves the natural same-sender chain.

## Why integrating into the main path is not worth it

1. **Below Go/No-Go bar.** The session decision threshold was 1.5×.
   1.15× end-to-end means we'd pay the integration cost (parallel apply
   semantics, abort-retry test surface, scheduler tuning) for ~13% wall
   clock improvement on the EVM phase — and the EVM phase is no longer
   the binding constraint after the staged catch-up landed (#137).

2. **Post-staged-catchup, root is the constraint.** With per-sub-batch
   `MerkleStageIncremental`, dRoot drops from 82ms to 6ms and tExec
   dominates again, but `dEVM` already runs at ~270 Mgas/s (Erigon-class)
   on the serial path. The parallel 1.15× would push us toward ~310
   Mgas/s, still order-of-magnitude below reth-2.0's 1.7 Ggas/s, and the
   gap there is the EVM interpreter — not the scheduling.

3. **MVStateView.Get overhead is the floor.** The per-read `ReadRecord`
   alloc + key copy is unavoidable in Block-STM (it's what makes
   validation work). Removing it requires a different abstraction
   entirely (vector-based read set, batch validation), which is its own
   redesign.

## What we'd revisit if conditions change

- Native EVM interpreter (revm-style) lifts the per-opcode floor enough
  that scheduling cost dominates → parallel becomes 2-3× → worth the
  integration. Currently blocked by [[no_evmone_integration]] (cgo
  callback 30× slower than witness SLOAD).
- A read-only block (no SSTORE / no balance transfer) workload where
  the conflict graph is sparse — but in practice mainnet blocks at 25M
  are tx-dense and sender-heavy.

## Files left as-is

- `modules/state/parallel_executor.go` — keep `ExecuteBlockParallelF`
- `modules/state/parallel_apply_ibs.go` — keep `ApplyBlockCommitToIBS`
- `internal/ethel/parallel_evm.go` — keep `RealParallelEVM`, lazy-coinbase
- `internal/api/engine_state_adapter.go` lines 357-439 — keep shadow bench

No code change for this task; this document is the deliverable.
