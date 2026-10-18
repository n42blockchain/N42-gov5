# Retained nonempty child after the q4 bounded-window failure

Q4 failed its unchanged 45-second drain. Its 75,041 canonical transactions over
20.001005871 seconds (3,751.861 TPS) are diagnostic, not an accepted performance
result. Failure acceptance `194bc3a5` binds 10,291 original frames, 1,717,147 new
ACK admissions and the last observed canonical count 4,481,394 at height 1407.
Ordinary zero-dispatch recovery also failed; acceptance `5a048b89` binds its
empty owner epoch, reconciling gate and seven nodes still at that canonical head.
The report's zero nonce sum is an unpopulated failure default.

The sealed ordinary owner log contains seven matching successful durable
AppliedChild probes for height 1408, hash
`010a51ea7250aba813541fc2cbbfc0f1254aba6b4fb51fed03ac791823cee93d`, parent
`62afcb70e4513258ee572197119f543155ef0e96ebc238015e45d21032251e32`, with 163,000
transactions. Seven sealed startup logs show equal locked/committed QC views at
the previous head. These logs justify a bounded recovery attempt; they do not
constitute a new QC or a complete state snapshot.

`post_bounded_quiescent.py` derives the explicit target from those pinned logs
and the ordinary failure seal. It starts the existing owner and seven validators
in startup-latched recovery-only mode. Fresh builds must remain zero, while WAL
and AppliedChild recovery, networking, public 5-of-7 QC verification and canonical
commit remain active. During convergence a peer may report only the exact old
head/count or the exact retained target/count, without regression. The target
count is 4,644,394, below the known ACK upper bound 5,145,500.

A successful attempt requires all seven peers and owner canonical/applied roots
to agree at that target. A complete ordered 6000-account vector must remain
within each sender's historical nonce plus known ACK limit; balances deduct
21,001 for each actual nonce increment. Uncanonical ACKs are not replayed. The
live snapshot must survive clean validator stop exactly, then two ordinary
nonproducing physical reopens must preserve the complete head, vector and root
with independent empty epochs. The original claim, bounded cleanup, same-claim
four-DB PRE/POST hashing and protected copies remain mandatory in the external
wrapper. There is no transaction dispatch, source scan, Go rebuild, database
marker edit, rollback or TPS measurement in this recovery.

Twenty pure mock tests pass, including partial ACK bounds, floor-to-target
transitions, changed/conflicting retained evidence, zero fresh builds, root
changes, late imports and the full 26-process lifecycle. The first synthetic log
fixture lacked the real logfmt prefix; correcting that fixture made the two new
log-projection tests exercise their intended paths. No retained recovery runtime
has been launched for this new runner. Failure preserves its unique attempt;
there is no automatic fallback to production or another recovery attempt.
