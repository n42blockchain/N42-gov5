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
block P2P or sync (the fork digest is derived from the registered canonical constant on
all nodes) but should be reconciled (register the real staggered genesis hash).

### Layer-6 fixes landed (necessary, not yet sufficient)

Six fixes, each independently correct and live-fire informed:
1. Static peers joined `peersToWatch` — re-dialed every 10s on disconnect (previously a
   dropped static peer was NEVER re-dialed: no discovery, no reconnect loop; the mesh
   was a one-way ratchet down). Verified live: mesh now recovers 4→6.
2. Connection gater exempts static peers from bad-score rejection (dial/addr/secured)
   and loopback from the per-IP inbound rate limit (7 local nodes share 127.0.0.1's
   bucket; reconnect bursts had them rejecting each other).
3. `peers.Status.SetTrusted` — static peers are never classified bad at the SCORING
   layer, covering every IsBad consumer at once (gater + the sync layer's
   goodbye+disconnect on status revalidation).
4. Consensus broadcasts moved off the serial output loop (goroutine) — vote/timeout
   broadcasts no longer queue behind CommitToCanonical/persistState heavy work or
   PublishToTopic's 30s topic-peer spin.
5. Timeout re-broadcast also RE-SENDS this view's vote (votes were single-shot; observed
   live: all nodes logged "voting now" yet no QC formed). Duplicate votes are no-ops at
   the leader (DuplicateVoteError).
6. fetch-on-miss not-found now writes an explicit error response instead of silently
   closing (naked EOF was indistinguishable from transport faults and drowned the logs),
   and per-peer misses log at Debug.

### Layer-6 REMAINING (next session)

Live-fire after the six fixes still stalls at 0 new blocks on restart with two clear
signals:
1. **Control-message rate limiting still scores/disconnects trusted peers**: goodbye /
   status topics are limited to ~1/s burst 1 (`internal/sync/rate_limiter.go:57-59`) and
   over-limit requests penalize the sender (`:126-129`) — a 7-node simultaneous restart
   storm trips this immediately (observed: `validate Request failure …
   /rpc/goodbye/1/ssz_snappy remaining=0` followed by disconnects). Trusted peers need a
   rate-limiter exemption (or control topics need sane burst sizes for a validator mesh).
2. **Restart liveness**: nodes restore persisted HotStuff state to different views
   (engine restored to view 15/16/17), then everyone is a follower waiting for a
   proposal that keeps getting lost in the churn; no block is ever built. Needs a
   post-restart proposal recovery path (leader re-proposes on view entry even without a
   fresh trigger, or view sync on startup).
3. Mid-term (from L6 investigation): the direct-vote path is dead code (rotor's
   `RegisterValidator` is never called in production and `p2p.Service` doesn't implement
   `P2PDirectSender`) — every vote is a gossip broadcast today. Implementing real direct
   send would shrink the vote path's exposure to mesh churn.
The overall pattern: the scoring/limiting stack was tuned for a large open network and
fights a small fixed validator mesh point by point. Consider a "validator mesh" P2P mode
that switches these heuristics off wholesale instead of exempting one at a time.

### Validator-mesh mode + parent-fetch + leader-trigger fixes (landed, second batch)

- **Validator-mesh mode**: trusted (static) peers are fully exempt from request rate
  limiting (`rate_limiter.go` validateRequest/validateRawRpcRequest via
  `peers.Status.IsTrusted`) — the 1/s control-message buckets penalized restart storms
  into disconnects. **Live-fire verified: 0 disconnects across a full run** (previously
  the mesh oscillated every few seconds).
- **Missing-parent recovery**: `insertSideChain`'s bare "missing parent" now returns
  ErrUnknownAncestor (a bare error marked the child BAD and dropped it permanently);
  both the gossip and direct-push import paths future-queue the child AND actively
  `FetchBlockByHash(parent)` — a committed same-height sibling from a passed view is
  never re-gossiped, so passive waiting deadlocked at the first divergence (observed:
  "insert failed err=missing parent" at 146 on every node).
