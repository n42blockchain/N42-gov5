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

What is NOT yet decided is the split between the two halves. `WriteHistory`
looks tierable on this chain (its only consumers are historical RPC);
`WriteChangeSets` does not. A round measuring `chgTrunc` / `chgSets` /
`chgHist` separately (commit 5f05595f) is what decides whether a non-archive
mode is worth building. Until that number exists, this document deliberately
does not claim a win.

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
reorg deeper than the committed window is out of scope by construction — which
puts the safe prune floor many blocks shy of any target worth pruning to. The
constraint is real but it is not tight, and that is worth knowing before
someone decides the whole idea is unsafe.

Concretely, for a future Go prune mode: the floor is the highest
QC-committed height, not a fixed depth, and it must be read from consensus
rather than assumed. `qmdbUndoWindow = 256` in blockchain_write.go is a
DIFFERENT window with a different justification and must not be reused as the
prune floor by analogy.

**For any client:** if you are about to disable one of these to raise a
benchmark number, say in the commit message which reader you checked. The four
mechanisms above each took a grep to find and one of them (the PlainState
unwind) is the kind that fails months later, on a reorg, on one node.
