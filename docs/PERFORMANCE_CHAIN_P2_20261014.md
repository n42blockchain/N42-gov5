# Performance-chain P2: crash recovery and strict throughput

Status: active. No new throughput score has been established. The Go S103
standing best remains 776,967 TPS; the historical Rust 4M run used gateway
attestation and is not a strict Ed25519 baseline.

## Whole-fleet termination exposed a separate defect

The retained P1 V=7/E=1 chain advanced to head 3762. SIGKILL of its execution
layer and all seven validators restored the durable EL head at 3721 while the
consensus vote log and QC remained near view 3764. The fleet could no longer
fetch the certified blocks: their bodies had only existed in memory. This does
not invalidate the two earlier successful normal shutdown/restart checks.

The original data and vote logs remain under
`/data/blockchain/n42-performance-chain/e1/data`; evidence is under
`/data/blockchain/wr-analysis/performance-chain-p2-20261014`. Missing legacy
bodies cannot be recreated by the new implementation.

## Repair and validation

Rust branch `perf/deferred-frontier-recovery-20261013`:

* `e52df8675`: bounded, fsynced proposal body store; ancestry replay through
  normal Engine API validation; live-upgrade snapshot; safe pruning after EL
  persistence.
* `d05a15be7`: leader and follower persist full bodies before voting; write
  failures stop the service before the execution event reaches consensus.
  Startup replays committed/locked QCs without lowering vote watermarks,
  serializes validators sharing an EL, and anchors the driver at the highest
  recovered lock.

149 service/library tests and 14 consensus-persistence integration tests passed.
The full workspace format check finds pre-existing formatting differences;
the new recovery module is formatted. Release compilation is in progress after
removing stale cached codec artifacts from another checkout. Actual SIGKILL
recovery of this repair is still pending; a process kill is not a power cut.

Recovery requires full block bodies. Compact-only voting is explicitly refused
when a needed body is absent. A live upgrade cannot reconstruct an Amsterdam
access-list body solely from a header/transaction RPC response. Unsupported or
missing required recovery data stops startup rather than inventing a result.

`scripts/performance-chain/recovery_round.py` verifies strict RPC and binary
ingest, captures an unpersisted tail, kills only exact bundle PIDs, restarts
without resetting data, and compares 64 prior successful receipts and canonical
hashes. It then submits another 32 transactions. Results and rotated node logs
are retained; every exit stops the owned fleet.

## Unattended sequence

Independent E1, E4 and E7 profiles are prepared under
`/data/blockchain/n42-performance-chain/p2-20261014`. Run recovery checks first,
then strict-signature throughput windows. Report committed transaction rates,
occupancy, successful execution, identical validator commits, generator supply,
CPU allocation and persistence cost. Change one bottleneck at a time and compare
against the same signature and durability requirements. Commit and push each
logical source change separately.

Ethereum EL+CL, weekly updates, minimal/full/archive and Pro+DATC remain the
completed maintenance track. Development effort stays on the performance chain.
