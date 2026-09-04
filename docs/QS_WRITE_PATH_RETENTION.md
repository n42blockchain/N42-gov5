# Four retention mechanisms, and what they cost a throughput benchmark

Written 2026-09-04 after a benchmark round asked "does the native chain need
changesets and history at all?" — a good question with a partly uncomfortable
answer. This document is for anyone about to delete one of these to make a
number go up, and for the sibling clients (N42-26, n42-rs) where the same
question has different answers.

Scope: the **n42 native chain** (`--chain private` / `mainnet_qmdb_staggered`,
hotstuff + QMDB). The eth-el path commits with the Ethereum MPT and its
answers differ; where that matters it is called out.

---

## 1. The four mechanisms are not interchangeable

They are often discussed together as "history writes". They are not one thing.
Each rolls back or serves a DIFFERENT store, and deleting one does not cover
for another.

| mechanism | table(s) | what it protects / serves | who reads it |
|---|---|---|---|
| **QMDB undo** | `QMDBUndoWindow` | the **QMDB tree** (the commitment) | `revertUncommittedQMDBAppends`, `ReadQMDBUndos` (recent-height proofs) |
| **changesets** | `AccountChangeSet`, `StorageChangeSet` | **PlainState** (the flat account/storage tables) | `realignAppliedToTree`, `healPlainStateAheadOfMarkerOnStartup` |
| **history index** | `AccountHistory`, `StorageHistory` | historical **reads** | `state.NewPlainState(tx, blockNr+1)` → `eth_getBalance` at a height, `debug_trace*` |
| **txlookup tail** | `TxLookup` | `eth_getTransactionByHash` | the RPC lookup path |

**The single most important line in this document:** QMDB's undo covers the
tree and NOT PlainState. `realignAppliedToTree` unwinds PlainState from the
changeset PRE-VALUES. There is no other source for them. So "QMDB has its own
undo, therefore changesets are redundant" is false, and it is the natural wrong
conclusion to reach from the code's shape.

## 2. What the native chain would actually break

**Delete changesets →** two failures, one of them observed live.

- `realignAppliedToTree` can no longer roll PlainState back to the height the
  QMDB tree holds. The three-state invariant (tree / PlainState / applied
  marker) has no repair path.
- `healPlainStateAheadOfMarkerOnStartup` loses its detector. Its comment
  records the failure it was written for: a node whose PlainState ran ahead of
  the marker built blocks carrying stale nonces and the whole network rejected
  them ("the half-healed shape observed live on node4"). The changeset head is
  what proves PlainState is ahead, because writeBlockWithState persists
  PlainState, its changesets and the marker in one transaction.

**Delete the history index →** historical RPC silently degrades.
`internal/api/api.go` builds its reader as `state.NewPlainState(tx, blockNr+1)`,
which walks `AccountHistory`/`StorageHistory` to find the value as of a height.
`debug_trace*` does the same. Without the index those queries do not error —
they answer from whatever PlainState currently holds, which is the wrong answer
delivered confidently. That is worse than a slow chain.

**Delete QMDB undo →** a failed or re-imported block cannot have its appends
peeled off the live tree, and recent-height proofs lose their replay source.

## 3. What a throughput benchmark actually needs

Of the four, a saturation round issues **none** of the reads:

- it never calls `eth_getBalance` at a past height, so the history index has no
  consumer for the duration;
- it never reorgs on the happy path, so changesets are insurance rather than a
  running cost — but insurance against a failure this fleet HAS hit;
- it does call `eth_getTransactionByHash` never, which is why
  `N42_TXINDEX_TAIL=1` was worth **2.5x** (the largest single win on this rig):
  without it every transaction writes a random-key `TxLookup` row, scattering
  thousands of dirty pages and making `mdbx_txn_commit` the dominant cost.

That precedent is the reason this question is worth asking at all, and also the
reason to be careful: the txlookup tail was safe to tier because nothing in the
round read it. The history index is the same shape. **The changesets are not**,
because their consumer is a recovery path, not a query.

