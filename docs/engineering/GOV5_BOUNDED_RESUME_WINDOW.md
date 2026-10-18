# Bounded original-source window v2

Concentrated source validation passed on 2026-10-18: 67 Python tests, targeted
normal and race tests across five packages, vet, and `make -o version-build build`.
The last accepted target state is the independent double physical reopen at block 1231,
6000 sender nonces summing to 211917. The earlier four-frame dispatch remains a
failed moving-head observation; the independent audits proved its 1417 new
transactions persisted. No bounded-v2 transaction has been dispatched.

GSR2 is explicit and cannot upgrade GSI1 or four-frame GSR1. The owner rehashes
all original shards, recovers the public logical sender mapping, reconstructs a
complete original-frame prefix whitelist, and verifies its full fresh canonical
account vector, header root, loaded QMDB root, genesis configuration and PlanID
before opening admission. Source reports identify content and confer no runtime
authority. All 500 original signatures are checked online, including canonical
prefixes and duplicates; original bytes, nonce values and genesis stay intact.

Production fixes nonce `[0,10000)` for the original 6000 senders: 120000 original
frames, 60000000 original transactions and at most 6370080000 encoded source
bytes. A fresh baseline determines the smaller new-transaction cap. Owner and
sender enforce separate cumulative frame/transaction/byte/new budgets, including
in-flight reservations. Canonical commits return queue credits without refunding
session lifetime. Known-zero replies may retry the same frame on the same
connection. Unknown outcomes stop dispatch and require a new state audit.

The source client verifies source and an empty owner before signaling readiness.
Only then does the launcher start seven producers; GSR2 connections open after
producer readiness to preserve their first-header timeout. The command has a
180-second total deadline including source IO, at most 30 seconds for producer
readiness, a target four-second warmup, a full 20-second measurement, final ACKs
and a drain capped at 45 seconds. These are failure limits, not promises of
completion; they never enlarge the source caps. JSON readiness is consumed only
after a separately synced completion marker.

The reference validator's canonical committed/executed counter is read before
and after at least 20 seconds measured from the first read's completion. The TPS
denominator encloses the entire first request through the final response. All
reference samples preserve identity, epoch, block limits and monotonic progress;
seven moving heads need not coincide. Dispatch continues through the final
response. Drain counts never replace the window endpoint. Any prefix exhaustion
before the final response fails the window instead of shortening it.

After drain, the launcher cleanly stops all producers, retains the owner to read
a stable complete account/root snapshot and verifies every sender's known ACK
increment and fee/value cost. It then stops the owner and physically reopens the
same data with seven nonproducing validators. The complete vector, root and
canonical tuple must match the stable snapshot. All process groups, sockets and
owned claims must be released before promotion; any actual failure is preserved
without automatic replay. The outer controller separately binds protected
copies, the original inode, source metadata, binary provenance and claim-scoped
pre/post observations.

The actual runtime profile is owner GOMAXPROCS 36, replay 4 and each validator 2
(total Go parallelism budget 54), with recorded inherited CPU affinity. This does
not claim 54 exclusive physical cores, an E1 capacity ceiling, or five-million
TPS capacity. Block selection stays 163000 transactions / 32 MiB; ingress queue
credits stay at most 2000000 transactions / 512 MiB.

A launch must have at least `60000000*4096 + 64 GiB = 314479476736` free bytes,
using a fresh filesystem observation, and preserve the 64 GiB runtime low-water
mark. The existing estimate is retained; no lower per-transaction disk estimate
or foreign-data cleanup is implied. Sixty million source transactions cannot
prove sustained five-million TPS over twenty seconds. A successful observation
reports only this bounded configuration's actual canonical rate.

The accepted source validation is recorded at
`/data/blockchain/gov5-work/validation/20261018-gov5-bounded-window-validation-v2`,
with independent acceptance SHA256
`81531a164588b4624e0f9ffd7041292cad750476c23f23b8f83ad63eb7a0bb54`
and status SHA256
`b10f0a5d5d8d34f87ef0df76066df9ca9c31d6afacf6ed1a237992b97e5f12a4`.
The real QMDB fixture exercised two GSR2 rounds, public 5-of-7 certified commits,
nonrefundable lifetime caps and physical reopen; it used four fixture senders,
not the retained seven-validator fleet. All five stage processes and the
controller exited, their groups were empty, and all five own claims were absent.
No actual corpus scan or retained target database was used in this test turn,
and no target transactions or TPS were measured.

The first validation attempt remains failed and preserved: its Python negative
permission fixture requested mode0755 under umask077 and unintentionally created
mode0700. The fixture now explicitly sets and asserts0755. The production guard
was unchanged, and the second attempt retained the complete test set.

## Terminal production barrier and recovered baseline

The retained-child recovery completed at height1246/hash9a90b0c5, nonce sum
1104509, with complete6000-account/root agreement live, after validator stop,
and across two nonproducing physical reopens. Its accepted outer kind is
`gov5-quiescent-retained-child-double-physical-reopen-accepted-not-tps`; the
plan reader explicitly recognizes that pinned double-report shape and still
requires its second recovery path/hash and all existing inner/root checks.

The next bounded window keeps the existing4-second target warmup,20-second
conservative canonical bracket,6.5M offered pacer,36/4/2 execution budget,
60M source prefix and45-second drain. Once the command and drain have ended,
each validator terminally fences fresh builds while its network, WAL child
recovery and public CommitQC remain alive. Its ACK records that validator's
fresh-build counter, which may be positive and differ from other validators.
Every later status must keep that counter unchanged. Seven canonical tuples
must converge with the owner canonical=applied at the unchanged acknowledged
transaction count; then capture the complete address-ordered account vector,
all six root batches and final root read. Lagging peers may catch up before
this terminal proof, while observed regressions remain fatal.

After stopping the seven validators, the owner snapshot must equal that live
quiescent snapshot before owner shutdown. The ordinary Miner=false physical
reopen must retain the same full vector, tuple and roots. Missing ACKs, new
fresh-build admissions, late imports or changed roots prevent promotion. These
phases occur after the measured interval and never change its numerator or
denominator. A verified nonce sum1104509 leaves58,895,491 possible new source
transactions under the unchanged60M lifetime cap; it cannot prove5M sustained
for20seconds. The next production window has not yet executed.
