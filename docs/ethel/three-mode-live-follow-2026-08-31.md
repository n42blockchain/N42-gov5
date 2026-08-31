# eth-el minimal / full / archive — catch-up to tip and 12 s live follow (2026-08-31)

Binary: `cmd/eth-el`, `-tags n42el,nosqlite,noboltdb`, built from main at the
bodyc-branch merge (`cf96a6dc`) plus the two devp2p fixes below (`9add9d47`).
Datadirs under `/data/blockchain/ethel-test/`, all anchored at H0 = 25,864,981:

| mode | state at H0 | freezer | flags beyond the common set |
|---|---|---|---|
| minimal | RecSplit snapshot + warm MDBX overlay | headerc, codes | `--bootstrap.mode snapshot --snapshot.mode minimal` |
| full | same | + bodyc, txindex | `--bootstrap.mode snapshot --snapshot.mode full` |
| archive | hashed-canonical MDBX (171 GB: HashedAccount/HashedStorage + TrieAccount/TrieStorage) | headerc | `--bootstrap.enabled=false --bootstrap.mode none --hashed-canonical` |

Common: `--network mainnet --eldevp2p.enabled --engine.enabled=false`.
There is no CL in this test — `eldevp2p` alone drives the head, and disabling
the Engine API is what lets the node run without a JWT secret.

## Result — all three PASS

| | minimal | full | archive |
|---|---|---|---|
| start head | 25,870,750 | 25,870,958 | 25,871,162 |
| blocks to tip | 3,164 | 3,174 | 3,031 |
| catch-up wall | 8 m 30 s | 6 m 20 s | 15 m 54 s |
| catch-up rate | ~6 blk/s | ~8 blk/s | ~3 blk/s |
| tip reached | 25,873,914 @ 07:57:26 | 25,874,165 @ 08:47:54 | 25,874,193 @ 09:00:42 |
| live window | 07:57:24–07:58:26 | 08:48:15–08:49:17 | 09:00:55–09:01:57 |
| head over the window | 25,873,896 → 25,873,917 | 25,874,166 → 25,874,171 (+5) | 25,874,229 → 25,874,235 (+6) |
| `lag` after every import | 0 | 0 | 0 |
| state/receipt root mismatches | 0 | 0 | 0 |
| `level=error` lines | 0 | 0 | 0 |

minimal's window opened while it was still 18 blocks short, so its +21 covers
the last of catch-up and then the live cadence; full and archive were already
at tip when theirs opened and show the cadence alone — 5 and 6 blocks in 62 s,
i.e. one block per ~12 s, which is the slot time.

Every one of those live blocks came from `probeForNewTip` asking a peer for
head+1: post-merge mainnet peers do not push NewBlock/NewBlockHashes (block
gossip moved to the CL), and a peer's head in our table is frozen at handshake.
That probe is the whole live-follow mechanism for a pure-EL follower, and it
works.

archive is ~2.5× slower to catch up than full, which is expected: it commits
through the hashed-canonical incremental Merkle stage on every sub-batch.
Its per-batch `tExec` reached 10 m 30 s for 2,048 blocks.

## What had to be fixed to get there

Both are peer-acquisition bugs. Before them a minimal node held 2 confirmed
mainnet peers out of 167 connections in 100 s and imported nothing in 5 minutes.

### 1. The CLI default silently overrode the tuned peer cap

`eldevp2p.DefaultConfig()` sets `MaxPeers: 200`, deliberately large because
PulseChain and the other mainnet forks share our bootnodes AND our forkid
(`07c9462e`), so a junk candidate only reveals itself at the Status handshake.
But `cmd/eth-el`'s `--eldevp2p.max-peers` flag defaulted to 50 and the wiring
applies any value `> 0`, so every run capped at 50 and the pool filled with
foreign-network peers before a real one landed.

The flag default is now 0 = "use the built-in 200". Measured on this host:
50 peers → 0 blocks in 5 min; 200–300 peers → first batch within 30 s.

### 2. Dialing had no chain-specific node source

The dialer drew only from the discv4/discv5 tables, shared with every chain
that forked Ethereum and kept the bootnodes: 66 of 167 connections were
rejected on networkID (57, 369, 1315, 10200, 560048, …) and 2 completed the
handshake — 1.2 %.

`internal/devp2p` now also feeds the dialer the public EIP-1459 DNS node list
for the chain (`all.mainnet.ethdisco.net`, resolved from the genesis hash via
`params.KnownDNSNetwork`).

The mix has to be built by us, not by `p2p.Server`: `setupDiscovery` installs
its own discv4/discv5 feeds **only when no protocol supplies
`DialCandidates`**. A first cut that handed it a DNS-only iterator therefore
switched the DHT off — dial volume collapsed from ~170 to 5 connections a
minute, though the handshake success rate rose to 31 %. The committed version
puts both in one `enode.FairMix` (DNS added before start, the table iterators
attached right after).

Handshake yield over the two runs above, both sources mixed: full 48 confirmed
mainnet peers out of 712 connections, archive 53 out of 800 — ~6.7 %, five times
the tables-only rate, with 3–7 peers held concurrently throughout.

## Reproducing

Launchers, a status poller, and the logs quoted here are beside the datadirs:
`run-min.sh`, `run-full.sh`, `run-archive.sh`, `status.sh`,
`logs-20260831/{min-mix,full-v2,archive-v2}.log`. Head probe:
`cmd/headcheck -dir <datadir>/chaindata`.
