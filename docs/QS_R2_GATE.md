# The R2 commit-vote gate: what it waits for, and whether the wait can start earlier

Follow-up to `docs/QS_PACING_125.md` (175faf07), which found the two-phase
commit-vote gate in `processPrepareQC` (`internal/consensus/hotstuff/proposal.go:290-296`)
absorbing the time the 125ms pacing cut freed, ballooning to 200-377ms per
view on a follower. This note traces exactly what the gate waits for, why it
exists, and whether the vote can legally fire earlier than it does today.

## 1. What sets `importedBlocks[hash]`, and when

`importedBlocks[h] = true` is set in `onBlockImported`
(`internal/consensus/hotstuff/proposal.go:681`, via `rememberImported`),
called from `ConsensusEngine.ProcessEvent(EventBlockImported)`
(`internal/consensus/hotstuff/engine.go:698`). Two independent callers feed
that event, deduped by a shared `notifiedImports` set (`service.go:1722`,
`service.go:1885` — whichever hash arrives first wins, the second is a no-op):

- `Service.NotifyBlockImported` (`service.go:1722`) — the **late** path. Sync
  layer, fires after body import AND after a `BlockApplied` DB probe
  (`service.go:1787-1800`) confirms the block is actually applied (added in
  the S26/35zzzg hardening to stop voting on bodies that never executed).
- `Service.NotifyBlockExecuted` (`service.go:1885`) — the **early** path,
  wired via `BlockChain.SetExecutedHook(svc.NotifyBlockExecuted)`
  (`internal/node/node.go:1898`). The hook fires in `internal/blockchain.go:2524-2533`,
  **immediately after `bc.process.Process()` (EVM execution) and
  `bc.validator.ValidateState()` (state-root check) both succeed, and
  BEFORE `writeBlockWithState` (the ~150-250ms MDBX persist)** — on its own
  goroutine so the vote round overlaps the write instead of waiting behind
  it. Comment at the call site: "A crash between the vote and the persist
  costs this node a re-import on restart and nothing else — HotStuff quorum
  safety never depended on a voter's local durability."

**This early path is already the one firing in production.** Sample from
B2 win1, follower node0, view 13379 (`/data/blockchain/wr-logs/r35zzzbc-keep/node0/n42.log`,
`18:20:50`): `cvGate=own-import`, and `blockExecutedUs` (1791066050936563)
equals `commitVoteCastUs` (1791066050936559) to the microsecond — the commit
vote is cast the instant execution+validation finish, not after persistence.

Per-view timeline, follower node0, full B2 win1 (~21s, n=55-58 views, medians):

| segment | median | source |
|---|---|---|
| EVM exec (`StateProcessor.Process`, procPhases.Exec) | 37.7 ms | `blockimport phases` `exec` |
| state root (`procPhases.Finalize`) | 24.8 ms | `blockimport phases` `root` |
| `proc` (exec+root+setup/collect overhead) | 68.3 ms | `blockimport phases` `proc` |
| MDBX write/persist | 24.3 ms | `blockimport phases` `write` |
| full insertChain (`dTotal`) | 103.0 ms | `blockimport phases` `total` |
| QC→commitQC (r2) | 10 ms | `hotstuff view timing` `r2=` |
| PrepareQC→commit-vote (`pqc2cv`) | 2 ms | `hotstuff view timing` `pqc2cv=` |

At median tx counts the gate is cheap and invisible — consistent with
`QS_PACING_125.md`'s B1 finding that at 250ms/view the gate "usually resolved
inside the pacing slack." The 200-377ms outliers in that doc's 4-view leader
snapshot come from heavy-tx-count views: e.g. a 163k-tx block in this same
window has `proc=483 ms` (`exec=274 ms`, `root=139 ms`), and a 58k-tx block
has `proc=216 ms` (`exec=117 ms`, `root=81 ms`) — both close to the 200/377ms
r2 stalls reported there. **r2 tracks `exec+root`, not the write** — the
write already happens off the critical path via the early-vote goroutine.

## 2. Why the gate exists, and what the commit vote attests under depth-1

`proposal.go:297-309` ("Import-gated voting (NOT optimistic)"): voting only
after local import means a CommitQC proves "a quorum actually holds the
block — not just its hash," which bounds how far views can run ahead of
real block propagation/execution. The S26 comment block just above
(`proposal.go:~277-296`, citing `docs/OPEN_ISSUES.md` "A quorum-committed
block that no node stored", round 35zzzg) records the concrete failure this
replaced: a one-phase early vote gated only on "extendsJustify if already
imported" was skipped on the common path (a Proposal always arrives before
import completes), so a vote could certify a block whose extends-rule was
never actually checked against the real parent — the 35zzzg conflicting
commit (a non-extending sibling slipping into a CommitQC).

