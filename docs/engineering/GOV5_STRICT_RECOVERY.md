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

The current slice adds this endpoint/client and existence metadata only.
Recovery-only fleet launch, collection of a full same-snapshot vector, and a
same-data actual recovery remain pending. No runtime feature, signature policy,
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
