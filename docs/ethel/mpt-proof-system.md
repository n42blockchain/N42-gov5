# N42 MPT Proof System — Implementation Summary

**Date:** 2026-05-21
**Status:** ✅ Phases A–D complete, production-validated against `D:\reth2k\db` (~386M accounts) + `D:\n42-history-full` (137 GB)
**Predecessor design:** [`archive-commitment-final-design.md`](archive-commitment-final-design.md)

---

## 1. What was built

A reth-format Merkle Patricia Trie (account + storage) maintained on top of the existing N42 snapshot + history, with an EIP-1186 wire-format proof generator that answers both `latest` and historical state-as-of queries.

| Layer | Path | Size | Role |
|---|---|---|---|
| Latest plain state (reth schema) | `D:\reth2k\db` (PlainAccountState + PlainStorageState) | ~52 GB | Leaf-value source |
| Per-block history | `D:\n42-history-full` | 137 GB | `internal/history.AsOf` |
| **Latest MPT (built)** | `D:\n42-mpt\{accounts,storage}-mptcache\` → `D:\n42-chaindata\` after migrate | ~37 GB | reth `BranchNodeCompact` cache |
| **Proof generator** | `internal/mptproof` (~4 KLOC) | code | Walk + sibling-rebuild + wire emit |
| **RPC adapter** | `internal/state_proof_mptbuild.go` | code | `StateProofProvider` implementation |

**Functional parity with `eth_getProof`** (account proof + per-slot storage proofs against `latest`) and **historical state-as-of** (returns `AccountValueAtBlockN` / `StorageValueAtBlockN`; the latest-root proof bytes are returned as the verifiable anchor, see §6 "Limitations").

---

## 2. Phase timeline

| Phase | Deliverable | LOC | Output |
|---|---|---|---|
| A | MPT builder (3-pass: scan → ETL sort → HashBuilder → AppendDup) | ~800 | `D:\n42-mpt\{accounts,storage}-mptcache` |
| B | MPT reader (Walk, sibling collection, BranchNode decode) | ~600 | `internal/mpttrie` |
| C | Proof generator MVP (AccountProof / StorageProof structs) | ~500 | `internal/mptproof` |
| C.5 | Walk-fold self-consistency verify | ~250 | `verify.go` |
| D pre | Migrate two envs → unified `D:\n42-chaindata` MDBX | ~300 | `cmd/n42-mpt-migrate` |
| D.2 | Subtree-rebuild verify (recovers inline siblings stripped by compact) | ~400 | `verify_subtree.go` |
| D.1 | EIP-1186 wire format (`ProofBytes` + `VerifyStandardProof` oracle) | ~700 | `wire.go`, `wire_verify.go` |
| D.1.5 | Target-subtree expansion (extension/branch nodes between deepest branch and leaf) | ~400 | `wire_expand.go`, `wire_full.go` |
| D.3 | RPC `StateProofProvider` integration | ~250 | `internal/state_proof_mptbuild.go` |
| D.4 | Historical state-as-of (`HistoricalLeafSource`, `HistoricalProof` bundle) | ~300 | `historical.go` |
| **E** | docs + production validation + diagnostic tooling | n/a | this file + `cmd/n42-proof-debug` |

---

## 3. Public API

```go
// Open
g, err := mptproof.New(mptproof.Config{
    ChaindataDir: "D:\\n42-chaindata",  // unified mode (preferred)
    HistoryDir:   "D:\\n42-history-full",
    Leaves:       leaves,                // RethLeafSource | MapLeafSource
})

// Latest proofs
proof, _ := g.LatestAccountProof(addr)        // walk + leaf
proofs, _ := g.LatestStorageProofs(addr, slots)
bundle, _ := g.LatestProof(addr, slots)       // account + storage in one shot

// Wire format
pb, _ := g.FullAccountProofBytes(proof)       // [][]byte, EIP-1186 RLP
val, found, _ := mptproof.VerifyStandardProof(pb, stateRoot, hashedAddr)