So the property protected is **both** the deferred per-transaction check
(validity) **and** that the voter actually executed the block — the commit
vote is explicitly called "the execution attestation" in the two-phase gate's
own comment (`proposal.go:286-289`: "hold it until the block is imported
locally, so a CommitQC still proves 2f+1 validators EXECUTED the block").

Under depth-1 deferred execution, the sampled view (13379) used
`cvGate=own-import` — the commit vote on N fired on N's *own* exec/validate
completing, not merely on N-1's. `deferredAttested` (`proposal.go:402-408`)
exists as a second, cheaper path (`checkedBlocks[N]` + `importedBlocks[parent]`,
no wait for N's own execution) and does fire in some pipelined cases
(`cvGate=parent-import` in `onBlockImported`'s branch comment), but this
window's sample shows own-import as the active path, so N's commit vote today
asserts N is valid **and** N was executed by this node — not merely that it
extends the lock.

## 3. Earliest safe point for the commit vote

**(a) Vote after exec+root, before persist.** **Already shipped** —
`NotifyBlockExecuted`/`SetExecutedHook` (§1) is exactly this, and it is the
path firing in the sampled window. No further "pending" saving here; the
~24ms median write (up to ~150-250ms on heavy blocks) is already off the
critical path. Safety: the code's own argument holds — HotStuff quorum
safety needs 2f+1 signed attestations, not any one voter's local durability;
a crash between vote and persist just forces a re-import, and `JournalVote`
(prepare/commit vote journaling, referenced via `journalPrepareVote`/`jcvMs`
in the view-timing log) already exists to make the vote's own durability
independent of the block persist. **Consensus-safe, no new assumptions,
because it is current behavior.**

**(b) Vote commit on N before N's own execution, relying only on N-1's
execution + the deferred per-tx/header check.** This is `deferredAttested`'s
`parent-import` path, already coded but (per the sample) not the one
binding today. Making it the *primary* path — i.e., always casting the
commit vote once `checkedBlocks[N]` + `importedBlocks[parent]` hold, without
waiting for N's own exec+root — would reintroduce exactly the class of gap
S26 closed: a commit vote (hence a CommitQC) on N that does not itself
certify N's own execution, only a structural/header check plus the parent's
execution. Against the two named hazards: it does not add crash-recovery
risk beyond (a) (same journal covers it), but it *does* weaken the
35zzzg-class guarantee, because CheckDeferredBlock ("static validation") is
explicitly called out at `proposal.go:~286` as *not* equivalent to the
execution guarantee ("Round 2's job either way"). **Needs a design review**
before adoption as default — it changes what a CommitQC proves, not just
when the vote is journaled.

**(c) Keep the gate, shrink the import it waits on.** Consistent with
§1's finding that r2 tracks `exec+root`, the lever is whatever shortens
`StateProcessor.Process`/state-root compute on heavy blocks (parallel EVM
tuning, S74's `qmdb-evict-lag-blocks`, or further root-computation work) —
no vote-timing change at all. **Already consensus-safe by construction** (no
change to the gate), and is what `QS_PACING_125.md`'s own "next lever"
paragraph pointed at, correctly, modulo the write-phase correction above (it
guessed `exec+write+root`; measurement here shows write is not on the
gated critical path).

Estimated per-view saving: (a) ~0 ms further (already captured, worth noting
so no one re-proposes it as new work); (b) could save the full `exec+root`
median (~65-500ms on heavy views) but is a safety-relevant redesign, not an
env flag; (c) saves whatever fraction of `exec+root`'s 38+25ms (median) or
274+139ms (worst observed) a given optimization removes — directly on the
critical path, 1:1.

## 4. S78 proposal

Not a new R2-relaxation knob — (a) is already in and (b) needs a design
review this round shouldn't rush. S78 is a **measurement** round: correlate
`two-phase vote: holding` duration (or `r2=`) per view against that same
view's `blockimport phases` `exec`+`root` (not `write`) across one full A/B
window, to confirm the outlier stalls are ~1:1 with `exec+root` on
heavy-tx views (falsifier: if r2 exceeds `exec+root` by a wide, consistent
margin, something other than execution — e.g. goroutine/lock queueing before
the hook fires — is also contributing, and that would be new work).
Env-gated: none needed, this is log correlation over an existing run,
default behavior unchanged either way.

Stability counters to watch if this later turns into a (c)-style change:
"refusing block production on unexecuted committed parent", "sealed block is
stale", "conflicting commits" (must stay 0), hotstuff "timeout" count —
same signals `QS_PACING_125.md` rule 133 already tracks.

Prediction: r2 (or the `two-phase vote: holding` → cast gap) tracks
`exec+root` within ~20ms on heavy views; confirms the existing doc's
"next lever" framing once corrected to exclude `write`.

**Level: Sonnet** — log correlation and arithmetic over already-captured
fields, no new instrumentation or code change, well under an hour.
