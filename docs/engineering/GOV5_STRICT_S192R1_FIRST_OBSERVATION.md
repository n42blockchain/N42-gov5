# First strict signed-corpus observation: retained failure

On 2026-10-16 at 11:05–11:06 UTC, the first schema-2 strict EIP155 owner-queue
runtime actually ran. Root `/data/blockchain/gov5-performance-5m/runtime/s192r1`
was provisioned from the exact `s192a` genesis and corresponding seven BLS
identities, with new P2P identities/ports 29000–29006, 100ms per-validator
runtime pacing, 163,000-transaction block cap and 32MiB block encoding cap.
Original `q1016a`, `f1015a` and source corpus were retained unchanged.

All source and binaries were clean `757c2ded8aa98656bfea4c0be31f23186f2e1148`.
Node/owner/provision reused the passed startup-recovery artifacts; only replay
was newly built by Make. Its SHA-256 is
`e7184a9c4103f4f8b5fdab510c418ff6f856dde0d54563c5b6bc237530881366`.
The other three hashes are in [the recovery record](GOV5_OWNERQUEUE_Q1016A_RECOVERY.md).
Runner bytes, hashes, commands, thread budgets and affinity are recorded under
`/data/blockchain/gov5-work/validation/20261016-gov5-strict-s192r1-757c2ded/`.
The build/provision turn and actual run held separate BOX claims. Owner/replay
GOMAXPROCS were 36 each, metadata two each; these are thread budgets, not
exclusive physical core assignments. Offered 6.5M/s was a target, not a result.

## Actual result

The controller stopped with failure after the replay process exited one.
All eight initializations and eight runtime processes exited zero. Claim and
owned-child cleanup completed. The retained attempt is
`runtime/s192r1/attempts/strict-q1/`, with lifecycle and initial observations;
full node logs are in each `vN/log/n42.log`. Replay evidence is
`runtime/s192r1/strict-attempts/strict-q1/result.json`.

All 64 complete source shard hashes were verified before dispatch in
29.434324901 seconds. No shard exhausted. The four-second warm-up completed;
WindowStart was sampled at elapsed 4.001266497 seconds, canonical height 252
and count zero. Samples continued through elapsed 7.602250528 seconds, but no
WindowEnd was obtained. Therefore `passed=false`, `actual_window_seconds=0`,
and **no valid twenty-second TPS was produced**.

The reported error was:

```
strict ingress outcome unknown; stop and reconcile, do not replay: read unix @->/data/blockchain/gov5-performance-5m/runtime/s192r1/e/execution-rpc/ingress.sock: use of closed network connection
```

| Evidence at the last checker observation | Value |
| --- | ---: |
| Source transactions read | 2,032,000 |
| Final-ACK unique admissions | 2,008,500 |
| Owner NewlyAdmitted | 2,032,000 |
| Owner CryptoVerified attempts | 2,154,500 |
| Busy retries | 20,520 |
| Snapshot retries | 10,148 |
| Owner pending transactions | 1,984,500 |
| Owner retained encoded bytes | 208,809,772 |
| Owner reserved transactions / bytes | 0 / 0 |
| Seven observed canonical transactions | 47,500 |

Owner and all seven last observations agreed on height 253/hash
`0x51fcef29d8b3db4a24918b5f1be2d4cba02ae88db9e1830c11ecec58d7c05688`.
Owner Reconciling was true. The 23,500 difference between owner admission and
client final ACKs is an unknown-outcome set, not permission to replay. The
volatile queue was not empty and does not persist across owner restart.

These are observation boundaries, **not the final stopped database state**.
The owner log records canonicalization of height 254 during cleanup at
11:06:50 UTC; metadata logs already received Decide for view 256/block hash
`0x62bbcf53ba1c0e26f93dc2a1e2c3a0bbe8ca44abdcc342899fb893069ae3258b`.
Recovery must inspect/reconcile this late commit before trusting any nonce or
canonical counter. No nonce-zero retry or replacement runtime was launched.

## Diagnosis and next gate

The queue reached its two-million-transaction limit and accumulated Busy and
canonical/applied snapshot waits. The existing client uses a five-second work
context per frame; timeout closes the socket and conservatively reports an
unknown outcome when a request may be outstanding. Current evidence does not
identify the first affected shard/frame or prove whether its request was in
header, body or final-ACK phase. Do not relabel an ambiguous request as a known
zero retry merely to keep the run alive.

Large blocks did execute: logs contain 47,500- and 163,000-transaction execution
phases, with core EVM/root work in tens of milliseconds, while full proposal,
import and QC progress took seconds. Views 254 and 255 timed out before view
256 re-proposed an applied child. These logs justify investigating selection,
encoding/decoding, full-body fan-out and import gating; they do not establish a
CPU-profile attribution or authorize bypassing signatures, execution evidence,
body availability or five-vote commit certificates. Build and Import still
execute twice.

Next recover the same databases without feeding transactions, reconcile owner
and seven canonical evidence including the late height-254 commit, then obtain
canonical-bound sender nonce evidence before deciding how source prefixes can
be resumed. Preserve all unknown frames and distinguish volatile admission
from canonical durability. A later complete twenty-second observation remains
required; general process fault gates and matched capacity promotion also
remain outstanding.

The initial default disk planning check passed at about 807.8GiB free against
796.421875GiB required. After the run, free space was about 791.75GiB, below that
same default gate. Do not weaken the estimate, delete this evidence or remove
foreign data to force another capacity run. A read-only recovery turn has a
different, explicitly bounded workload and is not a capacity measurement.
