# Strict owner-queue recovery

The retained first `s192r1/strict-q1` run entered its observation window but
stopped on an unknown ingress ACK outcome. It did not produce a complete
20-second TPS result. Client-confirmed admissions were 2,008,500 and the owner's
last observed unique admissions 2,032,000; canonicalization continued during
cleanup. The runtime databases and both attempt directories remain evidence.
Do not replay nonce zero, infer durability from a new empty queue epoch, or
replace the runtime to conceal this failure.

## Canonical account query

`POST /v1/ingress-accounts` is a read-only endpoint on the existing private Unix
execution socket, available only when strict ingress is explicitly enabled.
It requires exact chain identity, owner epoch, final manifest hash, expected
canonical hash/height, and 1–1000 distinct addresses. Request bytes are capped
at 64KiB. The response is bounded to 384KiB by the client, sufficient for 1000
maximum-width uint256 balances and uint64 nonces. Balances use explicit decimal
encoding, independent of the repository's uint256 String implementation.

The owner registers the read in its lifetime drain, holds the existing execution
lock, checks the queue canonical generation/binding, and reads one actual
canonical account snapshot. Speculative applied state, a reconciling queue or
a changed canonical tuple returns a snapshot conflict with no accounts. Missing
markers and other database errors remain failures. The final generation is
checked again before returning. A read does not advance canonical state, admit
transactions, release credits or authorize a frame replay.

Each ordered account result includes Address, Exists, Nonce, Balance and
CodeHash. Exists records an actual database account, not the helper's empty
account default; an absent account must never be treated as a funded sender
with nonce zero. The client validates exact outer binding, address order,
count, canonical decimal balances, and consistent empty state for absent
accounts. A consumer verifying the funded supply EOAs must additionally require
Exists, the empty EOA code hash, adequate balance, and nonces within the signed
source range.

All six batches for the 6000 source senders must use one identical expected
canonical tuple. Any changed tuple or actual/canonical conflict discards the
whole vector and permits only bounded read-only retry. Nonce totals must be
checked against the seven canonical counters. Neither this RPC nor a successful
snapshot resolves the old volatile frame ACK set automatically; any subsequent
replay plan is a separate explicit, validated step.

The endpoint slice adds the client and existence metadata. The separate
`gov5-replay recover` command now collects the public genesis sender set in
1000-account batches, requires a new empty epoch and disabled producers, and
checks a complete canonical-bound nonce vector against all seven counters.
It requires the retained failed result, permits the documented late canonical
commit without count/height regression, and never opens an admission transport.
A changed batch tuple discards the entire vector. The separate recovery-only
fleet launcher is implemented; its completed same-data account collection is
recorded below. No runtime feature, signature policy, consensus certificate or
default native/ETH behavior is relaxed.

Validation: three `TestCanonicalAccounts*` tests passed under nice19/ionice idle,
GOMAXPROCS=2 and -p=1 (0.061s). Real QMDB plus Unix RPC tests distinguish a funded
nonce-zero account from an absent address, refuse executed-but-uncommitted and
four-vote state, and return nonce three only after the actual five-vote canonical
commit. Old tuple queries then fail. Other tests cover wrong identity/session/
manifest/tuple, duplicate/oversized address lists, malformed/unknown/trailing
JSON, closed owner, altered responses and maximum-width 1000-account responses.

`go vet ./internal/sharedexec ./internal` and `git diff --check` passed. The
targeted race, full Make build and updated owner/audit binaries subsequently
passed in the hardware turn recorded below. Recovery was still pending at that
build checkpoint; the later same-data result is recorded below.

The recovery command has a 45-second bound and records its failed-run hash,
nonce vector/hash, canonical nonce sum, owner/validator observations, and zero
dispatched transactions separately from any throughput result. It rejects absent,
contract, underfunded and out-of-source-range senders. New queue epoch and nonce
state do not grant replay permission for the previous unknown ACKs.
Three `TestRecovery*` tests passed (0.015s) with GOMAXPROCS=2/-p=1, covering a
1001-account two-batch snapshot, whole-vector rejection on a changed second
batch, typed snapshot conflict, invalid sender state, late commit acceptance,
immutable same-block count, nonregression and zero-activity session binding.
Replay vet passed. These unit results preceded the actual account collection
recorded below.

## Producer-disabled replay launcher

`scripts/gov5-perf/recovery_fleet.py` explicitly requires `--run`, a retained
failed `--baseline-result`, fresh attempt name and node/owner/audit binaries.
It has no initialization or transaction-dispatch option. It reuses schema-2
validation and full BOX claims/guards; before startup it rechecks the runtime
and exact failure bytes. Original node configurations remain unchanged.
Private attempt-local copies set only node.miner=false, so service startup can
replay stored CommitQCs and exchange existing bodies without producing new
candidates. This mode cannot recover an applied child lacking a usable QC.

