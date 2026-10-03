# What still grows from win1 to win2 under lag 4 (35zzzaz B2, n42-r108)

Source: /data/blockchain/wr-logs/r35zzzaz-keep/node*/n42.log(.gz), r35zzzaz-mem.log, r35zzzaz-vm.log, r35zzzaz-memstats.log.
Windows: win1 01:00:39-01:00:59, win2 01:01:39-01:02:00 (both inside B2, lag=4, same leg as the S74 ruling).

## Measurements, win1 -> win2

| metric | win1 | win2 | delta (abs) | ratio |
|---|---|---|---|---|
| fillTx (fleet-pooled 'miner: build phases', p50) | 143.9 ms | 457.0 ms | **+313.1 ms** | 3.18x |
| fillTx (p90) | 546.7 ms | 675.2 ms | +128.5 ms | 1.24x |
| blockimport total (node1, mean) | 320.1 ms | 415.1 ms | +95.0 ms | 1.30x |
| blockimport proc | 218.6 ms | 300.1 ms | +81.4 ms | 1.37x |
| blockimport exec | 139.6 ms | 189.1 ms | +49.5 ms | 1.35x |
| blockimport root | 60.2 ms | 83.3 ms | +23.1 ms | 1.38x |
| blockimport write | 87.0 ms | 93.4 ms | +6.4 ms | 1.07x |
| blockimport collect | 2.4 ms | 3.6 ms | +1.3 ms | 1.53x |
| blockimport setup | 1.5 ms | 0.6 ms | -0.9 ms | 0.38x |
| qmdb root phases: apply | 16.79 ms | 20.12 ms | +3.33 ms | 1.20x |
| qmdb root phases: fold | ~0.0003 ms | ~0.0003 ms | ~0 | flat |
| qmdb root phases: coldReads/block (mean) | 27,832 | 11,304 | -16,528 | 0.41x (falls) |
| GC/min (node0/node1, whole-leg rate) | ~7.6-7.7/min | ~7.6-7.7/min | ~0 | flat |
| heap median (node0/node1) | 1.36-1.40 GiB | 1.32-1.36 GiB | slightly down | flat/down |
| Cached (page cache, box-wide) | ~62.1 GB -> 51.3 GB (falling through win1) | 47.0 GB -> 45.9 GB (falling, flatter) | -11-16 GB over win1, -1-1.1 GB over win2 | falling throughout, steepest in win1 |
| refaultFileD (vmstat, box-wide) | mean 56.8k/10s lower in B2 than B1 overall | (see leg-level numbers; window-level too sparse to split reliably) | n/a | n/a |

Block tx counts were not separately logged per block in this run (gasceil/fillgas fixed per leg, not per block); the growth above is read against a constant target block size, so it is not explained by bigger blocks late in the window.

## Ranking (largest absolute ms growth first)

1. **fillTx, leader side, +313 ms (p50)** -- by far the largest grower, ~3x everything else combined.
2. blockimport total, follower side, +95 ms.
3. blockimport proc, +81 ms.
4. blockimport exec, +49 ms.
5. blockimport root (state-root hookup inside import), +23 ms.
6. qmdb apply, +3.3 ms (now small under lag 4; this used to be bigger before S74).
7. blockimport write, +6.4 ms; collect, +1.3 ms -- both minor.
8. coldReads *fell* win1->win2 (lag 4 is still doing its job inside the window, not degrading).

## What is it

- **Not GC/heap pressure**: NumGC rate is flat across the whole leg (~7.6/min both windows) and heap median is flat-to-slightly-down win1->win2; nowhere near the 11 GiB GOMEMLIMIT ceiling (max ~9.2 GiB over the whole leg, not concentrated in win2).
- **Not the qmdb/MDBX apply-fold path directly**: apply only grows +3.3 ms and fold is near zero -- S66's split of apply from fold already removed that as a lever, and S74's lag keeps coldReads falling rather than rising inside the window.
- **Partly page-cache/MDBX pressure, one level up**: box-wide `Cached` drops hard through win1 (62.1 GB -> 51.3 GB, -11 GB in 20 s) as the flood generators' `AnonPages` balloons (40 GB -> 76 GB over the same stretch), then keeps falling but more slowly through win2 (47.0 GB -> 45.9 GB). That squeeze lands on the follower's `exec`/`proc`/`root` stages, which is consistent with them growing 35-38% each -- those stages touch MDBX-backed state and pay more when fewer pages are resident.
- **Mostly the leader-side `fillTx` growth itself**, which dwarfs everything else and sits in `miner: build phases`, i.e. mempool scan + transaction selection on the leader, not the follower import path. This is the best candidate for the **unmeasured vote-round-trip residual from docs/QS_CYCLE_ANATOMY.md**: `fillTx` as logged bundles pool-scan compute with however long the builder waited to be handed the next slot, and that wait lengthens as the window's backlog (and gossip/vote traffic under load) builds up over the first ~75 s of sustained flood pressure. The follower-side blockimport growth (proc/exec/root, +23 to +81 ms) is a secondary, smaller compute effect riding on the same page-cache squeeze, not the dominant term.
- Supply does not look like the proximate cause here: the leg is a steady-state flood (constant gasceil/fillgas, same generators throughout), and coldReads/apply are actually improving (falling) across the same stretch, ruling out a growing-working-set story for the biggest line item.

## Proposed next lever

Instrument the actual leader-side vote round trip inside `fillTx` (or alongside it) to split pool-scan compute from propose-wait, per docs/QS_CYCLE_ANATOMY.md's "unmeasured residual" -- today `fillTx`'s +313 ms win1->win2 growth is the single biggest number in the whole cycle and nobody can yet say how much of it is CPU vs. waiting on votes/gossip under rising load. Expected win2 gain is unknown until the split exists (that's the point), but if even half of the +313 ms turns out to be an avoidable propose-wait rather than real pool-scan work, a targeted fix (e.g. widening the propose window or decoupling pool-scan from the vote wait) could plausibly recover several percent of B2's win2 throughput, closing a meaningful chunk of the B-mean gap between win1 (142.1k) and win2 (133.5k) in this same round. This is Opus-level: it requires adding new timing instrumentation to the hotstuff proposal/vote path and the miner's build loop, correlating it against the existing `fillTx`/`hotstuff view timing` lines, and reasoning about where in the critical path the time actually goes -- not a config knob.
