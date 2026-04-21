# Parallel EVM for n42 ethexec — Feasibility & Implementation Plan

**Status**: Research complete (2026-04-21). Phase 1 PoC starts next.

## Executive summary

n42 ethexec currently runs at **~55 blk/s ≈ 0.82 Ggas/s** on ETH mainnet
block 13.6M (sequential Go EVM). pprof shows `EVMInterpreter.Run = 55% cum`
dominates — further optimization requires architectural change.

Empirical conflict analysis on **20,000 mainnet blocks @ 18M** shows
**87% of tx pairs are non-conflicting at the account-level granularity**.
Ideal speedup at N cores (assuming zero overhead):

| Cores | Ideal speedup | Realistic Go impl | Target blk/s |
|---|---|---|---|
| 4 | 3.95× | ~1.8–2.0× | ~100 |
| 8 | 7.33× | ~2.5–3.0× | ~130–160 |
| 16 | 8.87× | ~2.8–3.2× | ~150–170 |

Even discounting for Go's ~30-40% per-core penalty vs Rust and Block-STM's
~50% implementation overhead (Grevm 2.1 measured 1.5× on Rust), a Go port
can realistically achieve **2.0–2.5× throughput** at 8 cores.

Engineering cost: **~3,000 LoC**, ~4–8 weeks for one engineer.

## Data: conflict analysis

Tool: `cmd/conflict-analyze`. Method: static, account-level analysis. For
each tx: sender, `tx.To` (if data/value), EIP-2930 AccessList entries go
in read/write sets. Conflict iff write sets overlap or cross read/write.

### Block range 17,900,000 – 18,100,000 (sample 1/10, n = 19,994 blocks)

```
Avg parallel ratio:  0.870  (87% of tx pairs disjoint)
Conflict pairs:      7.68%  of all pairs
Avg tx/block:        141.7
Ideal speedup N=4:   3.95×
Ideal speedup N=8:   7.33×
Ideal speedup N=16:  8.87×
```

Width distribution (colors per block = chromatic number of conflict graph,
Welsh-Powell greedy upper bound → lower bound on real parallelism):

```
width 1:     0.03%  (single-tx or fully-serial blocks)
width 2-3:   0.07%
width 4-7:   2.50%
width 8-15:  49.98%  ← median; sweet spot for 8-core scheduling
width 16-31: 40.18%
width 32-63: 4.25%
width 64+:   3.00%
```

### Hot contracts (top writer addresses, % of txs that write them)

| Addr | % | Name |
|---|---|---|
| `dac17f958d...` | 8.20 | USDT |
| `3fc91a3afd...` | 8.45 | MEV / TG-bot router (18M era) |
| `7a250d5630...` | 6.15 | Uniswap V2 Router |
| `c02aaa39b2...` | 2.16 | WETH |
| `a0b86991c6...` | 2.03 | USDC |
| `1111111254...` | 0.97 | 1inch aggregator |
| `32400084c2...` | 1.02 | Beacon deposit |

Hot contracts create narrow serial bottlenecks. At slot-level granularity
(true Block-STM) these would parallelize better — two `USDT.transfer` calls
touch disjoint `balanceOf[account]` slots. Our account-level estimate is
therefore a LOWER BOUND on real parallelism.

### Comparison pre/post-Merge

| Range | Parallel ratio | Median width |
|---|---|---|
| 13.0M–14.0M (pre-Merge) | 0.837 | 16–31 |
| 17.9M–18.1M (post-Merge) | **0.870** | 8–15 |

Post-Merge blocks are more uniformly sized (fixed 12s slots) and feature
more diverse tx sources (MEV aggregators, L2 bridges, staking). This is
where Block-STM pays off most.

## Related work (2026)

- **Aptos Block-STM**: https://arxiv.org/abs/2203.06871 — original algorithm
- **RISE pevm** (Rust): https://github.com/risechain/pevm — cleanest reth-compatible port, 2.0–2.2× on mainnet replay. **Primary reference.**
- **Grevm 2.1** (Rust, Galxe): https://github.com/Galxe/grevm — 1.5 Ggas/s vs 1.0 for sequential reth
- **Sei V2**: 64.85% of ETH mainnet txs parallelizable (their methodology)
- **BSC parallel EVM** (Go, BNB mainnet 2024+): https://github.com/bnb-chain/bsc — Go reference for interface patterns
- **Aptos AIP-107**: pre-execution hints for faster convergence

