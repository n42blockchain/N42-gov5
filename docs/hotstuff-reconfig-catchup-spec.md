# HotStuff dynamic reconfiguration — catch-up across epoch boundaries (spec)

Status: **open** — the last blocker for a full end-to-end validator scale test
(7→11→4→7). The reconfiguration feature itself (observer bootstrap, activation,
removal/rejoin, cross-boundary QC verification, PoS-style difficulty) landed in
commit `284b71f7`; this document specifies the remaining core fix.

## 1. Goal

Let a node that has fallen behind and is syncing by **block-range catch-up** cross
one or more validator-set changes and end up correctly caught up and (if it was
being added) participating. Concretely: make the full `7→8→9→10→11 → …→4 → …→7`
dynamic-membership sequence converge with all nodes agreeing on every block, even
when observers/new validators briefly lag into catch-up during a transition.

## 2. What works today

- **Live consensus-message path**: a node kept current by consensus messages
  applies reconfigurations correctly. `7→8` was validated end-to-end: all 11 nodes
  (7 genesis validators + 4 observers) stayed at the same height and hash, the new
  validator activated at the epoch boundary, and there was no fork.
- **Cross-boundary QC verification**: `resolveQCValidatorSet` (view-set first, then
  size-match the signer bitmap against known sets) is wired into every engine
  verification site, `VerifyHeader`, and the header-embedded CommitQC path.
- **Per-block catch-up insert**: `insertCatchUpBlocks` now inserts one block at a
  time and calls `NotifyBlockImported` after each.

## 3. The failure

Reproduces reliably at the **`8→9`** step (adding node8) once observers 8/9/10 lag
far enough to leave the live message path and enter block-range catch-up:

```
sync hotstuff catch-up: insert failed authorized=true count=25
  err=QC verification failed: invalid quorum certificate for view 41:
      signers bitmap length mismatch: got 9, expected 8   from=38 imported=2
```

The node is stuck at the boundary block: it holds the OLD (8-member) set, but the
post-boundary blocks carry QCs signed by the NEW (9-member) set, and it cannot
verify them.

## 4. Root cause

A node applies a staged reconfiguration only inside `advanceToView`, at the epoch
boundary. In live operation the view is advanced by **consensus messages**
(`tryQCViewJump` / `processEmbeddedTC` → `advanceToView`). But:

- `onBlockImported` (reached via `NotifyBlockImported` → `ProcessEvent(EventBlockImported)`)
  does **not** advance the view — it only casts a deferred import-gated vote.
- So a node catching up purely by importing blocks **never advances its view/epoch**,
  **never applies the staged reconfiguration**, and therefore never obtains the new
  validator set needed to verify the post-boundary blocks. Chicken-and-egg: it must
  apply the transition to verify the block, but it applies transitions by advancing
  the view, which nothing drives during catch-up.

`7→8` works because the observers stayed on the message path; `8→9` fails because,
by then, they lag into catch-up (aggravated by host load — the live 7-node fleet and
the 11-node test fleet share one machine).

## 5. Requirements

R1. During block-range catch-up, the engine's view/epoch MUST advance as blocks are
    imported, applying each staged reconfiguration at the boundaries it crosses,
    **before** the next block's header/QC is verified.

R2. Epoch numbering MUST track the view-derived epoch (`EpochForView`) so
    `historicalSets` stay keyed consistently — even across boundaries with **no**
    membership change. (Today `AdvanceEpoch` only advances the counter when a set is
    staged; the workaround is `prevSet`/`setSinceView` + bitmap size-matching.)

R3. A view jump that crosses **more than one** boundary (e.g. a TC/QC jump after a
    long stall) MUST apply every crossed boundary, not just the target view's.

R4. A node that joined via sync/catch-up MUST re-derive its own validator index from
    the current set on every advance, or it keeps a stale observer index and fails
    BLS verification on messages it emits.

R5. No regressions to the live path: a caught-up or producing node must be
    unaffected (advancing to a non-future view is a no-op).

