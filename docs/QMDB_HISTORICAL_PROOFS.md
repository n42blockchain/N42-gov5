# QMDB historical state proofs (eth_getProof at past heights)

QMDB chains (`stateScheme: "qmdb"`, e.g. `mainnet_qmdb`) answer `eth_getProof`
with QMDB membership proofs: each `accountProof` / `storageProof[i].proof`
element is one hex blob (`qmdb.Proof.Marshal`, codec v2) that a client checks
with `qmdb.VerifyEncodedProofForKey(stateRoot, keyHash, blob)` (Go) or
`QmdbProof::decode(..).verify_for_key(..)` (N42-rs `twig-core`).
`n42_getStateProofInfo` reports the backend and what is provable.

What can be proven depends on the node's configuration:

| configuration | provable heights |
|---|---|
| default | the applied head and the last `--state.qmdb-undo-window` blocks (default 256) |
| `--state.qmdb-history` | additionally every height from the journal start (genesis on a node synced from genesis) |

A request outside the provable range is an explicit JSON-RPC error
(code `-32000`, `data: {requestedBlock, earliestProvableBlock,
latestProvableBlock, reason}`). Before this change such requests returned a
keccak placeholder `accountProof` that looked like a proof and was not one.
An account that does not exist at the height returns an empty `accountProof`
(QMDB has no non-membership proofs; see "Limits").

## Flags and config keys

| flag | config key (`node`) | default | meaning |
|---|---|---|---|
| `--state.qmdb-history` | `qmdb_history` | off | keep the full-history journal and serve any height from its start |
| `--state.qmdb-history.reseed` | `qmdb_history_reseed` | off | discard a journal that cannot continue and start a new one at the current head instead of refusing to start |
| `--state.qmdb-undo-window` | `qmdb_undo_window` | 256 | recent blocks with an undo record (recent-height proofs and the HotStuff sibling-switch reach); values below 256 are raised to 256 |
| `--prune.mode` | `prune.mode` | `archive` | `full` prunes changesets/history older than `--prune.retention` |
| `--prune.retention` | `prune.block_retention` | 90000 | blocks of state history kept in full prune mode |

Without `--state.qmdb-history` nothing about the node changes: same tables,
same rows, same state roots, same performance.

`eth_getProof` also returns `balance`/`nonce`/storage values, read from the
state history tables. In `--prune.mode full` heights below the prune horizon
are refused (the proof alone would still be valid, but the values could not
be read). Run archive mode on a node that serves deep historical proofs.

## How it works

The QMDB world root is a binary Merkle tree (Blake3) over twig roots; a twig
root is `hashNode(leafRoot, Blake3(0x03 || activeBits))` over 2048 append-only
slots. Leaves never change after append; only a slot's liveness bit flips,
once. So the tree at height H is determined by: the append cursor E(H), the
block each slot died in (death stamp), the frozen leaf trees, and per key the
slots it was written to. The journal stores exactly that (tables
`qmdbBlockPos`, `qmdbDeathStamps`, `qmdbKeyVers`, plus `qmdbTopBand`, the top
6 levels of the upper tree per block, so a proof does not refold the whole
upper tree).

- The block write journals each block from the block's undo record, in the
  block's own transaction (`qmdb.RecordBlockHistory`,
  `QMDBRootComputer.RecordBlockHistory`). Every unwind of a persisted block
  (HotStuff sibling switch, startup speculative revert, three-state realign)
  removes the block's journal in the unwind's transaction
  (`qmdb.RevertBlockHistory`).
- Dead entry rows are kept on disk (normally they are reclaimed) because a
  past proof needs the value of a slot that has died since.
- A proof at height H older than the undo window is built by
  `qmdb.Tree.ProofAtHeightFrom`: the target twig's leaf path from the frozen
  leaf tree (the boundary twig is refolded with the not-yet-appended tail
  nulled), the twig's activeBits at H from the death stamps, and the upper
  path from the journaled top band plus twig roots at H for deeper siblings
  (a twig fully appended by H reuses its frozen leaf root; only its bits are
  rehashed). Proofs within the undo window use `qmdb.Tree.ProofAt` (undo
  replay on scratch copies). The latest height uses `qmdb.Tree.GetProof`.
- All three produce the same `qmdb.Proof` (key, value, slot, 11 twig
  siblings, 256-byte activeBits, upper siblings), encoded by the same
  `Proof.Marshal`, verified by the same `qmdb.VerifyProof`.
- Before any non-latest proof is returned, the reconstructed root is compared
  with the canonical block's executed state root (`ExecutedResultOfHeader`);
  a mismatch is an error, never a proof.

## Enabling on an existing node

On start with `--state.qmdb-history` the node checks the journal after its
startup repairs, before any block is processed:

| database | result |
|---|---|
| new chain / no journal yet | journal seeded at the applied head (genesis on a new datadir): heights from there on are provable |
| journal continuing exactly to the applied head (this node, or a `replay-v2 --qmdb-history` rebuild) | continues; earliest provable = journal start |
| journal with a gap (node ran without the flag for a while) or records above the head | **refuses to start**, unless `--state.qmdb-history.reseed`, which discards the journal and seeds a new one at the head |

Seeding writes one key-version row per live key and the death stamps of every
slot (one pass over the live state; minutes on a full mainnet-size state).
Heights before the seed are not provable; history cannot be filled in
backwards on a live node.

To get full history from genesis for an existing chain, rebuild the database
from an empty target:

```
n42 replay-v2 --source <existing chaindata> --target <new datadir> \
    --chain mainnet_qmdb --tree qmdb --qmdb-history --from 0 --to <head>
```

then start the node on the new datadir with `--state.qmdb-history`; the
replayed journal is adopted (`qmdb-histcheck --db <new>/chaindata` audits it
offline). Keep the old datadir until the new one has caught up.

If the journal is ever marked broken (an unwind it could not follow, a
journal/undo disagreement; logged as `QMDB history journal broken`), heights
from that block on are refused while lower heights stay provable; restart with
`--state.qmdb-history.reseed` or rebuild. Unwinds below a mid-chain seed point
also mark it broken.

## Disk and latency

The journal costs, per block that changes state: one 16-byte cursor row, one
~2 KiB top-band row, one key-version row per written key, and one 8 KiB
death-stamp blob per twig touched by a deactivation (read-modify-write).
Retained dead entry rows grow the entry table with every overwrite instead of
staying at the live set. The offline replay with `--qmdb-history` measured
about 3x the QMDB tables of a plain replay on mainnet data; plan disk
accordingly. See the measured devnet numbers in the change's report.

## Limits

- Non-membership (absence) proofs and versioned per-key proofs need an
  entry-format change (a consensus fork) and are out of scope.
- The journal starts where it was seeded; earlier heights are only provable
  after a rebuild from genesis.
