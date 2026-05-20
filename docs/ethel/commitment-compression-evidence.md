# Commitment Compression — Evidence & Decision

**Date:** 2026-05-20
**Status:** A-track investigation complete, B-track partial (Round 1)
**TL;DR:** Reth-style MPT commitment (37 GB / 25M-block mainnet) is **already at the schema lower bound**. None of MPHF+fp / JMT-BMT content-addressing / HPH ReplaceKeysInValues delivers further savings. Commitment is **1.7%** of the 2.14 TB archive total — engineering effort here is low-leverage.

---

## Context

User goal: "extreme compression of commitment (current and history)" for the N42 archive node positioned as reth-style (small storage, on-demand historical proof).

Reth's mainnet archive at 25M blocks:

| Component | Size | % of archive |
|---|---|---|
| `PlainState` (latest accounts + storage values) | 153 GB | 7.1% |
| `ChangeSets` (per-block writes) | 874 GB | 40.8% |
| `History` (inverted index) | 400 GB | 18.7% |
| **`AccountsTrie` + `StoragesTrie` (live trie)** | **37 GB** | **1.7%** |
| `Bytecodes` | 18 GB | 0.8% |
| Headers / bodies / tx / receipts (static_files) | ~660 GB | ~30.8% |
| **Total** | **2.14 TB** | 100% |

Commitment is **1.7%** of the archive. Any savings on this slice are small in absolute terms.

---

## A-track candidates evaluated

### A2 — Content-addressed dedup (JMT / BMT)

**Hypothesis:** Content-addressed trees (same hash = same key) might dedup identical subtrees at different paths, yielding smaller storage than path-keyed MPT.

**Result (commit `3ca647e2`):**
- Measured: `cmd/n42-jmt-from-reth` on 100K real reth PlainAccountState entries.
- Inserted into JMT via `BatchUpdate`.

| Metric | Value |
|---|---|
| Samples (real reth account addrs) | 100,000 |
| JMT nodes after BatchUpdate + Flush | 548,044 (5.48 nodes/leaf) |
| JMT total bytes | 113.97 MB (1140 B/leaf) |
| **Extrapolated to 386M accounts** | **~440 GB** |
| reth `AccountsTrie` baseline | **5.4 GB** |
| **Ratio** | **JMT 80× larger** |

**Verdict:** **DISPROVED.** Subtree-dedup gain is ~0% on uniformly-random hashed keys (mainnet density). Each random 64-nibble leaf path creates ~log₁₆(N) ≈ 5 unique internal nodes; subtrees never collide. Meanwhile reth's 16-way MPT packs 16 children per branch (~150 B amortising many leaves) and is structurally far more compact.

JMT/BMT's real value is in **historical proof dedup** (versioned trees with stale-node-index) and **ZK-friendly hashing**, **not** in mainnet-density storage.

---

### A1 — HPH ReplaceKeysInValues short-key references

**Hypothesis:** Erigon's HPH commitment branches contain plaintext account/slot keys. The `ReplaceKeysInValues` optimization replaces these with 8-byte file-offset refs into adjacent segment files, saving ~12-24 bytes per occurrence. Erigon empirical: 1.5-2× total commitment savings.

**Result (commit `<this commit>`):**
- Measured: `cmd/n42-trie-branch-anatomy` dissecting reth's `AccountsTrie` branch encoding (200K samples).

| Metric | Value |
|---|---|
| Avg key bytes (Nibbles path) | 6.34 |
| Avg value bytes (BranchNodeCompact) | **159.22** |
| Theoretical minimum (masks + only required hashes, no padding) | **159.22** |
| **Encoding overhead** | **0.00 bytes (0.00%)** |
| Avg hashes per branch | 4.79 |
| Branches with explicit root hash | 0.0% |

**Verdict:** **NOT APPLICABLE.** reth's `BranchNodeCompact` is already at the byte-level theoretical lower bound:
- `state_mask(2) + tree_mask(2) + hash_mask(2) + 32 × popcount(hash_mask)`
- No padding, no plaintext keys (those live in `PlainAccountState`)
- 0.00% encoding overhead

The Erigon RKV gain comes from replacing **plaintext keys inside HPH commitment values**. reth has already separated keys from trie structure entirely — there are no keys in the trie nodes to replace.

Erigon's "1.5-2× commitment savings" from RKV is measured vs Erigon-without-RKV, **not** vs reth. reth already has the structural equivalent.

---

### A3 — No persistent trie, full on-demand recomputation

**Hypothesis:** Don't persist `AccountsTrie`+`StoragesTrie` at all. Recompute stateRoot from `PlainState` whenever needed. Saves 37 GB.

