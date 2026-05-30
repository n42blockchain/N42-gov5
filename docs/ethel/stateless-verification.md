# eth-el Minimal-Client Stateless Verification

> Status 2026-05-30. Package `internal/ethel/stateless` (20 tests, PowerShell-
> verified). Verifier (consumer) side complete. Producer side (③b) and
> integration (④b/⑤b) are the remaining work; the producer should extend
> `internal/mptproof`, not duplicate it.

## Goal

Let an eth-el **minimal** node verify any block end-to-end — at any height,
non-contiguously, interruptibly — without holding the full state trie, and emit
a multi-signature attestation that an aggregator counts. The trie analogue of an
execution witness: a witness makes the EVM run statelessly (verify receipts); a
**multiproof** makes the state root recomputable statelessly.

## Three-layer trust model

```
[anchor header]  trust root (checkpoint: hard-coded / social consensus / PoS finality)
   │ parentHash hash-chain  (~600 B/header, ~30 MB for a week of 12 s blocks)
   ▼ propagates the anchor's trust to every block's stateRoot + receiptRoot
for any block N after the anchor — targets already anchored ⇒ independent / parallel:
   ① header chain : parentHash traces to anchor → header.stateRoot/receiptRoot trusted
   ② EVM exec     : witness (~8 KB) replays txs → receiptRoot == header  ✓
   ③ state root   : MPT multiproof (~277 KB) preRoot+changeset recompute == header.stateRoot ✓
```

Because every block's pre/post target is anchored by the header chain, block N's
verification does **not** depend on block N−1 — so a window of blocks verifies in
parallel (`VerifyBatch`). Fast bootstrap: fetch a window of `(witness, proof)`,
fan out, a block is zero-trust verified once its goroutine returns nil; then
sweep to tip.

## Measured data sizes (100 real blocks @ 25.09M, `cmd/n42-stateless-bench`)

| item | size | note |
|---|---|---|
| witness (changeset values) | ~7.8 KB/block | EVM-exec check |
| MPT multiproof (dedup) | ~276.7 KB/block (p99 ~451) | state-root check; ~36× witness |
| zstd of multiproof | ~0% | boundary hashes are high-entropy keccak |
| block (header+body) | ~80–150 KB mainnet | reference |

zstd barely helps; the saving is **structural** — the compact wire
(`proofwire.go`) omits recomputable internal-node hashes. Cadence is
configurable (`--mpt-verify-interval`): every block / every 100 (anchor,
amortized ~2.8 KB/block) / on-demand. Daily operation stays on
trust-wire-root + receipts (`engine_state_adapter`).

## Components (all in `internal/ethel/stateless`, all tested)

| file | role |
|---|---|
| `node.go` / `rlp.go` / `decode.go` / `hash.go` | minimal standard-Ethereum MPT node model + RLP/HP codec (N42 `lib/trie` has no geth-style mutable Trie) |
| `partial_trie.go` | partial trie from a multiproof; get/insert/delete with boundary `hashNode` resolution; bottom-up `hash()` |
| `account.go` | account-leaf codec `RLP[nonce,balance,storageRoot,codeHash]`, byte-identical to `account.EncodeForHashing` |
| `stateroot.go` | two-level `StateRootUpdater`: storage change → re-root per-account subtree → patch account leaf storageRoot → re-root account trie |
| `headerchain.go` | ④ anchor + parentHash chain → trusted per-block stateRoot/receiptRoot; `Prune` rolling window |
| `proofwire.go` | compact multiproof wire (structure + boundary hashes + leaf values). NOTE: a faithful-encoding fix is pending — a child whose RLP is ≥32 B must be referenced by hash, not inlined, or the decoded trie re-hashes to a different root. `verify.go` therefore uses RLP node sets today, not the compact wire. |
| `verify.go` | `BlockProof` (RLP node sets) + `VerifyStateRoot` / `VerifyAgainstChain` |
| `batch.go` | `VerifyBatch` — parallel window verification (race-clean) |
| `attest.go` | ⑥ `Attestation` (secp256k1, pkg `N42/crypto`) + `AttestationPool` (distinct-signer count, allowlist, threshold, fork-split) |
| `minimal.go` | `MinimalVerifier` orchestrator (anchor → extend → verify → attest) |

### Anchoring (correctness foundation)

`anchor_test.go` feeds the same `(accounts, storage)` to the production
`commitment.TrieRootComputer.ComputeRoot` (what eth-el `header.Root` actually
uses) and to this engine, asserting **byte-identical roots**. This proves the
engine computes the real chain's state root, not a self-consistent look-alike.
(It first caught a real double-encode bug in `encodeRef`.)

## Remaining work

### proofwire faithful encoding

`EncodeCompactProof` must preserve each child's inline-vs-hash boundary exactly:
a child whose canonical RLP is ≥32 B is referenced by its 32-byte hash in the
parent; only <32 B children are inlined. The current encoder loses this when it
materialises lazy `hashNode`s, so a decoded trie can re-hash to a different root.
Until fixed, `verify.go` uses standard RLP node sets (correct, just larger).

### ③b Producer — generate `BlockProof` from real state

Producing a `BlockProof` (account multiproof + per-contract storage proofs +
changeset) from real N42 state requires walking `TrieAccount`/`TrieStorage`
(N42 `BranchNodeCompact`) for changed keys' paths + boundary sibling hashes.
**Do not build a standalone BranchNodeCompact→proof extractor** —
`internal/mptproof` (`wire_full.go` / `wire_expand.go`) already converts N42 trie
nodes to standard-MPT proof bytes. Reuse it; read the per-block changeset
(`AccountChangeSet`/`StorageChangeSet`, decodable today — see
`cmd/n42-stateless-bench`) for `Changes`. mptproof is keyed to
`AccountsTrie`/`StoragesTrie`; a migrated datadir uses `TrieAccount`/`TrieStorage`
— reconcile the bucket names.

### ④b Anchor source

Decide the checkpoint policy and load+decode the real anchor `*block.Header`
(`block.Header.Unmarshal` is a proto+trailer hybrid). `NewMinimalVerifier`
already accepts it.

### ⑤b eth-el integration

Wire `MinimalVerifier` into the eth-el downloader behind `--mpt-verify-interval N`
(1 = every block, 100 = periodic anchor, 0 = off / on-demand), and the
multi-IDC workflow (#9): producers run EVM to emit `(witness, proof)`, keep one
rolling week indexed by block number (codes one week + incremental), verifiers
fan out + attest, aggregator counts.
