# N42 Commitment Domain — Engineering Plan

**Date:** 2026-05-21
**Status:** Plan, not started
**Predecessors:**
- [`mpt-proof-system.md`](mpt-proof-system.md) — current Phase A-D state
- Erigon `../erigon/execution/commitment/` + `../erigon/db/state/` — reference impl

---

## 1. Why this exists

Current MPT proof system has two unsolved problems:

1. **Storage proofs take seconds** in the unified storage trie because
   reth's compact `BranchNodeCompact` omits hashes for some children,
   forcing on-demand sub-tree rebuild. Inline-sibling rebuild at hops
   within the address-keccak portion of the composite key matches
   millions of leaves from heavy accounts (USDC, Uniswap pools, etc).

2. **MPT cache is frozen at bootstrap height**. There is no per-block
   updater. To advance the cache by one block, you must re-bootstrap
   (~4 h). This is intolerable for a live RPC node.

Both are solved by Erigon's commitment-domain pattern.

## 2. What we copy from Erigon

| Erigon mechanism | What it gives us |
|---|---|
| **CommitmentDomain** as 4th Domain (`db/state/domain.go:71`) | Trie data sits alongside accounts/storage/code in one Domain abstraction; shares the same temporal-tx atomicity |
| **TouchKey** → **Updates** btree → **ComputeCommitment** (`execution/commitment/commitmentdb/commitment_context.go:246-302`) | Per-block update flow: collect touched keys → walk HPH → emit modified branches |
| **Full interior hashes stored** (no compact omission) | EIP-1186 proof = direct slot read, never any sub-tree rebuild |
| **Plain-key referencing** (`db/state/domain_committed.go:42-136`) | Branch nodes embed account/storage keys as uvarint offsets into the corresponding domain's file (1-3 B vs 32 B keccak) — 5-15× compression of branch internals |
| **Step/merge model** (`db/state/aggregator.go:60-165, 267-337`) | Steps of ~1000 blocks accumulate in MDBX; periodic merge writes immutable `commitment.{N-M}.kv` snapshot files |
| **kv/kvi/bt/kvei file family** (`db/state/snap_schema.go`) | `.kv` zstd payload + `.kvi` recsplit accessor + `.kvei` bloom; same shape we use for `internal/history` MPHF stores |

We already have most of the lower-level pieces:

| Piece we have | Source |
|---|---|
| HexPatriciaHashed (HPH) — fold/unfold/Process | `lib/commitment/` |
| MPT BranchNodeCompact reader (rmove compact omission) | `internal/mpttrie/` |
| ETL + AppendDup MDBX patterns | `lib/etl/`, `internal/mptbuild/` |
| Recsplit MPHF + zstd dict | `internal/history/store_mphf.go` |
| Snapshot manifest + blake3 chain | `cmd/reth-snapshot-export/` |

What's missing: **the glue**.

## 3. Target architecture

```
                +-----------------------+
                | New block N+1 arrives |
                +-----------+-----------+
                            |
              freezer.Changeset(N+1) =
              {(addr, slot, oldVal, newVal), ...}
                            |
                            v
          +------ Begin temporal RW tx -------+
          |                                    |
          |  Apply state diff:                 |
          |    AccountsDomain.Put              |
          |    StorageDomain.Put               |
          |    CodeDomain.Put                  |
          |                                    |
          |  TouchKey for each change          |
          |    Updates btree (sorted by keccak)|
          |                                    |
          |  ComputeCommitment(tx, blockN+1):  |
          |    HPH.Process(updates)            |
          |    for each modified branch:       |
          |      CommitmentDomain.Put(         |
          |        branchKey, encoded,         |
          |        txNum, prevValue)           |
          |    return newRoot                  |
          |                                    |
          |  if newRoot != header.StateRoot:   |
          |    Rollback  (all 4 domains revert)|
          |                                    |
          |  Commit                            |
          +------------------------------------+
                            |
                            v
              Step boundary every ~1k blocks:
                aggregator.collateAndBuild()
                aggregator.MergeDomainFiles()
                  -> snapshots/domain/commitment.{N-M}.kv
                     snapshots/domain/commitment.{N-M}.kvi
                     snapshots/domain/commitment.{N-M}.bt
```

## 4. Phased delivery

### Phase G1 — CommitmentDomain table + writer (1-2 weeks)

**Deliverable**: a `CommitmentDomain` MDBX table in `D:\n42-chaindata`, populated by Phase A-style one-shot build from snapshot at H_snap.

- New table: `CommitmentDomain` (in chaindata env).
  - key = hex nibble path (1-128 nibbles)
  - val = encoded branch node (state_mask + tree_mask + hash_mask + child hashes, **hash_mask = state_mask always**)
- Modify `internal/mptbuild/` to emit dense form (not reth compact) for both AccountsTrie and StoragesTrie merged into CommitmentDomain.
- Validation: build at H_snap, root matches block header's stateRoot.

**Wins after G1**: storage proof goes to ms (no rebuild needed since all hashes cached). MPT cache size grows ~2× (compact 36 GB → dense ~72 GB), but we delete the 210 GB of HashedAccount/HashedStorageRef so net is much smaller.

Expected size: ~75-80 GB CommitmentDomain (dense).

### Phase G2 — Plain-key referencing (1 week)

**Deliverable**: branch encoding stores plain-key references as uvarint snapshot ords instead of 32-byte keccak hashes.

