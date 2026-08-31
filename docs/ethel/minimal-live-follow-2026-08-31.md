# eth-el minimal — catch-up to tip and 12 s live follow (2026-08-31)

Binary: `cmd/eth-el`, `-tags n42el,nosqlite,noboltdb`, built from main at the
bodyc-branch merge (`cf96a6dc`) plus the two devp2p fixes below.
Datadir: `/data/blockchain/ethel-test/ethel-min-25864981` — snapshot-direct
(H0 = 25,864,981 RecSplit accounts/storage + headerc/codes freezer, fresh warm
MDBX overlay). No CL: the head is driven by `--eldevp2p.enabled` alone.

```
eth-el --datadir <min datadir> --network mainnet \
       --bootstrap.mode snapshot --snapshot.mode minimal \
       --eldevp2p.enabled --eldevp2p.listen :30313 \
       --engine.enabled=false --storage.mapsize.gb 256
```

## Result — PASS

| | |
|---|---|
| start head | 25,870,750 (3,062 behind) |
| caught up | 25,873,914 at 07:57:26 UTC, `lag=0 tip=25873914` |
| catch-up wall | ~8 min 30 s from first import, 4–8 blocks/s |
| live window | 07:57:24 → 07:58:26 UTC, head 25,873,896 → 25,873,917 |
| live cadence | one block per 12–15 s, `lag=0` after every import |
| state/receipt root mismatches | 0 |
| `level=error` lines | 0 |

Live-window imports: 25,873,915 (07:57:42) → 25,873,916 (07:57:57) →
25,873,917 (07:58:13), each followed by `caught up ... lag=0`. That is the
post-merge EL follower path working end to end: no peer pushes NewBlock, so
every one of those came from `probeForNewTip` asking a peer for head+1.

## What had to be fixed to get there

Both are peer-acquisition bugs. Before them the node held 2 confirmed mainnet
peers out of 167 connections in 100 s and imported nothing in 5 minutes.

### 1. The CLI default silently overrode the tuned peer cap

`eldevp2p.DefaultConfig()` sets `MaxPeers: 200`, deliberately large because
PulseChain and the other mainnet forks share our bootnodes AND our forkid
(`07c9462e`), so most dial candidates are junk that only reveals itself at the
Status handshake. But `cmd/eth-el`'s `--eldevp2p.max-peers` flag defaulted to
50 and the wiring applies any value `> 0`, so every run capped at 50 and the
pool filled with foreign-network peers before a real one landed.

Flag default is now 0 = "use the built-in 200". Measured on this host:
50 peers → 0 blocks in 5 min; 300 peers → first batch within 30 s.

### 2. Dialing had no chain-specific node source

The dialer drew only from the discv4/discv5 tables, which are shared with
every chain that forked Ethereum and kept the bootnodes: 66 of 167 connections
were rejected on networkID (57, 369, 1315, 10200, 560048, …), and only 2
completed the handshake.

`internal/devp2p` now feeds the dialer the public EIP-1459 DNS node list for
the chain (`all.mainnet.ethdisco.net`, resolved from the genesis hash via
`params.KnownDNSNetwork`) alongside the DHT tables.

The mix has to be built by us, not by `p2p.Server`: `setupDiscovery` installs
its own discv4/discv5 feeds **only when no protocol supplies
`DialCandidates`**. A first cut that handed it a DNS-only iterator therefore
switched the DHT off — dial volume collapsed from ~170 to 5 connections a
minute, though the handshake success rate rose from 1.2 % to 31 %. The
committed version puts both in one `enode.FairMix` (DNS added before start,
the table iterators attached right after), which gave 64 connections and a
stable 2–3 serving peers in the run above.

## Not covered here

`full` and `archive` were started on the same binary and stopped before their
catch-up finished (the box was hosting a CPU stress test). Both reached the
`caught up head=… lag=0 tip=0` idle state on the OLD binary, which is the
peer-starvation signature above, not a mode bug — they need a rerun on this
binary before either can be called a pass.
