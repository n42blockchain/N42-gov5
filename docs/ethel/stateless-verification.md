# eth-el Minimal-Client Stateless Verification

> Status 2026-06-05. Package `internal/ethel/stateless` (21 tests, PowerShell-
> verified). Verifier (consumer) side complete. Producer (③b) DONE —
> `cmd/n42-stateless-blockproof-produce` (merged-walk + shared-pool, NOT mptproof;
> 3398 anchors genesis→24.998M client-verified 2026-06-05). Remaining: ④b real
> anchor `Header.Unmarshal` + checkpoint policy, ⑤b eth-el downloader integration
> (`--mpt-verify-interval` + multi-IDC workflow), and the ① proofwire compact-encoding
> optimization (verify.go uses correct RLP node sets today).

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

### ③b Producer — DONE (`cmd/n42-stateless-blockproof-produce`)

> 2026-06-05 update: this is implemented and verified. The "reuse `internal/mptproof`"
> plan below was SUPERSEDED — `mptproof` proves against N42's **unified** storage
> trie (composite key `keccak(addr)||keccak(slot)`, see `generator.go:227`), which
> does not fit P8's standard **two-level** model (`account.storageRoot` → per-account
> subtree). Do not reuse it for this.

The working producer is `cmd/n42-stateless-blockproof-produce`'s `buildBlockProof`:
it retains all touched keys (account hashes + storage composite keys) in ONE
`trie.WitnessRetainer` and proves them with a **single merged `CalcTrieRoot` walk**
(the erigon FlatDBTrieLoader two-level model over `HashedAccounts` + composite-key
storage) — O(trie scan), not O(accounts × scan). The flat node set is shipped once
as `BlockProof.AccountProof`; the consumer's `StateRootUpdater` treats it as a
**shared pool** for every account's storage subtree, so per-account `StorageProof`
is left empty (no node duplication) and each pre-state `storageRoot` is read back via
`stateless.StorageRootsFromProof`. Verified end-to-end: 3398 anchors (genesis→24.998M)
re-verified by `n42-stateless-client-test --transition` (changeset recompute == header).

~~Original plan (superseded): reuse `internal/mptproof` wire_full/wire_expand; reconcile
`AccountsTrie`/`StoragesTrie` vs `TrieAccount`/`TrieStorage` bucket names.~~

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
