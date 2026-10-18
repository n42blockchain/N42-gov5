# Retained-child recovery without fresh builds

This runner is pending review and target execution. It does not replay the old
admission session, arm a source plan, initialize a chain, alter markers, force
canonical state, or measure TPS. The historical plan and known ACK journal only
bind the expected account deltas. The stopped chain's existing sealed child is
the only candidate the recovery-only producers may import and propose.

For the current finite failure, the metadata floor is height 1245 and 1104509
canonical transactions. The expected retained empty child is height 1246,
hash `0x9a90b0c5837a99621360cd7b63054b65865e9aa67a6f57a039bd488d063a600b`.
A new strict empty owner epoch and seven startup-latched recovery-only producers
must form and persist a real public CommitQC. All seven ACKs bind the identity,
owner epoch and source manifest, and fresh-build admissions must remain zero.
The network, voting and certificate replay remain active during convergence.

With all seven canonical tuples matching that exact retained child and the
unchanged transaction count, read the complete address-ordered 6000-account
vector in six root-bound batches and a final root read. Every account must equal
the historical full vector plus its known finite admission delta, including the
exact balance cost. Stop only the validators, recapture the owner's full vector,
root and tuple, and require exact equality. This rejects a late owner Import.
Then stop the owner and perform two ordinary Miner=false physical reopens, each
with a fresh epoch, the complete vector, root=loaded QMDB root, and seven equal
canonical tuples. The two reopened snapshots must equal the live snapshot.

All 26 runtime processes must exit cleanly without forced termination, with
registered groups and nine sockets absent. Publication follows the original
claim tail and independent proof that captured claim identities were released.
The separate outer wrapper pins failure evidence, binaries and runners, hashes
the original database and three protected copies before/after within the same
claim, and preserves every failure without automatic replay. Original inode,
source and genesis remain fixed. The existing runtime90 memory admission,
64GiB disk reserve, 600s runner and 900s outer bounds remain; cleanup can extend
only to drain owned groups safely.