- **Leader trigger drop**: `newWorkCh` was UNBUFFERED and TriggerBlockProduction sends
  non-blocking — the leader's one build request per view was silently dropped whenever
  runLoop wasn't parked exactly on the receive. Observed live: "TC formed, I am the new
  leader" followed by nothing on all 7 nodes. Now capacity 1 (one pending build queues).

### FINAL REMAINING — post-restart view synchronization (pacemaker level)

With the mesh stable (0 disconnects) and all state/import layers fixed, a 7-node
restart from a wedged height still fails to converge: nodes restore persisted HotStuff
state to different views; in the latest run NO node ever claimed leadership (zero
"TC formed" — timeout collectors never reach quorum because nodes sit in different
views, and each advance resets the collector). handleFutureViewTimeout exists (a
future-view timeout advances the receiver) but convergence appears racy when views are
spread and every advance restarts collection. Directions, in order of preference:
1. On startup, don't trust the persisted view alone: broadcast a view-sync probe (or
   simply the timeout for the persisted view) immediately, and adopt max(persisted,
   highest-observed) BEFORE starting the pacemaker timer.
2. Bracha-style fast-forward: on receiving f+1 distinct timeouts for views > current,
   jump to the (f+1)-th highest view even mid-collection, carrying collected timeouts.
3. Blunt fallback: don't persist/restore the view at all — restart from the highest
   committed view and let the first timeout round elect a leader (slower but converges).
Reproduce: seed 7 nodes from E:/n42-qmdb-staggered-7node, run to the first wedge,
restart all 7 — they restore to spread views and never form a TC.

### RESOLUTION — pivot from polluted-DB archaeology to the HotStuff-2 restart model (2026-07-09)

The "post-restart view synchronization" framing above proved WRONG in two steps:

1. **View sync was never broken.** With patient verification (baseTimeout=60s ×
   2^backoff → TC forms in 2-4 min), the whole pipeline was traced live and fixed
   link by link: TC forms → leader claims → build triggers (newWorkCh buffering)
   → the deterministic rebuild reaches Seal (sealHash dedup now timer-engine-only)
   → an already-imported candidate is re-proposed (resultLoop re-push +
   NotifyBlockSealed) → proposals extend the LockedQC block, pinned through
   TriggerBlockProduction(parentHash) + AlignAppliedBranch (HotStuff-2 safety) →
   followers chain-align synchronously along stored parents. Commits 9329990f,
   8df411a0, c1ef48a7 + the chain-align fix.

2. **The remaining non-convergence was an invalid test fixture, not a code bug.**
   The wedged-height DB had accumulated 10+ same-height sibling candidates across
   many experiment rounds with different (buggy) code versions: nodes restored
   with divergent applied heads and locked QCs, and the future queue kept tugging
   the applied head between dead siblings. That state cannot arise in production
   and is not what the paper's recovery model addresses.

Per HotStuff-2, a recovering validator trusts only COMMITTED blocks and converges
via consensus (proposals carrying JustifyQC), not by replaying stale local
candidates. Implemented as `revertSpeculativeOnStartup` (blockchain.go Start):
if the tracked applied head differs from the canonical (= commit-driven) head,
revert the speculative blocks in one pass via AlignAppliedBranch. Uncommitted
candidates become inert sidechain archive.

Validation plan: reseed all 7 node dirs from the pristine replay DB
(E:\n42-qmdb-staggered-7node), run the current code from the replayed head —
the original 5-6-block run had none of the L5 unwind / mesh / extend-HighQC
fixes, so a clean start should now produce blocks continuously; then kill/restart
nodes to exercise revertSpeculativeOnStartup against a REAL crash state (own
consistent DB + converged network), which is the production scenario. Polluted
fixture archived at E:\qs-node0-polluted-archive + qs-logs-archive-20260709.

