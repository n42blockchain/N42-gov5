# Finite shared-owner phase diagnostic

Source validation passed in the dedicated 20261018 finite-phase turn: Python12
mocks, three-package normal/race/vet and Make build all exited zero. Independent
Sol acceptance is `f916f407b2814930d13eebdbd60cdeebd7918dff213fafa28401285d4cf5a803`
in `validation/20261018-gov5-finite-phase-validation-v1/acceptance.json`. That source-only validation made no target throughput claim. The separate
actual target diagnostic subsequently passed as recorded below.
The previous 20-second run remains failed because its queue did not drain within
45 seconds. Its subsequent two physical reopens established the new complete
6000-account baseline: canonical height 1240 and nonce sum 704125.

The diagnostic keeps the original corpus, signed transactions, bounded v2
60-million original transaction prefix, owner admission validation, public 5/7
QC, 163000-transaction/32MiB block limits, and existing queue credits. It selects
whole original 500-transaction frames by round-robin traversal of 64 shards,
starting each shard at sequence zero. Selection stops at the first prefix with
200000 through 200499 new transactions after fixed canonical skips. The schedule
binds every original frame's identity, sender, nonce range, payload hash and size;
its totals and per-sender increments are independently recomputed from frames.
The selection has at most 1810 frames and does not truncate or re-sign original
transactions. It does not resume the previous admission session; the fresh plan
fixes skips from the audited canonical state.

`bounded-phase-preload` validates all source shard hashes and reconstructs the
public mapping and whitelist through the existing shared scanner. The scanner
uses the existing temporary genesis MDBX calculation; it does not open a retained
chain database. The command then sends the finite schedule to the independently
armed owner with one admission in flight. Seven validator processes have not yet
started. Every receipt must retain the original canonical baseline and exact
cumulative pending count. Unknown outcomes or journal failures stop dispatch and
retain the charged budget and intent; they never authorize retransmission.
The candidate is not a promoted result. The launcher requires command exit zero,
all 64 durable journals, exact schedule/receipt/lifetime accounting, an unchanged
owner baseline, and absent validator sockets before it starts production.

`finite_fleet.py` starts seven producers only after that verification. It allows
45 seconds for the reference validator's canonical count to reach baseline plus
acknowledged new transactions and for all owner credits to drain. Moving heads
are allowed to differ. It stops the seven producers while retaining the owner,
freezes only their newly appended log segments, and requires a successful
163000-transaction Produce and durable reference Import joined by hash, number
and parent to a successful reference `advanceCanonical` certificate operation.
Error, canceled and deadline phase records remain in the evidence, including
legitimate zero hashes before a candidate existed. Phase intervals are monotonic
operation durations, not additive CPU time across nested operations.

The owner then supplies a stable complete 6000-account vector with exact known
sender nonce and balance deltas and equal header/QMDB roots. After clean stop,
a separate ordinary strict owner and seven nonproducing validators physically
reopen the same data; a fresh empty epoch, identical full vector, canonical tuple
and roots are mandatory. All 18 processes must exit cleanly, all nine sockets
must disappear, and the captured claim must be released before promotion.
Failure retains the attempt, logs and current data without automatic replay or
rollback. The result always has `tps_measured=false` and
`capacity_5m_proven=false`; this diagnostic has no 20-second denominator.

The completed validation used the authorized source-only 80GiB profile, GOMAXPROCS=2,
`-p=1`, nice 19 and idle I/O, within a single 600-second claimed turn for targeted
normal/race/vet, Python mocks and Make build. Runtime is a separate 90GiB claim,
with the unchanged 314479476736-byte launch disk budget and 64GiB running low
water mark. Clean committed binaries must include their actual VCS provenance;
a fresh full-source plan scan must bind the 704125 audit before any target run.
The existing same-claim original/three-protected-copy pre/post proof applies.

## Actual target acceptance

The separately claimed runtime completed on 2026-10-18 using clean `74de4981`
binaries and the source-scanned 704125-baseline plan. It sent 1032 whole frames:
516000 original transactions, 315824 canonical skips and 200176 new transactions.
All new transactions became canonical in blocks 1241 (163000) and 1242 (37176),
then persisted through the specified stable 6000-account/root snapshot and
physical nonproducing reopen. The final nonce sum is 904301 at height 1242.

Independent Sol acceptance is
`e8b34b44f799efc0e054b8c19fb31b7e4e58f5a38c86afecb1ecedff15bedafe`
in `validation/20261018-gov5-finite-phase-target-v1/acceptance.json`; it rechecks
all known receipts, phase association, full vector/root, 18-process cleanup,
same-claim original/protected file observations and claim release. The previous
20-second drain failure is unchanged, and this finite result has no TPS value.
The new 904301 complete vector is the basis for any later plan, not permission
to reuse this completed session. See the operations checkpoint for exact heads,
roots, file hashes and measured phase spans.
