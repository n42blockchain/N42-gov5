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

### The horizon, concretely — and one half of it is live right now

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
