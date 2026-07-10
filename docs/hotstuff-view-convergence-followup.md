# HotStuff-2 View Convergence — Residual Stall Follow-up

**Status:** Direction A LANDED (`da843ca4`) — maxStall 240s → 20s live-fire.
Hardening batch LANDED (`c78f5ab6`): canonical header-walk + rewrote-cache
invalidation, startup canonical-linkage repair, fix-A gated to committedHead+1,
vote-time extends-rule enforcement. Crash-recovery (hard-kill of all 7 →
restart) validated: nodes restore to the same persisted view and resume
committing immediately.

**OPEN (next session):** by-number reads (RPC `eth_getBlockByNumber`, and thus
the catch-up range server) return a DIFFERENT chain at old heights (241–243)
than the on-disk HeaderCanonical table — and the discrepancy SURVIVES a process
restart, so it is not the in-process layered cache. A read-only probe of the
live MDBX shows canonical[13014242]=1f66ad (linked) while RPC returns 8558ac.
Suspect a long-lived read transaction / overlay in the chain-DB stack; note the
working tree carries in-flight WIP in `lib/kv/mdbx/kv_mdbx_opts.go` and
`modules/state/warm_overlay_reader.go`. This is what actually fed the wedged
laggard a non-linked chain. Direction B (view fast-forward) remains optional.
**Scope:** `mainnet_qmdb_staggered` 7-node live network (chainId 94, hotstuff, qmdb).
**Prereq context:** `docs/mainnet_qmdb_staggered-7node-status.md` (L5/L6 history).

This document isolates the **only** remaining liveness defect after the
state-consistency (`d0749a56`), view-timeout tuning (`850e3d04`), dependency
upgrade (`af8ed9a9`), and loopback-mesh (`E:/deploy-7node.ps1`) fixes. The
network is otherwise healthy: it clears the ≥500-block acceptance gate with a
unique block hash per height across all 7 nodes, 7/7 alive, and self-heals from
every stall. What remains is a **tail-latency** defect, not a wedge and not a
correctness bug.

---

## 1. Symptom

On an otherwise healthy mesh (0 peer disconnects, 7/7 alive, `uniq=1` per
height), block production occasionally freezes for **2–4 minutes**, then resumes
on its own. Measured cadence: roughly one such stall per ~15 minutes; between
stalls the chain runs at ~3–5 s/block.

Representative measurement (deploy-run30, 14 min window):

```
+101 blocks, uniqAlways=YES, minAlive=7, node0 disconnects=0
maxStall=240s, totalTimeouts(7 nodes)=49
```

The view-timeout events cluster (they are NOT evenly spread). Example timed-out
view numbers on one node over a run: `381 413 445 447 477 509 541 543 574 606
608 639 641 667 669 693 718 720 746 748` — clusters recur roughly every ~32
committed blocks, and each cluster is a burst of consecutive timed-out views.

---

## 2. What it is NOT (ruled out)

- **NOT peer churn.** `node0 disconnects = 0` across the entire stall. The
  earlier ~2-min periodic disconnect was a same-host multi-address artifact
  (nodes auto-advertised the machine's public IP → duplicate connections →
  libp2p dedup churn), fixed by advertising loopback only
  (`--p2p.local-ip 127.0.0.1 --p2p.host-ip 127.0.0.1`). That fix holds: 0
  disconnects during stalls.
- **NOT the per-view timeout duration.** `baseTimeout=6000, maxTimeout=30000`
  are in effect (verified in the running binary). The pacemaker caps any single
  view at `maxTimeout=30s`
  (`internal/consensus/hotstuff/pacemaker.go` `TimeoutDuration` =
  `min(effectiveBase·2^n, maxTimeout)`). Yet observed *view-to-view*
  advancement gaps are ~2 min — an order of magnitude above the 30 s cap. So the
  stall is NOT a node waiting out its own timer.