## 4. So the answer is a mode, not a deletion

The machinery for this already exists and was found rather than built:

    state.NewPlainStateWriterNoHistory(db)   // modules/state/plain_state_writer.go:54

With no changeset writer, `WriteChangeSets`, `WriteHistory` AND the per-update
`csw.UpdateAccountData` work inside execution all become no-ops. Note the third
one: the cost is not only the two calls at the end of the write path, it is
spread across every account and storage update in the `state` phase, so the
`chgset` phase understates it.

### Measured (2026-09-04, 76 full blocks, one leg, 22,857 tx a block)

    Truncate            0.0 ms    0.0% of chgset
    WriteChangeSets     8.7 ms    6.9%
    WriteHistory      117.6 ms   92.7%
    chgset total      126.8 ms
    write path total  579.9 ms

**The load-bearing half costs 8.7 ms and the tierable half costs 117.6 ms — a
factor of thirteen, in favour of the one that can go.** WriteHistory alone is
20.3% of the write path.

Three things follow:

- `Truncate`'s "no-op on the strictly-forward happy path" comment is accurate;
  it really is free. That hypothesis died for the price of one timer.
- Everything section 6 protects — native rewind, eth-el rewind, the DATC
  archive input — depends on **changesets**, and changesets cost 8.7 ms. None
  of the three depends on the history index. So the expensive half is the one
  with a single consumer (historical RPC on this chain) and the cheap half is
  the one with three.
- The split is clean to implement: `ChangeSetWriter.WriteHistory` reads the
  in-memory changes already accumulated for the changeset write and only writes
  the inverted-index bitmaps, so skipping it does not affect `WriteChangeSets`
  at all.

Absolute numbers are leg-dependent — this leg's write path was 579.9 ms where
earlier rounds measured ~870 ms — so the 20.3% share travels and the 117.6 ms
does not.

### Why the existing aggregator does not solve it

`HistoryAggregator` (modules/state/history_aggregator.go) was written for
exactly this cost: per-block `writeIndex` pays a full read-decode-add-encode-
write roaring round-trip PER CHANGED KEY PER BLOCK, and a full-chain conversion
attributed ~330 GB of allocations to that path. It is wired into
`internal/replay/engine_v2.go` and **nowhere else**.

It does not transfer to live import for two reasons, both worth knowing before
someone tries:

- Its correctness note scopes itself: "nothing reads the history tables
  MID-REPLAY". On a live node serving historical RPC that precondition does not
  hold — a query landing mid-batch would read an incomplete index.
- Its win is deduplicating a hot key touched across MANY blocks (the coinbase
  is in every block) into one round-trip. Within a single live block each of
  the ~24k keys is touched once, so there is nothing to deduplicate.

So the 117.6 ms is inherent to writing an inverted-index entry for ~24k keys
per block as read-modify-write. The available lever is not to batch it but to
not write it on a node that does not serve historical queries.

## 5. Cross-client notes — the same question has different answers

**N42-26.** Has none of it: no `WriteChangeSets`, no `WriteHistory`, no
`ChangeSetWriter`, no `AccountHistory` table, no `NewPlainState`, and `qmdb`
appears in one file against 92 here. **This is not evidence that removal is
safe.** N42-26 has no QMDB/PlainState split to keep consistent and no
historical RPC to serve; it does not write these because it does not have the
features that consume them. Reading it as "the older client was fine without
them" inverts cause and effect.

**n42-rs.** Keeps changesets but as **static-file segments** with real pruning
(`prune_account_changesets`, `AccountChangeSets` / `StorageChangesets` masks,
`--static-files.*-changesets` block-per-file args). That is the shape the Go
side should copy: not deletion, but a segment that can be pruned or never
written according to the node's role. Note also the mirror-image blindness
between the two clients' profilers — a Go heap profile cannot see MDBX's
malloc'd dirty pages, and a jemalloc profile cannot see reth's mmap'd static
files. A memory question on either client needs live heap, heap headroom and
non-heap separated before it is even a question.

