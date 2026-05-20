# N42 Archive Commitment — Final Design

**Date:** 2026-05-20
**Status:** Decided — implementation pending
**Predecessors:**
- [`commitment-compression-evidence.md`](commitment-compression-evidence.md) — A1-A4 investigation (all rejected)
- [`reth-mpt-vs-erigon-hph.md`](reth-mpt-vs-erigon-hph.md) — side-by-side
- [`archive-engineering-summary.md`](archive-engineering-summary.md) — overall N42 archive context

---

## 1. Decision

**Add a reth-style MPT layer on top of the existing N42 snapshot + history, build it ourselves from N42 data (not copied from reth). Persist into MDBX (not flat file) to support catch-up + 12-second live sync + 256-block MVCC diff layers.**

### Build target choice (updated 2026-05-20)

| Backend | Bucket used (accounts) | Live update? | MVCC | Verdict |
|---|---|---|---|---|
| Flat sorted file | 4.52 GB | ❌ rewrite whole file | ❌ | rejected — breaks under sync |
| **MDBX with AppendDup** | **5.29 GB** | ✅ B-tree mutation | ✅ native | **chosen** |
| reth MDBX (mixed insert) | 5.40 GB | ✅ | partial | reference |
| RocksDB | ~5.0 GB est. | ✅ | snapshot ref-count | rejected — new CGo dep, Windows pain |

MDBX AppendDup gives us ~95% page fill (vs ~80% for mixed inserts), so we still beat reth by 2% on storage while gaining native MVCC for the 256-block diff-layer requirement.

### Final archive layout

| Layer | Status | Path | Size | Role |
|---|---|---|---|---|
| Latest plain state | ✅ existing | `D:\n42-snapshot` | 52 GB | Account/storage value lookup by addr/slot |
| Per-block history | ✅ existing | `D:\n42-history-full` | 137 GB | MPHF+fp; `internal/history.AsOf` for state-as-of |
| Historical state reader API | ✅ existing | `internal/historicalstate` | code | Combines snapshot + history |
| **Latest MPT commitment** | ❌ **TO BUILD** | `D:\n42-mpt/{accounts,storage}.{nibbles,vals}` | ~37 GB | reth-format MPT for `eth_getProof("latest")` |
| **Historical proof generator** | ❌ **TO BUILD** | `internal/mptproof` | code | Combines MPT walk + historicalstate overlay |
| **Total archive** | | | **~226 GB** | Full commitment + state + history |

### Comparison vs alternatives

| Architecture | Total storage | Latest proof | Historical proof |
|---|---|---|---|
| N42 snapshot+history only | 189 GB | N/A | partial (state only, no proof) |
| **N42 + reth-MPT (chosen)** | **226 GB** | **100 µs** | **ms-second** |
| N42 + Erigon HPH with history | ~1.7 TB | 100 µs | ms |
| reth archive (reference) | 2.14 TB | 100 µs | seconds (recompute) |
| TopCache K=4 (A4, rejected) | ~250 GB | 50 ms | 100-300 ms |
| Geth archive (legacy) | ~16 TB | 100 µs | µs (persisted everything) |

**N42 + reth-MPT is 9.5× smaller than reth archive at equivalent functional level.** Wins come from: snapshot (3× via codedict+MPHF+zstd), history (9× via MPHF+fp), and reusing reth's already-optimal MPT branch encoding.

---

## 2. Why reth-MPT (not Erigon HPH)

Full analysis in [`reth-mpt-vs-erigon-hph.md`](reth-mpt-vs-erigon-hph.md). Summary:

| Criterion | Outcome |
|---|---|
| Storage (latest commitment) | reth: 37 GB | Erigon: 40-50 GB → reth ~30% smaller |
| Engineering complexity | reth: ~5K LOC | Erigon: ~25K LOC → 5× simpler |
| Aligns with N42 archive philosophy | reth: ✅ minimalist | Erigon: ⚠️ +1.5 TB commitment-history is wasteful for us |
| Latest proof latency | both ~100 µs | tie |
| Historical proof latency | reth: ms-s | Erigon: ms — Erigon wins, but we deprioritise historical perf |
| Write throughput | reth: lower | Erigon: 2-5× faster — only matters for active validators, not archive readers |