- Inside each branch's encoded value, when referencing a leaf, store `uvarint(snapshot_ord)` instead of `keccak(addr) || keccak(slot)`.
- At proof time, expand `snapshot_ord → val` via snapshot.MPHF reverse lookup or sidecar table.
- Expect 3-5× shrink of CommitmentDomain → ~20-30 GB.

Borrowed from `erigon/db/state/domain_committed.go::EncodeReferenceKey`.

### Phase G3 — Per-block live updater (2-3 weeks, core work)

**Deliverable**: `internal/mptlive/` package that processes block changesets to update CommitmentDomain in MDBX.

- `Updater.Apply(blockN, changeset)` runs inside the caller's RwTx.
- Uses `lib/commitment.HPH` Process() with TouchKey-style update batching.
- Writes modified branches to CommitmentDomain table.
- Returns computed stateRoot for caller to verify against header.

This is the analogue of Erigon's `ComputeCommitment` + `SharedDomainsCommitmentContext.putter` from `commitment_context.go:170-302`.

Atomicity guarantee: all writes happen inside the caller's MDBX tx. Failure → caller calls `Rollback` → state and commitment both revert together.

### Phase G4 — Aggregator + snapshot merge (3-4 weeks)

**Deliverable**: step-based aggregator that flushes completed steps to immutable snapshot files.

- Step size: 1024 blocks (~3.4 h at 12s/block).
- After step boundary: collate CommitmentDomain entries with txNum < step_end → write `commitment.{stepStart}-{stepEnd}.kv` (zstd-compressed) + `.kvi` (recsplit) + `.kvei` (bloom).
- MDBX hot data > some retention (e.g., 32 steps) gets garbage collected.
- Reader stitches: snapshot files (cold) + MDBX (hot) seamlessly.

Borrowed from `erigon/db/state/aggregator.go::collateAndBuild` + `MergeDomainFiles`.

### Phase G5 — Snapshot bundle distribution (1 week)

**Deliverable**: snapshot rotation runbook (weekly) — new H_snap produces new commitment files; nodes download bundle = snapshot + commitment files; verify root against block header.

- `cmd/n42-snapshot-build`: read mdbx-head plain state → generate `n42-snapshot/` (keccak-keyed MPHF) + `n42-mpt/commitment.*.kv` (dense + plain-key referencing).
- Manifest tracks: H_snap, stateRoot, blake3 hashes.
- Fast-sync downloads bundle; verifies stateRoot; ready to serve proofs.

## 5. Total cost

| Phase | Work | Storage delta | Wall-time win |
|---|---|---|---|
| Current | (no action) | 246 GB chaindata + reth-dependent | 35 s storage proof |
| G1 dense MPT | 1-2 weeks | + ~40 GB CommitmentDomain<br/>(delete 210 GB old) → **−170 GB net** | ms storage proof |
| G2 plain-key ref | 1 week | CommitmentDomain 75 GB → ~25 GB | same |
| G3 live updater | 2-3 weeks | nil | enable per-block updates |
| G4 step/merge | 3-4 weeks | snapshot files take over old steps | nil |
| G5 distribution | 1 week | nil | new-node sync in minutes |

Total: **8-11 weeks** for the full plan. Phase G1+G2 alone (~2-3 weeks) recovers 170 GB and fixes storage proof speed.

## 6. Risk register

1. **HPH integration with N42 conventions**: `lib/commitment` HPH is ported but has subtle account encoding differences from Erigon master (incarnation, code hash). Validate stateRoot byte-equality before claiming compatibility.
2. **Concurrent reads during writes**: MVCC is given by MDBX, but the Aggregator's snapshot rotation needs visible-set atomic publish (file-list swap). Erigon has `DirtyFiles → VisibleFiles` for this; we'll need the same pattern.
3. **Plain-key referencing reverse lookup**: snapshot must support `ord → val` (we have via `accounts.val` sequential reads). Reverse `key → ord` already exists (MPHF). But we need `keccak → val` lookup at proof time which is `MPHF(keccak) → ord` then `ord → val` — two file reads. Cost should be < 10 µs.

## 7. Out of scope (deferred)

- **Historical state-as-of proofs at arbitrary block N** — Erigon's History domain pattern, much bigger scope. Current `D:\n42-history-full` handles values; per-block trie root reconstruction is a different undertaking (Phase D.4.1).
- **Reorg handling at trie level** — Phase G3 only updates at FINALIZED blocks (reorg-impossible by definition). Live MPT at head requires more careful MVCC.
- **Multi-chain support** — Phase G stays mainnet-only.

## 8. Open questions for next planning round

1. **Tooling source**: should the build pipeline come from reth (current bootstrap), or from a future N42 self-hosting plain state? Affects whether N42 can drop reth.
2. **Snapshot rotation cadence**: weekly is the working assumption; might be better hourly or daily for tight finality lag.
3. **Whether to keep `internal/mptproof` as-is or fold into a new `internal/commitment/` package**. Naming + module boundaries decision.

## References

- Erigon code paths cited above (all in `../erigon/`):
  - `db/state/domain.go:71` — Domain struct
  - `db/state/domain_committed.go:42-136` — plain-key referencing
  - `execution/commitment/commitmentdb/commitment_context.go:246-302` — ComputeCommitment flow
  - `execution/commitment/trie/proof.go:40-57` — proof generation
  - `db/state/aggregator.go:60-165, 267-337` — step/merge
  - `db/state/snap_schema.go` — file format
  - `db/kv/tables.go` — CommitmentDomain = 3 alongside Accounts/Storage/Code