### The prune floor, and the rule that sets it

Raised with n42-rs as a hazard and answered by them: **the pruner and the
unwinder read the same changesets.** Pruning below a block makes an unwind to
that block impossible, silently, and the failure surfaces months later on one
node during a reorg. reth ties its prune floor to what it may still need to
revert.

Their rule, which transfers to any prune mode built on the Go side and is
sharper on a HotStuff chain than on a longest-chain one: **never prune inside
the unfinalised window.** On this chain finality is a QC, not a depth, so a
reorg deeper than the committed window is out of scope by construction.

**That rule covers rewind and nothing else, and rewind is not the binding
constraint.** See the third consumer below, which sets a far longer horizon.

Concretely, for a future Go prune mode: the floor is the highest
QC-committed height, not a fixed depth, and it must be read from consensus
rather than assumed. `qmdbUndoWindow = 256` in blockchain_write.go is a
DIFFERENT window with a different justification and must not be reused as the
prune floor by analogy.

## 6. The third consumer: changesets are the DATC archive's only input

Contributed by the eth-el session, which verified both arguments above against
the code before adding this one — and it is the leg that actually sets the
retention horizon.

The v2 full-chain DATC archive is built FROM the changeset freezer tables:
`acctcs` (146.8 GB) and `storcs` (278.5 GB), per
`modules/rawdb/freezer/freezer.go:57-58`. Every leaf-history row and every
change-index row in that archive comes from a changeset, and the leaf/chg
layers are not reconstructible from the trie — that is why Pipeline A exists at
all, extending leaf history from the forward changesets alone.

So: **prune a range's changesets and no DATC archive can ever be built or
rebuilt for those heights.** Not "recovery is harder"; the proofs are not
derivable from anything else that survives. This is not a rewind dependency, so
it appears in neither argument above, and it has a different and much longer
horizon:

| leg | what changesets are for | pruning costs |
|---|---|---|
| native chain | PlainState's only rewind source (`realignAppliedToTree`) | recovery breaks |
| eth-el | rewind goes via HashedAccounts/HashedStorages instead | rewind unaffected |
| **DATC (both chains)** | **the archive's only input** | **historical proofs unbuildable, permanently** |

The retention question for the third leg is not "how far back can we rewind"
but "how far back might we ever need to rebuild an archive", which is a
different kind of argument and a much longer answer. The unfinalised-window
rule from section 5 does not bound it.

## 5b. Correction: two horizons, not one — and the undo one is 512 blocks

Sections 5 and 6 discussed "retention" as a single question and let the
longest answer set the tone. That was an exaggeration and it is corrected
here. **Undo and archive-input are different consumers with different
horizons, and only one of them is long.**

The undo horizon is a constant in this repo, not a judgement:

    qmdbUndoWindow        = 256   blockchain_write.go:461, pruned every block
    qmdbRealignMaxDepth   = 512   blockchain.go:2684, REFUSES beyond it
    PruneConfig.Mode      = "archive" by default -> changesets never pruned
    PruneConfig.BlockRetention = 90000 in "full" mode (~3 days at 3 s)

`realignAppliedToTree` does not merely prefer recent changesets; it returns
`refusing to realign %d blocks ... beyond the changeset window` past 512, and
its comment explains why: "a partial heal is worse than staying wedged". So
**changesets older than 512 blocks are already dead weight for undo — the code
will not use them.** They are retained without bound to serve a mechanism that
refuses to read them.

Consensus makes the bound looser still rather than tighter. Practical finality
elsewhere is shallow — Bitcoin's 6 confirmations, Ethereum's 12 — and here it
is not a depth at all: a block with a commit QC cannot be reverted. 256 and 512
are already generous slack over what HotStuff requires, and they were chosen
that way deliberately (the 512 comment says "mirrors (with slack)").

