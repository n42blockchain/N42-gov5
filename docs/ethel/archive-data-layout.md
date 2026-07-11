# Archive-mode data layout (authoritative catalog)

Decision record, 2026-07-03. Defines what an archive node STORES as primary
data, what is DERIVED, and where each artifact lives (bounded MDBX vs
append-only freezer). Supersedes the ad-hoc layout used by the first
E:\n42-archive-test run, whose chaindata grew +93 GB over a 142k-block
catch-up because per-block changesets + history indexes were written INTO
the state MDBX.

## Principles

1. **witness is the derivation root for execution byproducts.** Receipts and
   account/storage changesets (acctcs/storcs) are fully reproducible by
   replaying per-block witnesses through the EVM (the existing
   witness-replay pipeline, `internal/ethel/witness_replay_pipeline.go`).
   They are NOT shipped as primary data; regenerate on bootstrap or on
   demand.
2. **The state MDBX stays bounded ≈ state size.** chaindata holds ONLY the
   hashed-canonical state: HashedAccounts / HashedStorage / TrieOf* / Code
   / headers / markers. Changesets, history indexes and every other
   append-forever stream live in freezer segment files (`*.cidx` +
   `*.NNNN.cdat`), not in MDBX — appends are cheap, cold segments are
   relocatable (EIP-4444 tooling), and the B-tree never bloats.
3. **The full ledger is kept.** bodyc (complete bodies), headerc, txindex,
   storhist, codes are primary archive data. Proof artifacts (witness,
   MPT-stateless anchors) join the same freezer directory as they are
   produced.

## Catalog

| Artifact | Class | Medium | Size @25.4M | Notes |
|---|---|---|---|---|
| HashedAccounts/HashedStorage/TrieOf*/Code | primary | MDBX chaindata | ~163 GB | bounded; no cs/history embedded |
| headerc | primary | freezer | 4.7 GB | full |
| bodyc | primary | freezer | 591 GB | full ledger (cold segments EIP-4444-relocatable) |
| txindex | primary | freezer | 13 GB | full |
| storhist | primary | freezer | 28 GB | full |
| codes | primary | freezer | 6 GB | by address, full history |
| witness | primary (proof) | freezer | 167 GB | derivation root; future proof files land beside it |
| acctcs / storcs | **derived** (from witness) | freezer | ~40-80 GB | needed hot for unwind + staged Merkle retain lists |
| receipts | **derived** (from witness) | freezer or on-demand | 172 GB if materialized | serve via regeneration; materialize only if RPC latency demands |
| MPT anchors | future (proof) | freezer dir | ~cadence-dependent | producer path exists (N42_ANCHOR_DIR) |

Shipped set (primary only): state 163 + ledger 643 + witness 167 ≈ **~975 GB**;
with derived cs materialized ≈ 1.03-1.06 TB. Receipts add 172 GB only if
materialized.

## Gaps to implement

1. **cs/history → freezer in the live import path.** The eldevp2p
   hashed-canonical writer (`HashedCanonicalWriter`) currently embeds
   `ChangeSetWriter` writing AccountChangeSet/StorageChangeSet + history
   index into MDBX. Target: emit per-block Erigon-V2 changeset blobs into
   acctcs/storcs freezer tables (same codec + tables the ethexec replay
   pipeline already writes via `writeOutputs`). Consumers to migrate:
   - `commitment.BuildRetainListFromChangesets` (staged Merkle): add a
     `cs.Source`-driven variant (decode V2 blobs → touched keys).
   - `commitment/unwind.go`: same (adapter.Reorg already supports
     `ReorgWithSource`).
2. **receipts/cs derivation from witness**: the witness-replay pipeline
   already outputs both; wire a bootstrap mode that regenerates them for a
   shipped witness set instead of expecting them pre-built.
3. **Existing E:\n42-archive-test chaindata** carries ~93 GB of embedded
   cs/history + MDBX slack; once (1) lands, a fresh migrate (or cs strip)
   returns it to ~163 GB.

## Sizing note (measured)

MDBX only ever grows; the async Merkle's long RoTx (~8 min snapshot)
prevents page reclamation while the writer produces GBs of dirty pages, so
budget the state MDBX at snapshot size +50% headroom during catch-up even
after (1) removes the cs/history stream.