**reth-MPT chosen.** Erigon HPH's main advantage (fast historical proofs via persisted commitment-history) costs +1.5-2 TB — exactly the storage we explicitly traded away by choosing the reth model.

---

## 3. Implementation plan

### Phase A — MPT Builder (1.5 weeks)

**Goal:** Produce `D:\n42-mpt/{accounts,storage}.*` files containing the latest MPT in reth's `BranchNodeCompact` format.

**Approach:** Build from `D:\n42-snapshot` (not copied from reth — N42 must be self-contained). Pipeline:

```
1. Open n42-snapshot account.kv via MPHF reader
   → iterate ALL (addr, account_value) pairs

2. ETL.Collector to external sort by keccak(addr):
   key   = keccak(addr) [32 B]
   value = account_value
   Spill to disk if exceeds buffer.

3. Bottom-up MPT build:
   - Read sorted (hashed_key, value) entries
   - For each, push to in-memory trie builder (lib/commitment/HashBuilder)
   - HashBuilder flushes branches as soon as a new key diverges from common prefix
   - Output: stream of (nibble_path, BranchNodeCompact bytes)

4. Persist to D:\n42-mpt/accounts.nibbles + accounts.vals:
   - .nibbles: sorted nibble paths (sparse-indexed for binary search)
   - .vals: BranchNodeCompact bytes at offsets

5. Same for storage with (keccak(addr) ‖ keccak(slot)) sort.
```

Estimated build time on 32-core / NVMe:
- Accounts (386M): ~2-4 hours (dominated by external sort)
- Storage (1.57B): ~8-16 hours
- One-shot during initial archive setup; not in critical path