**What this licenses, stated without inflation.** An undo window of a few
hundred blocks is small enough to hold in memory, and a node that loses it on
restart re-syncs those blocks from peers, which is fast and safe precisely
because the canonical chain is immutable under QC finality — there is no
ambiguity for it to resolve, only blocks to refetch. That is a design
direction, not a measurement: nothing here has built or measured a
memory-resident undo, and the on-disk changeset write is 8.7 ms, so the
motivation is retention size, not write cost.

**What this does NOT license.** The archive-input horizon in section 6 is
untouched by any of the above. DATC needs changesets over the whole chain as
build INPUT, which has nothing to do with how far a node can unwind. Erigon's
unbounded changeset retention is a staged-sync inheritance; this chain's
archive dependency is a real and separate requirement, and the two must not be
argued as one — which is what this document did before this section.

### The archive horizon, concretely — and one half of it is live right now

"How far back might we ever need to rebuild" is easy to write and hard to act
on, so the eth-el session supplied the numbers. The precondition has TWO parts,
and they bind differently.

**While an archive build is in flight, nothing in its range may be pruned at
all — and the range is [0, tip], not a recent window.** This is not
hypothetical: the v2 archive covers [0, 25,864,982] and is being built in two
halves as this is written. The Windows half is at roughly block 12,894,000 of
17,900,000, has been reading `acctcs`/`storcs` continuously for two days and
will keep reading them for days more. Pruning any changeset below 17.9M today
would not "degrade a future rebuild"; it would kill a running build with 28.7%
of its leaf work done.

**Once an archive exists and verifies, the changesets are still the only way to
rebuild it,** so the default horizon is forever.

That second one is a decision someone may legitimately make. The archive is
~800 GB for the lower half alone against 425 GB of changesets, so "keep the
archive, drop the inputs" is not obviously wrong. But it trades a rebuildable
system for an unrebuildable one, and a corrupted archive then cannot be
regenerated from anything. **That is a decision to take deliberately, not a
default for a flag to answer.**

### Where the three will collide

`n42-ancient prune --class aux --before-era N` (`cmd/n42-ancient/main.go:9`) is
the knob, and `docs/QS_WEEKLY_REPLAY_SYNC.md:115` already calls it "the
(future) knob for dropping old witnesses/changesets", noting only that the
chain class is not prunable. Nothing there records that the aux class is
load-bearing for anything beyond rewind. **Before that flag does anything, the
DATC horizon has to be written down next to it**, because an operator reading
the runbook today would reasonably conclude aux is spare.

**For any client:** if you are about to disable one of these to raise a
benchmark number, say in the commit message which reader you checked. The four
mechanisms above each took a grep to find and one of them (the PlainState
unwind) is the kind that fails months later, on a reorg, on one node.

## 5c. What actually bounds the window: consensus irreversibility, not depth

Section 5b took 256 and 512 as given because they are constants in the repo.
That describes the code; it does not justify the numbers. The right question is
which boundary the chain is actually subject to — longest-chain, heaviest-chain,
or consensus-irreversible — and for HotStuff-2 it is the third, which is a
much stronger guarantee than a depth heuristic.

**The commit is same-view.** `voting.go:376` forms the commit QC for the
CURRENT view and calls `roundState.Commit` immediately, then broadcasts Decide;
the phases `WaitingForProposal → Voting → PreCommit → Committed` all run inside
one view, and `AdvanceView` resets the phase only afterwards. There is no
multi-view commit lag as in three-chain HotStuff. So at any instant the
uncommitted suffix is **the current view's block, and nothing else**.

**Therefore the consensus revert depth is at most 1 block.** A block with a
commit QC cannot be reverted — that is the protocol's safety property, not a
probability like Bitcoin's 6 confirmations or Ethereum's 12. Those numbers are
answers to a different question (how deep before a longest-chain reorg becomes
improbable), and quoting them here, as this document did, imports a
probabilistic frame into a chain that does not have one.