### DESIGN NOTES — production hardening (2026-07-09, per operator direction)

Referencing HotStuff-2 (Malkhi&Nayak) fine print and production HotStuff-family
chains (Aptos/Jolteon+SyncInfo, Flow pacemaker, Monad MonadBFT tail-fork):

1. **Time-jump boundary (replay head → live)**: prepareWork uses now (not
   parent.Time+period) when parent time is historical, so the first live block
   after a replayed head jumps months — VERIFIED SAFE: VerifyHeader only
   rejects time<=parent, no upper bound. TODO(prod): add `header.Time <=
   now+maxDrift` (leader can currently claim any future time) — Aptos bounds
   timestamps by round; Ethereum uses 15s drift.
2. **Continuous vs breakpoint production**: implemented per paper — startup
   reverts speculative (uncommitted) blocks to last committed
   (revertSpeculativeOnStartup), consensus converges via proposals carrying
   JustifyQC. Aptos ships the same shape as `SyncInfo` piggybacked on every
   proposal/vote: highest_qc + highest_tc, and any node behind adopts it
   immediately. **DONE (2026-07-09)**: SyncInfo-style TC piggyback landed.
   - Wire: optional trailing `HighTC` ([]byte encoded TC, 0-len = absent) on
     `HotStuffVote`/`HotStuffCommitVote`/`HotStuffTimeoutMsg` (hand-written SSZ,
     tolerant unmarshal for version skew). Engine `Vote`/`CommitVote`/
     `TimeoutMessage` gain `HighTC *TimeoutCertificate`; codec via
     encode/decodeOptionalTC. `TimeoutCertificate.Clone()` added.
   - RoundState tracks `highestTC` (`HighestTC()`/`UpdateHighestTC()`, monotone
     by view, stores a clone). Populated in tryFormTCAndAdvance (leader) and
     processNewView (followers learn the TC), then attached to every outgoing
     vote/commit-vote/timeout (all 3 timeout emit sites).
   - Receive: `processEmbeddedTC` runs at the TOP of processMessage, BEFORE
     view-gated buffering — extracts HighTC from vote/commit-vote/timeout
     (NewView keeps its own path), verifies VerifyTC + verifyEmbeddedQC, then
     advances to tc.View+1 and locks tc.HighQC. A lagging validator now catches
     up from ANY vote/timeout even if it missed the NewView. Safety unaffected:
     advancing views never violates the voting rule.
   - Tests: internal/consensus/hotstuff/syncinfo_tc_test.go (codec round-trip
     incl. nil, highestTC monotonicity+clone isolation, view-jump, no-jump-
     backwards) — all green. Pre-existing 10 chaos/byzantine WIP reds unchanged.
   - NOT yet run on the 7-node fixture; next: reseed E:\qs-node0..6 and confirm
     the wedge at 13013145/146 clears (nodes were stuck timing out with a TC
     formed on node6 that never propagated).
3. **Disconnect tolerance without losing safety**: safety is vote-signature
   based (quorum 5/7) and never depends on liveness heuristics; disconnects
   only hurt liveness. Done: static-peer keepalive + trusted exemption from
   scoring/rate limits. TODO(prod): call swarm ClearBackoff (or
   host.Network().Peerstore addr refresh) before redial — observed dial
   backoff keeping a restarted node out for minutes; Aptos/Flow use persistent
   validator connections with immediate reconnect and no backoff among
   validators.
4. **Validator set changes (node addition)**: chainspec committeePool exists;
   production needs epoch-boundary reconfiguration (HotStuff-2 §reconfig =
   commit a config block, activate at epoch boundary; Aptos does 2-chain
   commit of an epoch-change transaction, then restarts consensus with the new
   set). Session-key/mobile-committee handover via
   consensus_registerCommitteeValidator follows the same shape.