**Critical reuse:** `lib/etl.Collector` for external sort; `lib/commitment` already has HPH writer code we can leverage (just need to emit reth's BranchNodeCompact format instead of HPH branch encoding).

**Tool:** `cmd/n42-mpt-build/main.go` with `--snapshot DIR --out DIR --table accounts|storage`.

### Phase B — Reader API (1 week)

**Goal:** `internal/mpttrie` package — point-lookup nodes by nibble path.

```go
type Reader interface {
    GetBranch(path []byte) (*BranchNodeCompact, bool, error)
    GetRootHash(table string) (common.Hash, error)
    Close() error
}

func Open(dir string) (Reader, error)
```

Internally: binary search on `.nibbles` for given path → read corresponding offset from `.vals`.

Tests: verify root hash against known state-root from N42 blocks.

### Phase C — Proof Generator (1 week)

**Goal:** `internal/mptproof` — eth_getProof for both latest and historical blocks.

```go
type ProofResult struct {
    AccountProof  [][]byte
    StorageProofs map[common.Hash][][]byte
    Balance, Nonce, CodeHash, StorageHash common.Hash
}

func (p *Generator) LatestProof(addr common.Address, slots []common.Hash) (*ProofResult, error)
func (p *Generator) HistoricalProof(addr common.Address, slots []common.Hash, blockN uint64) (*ProofResult, error)
```

- **Latest path:** walk reader's persisted MPT directly. ~100 µs.
- **Historical path:**
  1. Rebuild trie subset on accessed path using:
     - `historicalstate.AccountAsOf(addr, blockN)` for the leaf
     - For each sibling on the proof path, query its (subtree-hash) value:
       - First try latest MPT (still valid if no modifications since blockN)
       - If modified: rebuild that sibling subtree from leaves at blockN via historicalstate iteration
  2. Compose proof bytes.
  Latency: ms (recent blocks) to seconds (deep history with many modifications since).

### Phase D — RPC Integration (3-5 days)

Wire `internal/mptproof.Generator` into `internal/api/blockscout.go`'s `eth_getProof` handler, replacing the current JMT-or-fallback path.

### Phase E — Tests & Documentation (3-5 days)

- Unit tests for builder, reader, proof generator
- Integration test against reth's `eth_getProof` for known (addr, block) pairs
- Bench latency
- Update `CLAUDE.md` and `archive-engineering-summary.md`

### Total schedule

| Phase | Duration | Status |
|---|---|---|
| A — MPT Builder | 1.5 weeks → **actual ~3 days** | **✅ done for accounts; storage in progress** |
| B — Reader API | 1 week | not started |
| C — Proof Generator | 1 week | not started |
| D — RPC integration | 3-5 days | not started |
| E — Tests / docs | 3-5 days | not started |
| **Total** | **~4-5 weeks** | |

### Phase A actual results (accounts, 2026-05-20)

`cmd/n42-mpt-build` ran on full 386M PlainAccountState from reth:

  leaves                386,066,282
  branches              28,936,485   (=reth +1 for root)
  bytes/leaf            11.90        (vs reth 14.00)
  bucket used           5.29 GB      (real MDBX data, sums leaf+branch+overflow pages)
  pass-1 (scan+sort)    10m35s       (374-595K rows/s through ETL)
  pass-2 (HashBuilder)  5m15s
  pass-3 (AppendDup)    1m14s        (391K writes/s)
  total                 17m05s
  state root            0x781240..0033 (deterministic; not Ethereum canonical
                        because we use reth Compact values as leaves — Phase A.5
                        will add Compact→RLP transcoding for canonical root)

vs reth AccountsTrie MDBX: **5.29 GB vs 5.40 GB → 2% saved** via AppendDup
ordering (~95% page fill vs reth's mixed-insert ~80%). Functional
parity achieved with marginal storage win.

Storage table (1.57B entries) building in background.

---

## 4. Open questions (to resolve at Phase A kickoff)

1. **Use lib/commitment's HPH writer or write our own?**
   - lib/commitment has HPH which produces HPH branch encoding, NOT reth's BranchNodeCompact.
   - Options:
     - (a) Adapt lib/commitment to emit BranchNodeCompact format
     - (b) Write thin BranchNodeCompact writer from scratch (~500 LOC)
   - Lean: (b), simpler and avoids touching lib/commitment.

2. **One-shot rebuild or incremental updates per block?**
   - Initial: one-shot (build once at archive setup; treat as cold archive).
   - If N42 becomes a live full node: need incremental updater (later).
   - Lean: one-shot for now, defer incremental.

3. **Storage format for the MPT files**
   - Option 1: MDBX table (`AccountsTrie`/`StoragesTrie`), reth-equivalent layout
   - Option 2: cdat-style flat sorted file with sparse index
   - Lean: option 2 — matches N42's other coldstore patterns; immutable + content-addressable possible.

4. **Verify root hash matches block stateRoot?**
   - Yes — final step of build should verify computed root against the header `stateRoot` of the target block (must specify which block in build).
   - Mismatch = hard error.

---

## 5. Risks

| Risk | Probability | Mitigation |
|---|---|---|
| External sort of 1.57B storage entries OOMs | Medium | Use lib/etl with controlled buffer; spill to NVMe |
| HashBuilder bug produces wrong root | Low | Verify against reth's known root |
| 4-5 week schedule slips | Medium | Phase A is the longest; can be parallelised with Phase B/C planning |
| Build time on slower disks | Medium | Document NVMe as recommended; HDD fallback acceptable for one-shot |
| Need to rebuild on chain reorg | Low | Initial archive is post-finality; reorgs handled by future incremental updater |

---

## 6. Next concrete action

**Start Phase A spike:** write `cmd/n42-mpt-build` skeleton with:
- Snapshot reader (reuse existing MPHF reader)
- ETL collector for external sort
- BranchNodeCompact writer (~500 LOC, new)
- Stop after ~1000-block worth of state, verify correctness against reth's BranchNode encoding for those keys

This 2-3 day spike validates the approach before committing to full ~5-week implementation.