**Result (analytical, not new code):**
- This is the **degenerate form of the path we already implemented** in [`internal/historicalstate`](../../internal/historicalstate/). Reth already supports per-key state-as-of via `ChangeSets` + `PlainState`; we mirrored that.
- For proofs at block N, you need to recompute trie nodes along the accessed leaf paths. This requires reading the latest PlainState + applying reverse-deltas from ChangeSets.
- Cost: ~150 GB read per "full recompute" (impractical), or ~1-50 ms per single-leaf proof (acceptable).

**Verdict:** **MARGINAL SAVINGS, MAJOR UX HIT.** Saves 37 GB (1.7% of archive). Loses fast latest-block proof. Same trade reth already explicitly chose by keeping `AccountsTrie`/`StoragesTrie`. Not worth flipping.

---

## A-track conclusion

| Path | Predicted | Measured | Decision |
|---|---|---|---|
| A2 (JMT/BMT dedup) | "could be smaller" | **80× larger** | ❌ Eliminate |
| A1 (HPH RKV refs) | "1.5-2× smaller" | **0.0% overhead** (already optimal) | ❌ Not applicable |
| A3 (no-persist) | "saves 37 GB" | 37 GB / 2.14 TB = 1.7% | ❌ UX hit too big |

**Commitment IS the lower-bound implementation already.** No "extreme compression" path exists at the commitment layer that doesn't sacrifice query performance.

---

## What "extreme compression" actually means for archive

Real storage bottleneck is **not** commitment. It's:

1. **ChangeSets (874 GB / 40.8%)** — already MPHF+fp compressed to 137 GB in `D:\n42-history-full` (6.4× saving). Bigger gains possible via schema-aware encoding (cross-block addr dedup, RLP-aware varint).
2. **Headers + bodies (~660 GB / 30.8%)** — already batch-zstd compressed in cdat. Further gains require either CGo libzstd dict trainer or schema-aware preprocessing.
3. **History inverted index (400 GB / 18.7%)** — EliasFano + RecSplit already.

Engineering allocation: **drop A-track, focus on B-track + history encoding.**

---

## B-track Round 1 (commit `e9a3cac2`)

**B0 — Trie compress with MPHF+fp (commit `f68d0125`):**
- Apply N42's MPHF+fp coldstore format to reth's AccountsTrie.
- Result: 5.4 GB MDBX → 4.78 GB MPHF+fp. **11.5% saved** (just MDBX B-tree page overhead removal). Data itself is incompressible random hashes.

**B1 — zstd dict via pure-Go klauspost/compress (commit `e9a3cac2`):**
- Train a "pseudo-dict" by concatenating training samples as `History`.
- Result on N42-eth1177 acctcs (25.1M items, 8.6 KB avg):
  - 64 KB dict: 0.8% saving
  - 1 MB dict: 2.2% saving
- Senders crash with "invalid offset in dictionary" — pseudo-dict approach unstable.
- Pure Go has **no real dict trainer** (no `ZDICT_trainFromBuffer` equivalent).

**Remaining B paths:**
- **B1 proper** — Add libzstd CGo dep (`github.com/DataDog/zstd` or `github.com/valyala/gozstd`). Expected 15-30% additional savings. Adds CGo dependency.
- **B2 schema-aware** — RLP-aware varint, cross-block addr dedup table, tx selector dict. Expected 25-40% saving on bodyc (567 GB → ~340-425 GB). Large engineering effort (1-2 weeks).
- **B3 status quo** — accept current `compact.go` SpeedBestCompression.

---

## Decision (pending)

A-track is settled by evidence. B-track Round 1 shows pure-Go ceiling is ~2%. The decision is:

1. Drop further commitment-encoding work — reth-style is structurally optimal.
2. Either commit to libzstd CGo (B1 proper) or schema-aware encoding (B2) for the real bottleneck (bodyc, ChangeSets, history).

Spike artifacts kept as regression evidence:
- `cmd/n42-trie-compress` (commit `f68d0125`)
- `cmd/n42-zstd-dict-spike` (commit `e9a3cac2`)
- `cmd/n42-jmt-from-reth` (commit `3ca647e2`)
- `cmd/n42-trie-branch-anatomy` (this commit)

Cross-reference:
- [`docs/bench_state_report.md`](../bench_state_report.md) — synthetic 1M-block comparison of 5 commitment engines (note: synthetic, doesn't reflect mainnet density)
- [`internal/historicalstate/reader.go`](../../internal/historicalstate/reader.go) — reth-style historical state query, the actual A3-equivalent we shipped