The bounded claimed work window is 180 seconds, excluding the preceding quiet
and contention checks. The audit itself is bounded to 45 seconds. Existing-data
recovery requires 64GiB free and stops at that low-water mark; it neither uses
nor weakens the separate 796.422GiB default capacity-run planning gate. Runtime
and audit thread budgets are two, with actual affinity recorded.

The launcher independently checks the full public sender vector, its digest,
funding/EOA/range constraints, canonical sum, all seven heads and new zero-activity
owner epoch. Successful observation remains staged until every owned child exits
zero without forced termination and the claim tail passes. A forced kill,
nonzero shutdown or claim contention preserves evidence without promoting a
successful result. All original data and unknown frame outcomes remain intact;
this mode grants no permission to replay the previous volatile admissions.

Seven Python tests passed (0.022s), covering nonzero late commits, malformed
recovery evidence, failed-baseline binding, original configuration preservation,
no initialization/dispatch, dangling-attempt refusal, forced cleanup and claim
tail failure. A shared Go/Python JSON fixture also passed the actual Go recovery
result decoder and nonce-vector hash check (0.011s).

## Recovery build checkpoint, 2026-10-16

The clean frozen source was a81bb5a570f18a8cdeea5b036a1b37d8cf1fcc30.
During the completed hardware turn, targeted race passed for sharedexec (1.369s)
and gov5-replay (1.104s), followed by `make -o version-build build` and the two
Make binary targets, all exit zero. The claim tail passed and the build claim
was released before requesting a separate runtime turn. Artifact directory:
`/data/blockchain/gov5-work/validation/20261016-gov5-strict-recovery-a81bb5a5`.

| Binary | Source | SHA256 |
| --- | --- | --- |
| n42, reused | 757c2ded8aa98656bfea4c0be31f23186f2e1148 | 3cfa04eacf2bf7232cb0874f9fe33d98642896d93d530f218d1179d85ca93f7c |
| n42-execution-service | a81bb5a570f18a8cdeea5b036a1b37d8cf1fcc30 | 4cf74294516e23e241c36a4f2d184cd77e008196cb99e8fab5b55176bae15a17 |
| gov5-replay | a81bb5a570f18a8cdeea5b036a1b37d8cf1fcc30 | 14e84ef0b8c37a905cb996f6a954fe220a306e79e267736566f34679f283bf72 |

An earlier Make invocation was terminated by our guard when foreign cargo
appeared during the claim. Its exit -15, logs and status are retained. The
controller was then corrected to distinguish genuine compiler failures from
guard-triggered termination even when contention occurs simultaneously. Ten
small simulations of the actual controller loop passed; source, output and
historical controllers/status are retained in the artifact directory. Real
compiler errors, timeouts, unknown results and existing runtime attempts stop.
Only proven prelaunch or guard/tail resource contention may wait and retry.

As of 14:18 UTC, the single controller was waiting for the separate runtime
window; `recovery-q1` did not exist. No canonical nonce vector or recovery result
had been collected. This build checkpoint is not a recovery pass or TPS result.
The later request-deadline fix described below is excluded from these frozen
binaries and remains unverified in the runtime.

## Same-data no-dispatch recovery, 2026-10-17

The first `recovery-q1` later failed while the owner loaded missing sealed
genesis leaf blobs. The copy audit, candidate restoration and precise original
two-row repair are documented in `GOV5_QMDB_ORIGINAL_TWO_LEAF_RESTORE.md`.
The completed original-repair proof SHA256 is
`89796a91799813ddaf246802f8d8808720afae06cbec2865b4aaa58fd1bdcb7f`.
Neither failed attempt was replaced or discarded.

At 00:28 UTC the separate `recovery-q2` passed on the same `s192r1` data with
the three frozen binaries listed above. No rebuild, initialization, transaction
dispatch or original configuration change ran. A reviewed child shim wrapped
only the frozen runner's `fleet.claim`: complete original/protected-copy
identity and SHA256 checks preceded any attempt creation, and complete postchecks
followed clean child shutdown, inside the same delegated BOX claim. The frozen
six runner files remained byte-identical to a81bb5a5. The outer controller and
shim SHA256 values are respectively
`eec87b263d6be0d5ad05bfcfe5bebc77fb090433a6f2be0a12ca8c3cef5efa2f` and
`5685ad07f04ee58c878dad111a5d6ee4b59eec82790ad664b97ece4cf43371d6`.

Artifact directory:
`/data/blockchain/gov5-work/validation/20261016-gov5-strict-recovery-q2-after-97d591ae`.

