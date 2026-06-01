// Package stateless implements MPT stateless state-root verification for the
// eth-el minimal client: given a TRUSTED pre-state root R_{n-1}, a block's
// changeset (the set of account/storage keys it changed, with their new values),
// and a multiproof (the Merkle paths of those keys in the pre-state trie plus
// the boundary sibling hashes), it recomputes the post-state root R_n and checks
// R_n == header_n.stateRoot — WITHOUT holding the full state trie.
//
// This is the trie analogue of an execution witness: a witness lets the EVM run
// statelessly (verify receipts); a multiproof lets the state root be recomputed
// statelessly. Together a minimal node can verify a block end-to-end at any
// height, non-contiguously, and produce a multi-signature attestation it submits
// to an aggregator for cumulative verification counting.
//
// # Terminology — two DIFFERENT artifacts (do not conflate)
//
// N42 carries two distinct per-block stateless artifacts. They are not the same
// thing and serve different layers:
//
//   - "witness" (layer ②, /witness, internal/ethel.WitnessStateReader,
//     freezer.TableBlockWitness): an ADDRESS-LESS, ordered STATE-READ STREAM —
//     just the values the EVM reads, in execution order, length-prefixed. It has
//     NO addresses, NO keys, NO Merkle proof, and is NOT self-verifying: the
//     replayer re-runs the same EVM and dequeues values in lockstep, so a wrong
//     stream is caught only downstream by the receiptRoot (②) / stateRoot (③)
//     check. It is a re-execution READ-LOG, not a Merkle "witness" in the
//     stateless-Ethereum sense. (Name kept for production compatibility — it is
//     the format ethexec/the mining SDK validated across 25M blocks.)
//   - "proof" / "multiproof" / BlockProof (layer ③, /anchor): MERKLE proof nodes
//     (account-trie paths + per-account storage subtrees + boundary sibling
//     hashes) that ARE self-verifying — each node's keccak is checked against its
//     parent's reference up to a trusted root. This is what recomputes / proves
//     the state root. EIP-1186 per-account and BlockProof (changeset) variants.
//
// Rule of thumb: ② "witness" = address-less value stream for re-EXECUTION;
// ③ "proof"/"anchor" = Merkle nodes for STATE-ROOT recompute/inclusion.
//
// # Why a self-contained partial trie
//
// N42's lib/trie has no geth-style mutable Trie + NodeResolver — only the
// FlatDBTrieLoader (full-table scan) and HashBuilder/GenStructStep (bottom-up
// build from an ordered leaf stream). Neither fits "apply a sparse changeset to
// a sparse proof". So this package carries its own minimal standard-Ethereum MPT
// over the EIP-1186 RLP node format already emitted by internal/mptproof
// (FullAccountProofBytes / FullStorageProofBytes) and verified by
// VerifyStandardProof. We:
//
//  1. Load every proof node into an in-memory map keyed by keccak(node) == the
//     hash its parent references. This is the "partial trie": only nodes on the
//     changed keys' paths exist; everything else is a hash reference (the
//     boundary the proof provides).
//  2. For each changed key, descend from the root using the standard MPT rules,
//     verifying keccak(node)==expected at every hop (this re-uses the proof
//     verification invariant, so a tampered proof is rejected), then set/delete
//     the leaf value per the changeset.
//  3. Re-hash bottom-up: any node touched by a change is re-encoded (RLP) and
//     re-hashed; its parent's child slot is updated to the new hash; repeat up
//     to the root. Untouched siblings keep their proof-provided hashes.
//  4. The recomputed root is compared to the trusted header.stateRoot.
//
// Insert (new key) and delete (key→absent) reshape the trie (branch split /
// collapse, extension grow/shrink). The partial trie handles these with the
// standard node-type transitions; the proof must include enough boundary nodes
// for the reshape (mptproof's FullProofBytes already expands target subtrees).
//
// # Data sizes (measured, 100 real blocks @ 25.09M, cmd/n42-stateless-bench)
//
//	witness (changeset values) : ~7.8 KB/block
//	MPT multiproof (dedup)     : ~277 KB/block (p99 ~451 KB); ~36x the witness
//	zstd                       : ~0% (proof body is 32B keccak hashes, high entropy)
//
// So the per-block MPT cost is real (~36x the execution witness). The minimal
// client therefore makes root verification a configurable cadence
// (--mpt-verify-interval): every block (full zero-trust), every N (periodic
// anchor, amortized ~2.8 KB/block at N=100), or on-demand (audit only). Daily
// operation stays on trust-wire-root + receipts (see engine_state_adapter).
package stateless