// Historical state-as-of
res, _ := g.HistoricalProof(mptproof.HistoricalProofRequest{
    Address: addr, Slots: slots, BlockN: 25_000_000,
})
// res has: LatestStateRoot, AccountValueLatest, AccountValueAtBlockN,
//          LatestAccountProof (wire bytes), StorageProofs[], Notes
```

The RPC integration (`internal/state_proof_mptbuild.go`) wraps this behind the existing `StateProofProvider` interface, returning EIP-1186-shaped responses to `eth_getProof`.

---

## 4. Production validation

**Hardware:** Ryzen 9 9950X, 128 GB RAM, NVMe, Windows 11.
**Data:** `D:\reth2k\db` (~386M accounts at block 25M), `D:\n42-history-full` (Mar 2026 build).
**Target:** USDC `0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48`.

| Metric | Value |
|---|---|
| Account walk hops | 7 |
| Leaf depth | 7 |
| Total siblings collected | 97 |
| Inline siblings at deepest hop | 4/16 |
| Full proof nodes emitted | 9 |
| Full proof bytes (total) | 3,779 |
| Per-node sizes | 532, 532, 532, 532, 532, 532, 404, 115, 68 |
| Wall time (full proof) | 15m26s |
| Wall time (cold ScanAccounts) | ~3-4 min each |
| Oracle verify (`VerifyStandardProof`) | ✅ passes against latest stateRoot |

**Wall-time breakdown:** hops 0–5 are dense (16/16 children all stored as 32-byte hashes), so their full branch nodes assemble in microseconds. Hop 6 has 11/16 children present but only 8/16 hashed — the remaining 3 are inline-encoded sub-trees that reth's `BranchNodeCompact` strips. Each inline sibling triggers a full `ScanAccounts` of the 386M-row reth plain table (~3-4 min on this NVMe) to rebuild its subtree. 4 inline siblings × ~4 min ≈ 15 min total. **Sequential, not concurrent.**

This is the cost model for the slow path. The hot-path optimization (Phase F, §6) targets ms-level by replacing `ScanAccounts` with a hashed-key range cursor.

---

## 5. Component map

```
internal/mptproof/
├── generator.go         ✦ Generator (entry point), LatestAccountProof, LatestStorageProofs
├── source.go            ✦ LeafSource interface, RethLeafSource, MapLeafSource, callbackSafe wrappers
├── historical.go        ✦ HistoricalLeafSource overlay, HistoricalProof bundle
├── verify.go            ✦ walk-fold self-verify (cheap)
├── verify_subtree.go    ✦ subtree-rebuild verify (handles inline siblings)
├── wire.go              ✦ basic ProofBytes (fails on inline siblings)
├── wire_full.go         ✦ FullAccountProofBytes/FullStorageProofBytes (rebuilds inlines)
├── wire_expand.go       ✦ D.1.5 target-subtree recursive emitter
├── wire_verify.go       ✦ VerifyStandardProof (independent EIP-1186 oracle)
└── (tests)              ✦ synthetic round-trip + production-data _SLOW guards

cmd/
├── n42-mpt-build/       Phase A production builder
├── n42-mpt-migrate/     two-env → unified D:\n42-chaindata
├── n42-proof-debug/     interactive USDC runner with defer/recover + Step-3 oracle verify
└── n42-reth-scan-test/  isolated ScanAccounts bisection tool

