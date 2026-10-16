# Rebuilding the 7-node era seed from a replay (2026-10-14/16)

Lessons from rebuilding `/data/blockchain/qs-era-linux` out of a fresh offline replay
(`qs-replay-hist-20261014`, head 14,105,498, source head 14,066,100). Rounds 35zzzcf..35zzzch ran on it.

## Procedure that worked
1. Seal the ancient eras with an `n42-ancient-seal` built from the CURRENT code. The old
   `bin/n42-ancient-seal` (Aug 23) fails with `bucket BadHeaderNumber not in N42TableCfg`.
2. Emit the hot part, then build the tx index with `scripts-qs/build-seed-txindex.sh --bin <current n42>`
   (the Aug `bin/n42` is too old). Install the result as `qs-era-linux/txindex` and set the
   watermark to the sealed end (13,631,488 here) with `txindex-watermark`.
3. Reseed all 7 nodes with `seed-7node-nolaunch.sh` (needs `qs-era-linux`).

## Traps
- **No applied marker.** The replay may leave `qmdbAppliedHead` in DatabaseInfo (the Mac replay wrote
  `(0, genesis hash)`). `seededResult` (internal/deferred_execution.go) only treats the data as a
  seeded replay when NO marker exists; with a marker the leader's `LagReady` waits forever for the
  head's executed result and no block is ever produced. Delete the marker from the seed.
- **Activation times.** The run script hardcodes `N42_DEFERRED_EXECUTION_TIME` and
  `N42_TXROOT_BLAKE3_TIME`. They must be the seed head's header time + 1 (here 1792018942). A stale
  value gives `QMDB tree/marker discontinuity before execution ... matches no header` on every node.
- **Do not delete `qs-era-linux`** without a rebuild path; rebuilding takes hours and needs the
  85 GB replay copy.
- The txindex builder node may ignore SIGINT; stop it explicitly.
- **Leg restarts are flaky.** After a leg the nodes are stopped with SIGTERM; a committed but not yet
  executed block can leave `committed block not executed locally`, and the next leg then refuses to
  produce (`PERSISTED CONSENSUS STATE HAS DIVERGED`). Running `hotstuff-reset` before each leg did not
  cure it: on n42-r143 the B1 leg produced 29 empty blocks and then timed out views. The warm-up leg
  uses the B configuration, so a one-leg round is a valid measurement.

## Results on this seed (B configuration, 125 ms, 163k-tx blocks)
| round | build | win1 / win2 TPS |
|---|---|---|
| 35zzzcg warm-up | r139 (S103 build) | 785,117 / 776,967 |
| 35zzzcg B1 / B2 | r139 | 741,650 / 709,050 ; 722,633 / 714,483 |
| 35zzzcf B1 / B2 | r141 + `N42_LAG_GATE_APPLIED=0` | 747,083 / 714,483 ; 749,800 / 709,050 |
| 35zzzch warm-up | r143 (r141 + sender counters, lag gate on) | 752,517 / 695,467 |

Sender-source counters (b8daf280), node0, 745 blocks / 120 M txs of the 35zzzch warm-up leg:
`recoverTxsTxs` 5.97 M (about 5% of txs), `framesOwned` 38 k, `pickedUnreadyAfterGrace` 9.7 k,
`framesNotStored` 0, `localFillRecovered` 0. Block-time recovery is therefore small on this seed;
the S104 regression (643k) is not reproduced with the lag gate on. Read the counters from the
`blockimport phases` log line (cumulative; subtract first from last).