## Implementation plan

### Phase 1 — PoC foundations (2 weeks)

Goal: build the core data structures and scheduler, tests pass.

**New files**:
- `modules/state/mvhashmap.go` (~300 LoC + tests)
  - Versioned key-value store: `map[K] -> [](txIdx, incarnation, value)`
  - API: `Write(key, txIdx, inc, val)`, `Read(key, txIdx) -> (val, writerTxIdx, writerInc, found)`
  - Uses `sync.Map` shards + per-key sorted slice (locked writes, lock-free reads)
- `modules/state/parallel_scheduler.go` (~400 LoC + tests)
  - Atomic `execution_idx`, `validation_idx` counters
  - Task types: Execute(txIdx), Validate(txIdx)
  - Dependency tracking + re-execution logic
- `common/gaspool.go` patch (~50 LoC)
  - Replace `uint64` with `atomic.Uint64` for concurrent SubGas/AddGas

**Non-goals in Phase 1**:
- Do NOT integrate with `IntraBlockState` yet
- Do NOT modify `process.go` tx loop
- Just verify the primitives in isolation with unit tests

**Deliverable**: `go test ./modules/state/ -run TestMVHashMap` + `TestScheduler`.

### Phase 2 — IntraBlockState isolation (3 weeks)

- Per-tx journal instance (split `modules/state/journal.go`, ~350 LoC)
- `stateObject` epoch field (~250 LoC)
- `IntraBlockState.Fork(txIdx)` returns tx-local view reading from MVHashMap, falling through to MDBX
- `IntraBlockState.Merge(txIdx)` writes to MVHashMap
- Integration tests: parallel tx execution on toy workloads

### Phase 3 — Executor integration (3 weeks)

- New `parallel_processor.go` (~500 LoC) alongside existing `process.go`
- CLI flag `--parallel-evm` to switch between sequential and parallel paths
- Pre-Cancun SELFDESTRUCT: poison-write approach (tx dependents re-execute)
- Coinbase + gas pool deferred to commit phase
- Log indices assigned at commit, not during speculative execution

### Phase 4 — Verification & tuning (2 weeks)

- State root verification: parallel replay vs sequential for [13M, 18M]
- blk/s benchmark at GOMAXPROCS=4, 8, 16
- pprof to identify actual bottlenecks (MVHashMap lock contention, GC pressure)
- Tuning: shard count, worker count, MVHashMap backing map choice
- Go/No-Go decision: if speedup < 1.5× at 8 cores, investigate Rust revm FFI

## Risks

| Risk | Severity | Mitigation |
|---|---|---|
| Data race in state cache → wrong root | **HIGH** | Replay full chain, bit-for-bit compare |
| Go GC pressure from MVHashMap churn | MED | `sync.Pool` for write records; measure with `GODEBUG=gctrace=1` |
| Gas refund edge cases under parallel | MED | Apply refunds only at commit phase; test explicit fixtures |
| SELFDESTRUCT + CREATE2 (already a saga here) | MED | Reuse existing collectPreWipeSlots logic; add parallel-specific tests |
| Single-writer MDBX contention at commit | LOW | Already serialized via `asyncFlush.hand` |
| Actual speedup < 1.5× at 8 cores | MED | Phase 4 Go/No-Go; Rust FFI as plan B |

## Non-goals (out of scope)

- Parallel merklization (state root computation). Forward-replay doesn't
  compute per-block root; only at `verify-root` / verify interval.
- Rust revm FFI / `go-revm` binding. Considered but Go ecosystem has no
  mature revm CGO wrapper in 2026.
- Static dependency prediction (e.g. DAG-based as in pre-EIP-2930 era).
  Optimistic Block-STM handles this dynamically.

## References

- conflict-analyze results: `profiles/conflict_13m.txt`, `profiles/conflict_18m.txt` (local)
- Grevm 2.1 announcement: https://medium.com/@galxe/introducing-grevm-2-1-1c1fe0e4d3b4
- RISE pevm source: https://github.com/risechain/pevm
- Aptos Block-STM: https://arxiv.org/abs/2203.06871
- Reth 2.0 release: https://www.paradigm.xyz/2026/04/releasing-reth-2-0
- Sei V2 parallelizability study: https://blog.sei.io/sei-v2-performance-data/
