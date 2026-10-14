# QS handover, 2026-10-14 (rounds S98-S105, P4b-P12)

Goal: 1M TPS on a 7-node HotStuff fleet on one box (EPYC 9B45, 128c/256t), 163k-tx blocks.
Model: TPS ~ 256 threads / (7 x CPU-us per tx per node). 1M needs <= 36 us/tx/node AND a cycle <= 163 ms per 163k block.

## Round table (best B leg, window 1 / window 2)
| Round | Build | Content | Result |
|---|---|---|---|
| S98 | r131 | baseline | 274k, stalls 34-41% (root cause: P5a) |
| S99 | r133 | P4b+P3d+P5a | 355,883 |
| S100 | r135 | +P6+P7 | 380-383k; win2 collapsed; recovery 64% of node CPU |
| S101 | r137 | +P8a+P8b frame attestation | 546,050 (recovery 130 -> 48 ms) |
| S102 | r138 | +P9 | 627-655k both windows; 32.5-34.7 us/tx |
| S103 | r139 | +P10, interval 125 ms | **776,967 / 755,233 (standing best)**; B2 692,750 / 679,167; cycle = import chain (aheadRun 120 + root 58) |
| S104 | r141 | +P11+P12 | **REGRESSION** 643,850 / 600,383 at 0.25-0.27 s; win2 < win1 every leg; not analysed |
| S105 | (other session) | unknown build | 682-717k win1 / 641-652k win2; not analysed (log wr-logs/r35zzzce.log) |

## Landed code (branch qs/replan, worktree /data/blockchain/gov5-work/wt-r27, never pushed)
Each item has a "Px result" block in docs/QS_MULTI_MILLION_DESIGN.md and rows in docs/QS_QUEUE.md.
- P4b: credits into root without objects; ordered async lag writer (N42_LAG_WRITE_ASYNC). P3d: direct transfer path (N42_TRANSFER_DIRECT).
- P5a: duplicate-commit TxLookup rows no longer written in one long MDBX txn.
- P6: no pre-proposal sleep, separate vote journal, FIFO commit loop. P7: writer bypass on push path, per-block sender jobs, history fold batches.
- P8a/P8b: atomic recovery claim; committee frame attestation (N42_FRAME_ATTEST=1, committee 3).
- P9: in-memory RecSplit for the txindex sealer, hints skipped on frame blocks, hand-rolled tx decode.
- P10: credits detached through Prague system calls (bench aheadRun 252 -> 203, root 65 -> 54).
- P11: write prepared off the lock (N42_LAG_WRITE_PREPARE), frames in a separate MDBX store (N42_FRAME_DB=1); bench write 79 -> 25 ms. Builds: n42-r140-p11only.
- P12: snapshot stays published for late children; leader gate on in-flight write (N42_LAG_GATE_APPLIED); setupAff/setupSys. Build n42-r141 (HEAD 68ea747a).

## Open: S104 regression, resume plan
1. Sonnet analyst, S104 vs S103: which phase grew. P11 suspects: handWait, prepWait, frames, framesWait. P12 suspects: aheadLate, aheadTakeLaunch, lag-wait, setupAff/setupSys. Also root, us/tx, and why win2 < win1. Sources: node logs qs-node0/6 (copy first) and wr-pprof/r35zzzcd-*.
2. Single-knob A/B legs: r140-p11only vs r141 with N42_LAG_GATE_APPLIED=0, and/or N42_FRAME_DB unset.
3. Fix the offender, then new round scripts cloned from 35zzzcd with id 35zzzcf (35zzzce is taken).

## Remaining cycle items toward 1M (from S102/S103 analysis)
Ahead run (exec + collect + recov) and root (grows 58 -> 77 with distinct accounts, major faults); version-list presizing, fold 1.9%, keccak 3.2%, receipts. Per-tx CPU is already ~34 us, so the cycle length, not CPU, is the gap.

## Operating rules
Share the box: build `nice -n 19 go build -p 4 -tags nosqlite,noboltdb`; test `GOMAXPROCS=4 nice -n 19 go test -p 2`; never `make n42`/`make build`. Never push qs/replan. Never touch qs-node* beyond reading while a round runs. Chain scripts wait at claim/quiet/memory gates (90 GB gate).
Harness: scripts-qs/measure-tps.sh (boundaries-first), bench-run.sh, QS_MAXCPU=36, 15M corpus, RPC poll on node6 (20018).
Flaky at base: ingest TestFrameIngestBusyAtCap/TestFramePullBypassesCap, txindexer TestTailStaysBoundedUnderSteadyCommits, recsplit TestOpenIndexFromBytes under -race.

## Disk (2026-10-14)
/data was 97%; cleaned ~1.4 TB (qs-node* and pre-copies, replay node3 copies, era copies). Now 82%, 1.4 TB free. qs-source (24 GB) and qs-seedidx kept; next round reseeds qs-node*. Frames store grows ~37 GB per node per round. /tmp erigon-lfp-buf test leak recurs (see memory project-tmp-erigon-buf-leak).
