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
A changed batch tuple discards the entire vector. The separate recovery-only fleet launcher is implemented; a same-data actual
recovery with account-vector collection remains pending. No runtime feature, signature policy,
consensus certificate or default native/ETH behavior is relaxed.

Validation: three `TestCanonicalAccounts*` tests passed under nice19/ionice idle,
GOMAXPROCS=2 and -p=1 (0.061s). Real QMDB plus Unix RPC tests distinguish a funded
nonce-zero account from an absent address, refuse executed-but-uncommitted and
four-vote state, and return nonce three only after the actual five-vote canonical
commit. Old tuple queries then fail. Other tests cover wrong identity/session/
manifest/tuple, duplicate/oversized address lists, malformed/unknown/trailing
JSON, closed owner, altered responses and maximum-width 1000-account responses.

`go vet ./internal/sharedexec ./internal` and `git diff --check` passed. Full Make
build, targeted race and updated runtime binaries await the next hardware turn.

The recovery command has a 45-second bound and records its failed-run hash,
nonce vector/hash, canonical nonce sum, owner/validator observations, and zero
dispatched transactions separately from any throughput result. It rejects absent,
contract, underfunded and out-of-source-range senders. New queue epoch and nonce
state do not grant replay permission for the previous unknown ACKs.
Three `TestRecovery*` tests passed (0.015s) with GOMAXPROCS=2/-p=1, covering a
1001-account two-batch snapshot, whole-vector rejection on a changed second
batch, typed snapshot conflict, invalid sender state, late commit acceptance,
immutable same-block count, nonregression and zero-activity session binding.
Replay vet passed. Actual same-data account collection is not yet run.

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
