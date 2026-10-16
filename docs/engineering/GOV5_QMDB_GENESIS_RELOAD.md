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
