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

## Layer-5 design (finalized after full path investigation + on-disk evidence)

On-disk evidence from the 7 wedged node DBs (blocks 137-140): 137/138 fully agreed;
CE[139] identical on all 7 (one surviving 139) but **canonical[139] missing on 6/7**
(CommitToCanonical silently deferred, never retried — service.go:247) and the persistent
head pointer never advanced past the replay head; CE[140] differs across nodes (each
imported a different competing 140 candidate; the ones built on the overwritten earlier
139 were mis-rejected).

Root-cause chain (line-verified):
1. Same-height siblings are speculatively EXECUTED at import (ValidateBody passes when
   the sibling's parent is canonical), against the **dirty world state** (PlainStateReader
   on current state, blockchain.go:962-968) — no unwind exists anywhere; QMDB re-appends
   → tree forks irreversibly (append-order-dependent root).
2. HotStuff is leader-driven → import never writes canonical (blockchain_write.go:366);
   canonicalization rests entirely on CommitToCanonical, which has **no retry** when the
   committed block hasn't arrived, and equal-height fork-choice is a 50% coin flip
   (forkchoice.go:107-111, preserve=nil).
3. VerifyHeader resolves the parent **by number** (canonical-only,
   adapter.go GetHeaderByNumber) → a child built on the non-canonical sibling is
   mis-rejected (BAD BLOCK) → wedge.
4. Latent: header.Root is overwritten by Finalize (adapter.go ibs.IntermediateRoot), and
   ValidateState's root check is disabled — sibling state divergence is silently absorbed;
   reorg()/CommitToCanonical are pointer-moves, so a branch switch never executes the new
   branch's blocks.

Fix plan (aligned to HotStuff-2: only the 2-chain-committed block is final; speculative
imports must be cleanly revertible):
- **L5-1** VerifyHeader/Prepare resolve the parent via `GetHeaderByHash(header.ParentHash)`
  (headers are stored by (number,hash) for non-canonical blocks too); absent →
  ErrUnknownAncestor (future-queue + fetch-on-miss). Removes the canonical-choice
  dependence from header validation.
- **L5-2a** `qmdb.Tree.ApplyUndo(*BlockUndo)` — the missing mutating revert (mechanism F):
  truncate nextSlot back to PrevNextSlot (invalidate appended slots: index remove, bitmap
  clear, twig leaf reset + recompute) and revive the recorded deactivated slots. Plus
  `QMDBRootComputer.RevertBlock`: ApplyUndo + rewind flushedThrough + delete orphaned
  positional rows / rewrite meta in the same tx. Undo data already persisted live
  (QMDBUndoWindow, 256 blocks).
- **L5-2b** Unwind-then-execute on same-height re-import / branch switch: track the
  applied head; when importing at height ≤ appliedHead, revert block-by-block (QMDB
  ApplyUndo + changeset old-value restore of PlainState + delete receipts/logindex/CE/
  changeset rows) down to the block's parent, then execute normally.
- **L5-3** CommitToCanonical becomes the authoritative executing switch: walk-back
  reconciles each height whose canonical hash changes (unwind + re-execute the committed
  branch — usually 1 block), writes the persistent head, and RETRIES when the committed
  block arrives later (drive from NotifyBlockImported). Receipts/LogIndex/CE rewrite
  naturally via re-execution.
- Gate: 7-node E2E — sustain ≥500 blocks past 13013140, unique hash per height across
  all 7, kill/restart self-heal.

### Layer-5 implementation status (landed)

All of the above is implemented and live-fire validated on the 7-node network:
- `qmdb.Tree.ApplyUndo` / `ApplyUndoWithStorage` (lib/qmdb/revert.go) — the mutating
  live-tree revert (mechanism F). Unit-tested: exact root round-trips across 40
  blocks/multiple twigs, the sibling-equivalence property (base+A+revert+B == base+B),
  flush/evict + reload consistency.
- `commitment.UnwindPlainStateBlock` + `QMDBRootComputer.RevertBlock` (re-points the
  cold reader at the current tx — reading through the last execution's closed tx
  segfaults in MDBX).
- `BlockChain.unwindForReimport` — applied-head tracking (`qmdbAppliedHead` marker),
  READ-ONLY lineage pre-check (unapplied sibling parent → ErrUnknownAncestor →
  future-queue, zero side effects), then per-block revert (QMDB + PlainState + CE +
  receipts/logs + undo row) down to the incoming block's parent. Wired before
  evmRecord in insertChain.
- VerifyHeader/Prepare resolve parents by `header.ParentHash` (not by-number
  canonical); CommitToCanonical retries when the committed block arrives
  (pendingCommit in the hotstuff service).
- ForkChoice falls back to height comparison when virtual-TD synthesis races tx
  visibility (equal-height sibling defers to the commit).

**Observed**: both historical wedges (139/140) broken in 21s; 7/7 convergence held;
+11 blocks; branch switches at heights 134/144/145 executed repeatedly and correctly
(logs: "branch switch: unwinding applied blocks", depth=1, followed by clean
re-execution); zero panics after the cold-tx fix.

## Layer 6 — OPEN: consensus liveness / P2P transport

With state consistency solved, the remaining stall is LIVENESS: at height 145 every
view's candidate imports and executes cleanly (branch switch works), but CommitQCs
stop forming — views time out in a loop while peers drop (7-node mesh degrades to
4 peers), and `fetch-on-miss: read failed: EOF` (present since the first deployment)
indicates the block-by-hash serving stream misbehaves under load. Suspects, in order:
1. the rpc_block_by_hash serving/read path (chunk EOF under concurrent requests),
2. peer disconnects during heavy import/unwind cycles (blocking the consensus topic),
3. vote/QC message loss across the degraded mesh.
Also noted: every node prints a "Genesis Hash Mismatch" banner at startup (the patched
alloc changes the genesis root/hash vs the registered MainnetGenesisHash) — it does not
block P2P or sync but should be reconciled (register the real staggered genesis hash).