**So what are 256 and 512 for?** Not consensus reverts. They serve a LOCAL
discontinuity: PlainState having run ahead of the applied marker, which
`healPlainStateAheadOfMarkerOnStartup` detects and `realignAppliedToTree`
repairs. That is a different failure with a different bound, and it is worth
being exact about it, because the exactness cuts both ways:

- PlainState, its changesets and the marker are written in ONE MDBX
  transaction. A crash therefore cannot leave them apart — MDBX commits
  atomically. The divergence the heal exists for came from an earlier PARTIAL
  REPAIR, not from a crash.
- A window cannot be sized against a bug's depth. So 256/512 are defensive
  slack against unknown repair failures, which is a legitimate thing to have,
  but it should be called that rather than presented as a derived requirement.

**Should the changeset window equal the QMDB undo window?** For the repair
purpose they serve the same event on two stores — the tree via undo records,
PlainState via changeset pre-values — so equal sizing is coherent, and
`qmdbRealignMaxDepth = 512` already mirrors `qmdbUndoWindow = 256` "with
slack". What is NOT coherent is the current state: undo is pruned at 256 while
changesets are retained without bound by default. That is a three-order-of-
magnitude asymmetry between two halves of one mechanism, and no argument in
this document justifies it. The archive-input requirement of section 6 does
justify keeping changesets — but as an ARCHIVE input, deliberately, not as a
side effect of the prune mode defaulting to "archive".

**Rationally, and without inflating it in either direction:** consensus needs
~1 block; local repair needs a slack window whose size is a judgement, and 256
is already generous; the archive needs whatever an archive needs and that is a
policy decision. Three different answers, and only the third is long.

## 7. The index is derived, so skipping it is deferral rather than loss

The sharpest simplification in this whole document, and it reframes section 4.
`WriteHistory` is a **pure function of the changesets**: it reads the same
account/storage changes `WriteChangeSets` persists and emits (key → block
numbers) bitmaps. Nothing else feeds it.

So the history index is not a second source of truth that must be maintained in
lockstep. It is a **derived cache over an input that is being kept anyway**, and
building it inline — inside the block commit path, on every block — is a
choice rather than a requirement.

**The out-of-band tooling already exists.** `cmd/n42-hist-from-freezer` builds
accthist/storhist RecSplit segments directly from the `acctcs`/`storcs`
changeset freezer, and its own header states the purpose: so that "the
full/archive tiers can ship historical state-at-height indexes without an
Erigon source". `ethexec history-build` is the Erigon-sourced counterpart, and
`internal/cscompact` holds the builders both use.

That gives the correct mental model for the whole retention question:

    changesets      the durable INPUT     -- rewind, archive build, index build
    history index   a DERIVED CACHE       -- rebuildable offline, any time
    QMDB undo       the tree's own record -- bounded at 256, unrelated to both

And it settles what a benchmark should do. A node that never serves a
historical query spends 20.3% of its write path maintaining a cache with no
reader, on every block, when the same cache can be produced by one offline pass
whenever someone actually wants it. Skipping it is **deferral, not loss** —
which is a materially different claim from the one section 4 made, and a much
easier one to accept.

The interlock in `api.State` is still right, and for the same reason: while the
index is absent the node must refuse historical queries rather than answer them
from current PlainState. "Refuse until the index is built" is an honest state
for a node to be in. "Answer wrongly" is not.

## 8. Measured end to end: +47-55% TPS, and one number nobody can explain yet

