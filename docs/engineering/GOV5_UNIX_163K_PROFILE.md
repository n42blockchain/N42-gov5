# Independent Unix single-block profiling

This diagnostic is not yet executed. It changes no production behavior and
opens neither the retained chain nor the original supply corpus. The q6 window
remains failed; its 489,000 canonical transactions over 20.007302757 seconds
are diagnostic, without accepted throughput or capacity evidence.

`TestUnix163kOperationProfile` is opt-in through `GOV5_UNIX_PROFILE_DIR`, which
must name a fresh absolute output directory. A controlled source-only claim
must admit at least 90 GiB of available memory and maintain an 80 GiB runtime
floor. Signing, setup and validation use GOMAXPROCS=2; each explicitly measured
operation temporarily uses 36 and records the actual transition and restore.
Compilation and the normal/race/vet/Make checks continue at `go -p 1` and
GOMAXPROCS=2. Execution must follow the current retained-chain audit's terminal
cleanup; this file does not authorize a concurrent worker.

The synthetic chain has 6,000 funded senders and 22,857 recipient accounts
precreated with balance one. Recipient addresses use the existing corpus
recipient rule. All sender/recipient/validator addresses must be disjoint.
Each sender signs 28 protected EIP155 transfers. The 64-shard, 28-transaction
frames undergo actual strict signature admission before profiling starts.
This reproduces the owner block and sender-lane geometry, not the production
500-transaction resume protocol or source throughput.

The queue starts with 168,000 transactions. The real Unix BuildFromQueue RPC
selects 163,000 under the existing 32 MiB body limit: 27 complete rounds over
address-sorted senders, then the first 1,000 senders' next transaction. The
expected ordered hashes are computed from the signed input before selection.
An external test leader seals the result. Real Unix Import, AppliedChild,
one first certified commit and six concurrent certified duplicates follow.
Each commit request still supplies a public five-of-seven CommitQC; a
four-vote request must be rejected. This is certificate verification, not a
seven-process consensus-network measurement.

The default strict-import path and the existing own-build-reuse path share
exactly the same genesis, signing keys, signed bytes and manifest. Each runs
against its own fresh durable MDBX database with no synchronization override;
the test uses an explicit 8 GiB mapping and 256 MiB growth step. It does not
use `memdb`, whose options include UtterlyNoSync. Both databases and profiling
outputs remain under the owned artifact directory.

Each operation saves its CPU profile, cumulative allocation profiles before
and after, elapsed monotonic wall time, allocation-byte delta and malloc delta.
Use the allocation-before profile as `pprof -base` when interpreting the after
profile. CPU samples cover client and server work in the same Go process;
they do not include key generation, signing or admission. GC and profiler
startup/flush occur outside each recorded operation wall interval. The
explicit GC between operations changes cache/GC conditions compared with a
continuous production run, so these samples locate stacks rather than predict
sustained throughput. Nested production phase records overlap the outer RPC
intervals and must not be added as independent wall or CPU totals.

The build records whether a prepared candidate exists and its retained bytes.
The strict path must perform two actual execution attempts (build and import),
and the reuse path one. A prepared-candidate budget miss preserves failure;
the existing 128 MiB cap is not enlarged. The CPU profiler is flushed through a
defer even when an operation exits abnormally. Storage override environment
variables are rejected, and actual MDBX flags must exclude reduced-sync and
write-map modes.

Final checks run outside the profiles: exact gas and receipt roots/bloom,
all physical transaction lookups, canonical/applied marker equality,
root-bound Unix account reads for every sender and recipient, each sender's
nonce/balance and recipient credits, 5,000 pending transactions, zero reserved
credits and unchanged duplicate generation. Both modes must produce the same
sealed block hash, genesis/manifest hashes, actual root, receipts, gas and
complete account-vector digest. Successful
output remains explicitly `tps_measured=false`.

The purpose is to choose a measured architectural change. Q6's ten 163,000-tx
blocks spent roughly 1.7 seconds in queue Build RPC, about one second in
Replica Import and several seconds around owner read/commit serialization,
while strict EVM execution itself was about 14–22 ms. The proposed ViewID proof
sharing could remove duplicate physical scans, but its idealized critical-path
gain is much smaller than the historical throughput gap. No cache or next
retained-chain load is introduced by this diagnostic.

The preceding retained-chain audit has independently passed: v6 status
`f6fc5d87`, result `6c835e80`, acceptance `2f161c26`. Both physical reopens
confirm height 1431, canonical count 7,570,852 and root `8cb442c2…`, with all
runtime processes and claims released. Its original database is now 4 GiB;
this diagnostic does not open that database or reinterpret q6's failed window.