- **NOT `persistState` blocking the consensus loop.** Hypothesis tested:
  `persistState()` runs an MDBX write txn inline on the serial output loop
  (`service.go` `handleOutput` → `OutputBlockCommitted`), so a slow fsync could
  delay the next view. Fix attempted: move persistence to an async single-flight
  worker off the output loop. **Result: no improvement** — the 240 s stall
  persisted. The change was reverted (working tree clean). MDBX write-lock
  contention is not the bottleneck here; the bottleneck is consensus-level view
  agreement.

---

## 3. Root cause

When a height fails to commit on the **first** view that proposes it, two
mutually-reinforcing effects stall progress:

### 3a. Same-height candidate storm (across views / leaders)

Each view has a different leader. A leader proposes by extending its
LockedQC/HighQC block via `TriggerBlockProduction(lockedQC.BlockHash)`
(`service.go` `OutputViewChanged`). If height *H* did not commit, the next
view's leader re-proposes *H* — but it rebuilds a **byte-different** block
(different ConsensusEvidence / ParentBeaconRoot per view → different hash). Over
a few failing views this produces several distinct candidates at the same
height.

Observed at height 13013938 (5 candidates before one committed):

```
431c5b…  c7920f…  62f463…  79a94a…  3f22ca…(committed at view 859)
```

The `worker.go` single-candidate seal guard (`sealedOnParent`, fix ①) stops a
**single node** from sealing two siblings on one parent, and `forkchoice.go`
gives a deterministic lower-hash tie-break (fix ②) for the *canonical* choice —
but neither prevents **different leaders in different views** from each
proposing a different *H*. Import-gated voters then scatter their votes across
whichever candidate they imported for the current proposal, so no single
candidate reaches the 2f+1 = 5/7 vote quorum.

### 3b. View spread → TC-formation bootstrap gap

A view advances only when a **Timeout Certificate (TC)** forms, and
`timeoutCollector.BuildTC` requires `QuorumSize` = **2f+1 = 5/7 timeout messages
for the *same* view** (`timeout.go` `tryFormTCAndAdvance`). Once the candidate
storm desynchronizes the nodes, they sit in **different views** (e.g. some at
856, some 857, some 858). No single view accumulates 5/7 timeouts → **no TC
forms** → nobody advances → the round only breaks when the reactive
`handleFutureViewTimeout` path (a node adopts a peer's *higher* timeout view,
`timeout.go`) happens to pull ≥5 nodes into the same view by chance. That
convergence is a random walk and empirically takes ~2 min per step.

The existing **SyncInfo TC piggyback** (`processEmbeddedTC`) does NOT close this
gap: it only fast-forwards a lagging node to `tc.View+1` when a TC **already
exists** on the wire. During the bootstrap stall **no TC has formed yet**, so
there is nothing to piggyback. SyncInfo accelerates *post-formation* catch-up,
not *first* TC formation under view spread.

**Summary:** first-try commit miss → per-view divergent candidates (3a) →
vote scatter + view spread → no view reaches the 5/7 timeout quorum needed to
form a TC (3b) → multi-minute random-walk re-convergence.

This is the item the status doc tracks as *"FINAL REMAINING — post-restart view
synchronization (pacemaker level)"*; it also fires mid-run (not only on
restart) whenever a height misses its first commit.

---

## 4. Relevant code

