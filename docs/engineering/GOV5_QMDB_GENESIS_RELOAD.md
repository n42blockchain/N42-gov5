# QMDB genesis leaf persistence and retained recovery

The first same-data `s192r1/attempts/recovery-q1` run on 2026-10-16 started
only its execution owner. It exited one before validators or the audit started:
`load twig 0 of 9 (nextSlot 16957, active 8, indexed so far 668): twig metadata inconsistent`.
The original databases, failed strict result and recovery logs remain in place.
No transaction was dispatched, no nonce vector was collected, and no recovery
success or TPS result was published. The error establishes a failed root
validation on reload, not by itself physical database corruption. The partial
668-key scan is not the database's full live-account count.

`SeedQMDBGenesis` previously computed and flushed without attaching the backing
transaction as a cold/leaf store. It persisted entries and twig metadata but no
frozen leaf blobs. A first reload can reconstruct sealed genesis twigs from the
entries and release their node heaps. Later updates reclaim old entry rows;
already sealed twig heaps are absent, so that flush cannot create the missing
genesis blob. Another reload then lacks the frozen hashes of those dead slots.

The prevention fix attaches the genesis transaction before compute/flush and
detaches it before return. This persists the frozen leaf blobs from genesis.
Root computation, positional ordering, configuration, signatures and consensus
rules are unchanged. It is not a migration or an automatic repair of old data.

`TestQMDBGenesisReloadAfterSealedAccountUpdate` uses a temporary real durable
MDBX database with 6000 accounts. It seeds, physically closes/reopens the DB,
updates an account in the first sealed twig, flushes/commits/evicts, and closes/
reopens again. The test checks root equality, the 6000 live-account count, the updated
nonce/balance, sealed genesis blobs and actual reclamation of the old entry.
The seeded root must equal the original non-persisting genesis calculation.
The old implementation reproduced the same twig-zero error (0.034s); the fix
passed (0.033s), with GOMAXPROCS=2/-p=1, nice19 and idle I/O.

Existing data requires a separate read-only/copy audit. Stored metadata,
reconstructed blob leaf roots, current bitmaps, global root and applied/header
anchors must be distinguished. No stored root or bitmap may be deleted or
recomputed to make a check pass. In particular, deriving a missing *fully sealed
genesis* blob from the immutable genesis is only a potential recovery source;
it must first reproduce all relevant commitments on a preserved copy. This
slice does not modify the retained runtime or authorize replay of old unknown
ACKs. Full build/race and an actual retained-data recovery of this fix remain
pending the required hardware turn.

## Copy-only audit command

`cmd/gov5-qmdb-audit` has no repair or execution path. Its `--copy-db` must be a
non-aliased private `validation/.../audit-copy/chaindata` directory with an owned,
read-only (0400), non-hardlinked `mdbx.dat`. The caller supplies its complete
SHA256 and the exact original genesis SHA256. Database bytes, inode, size and
mtime are checked again before creating an exclusive new JSON report. The
original runtime path is rejected before opening any database.

The command regenerates the fixed genesis in a disposable scratch database and
checks its complete forest against its PlainState, then binds the copy's chain
configuration and genesis hash. It checks canonical/applied header ancestry and
requires the persisted world root to equal the applied header root. Only truly
absent leaf blobs of fully sealed genesis twigs may enter an in-memory overlay.
Existing empty or conflicting blobs and absent non-genesis blobs are errors.
Every blob must reproduce its stored leaf root and bitmap-combined root.

The audit validates positional key bounds and never-appended tails, all live
entry hashes and unique keys, a fresh full-map index and live population, and
both directions of exact PlainState account/storage values. Current 52-byte
storage keys are supported; unknown layouts are rejected. The incident tool is
bounded to one million append slots and 12,500 genesis accounts. It does not
change QMDB's normal loading policy or claim a general chain-scale repair.

Four targeted tests pass with GOMAXPROCS=2/-p=1 (0.064s), and targeted vet
passes. A real temporary 6000-account MDBX fixture exercises missing sealed
blobs after entry reclamation; malformed/existing blobs, partial-twig loss,
changed live entries or flat values, positional overflow and missing/wrong
storage all fail. A serialized header filed under an incorrect ancestor hash
is rejected even when its height matches. The actual retained s192r1 copy has
not yet been audited.
An audit pass would identify proven candidate bytes only; it would authorize
neither a database mutation nor replay of transactions with unknown ACKs.

## Claimed validation checkpoint, 2026-10-16 16:22 UTC

The frozen clean source `b074593757f44fac44bc269da8d8e0bf5b883f47` passed
the genesis regression under race (1.136s), the audit package under race
(1.268s), full `make -o version-build build` (170.144s wall), and the Make
audit-binary target (11.019s wall). GOMAXPROCS=2 and GOFLAGS=-p=1 applied.
Three quiet samples and claim contention checks passed; the build claim was
released normally before publishing the immutable binary and yielding.

Binary SHA256:
`8d244aac8ecc4571c6f8039e3f45ddede9a7ab83bd956399659973fe653374d1`.
Logs, binary and status are under
`/data/blockchain/gov5-work/validation/20261016-gov5-qmdb-audit-b0745937/`.
The earlier build/race pending item is therefore resolved. The separate
backup/read-only-copy turn is still waiting for hardware; no retained DB has
been opened, copied or repaired in this stage. Root/nonce recovery and resumed
transactions remain pending actual retained-data evidence.