R6. Trust: applying a staged reconfiguration is driven by the imported block's
    claimed view. The change applied is the node's OWN operator-supplied pending
    reconfiguration (not block content), and the block is still fully verified, but
    the design note on "advance based on an unverified view" must be reviewed for the
    production trust model (catch-up peers are authorized; reconfig is operator-coordinated).

## 6. Reference implementation (n42-26 Rust)

The Go engine was ported from `D:\n42\n42-26` (`crates/n42-consensus`). Its
`advance_to_view` already satisfies R1–R4 and is the model to port:

- `src/protocol/state_machine.rs::advance_to_view` — **loops** over every crossed
  boundary; at each, `advance_epoch()` if a set is staged else `carry_epoch_forward()`
  (advance the epoch WITHOUT changing the set). Updates `my_index` from `peek_next_set`,
  then calls `sync_local_validator_index()` at the end (R4, explicitly for sync/snapshot
  joiners).
- `src/protocol/state_machine.rs::resolve_qc_validator_set` + `validator/epoch.rs::
  find_validator_set_by_len` — size-matching fallback (already ported to Go).
- `validator/epoch.rs::validator_set_for_view` — epoch-keyed lookup; correct because
  `current_epoch` tracks the view-epoch via `carry_epoch_forward`.

Still to check in the Rust source: **how the sync/import path drives `advance_to_view`**
(does block import advance the view to the block's view, and in what order relative to
header verification?). That ordering is the crux of R1.

## 7. Proposed Go changes

1. `EpochManager.carryEpochForward()` — advance `currentEpoch` by one at a boundary
   with no staged set (record `historicalSets[old]=currentSet`, keep the set). This
   makes `currentEpoch == EpochForView` always and can retire the `prevSet`/`setSinceView`
   workaround (keep size-matching as belt-and-suspenders).
2. Rework `advanceToView` into the multi-boundary loop (R3), calling `advance` or
   `carryEpochForward` per boundary, and re-derive `myIndex` at the end (R4).
3. Drive advancement during catch-up (R1). Candidate: in the catch-up path, advance
   the engine to each block's view (from `ExtractViewFromExtra`) **before** inserting
   it, so `VerifyHeader` sees the applied set. This needs a clean seam between the
   sync package and the engine (an interface method that takes the header extra), and
   careful ordering vs. `VerifyHeader`, execution, and `NotifyBlockImported`.

## 8. Known trap (do NOT repeat)

A first attempt drove advancement from `NotifyBlockImported` (after insert) via
`ExtractViewFromExtra` + a public `AdvanceToView`. It ran AFTER verification (so it
could not help the failing block) and it introduced regressions: **QMDB
tree/marker discontinuity** on some observers and an observer casting votes it should
not. It was reverted. Advancement must happen BEFORE the block's verification, be a
strict no-op for non-lagging nodes, and must not disturb the QMDB execution/undo
alignment. Prototype on the isolated `qs_epoch_test` chain, never the live fleet.

## 9. Test plan

- Chain: `--chain qs_epoch_test` (chainId 95, epochLength 20), fresh datadirs,
  isolated ports; 7 genesis validators + 4 observers. Scripts under the session
  scratchpad: `deploy-testfleet.ps1`, `reconfig-full.ps1` (drives the full sequence
  with retry-until-all-accept + convergence gate), `do-transition.ps1` (single step).
- Broadcast every `admin_proposeAdd/RemoveValidator` to ALL nodes' authrpc ports
  (8651–8661), not just the active set — observers must track the set to verify its
  QCs.
- Success = the full `7→11→4→7` sequence converges: after every step all 11 nodes
  report the same height and the same block hash, and the chain never stalls. The
  specific gate is the `8→9` step under induced lag.
- Also run with the live fleet stopped (or on a separate host) to remove host-load as
  a confound, and with load (txgen on) to confirm robustness.

## 10. Out of scope

Changing the live chain's `epochLength` (a production cadence decision — the wiring
supports any value) and any live-fleet redeploy. This spec is about the code path;
deployment is separate.