| Evidence | SHA256 |
| --- | --- |
| Promoted outer `recovery-result.json` | cd72e20f725b004cf539408b0e699ea1cf35fca6240224d28fc101e923ad29e5 |
| `s192r1/strict-attempts/recovery-q2/recovery.json` | ae75ad7c49111e41bbf54ec12f9a949d17781caea105992e9397df61d56ff5fe |
| `s192r1/attempts/recovery-q2/recovery-fleet-result.json` | 99b410b1af2f0a66ec896f2b7c1ad55d684c903a2d2b03380d799a0103418baa |
| `try-0291-preproof.json` | d6f15a541127e9a711fb02b73334ad8ff56a24e10e7b7f29db5d2e9892d11dc3 |
| `try-0291-postproof.json` | fd8d68a702a5c0342039505ff04101bec7e74a965704dcfe8ed5acffdb77ae49 |

Independent acceptance reran the frozen evidence checks, verified all input and
controller digests, required both pre/post proofs from the same claim, and
recomputed the complete ordered account-vector JSON digest. Owner, seven
validators and audit all exited 0 without forced termination or surviving
process groups. The worker and child exited, and owned claims were released
before promotion. The preceding 290 tries were resource-only refusals with
neither runtime attempt present; they did not start the repaired database.

All seven initial and final samples and the owner agreed on canonical height
254, block hash
`0x62bbcf53ba1c0e26f93dc2a1e2c3a0bbe8ca44abdcc342899fb893069ae3258b`.
Each validator's single canonical executed counter was **210,500**; the seven
copies are not added together. All seven producers were disabled. The new
owner epoch had zero new/crypto attempts, pending/reserved transactions and
retained/reserved bytes, with reconciling false. Dispatched transactions were
zero. The owner log loaded 10,415 QMDB live keys and next slot 16,957 successfully.

All 6000 funded existing EOAs were read at the same canonical tuple, with zero
snapshot retries. Their nonce sum was **210,500** and account-vector SHA256 was
`a8550361fcc18205b30a0ee7628e8242ddc2386e544e8e79b19f66215f9fdd5c`.

| Canonical nonce | Accounts |
| --- | ---: |
| 0 | 2000 |
| 41 | 1010 |
| 42 | 2895 |
| 500 | 95 |

Thus **3905 accounts are not aligned to a 500-transaction source frame**.
The vector uses sorted public addresses, not the source's logical sender-index
order. A future explicit partial-frame recovery design must bind that mapping,
the complete source/proofs, canonical baseline and a new empty session. The v1
frame must not be arbitrarily cropped, rounded up, re-signed, assigned new
nonces or replayed unchanged across its already-canonical prefix. No such
replay was attempted here, and the old 23,500 unknown ACK outcomes are not
reclassified as duplicate or accepted.

The original remained device/inode `66305/3088445986`, 2GiB and mode 0600.
Its normal startup/QC-replay physical SHA256 changed from the repaired
`13ee1715089c496e943fc80debf1b7993c67da4ce00faaa6206e194d1310affc` to
`c805609d2598cfbf63f5614b40ae26d8f6b7f62f2ac25ad08073ddaff0ab1b53`;
all three protected copies retained their complete identities and hashes.
This is a successful same-data startup and canonical nonce audit, not new
transaction liveness, full WAL replay proof, a new TPS result or the 5M goal.

## Known-zero backpressure and request deadlines

The first strict run reused one five-second context across all Busy/Snapshot
retries of a frame. Known-zero backpressure could consume almost that whole
allowance before another request began. Its cancellation could then close the
connection while that request awaited its final ACK. This is a concrete failure
mechanism consistent with the retained run; the old logs cannot identify which
protocol phase produced its first unknown outcome.

Each synchronous Admit now gets a fresh five-second request context bounded by
the unchanged overall run context. After a typed zero-admission Busy/Snapshot
reply, retry waits consume the observation window rather than the next request's
I/O allowance. Dispatch stop prevents another request, while a request already
in flight may finish its final ACK. Unknown outcomes still poison the connection
and stop without replay; diagnostics now include shard, sequence, payload hash,
I/O phase and any context cancellation. This change does not resolve the old
23,500 unconfirmed admissions or authorize their replay.

Seven targeted driver/Unix transport tests passed (replay 0.069s, sharedexec
0.053s), covering fresh deadlines after Busy and Snapshot, cancelled prior
contexts, stop between known-zero retries, completion of an in-flight final ACK,
the parent deadline, and fatal lost/timeout ACKs. Three existing prefix/retry/
unknown regressions passed (0.034s and 0.023s). Vet for both packages and diff
checks passed. These were low-resource tests with GOMAXPROCS=2 and -p=1; this
change has not been built or run against the retained runtime. The separately
frozen account-recovery worker remains pinned to a81bb5a5 and is unaffected.