| Concern | Location |
|---|---|
| Leader proposes on LockedQC each view | `internal/consensus/hotstuff/service.go` `handleOutput` `OutputViewChanged` |
| Per-node single-candidate seal guard (fix ①) | `internal/miner/worker.go` `resultLoop` / `sealedOnParent` |
| Deterministic canonical tie-break (fix ②) | `internal/forkchoice.go` `ReorgNeeded` |
| TC quorum (2f+1) | `internal/consensus/hotstuff/timeout.go` `tryFormTCAndAdvance` → `timeoutCollector.BuildTC` |
| Reactive view sync (adopt peer's higher timeout view) | `internal/consensus/hotstuff/timeout.go` `handleFutureViewTimeout` |
| SyncInfo TC piggyback (post-formation catch-up) | `internal/consensus/hotstuff` `processEmbeddedTC`; `FutureViewWindow = 50` (`engine.go`) |
| Pacemaker backoff / adaptive base | `internal/consensus/hotstuff/pacemaker.go` |

---

## 5. Proposed directions (ranked)

The two levers are: (A) **shrink the candidate storm** so a single candidate
gathers votes; (B) **converge views faster** so a TC forms without a random
walk. They are complementary — do A first (smaller blast radius), then B.

### A. Leader re-proposes the deterministic existing candidate (LANDED — `da843ca4`)

When a leader is about to build at height *H* and one or more candidates at *H*
extending the same LockedQC parent are already imported locally, it should
**re-propose the deterministically-preferred existing candidate (lowest block
hash) instead of building a fresh divergent one**. This collapses 3a: every
node's leader, across views, converges on the *same* *H* block, so import-gated
votes stack on one candidate and reach 5/7 in a single view — no storm, no
spread.

- Reuse fix ②'s ordering (lowest hash wins) for consistency between the
  canonical choice and the proposal choice.
- Mechanism already half-present: `worker.go` `resultLoop` logs
  "re-proposing already-imported block" for the *same* hash; extend it to select
  the lowest-hash sibling at the height when several exist.
- Risk: must not re-propose a candidate that violates the safety rule (must
  still extend LockedQC). Only siblings extending the current LockedQC parent
  are eligible.

### B. Aggregate-highest-view timeout sync (Bracha-style fast-forward)

Make view convergence deterministic instead of a random walk:

- On receiving **f+1 distinct timeout messages for views > current**, jump to
  the **(f+1)-th highest** such view immediately, carrying collected timeouts
  into the new view's collector — so the target view reaches 5/7 in one hop.
- When a node advances to a new view via `handleFutureViewTimeout`, it should
  **re-emit its own timeout for the adopted view** so the target view actually
  accumulates the quorum (rather than each node contributing to a different
  view).
- This is the standard Jolteon/Aptos `SyncInfo`-on-every-message shape, but for
  the **TC-not-yet-formed** case (the current SyncInfo only helps once a TC
  exists — see 3b).

### C. Backoff / timing tweaks (cheap, secondary)

- Ensure the pacemaker backoff **resets** to `baseTimeout` on a successful
  commit (verify it does not stay elevated across a stall via the adaptive
  `2×p95` term feeding back on itself).
- Consider a shorter `maxTimeout` (e.g. 15 s) so a spread collapses faster; only
  meaningful in combination with A/B, since the stall is TC formation, not the
  per-view timer.

---

## 6. Validation plan

1. Reseed 7 nodes from the pristine replay DB (`E:/n42-qmdb-staggered-7node`) and
   run `E:/deploy-7node.ps1` (loopback-only mesh) on the candidate build.
2. Metric to move: **maxStall** over a ≥20-min soak should drop from ~120–240 s
   toward the single-digit-second range, with **total view-timeouts across 7
   nodes** falling substantially. Invariants that must hold: `uniq=1` per height,
   7/7 alive, 0 forks.
3. Regression guard: the state-consistency invariants (unique hash per height,
   no branch-switch livelock) must not regress — re-confirm branch-switch count
   stays low (single digits, not the historical 1219).
4. Then exercise the remaining acceptance item — **kill/restart self-heal**
   (crash-recovery via `revertSpeculativeOnStartup`) — which shares the same
   view-convergence machinery and should benefit from B.

---

## 7. Notes

- This defect does not affect **safety**: only a 2-chain-committed block is
  final, votes are BLS-signature quorums, and no stall ever produced a fork
  (`uniq=1` throughout every observed stall). It is purely **liveness /
  tail-latency**.
- In a real multi-machine deployment the loopback fix is unnecessary (each node
  has a distinct address); the view-convergence item here is
  deployment-independent and remains the true residual.
