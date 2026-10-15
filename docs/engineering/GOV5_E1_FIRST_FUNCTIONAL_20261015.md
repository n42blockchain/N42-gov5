# First Go E1 process functional result

On 2026-10-15 at 07:31:19 UTC, the isolated Go performance chain completed its
first real seven-validator plus one-executor signed-transaction functional run.
This is a low-load correctness milestone, not a TPS measurement or the 5M goal.

The original runtime `/data/blockchain/gov5-performance-5m/runtime/f1015a` was
resumed with `--attempt root-rules`, without initialization, reset or a changed
genesis. The two preceding failed attempts and their data remain retained. All
seven initial canonical counters were zero. The signed checker observed the
same height-one block and exactly eight committed/executed transactions on all
seven validators. After the bounded 30-second observation, all seven reported:

| Field | Value |
| --- | --- |
| Canonical height | 98 |
| Canonical hash | `0x36877c9324ec9219a2594fdabb99fd2654554da387bdbe5b4129023eaadfe898` |
| Committed/executed transactions | 8 |
| Queued transactions | 0 |
| Genesis hash | `0xa7e6dcb17ebf6137ac9eb8768f1677a43ccc54a1e90e06d3f79087517f2e1aca` |

The unique transaction count is eight. Seven replicas reporting eight does not
make 56 transactions. Subsequent empty blocks did not increase this count.

The n42 source is the clean baseline repair commit
`fb1671a4852d62671cfea186d90d19a018048a19`; the main-branch cherry-pick is
`ee83d5bf771452c4522d3cbbe65ebcc128426eda`. Its restricted race checks passed:
`internal` 1.090s and `internal/node` 2.915s. The restricted `make n42` completed
successfully. The owner and signed checker retain their original fcdb22d2
executables. No uncommitted Ed codec or import-reference draft entered the run.

| Executable | SHA256 |
| --- | --- |
| n42 | `959ff8aae6e33a91c49ae3fb0379eba221ec05dae79be617e733e9483d76025f` |
| n42-execution-service | `9fde6480a3e278e002a9f4ce1e6448b3f12c03a83475a0995970b3a653cc563e` |
| gov5-perf-provision | `46b396508f596bae0be263c8058606e6f5d31f6451892973ac30836aceffaac8` |

The actual runner is pinned to `7774408d` and verified byte-for-byte against
its committed fleet/preflight modules by the `61bb466a` watcher. Both build and
functional stages independently acquired the BOX quiet/dual-claim protocol.
The owned processes shut down and the claim was released after the run; no
foreign process or claim was modified.

Retained evidence is under
`/data/blockchain/gov5-performance-5m/runtime/f1015a/attempts/root-rules/`:
`functional-result.json`, `initial-status.json`, `smoke-result.json`,
`signed-smoke.log`, `owner.log`, seven process logs, and snapshots of the seven
runtime logs with SHA256 in `runtime-log-snapshot.json`. The watcher status,
source/runner proofs, build logs and binary are under
`/data/blockchain/gov5-work/validation/20261015-e1-fcdb22d2/root-rules-fb1671a4/`.
No keys or populated chain data are included in this source commit.

The owner initialized the public HotStuff verifier with seven validators and a
five-vote quorum, and enabled the existing Block-STM path. Its canonical log
reached height 98. Validators use separate metadata stores and local BLS keys;
the standalone owner starts no miner/voting service and loads no validator
signing key. Its data directory contains only `LOCK`, `chaindata`,
`execution-rpc` and `network.json`. Existing root/result and commit-QC checks
remain enforced. The result files do not separately export raw state roots,
receipts, execution-hook totals or a complete offline database audit; those
must not be inferred from the counter alone.

E1 remains an accepted trusted execution source. Seven votes do not constitute
seven independent EVM verifications. This baseline builds and imports separately
and therefore still executes leader payloads twice. The own-build reuse option
was off. No Ed runtime, frame transport, deferred execution or import-reference
optimization was used.

Next recovery checks must begin from the nonzero retained state. The existing
zero-only signed checker cannot prove restart safety and must not resend its
nonce-zero batch. First add a read-only seven-node convergence check against
archived nonzero evidence, then exercise normal restart, two validators offline,
three validators offline, and owner disconnection under separate bounded leases.
New transaction batches additionally require an explicit trustworthy nonce
source and unique payloads; the current private API has no nonce endpoint.

## Normal restart result

The same data and executable SHAs passed a normal restart in the separate
`attempts/restart-normal` directory using committed Python runner `5e3f507f`.
All seven initial observations were height 98/count 8. After ten seconds all
seven reported height 133, hash
`0xbd5b55f9689753a2671f5f1f89f2adc46c94fd5d3ccaa169fdaeb8c273933af4`,
count 8 and queued transactions 0. Final height 133 exceeds the maximum initial
height 98, demonstrating new empty-block commits after restart. No transaction
was resubmitted, and the count did not multiply or reset.

`functional-result.json`, `restart-check.json` and `process-lifecycle.json`
retain the observations and eight independent process start/stop records; all
eight exited with status 0. The BOX claim was released. Runner source/SHA proof
and launch log are under
`/data/blockchain/gov5-work/validation/20261015-e1-fcdb22d2/restart-normal-5e3f507f/`.
The Python checker/runner/claim suites passed 29 tests in 0.024s. This proves
bounded normal restart and empty-block progress, not complete WAL replay,
higher-head ancestry auditing, new-transaction liveness, fault tolerance or TPS.