Round 11, 2026-09-04. Warm-up leg discarded, then A-B-A-B, same binary
throughout, only `N42_NO_HISTORY_INDEX` differing.

    leg      n   chgTrunc  chgSets  chgHist   wtotal
    A1      81      0.0      8.3     128.4     661.5
    B1     118      0.0      9.1       0.0     273.8
    A2      80      0.0      8.4     132.4     668.8
    B2     121      0.0      9.0       0.0     273.9

    bookends   A 1.1%   B 0.0%
    write path 665.2 -> 273.9 ms   = -391.3 ms (-58.8%)
    win TPS    12,952-13,714  ->  19,048-20,952   = +47% to +55%

Every criterion registered before the round:

- **Interlock (outranked everything).** Both B legs REFUSED a historical
  `eth_getBalance` at head-100; both A legs answered. The gate works.
- **chgHist -> 0.** 128.4/132.4 becomes 0.0/0.0. The flag did what it says,
  which is not a given: two rounds today were lost to flags that silently did
  nothing.
- **Changesets survive.** chgSets 9.1/9.0 ms in the B legs. Rewind, the archive
  input and the index rebuild all keep their source.
- **Bookends 1.1% and 0.0%** — the tightest of the entire effort, and the
  effect is more than forty times the leg-to-leg spread. This is not one of the
  marginal results that four earlier rounds could not resolve.

**62. The prediction failed by 2.4x, in the favourable direction.** P3
registered "write path falls 60-160 ms" on the reasoning that the phase costs
117.6 ms. It fell 391.3. Being wrong in the direction that flatters the change
is still being wrong, and the size of the miss is the interesting part:

**63. Removing a 128 ms phase removed 391 ms of write path, and this document
does not explain it.** The amplification is roughly threefold and it is the
open question this round produced. A candidate exists -- the index is a
read-modify-write of a roaring bitmap per changed key per block, ~24k random
keys, which dirties MDBX pages whose commit cost also disappears, the same
shape as N42_TXINDEX_TAIL's 2.5x -- but it is UNTESTED and is recorded here as
a candidate rather than a mechanism. Three explanations offered on this rig
today were withdrawn after being named without measurement; a fourth is not
being added on the strength of a plausible story.

The consequence for the number: `-58.8%` is what was measured and it stands.
"Because it stops dirtying pages" is not established, and if the real cause
turns out to be something else the SAVING does not change -- only the
generalisation to other writes would.

## 9. Why this cost exists at all: the index is inline here and asynchronous in reth

Contributed by the native-chain session after round 11, and it reframes the
win. reth has the same work — `update_history_indices`, the same sharded
bitmaps over ~163k accounts a block — and it does not appear on their critical
path. Their persistence runs behind an in-memory tree and the engine only waits
when a block's persistence exceeds the cycle, or when the tree passes an
8-block threshold. At their current 0.37-0.45 s persistence that never happens.

Here the index is written **inside the block commit transaction**, so every
millisecond of it is on the critical path by construction.

That makes round 11's result narrower and more interesting than it first looks:

- Narrow, because it does not say the index is expensive in general. It says
  the index is expensive WHEN INLINE. The same code off the critical path
  costs the block nothing, which is why the equivalent lever is worth little
  on the Rust side and why the native-chain session is measuring their
  followers' per-block persistence before deciding.
- Interesting, because the flag removes one item from a critical path that
  arguably should not contain it. `WriteChangeSets` (8.7 ms) has to be in the
  commit transaction — its rows must be atomic with the state they describe.
  The history INDEX does not: it is derived, rebuildable offline, and read by
  nothing during import. It is in the transaction because that is where it was
  written, not because it needs to be.

**The larger lever this suggests, stated as a direction and not a result:**
moving index maintenance off the block commit path — asynchronous, batched, or
deferred to the offline builder that already exists — would recover the same
time without giving up the capability, and would apply to any other derived
artefact that has drifted into the commit transaction. Nothing here has
measured that, and the atomicity question (what a crash between the state write
and a deferred index write leaves behind) is exactly the kind that has to be
answered before it is built, not after. The `refuse rather than lie` gate is
the shape of the answer: an index known to be behind is safe if queries against
the gap are refused.
