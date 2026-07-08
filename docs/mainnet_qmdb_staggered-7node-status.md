# mainnet_qmdb_staggered 7-Node Live Network — Status & Layer-5 Follow-up

Base data + deploy for a 7-node HotStuff live network that continues producing
blocks past a replayed head, with **built-in transaction simulation** (`--dev.txgen`)
and **simulated mobile-voting committee** (chainspec `committeePool`, 200k pool /
512-per-block, auto). Designed so real mobile devices gradually take over via
`consensus_registerCommitteeValidator` (dial the simulation down as `ReplacedCount()`
rises).

## What works (validated this session)

- **Chain** `mainnet_qmdb_staggered` (chainId 94, hotstuff, qmdb, staggered forks):
  `params/chainspecs/mainnet_qmdb_staggered.json` + registrations in `params/config.go`,
  `params/network_preset.go`, `params/profile.go`, `internal/genesis_block.go`.
- **Data**: `replay-v2 --chain mainnet_qmdb_staggered --tree qmdb --bls-reseal --bls-seed
  0x03c75de6…` → `E:/n42-qmdb-staggered-7node` (35 GB, head 13,013,133, **txFailed=0**
  via the 624-address exact-minimum genesis top-up; receiptMismatch ~2930 = fork-semantics,
  not balance).
- **Deploy**: `E:/deploy-7node.ps1` (7 validators from BLS seed `0x42..42` — matches the
  chainspec; peer IDs from network keys 11..11–77..77; `--dev.txgen --dev.txgen.max 31`).
- **Observed**: 7 nodes boot, sync to head, **converge**, and produce ~5-6 new blocks
  (13013134–139) before wedging (see below).

## Bugs found & fixed (committed on feat/eth-el-snapshot-direct)

The deploy runbook (`mainnet_qmdb-7node-deploy.md`) only ever validated at block ~1000
(no forks active, empty blocks, minimal view-changes). Running live at a **fork-active
head** (all EIPs incl. Fusaka active, committee ramp, real 3s timing) exposed a cascade
of never-tested paths:

1. **Chain-name registration** — `network_preset.go` / `profile.go` rejected the new chain.
2. **System-contract bytecode** (`internal/replay/genesis_v2.go`) — EIP-2935/7002/7251
   used a broken draft blob (`JUMP 0x0000` → "invalid jump destination") the moment live
   production invokes the history-storage system call under Prague. Replay never hit it
   (builds headers directly). Now uses canonical `vm.HistoryStorageCode` etc. **mainnet_qmdb
   is affected too.**
3. **changeset AppendDup collision** (`internal/blockchain_write.go`) — same-height
   re-import (view-change / competing proposal / reorg) collides on the AppendDup
   changeset tables → `MDBX_EKEYMISMATCH`, wedging the node. Fixed with
   `changeset.Truncate(tx, blockNum)` before re-write (no-op on forward path).
4. **committee-evidence link** (`internal/consensus/hotstuff/adapter.go`) — ParentBeaconRoot
   read the parent CE from the by-number store, which a speculative same-height candidate
   overwrites. Now re-derives the parent CE deterministically from the actual parent
   header. Necessary but not sufficient (see Layer 5).

## Layer 5 — OPEN dedicated follow-up: fork-choice / canonical convergence

**Symptom**: after ~6 blocks the 7 nodes wedge at a height where HotStuff view-changes
produce **competing same-height blocks** (e.g. block 139 hash A vs hash B). Nodes disagree
on which 139 is canonical; block 140 is built on one 139 while a peer holds a different 139
canonical → `ParentBeaconRoot mismatch` / `unknown ancestor` / endless `import-gated vote:
no matching pending proposal`. The Layer-4 fix is correct but a node still verifies 140
against **its** canonical 139 (fetched by number), not the exact parent 140 extends.

**Root**: the live import path writes canonical-ish state (changeset / ConsensusEvidence /
PlainState world state) at **speculative import**, and `CommitToCanonical` / `reorg()`
(`internal/blockchain.go`) only move the head/canonical-hash pointer — they do **not**
unwind or reconcile CE/changeset/state to the 2-chain-committed block. So competing
same-height branches are not cleanly reconciled and nodes don't converge on one committed
parent before extending it.

**HotStuff-2 alignment (do NOT hack around view-changes)**: only the 2-chain-committed
block is final; speculative imports must be cleanly revertible. Proposed direction:
- `reorg()` should go through `commitment.UnwindHashedCanonical` (already exists) to unwind
  state to the fork point, then replay/canonicalize the committed branch.
- At `CommitToCanonical`, reconcile the by-number CE (and changeset) for each newly-canonical
  height to the committed block (`WriteConsensusEvidence(num, BuildBlockEvidence(committed))`).
- Verify a header against the block it actually extends (`header.ParentHash`), not the
  by-number parent, so a node imports/reconciles the exact parent first.
- Then the CE non-determinism at real-validator hand-over (`internal/blspool/handover.go:12-17`)
  needs CE propagation with the block (P2P), since re-derivation only holds for the
  all-simulated committee.

This is a multi-session consensus-hardening project, tracked here. The 3 committed fixes
above are independently valuable regardless.