internal/state_proof_mptbuild.go   D.3 RPC adapter implementing StateProofProvider
```

Build target: stays out of n42 binary by default (no init wiring); explicitly opt-in via RPC config.

---

## 6. Hot-path optimization (Phase F, 2026-05-21)

The 12-15 min SLOW-path latency is now solved. The hashed-key index
(see [`mpt-proof-hot-path-design.md`](mpt-proof-hot-path-design.md))
was bootstrapped on production data in 3h53m24s
(`docs/proof-archive/bootstrap_20260521_031803.log`):

| Phase | Wall | Detail |
|---|---|---|
| Scan + ETL sort (parallel) | 1h24m48s | 386M account rows + 1.57B storage rows |
| AppendDup load (serial) | 2h28m35s | MDBX single-writer constraint |
| **Total** | **3h53m24s** | |

Resulting MDBX env grew from 36 GB → **246 GB**.

**USDC hot-path measurement:**

| Step | Cold (SLOW path) | Hashed (this) | Speedup |
|---|---|---|---|
| LatestAccountProof (walk) | 0.07 s | 2 ms | ~35× |
| FullAccountProofBytes | 760 s | <1 ms | >700,000× |
| VerifyStandardProof oracle | <1 ms | <1 ms | — |
| **End-to-end** | **760 s** | **20 ms** | **~38,000×** |

Same wire-format output: 9 nodes, 3779 bytes, oracle PASS. Hashed
path is selected automatically via `LeafSource` type switch in
`collectAccountLeavesWithPrefix` / `collectStorageLeavesWithPrefix`.

To use the hot path in tests / RPC:

```go
g, _ := mptproof.New(mptproof.Config{
    ChaindataDir: "D:\\n42-chaindata",
    Leaves:       base, // any LeafSource (RethLeafSource etc)
})
hashed, _ := mptproof.NewHashedLeafSourceFromDB(g.UnifiedEnv(), base)
g.SetLeafSource(hashed)
// FullAccountProofBytes / FullStorageProofBytes are now hot-path
```

Bootstrap reproducer:

```powershell
n42-mpt-hashedindex `
  --reth        D:\reth2k\db `
  --chaindata   D:\n42-chaindata `
  --etl-buf-mb  4096
```

---

## 7. Remaining limitations & future work

### Historical proof is currently "value-as-of + latest-root anchor"

`HistoricalProof.LatestAccountProof` is verifiable against `res.LatestStateRoot`; `AccountValueAtBlockN` is the historical value but the per-block historical *root* is not currently computed. For RPC consumers needing "proof at block N", this requires per-block trie rebuilds (Phase D.4.1, deferred). The current API is sufficient for use cases that already trust the archive (most light-client and indexer workloads) and want a verifiable anchor to the latest known state.

### MapSize defaults assume archive scale

`NewRethLeafSource` defaults to 4 TB map. Smaller deployments (light-archive nodes, dev) should pass a tighter `mapSizeGB` to avoid 4 TB sparse files on macOS/Linux. Windows handles sparse files gracefully.

### `go test` timeouts

Production-data tests (`TestFullProofBytes_Production_USDC_SLOW`, `TestHistoricalProof_Production_USDC`, `TestHistoricalProof_WithStorageSlots`) require `-timeout 30m`. Without it, the test runner SIGKILL at the default 10 min produces a truncated goroutine dump that **looks like** a panic. The tests now `t.Skip` with a clear message when the deadline is too short or `-short` is passed. See `internal/mptproof/wire_full_test.go:97-104`.

---

## 8. Operational runbook

### Initial build (one-time)

```powershell
# Phase A: build account + storage tries from reth plain state
n42-mpt-build --reth D:\reth2k\db `
              --out  D:\n42-mpt `
              --account-table PlainAccountState `
              --storage-table PlainStorageState

# Phase D pre: consolidate two envs into one MDBX
n42-mpt-migrate --src-accounts D:\n42-mpt\accounts-mptcache `
                --src-storage  D:\n42-mpt\storage-mptcache `
                --dst          D:\n42-chaindata
```

### Health check / oracle verify

```powershell
# Cheap walk-only proof for any address (seconds):
n42-proof-debug --addr a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48 --skip-full-proof

# Full standard proof + Step-3 oracle verify (~15 min currently):
n42-proof-debug --addr a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48 --block 25000000
```

### Long-form test suite (CI / release validation)

```powershell
# Short tests (~2 s) — always run, skips all production-data tests:
go test -count=1 -short ./internal/mptproof/

# Full production validation (~15 min wall):
go test -count=1 -timeout 30m -run TestFullProofBytes_Production_USDC_SLOW -v ./internal/mptproof/
go test -count=1 -timeout 30m -run TestHistoricalProof_ -v ./internal/mptproof/
```

Archived runs: `docs/proof-archive/usdc_production_run_*.log`.

---

## 9. References

- Design doc: [`archive-commitment-final-design.md`](archive-commitment-final-design.md)
- reth vs Erigon HPH analysis: [`reth-mpt-vs-erigon-hph.md`](reth-mpt-vs-erigon-hph.md)
- A1–A4 compression investigation (all rejected): [`commitment-compression-evidence.md`](commitment-compression-evidence.md)
- EIP-1186: <https://eips.ethereum.org/EIPS/eip-1186>
