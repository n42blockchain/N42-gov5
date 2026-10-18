# Bounded concurrent certified duplicate verification

The q5 20-second run remains a failed drain attempt, sealed by `6d508cfa`.
Its 312,125 canonical transactions over 20.024101654 seconds are diagnostic only.
Frozen phase analysis `7607b3f8` confirms no successful fresh empty canonical
commits. Thirteen nonempty canonical blocks each still received six duplicate
owner commit requests. Their physical verification and execution-lock waits
remain serialized; accumulated overlapping request times are not CPU totals.

The accepted ordinary double-reopen audit `1858dbf8` / `ee5a3c28` establishes
head 1421, hash `421a9179`, root `08e7b763`, nonce sum 5,940,852 and complete
account-vector digest `b3308c7f`. Both real reopens agree on all 6000 accounts and
roots. Actual new persistence from q5 is 1,296,458; 1,021,648 acknowledged new
transactions are not canonical. All twenty audit/controller process groups,
nine sockets and owned claims are gone. Future plans must use the second report
`4bc99acc`; the old admission session and plan are never resumed.

This source draft changes the executor and chain mutation locks to RWMutexes.
All existing writers retain exclusive Lock/Unlock. Only certified canonical
duplicate verification takes shared read ownership. A cheap canonical-hash probe
routes existing requests; it provides no execution evidence or authorization.
Each accepted request still freshly verifies its complete public 5-of-7 CommitQC,
header/proposal seal, body, receipts and physical transaction lookup entries.
The final canonical/finality durability View is unchanged. There is no receipt,
transaction, QC or cross-request execution cache.

The owner admits at most min(server capacity, 2) concurrent shared physical
verifications. Slot acquisition holds neither the executor lock nor the queue
mutex, honors requester and owner cancellation, and never closes the semaphore.
The queue mutex is used only briefly to check the clean canonical stamp. Missing
indexes, dirty/bootstrap state or a changed stamp release all shared ownership
and restart the entire old exclusive classification and repair path once.
No earlier proof, header or decoded body authorizes that retry. Read errors fail
without enabling repair. Initially noncanonical sibling QCs retain exclusive
classification after the preceding writer, then may join the shared path.

One lifecycle registration covers both attempts. Close still cancels the owner,
waits for registered operations and only then cleans state under exclusive
ownership. Existing ingress reconciliation remains owner-lifetime work and its
deferred error reaches the returned result before operation timing is recorded.
No RPC, runtime parameter, source transaction, signature, metering or measurement
window changes are introduced.

The new channel-gated fixture tests target queued and staggered overlap, the
physical-read cap, canonical-writer exclusion, requester cancellation, active
readers and slot waiters during Close, independent forged-QC rejection and fresh
exclusive fallback validation. Existing real missing-index repair, dirty/startup
recovery, cancellation, held grants, physical-damage and read-error regressions
remain.

Source80 v1 failed its normal stage: 102 top-level tests passed, including all
eight new reader tests; an existing held-reservation fixture failed during its
initial four-account QMDB load, before Server.New or the duplicate-read path.
The error was stored twig root versus reconstructed leaf/bits inconsistency.
Race, vet and Make did not run. Failure acceptance `85e308cc` seals the seven
tested source files and four unchanged common fixtures. The cause is unknown;
a passing later run cannot explain that failure.

The new test-only failure diagnostic copies genesis and the complete Account,
Storage and four QMDB tables from that same read transaction into the test log.
It emits the captured bytes before attempting an independent copied-byte load
and fresh alloc root computation, then emits their results and still returns
the original load error. It does not replace
keys, repair metadata, retry successful setup or relax any consistency check.
Focused tests inject a metadata read error and actual malformed twig metadata,
checking original-error preservation and stable copied bytes. This diagnostic
addition has not yet run under Go. A separately reviewed source80 version must
pass normal/race/vet/Make before clean binaries or any retained runtime.
No accepted runtime throughput improvement is established.
