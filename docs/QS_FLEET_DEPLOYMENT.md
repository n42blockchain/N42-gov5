# QS Fleet: chain sync, replay-v2, and 7-node deployment — full reference

End-to-end operational reference for the `mainnet_qmdb_staggered` 7-node
HotStuff fleet: how the live chain is synced and folded into the replay-v2
base, and how the fleet is (re)deployed on it — including transaction
simulation and the mobile-verification committee. The weekly condensed
procedure lives in `docs/QS_WEEKLY_REPLAY_SYNC.md`; this document is the
"why and every knob" companion. A Chinese translation is maintained locally
(not committed).

All host-specific paths reference the current single-host layout
(`E:\qs-node0..6`, `E:\qs-replay-v4`, scripts on `E:\`). Private keys are
never reproduced here — they live only in `E:\qs-validators.md` and
`E:\deploy-7node.ps1` on the operator host.

---

## 1. Chain identity

`params/chainspecs/mainnet_qmdb_staggered.json`:

| Field | Value | Notes |
|---|---|---|
| chainId | 94 | local performance network |
| stateScheme | `qmdb` | twig-forest / binary / Blake3 commitment |
| consensus | `hotstuff` | HotStuff-2, view-per-block |
| period / baseTimeout / maxTimeout | 3 s / 6 s / 30 s | `fastPropose: true` + `minProposeDelayMs: 200` currently drive ~1 s blocks under load; **the new-system block interval is tentatively set to 4 s** (pending a chainspec/pace change — adjust `hotstuff.period` and re-roll the fleet when finalized) |
| epochLength | 200 | validator-set epoch granularity |
| validators | 7 static | addresses + BLS pubkeys in the chainspec; privkeys in `E:\qs-validators.md` (seed-derivable, NEVER committed) |
| committeePool | 200,000 / 512 / ramp 1 M | simulated mobile-voter pool (see §6) |
| devBlockReward | 1 ETH / block | credited to `devFaucetAddress` every block — the faucet's income |
| devFaucetAddress | `0x42e9…586e` | funds `--dev.txgen` and txflood rounds |
| twoPhaseVoteGate | true | import-gated voting (vote only after local execution) |

Fork schedule mirrors ETH mainnet forks (shanghai@305000, cancun@3935000,
pectra/osaka/fusaka by time) so EVM behavior tracks the production ruleset.

## 2. Phase A — sync/stop the old (live) chain

The fleet is the only writer of its chain; "syncing the old chain" means
bringing it to a clean, converged stop so its head can be folded into the
replay base.

1. **Graceful stop, all 7 nodes.** CTRL_BREAK only (`sendbreak.exe <pid>` —
   AttachConsole + GenerateConsoleCtrlEvent, mirroring `cmd/n42/stop_windows.go`).
   Pids: first field of `E:\qs-nodeN\n42.pid` (second field is a start-time
   stamp guarding against PID reuse). A hard kill truncates the MDBX spill and
   poisons the QMDB undo layer — never.
2. **Wait until zero `n42*` processes**, then verify convergence:

   ```bash
   for i in 0 1 2 3 4 5 6; do go run ./cmd/hotstuff-inspect -datadir E:/qs-node$i/chaindata; done
   ```

   ALL SEVEN must print the identical `committedQC=<view>/<hash>`. If one lags,
   it stopped a view early — its chain data is still a strict prefix and any
   converged node (node0 by convention) serves as the replay source.
3. **Record the final head H**: last `commit-to-canonical phases … "number":H`
   line in `E:\qs-node0\log\n42.log`.

2026-08-10 reference: 7/7 `committedQC=533538/59eb15d0`, H=13,912,188.

## 3. Phase B — replay-v2 (the canonical base)

`n42 replay-v2` re-executes the source chain block-by-block into a fresh
target with compact codecs and QMDB commitment. It is both a data-hygiene
step (clean base, bounded node-dir growth) and a full re-execution audit.

### 3.1 Command (incremental, weekly)

```powershell
Start-Process C:\N42\N42-gov5\build\bin\n42-v5.7.<latest>.exe -ArgumentList `
  'replay-v2','--source','E:/qs-node0','--target','E:/qs-replay-v4', `
  '--chain','mainnet_qmdb_staggered','--tree','qmdb', `
  '--output','E:/qs-replay-v4/replay_stats-<date>.json' `
  -RedirectStandardOutput E:\qs-replay-v4\replay-inc-<date>.log `
  -RedirectStandardError E:\qs-replay-v4\replay-inc-<date>.err -WindowStyle Hidden
```

- `--source` / `--target` take the **datadir root**; the tool appends
  `/chaindata` itself (passing the chaindata path fails with an MDBX
  Accede-mode error).
- **Resume is automatic**: the target records its last replayed source block;
  the run logs `resuming from checkpoint lastSourceBlock=N startBlock=N+1`.
  `--to 0` (default) targets the current source head. Confirm the logged
  range matches Phase A's H.
- The same binary drives replay and the fleet — never a stale side-build
  (positional/unknown-flag parsers ignore flags they predate, silently).
- Detach via `Start-Process`; a background shell that gets reclaimed kills
  its children mid-write.

### 3.2 What the defaults mean (and why they stay default)

| Flag (default) | Effect |
|---|---|
| `--tree qmdb` | QMDB commitment — must match the fleet's stateScheme |
| `--fill-gaps` (on), `--gap-period 8`, `--gap-tolerance 15`, `--gap-max 10000` | inserts synthetic empty blocks into timeline holes (downtime, resets) so block time stays roughly uniform; shifts target block numbers ahead of source numbers |
| `--compact-headers/txs/receipts/logs/bodies` (on) | compact storage codecs (~4× smaller headers, 9× receipts); read path accepts both old and new encodings |
| `--virtual-td` (on) | drops all-zero PoS TD rows; ReadTd synthesizes 0 |
| `--qmdb-undo-window 64` | rolling per-block undo for recent-height proofs |
| `--qmdb-history` (off) | full-history proof layer — not needed for the fleet base |
| `--batch 100000` | blocks per MDBX commit; each batch logs its `qmdbRoot` |
| `--snapshot-at-end` (on) | final state snapshot for fast node boot |
| `--bls-reseal` (off) | only for the mobile-committee re-seal conversion experiments; NOT part of the weekly cycle |

### 3.3 Accepted deviations (synthetic-load chain)

Gap-fill shifts numbers/timestamps ⇒ baseFee sequence and `BLOCKHASH` window
differ from the live chain, so a small fraction of the (synthetic) traffic
drifts on re-execution: 2026-08-10 measured txFailed 105,080 / 128.8 M
(0.082 %, `evm_error` nonce-cascades) and receiptMismatch 36,853 (the v3
full-replay baseline was already 2,952). The bar for THIS network is internal
consistency: per-batch `qmdbRoot` lines, clean exit, checkpoint == H. A
real-asset chain would demand `--fill-gaps=false` and a byte-exact receipt
gate instead.

Throughput reference: empty/light-era ~6,000 blk/s; stress-era ~200-tx blocks
~150-225 blk/s (2026-08-10: 708,139 blocks / 128.7 M txs in 52m46s).

## 4. Phase C — deploy the fleet

`E:\deploy-7node.ps1` (operator host, not in repo) does seed + keys + launch:

```powershell
foreach ($i in 0..6) { Move-Item E:\qs-node$i E:\qs-node$i-pre<date> }   # trigger re-seed
pwsh -File E:\deploy-7node.ps1 -Data E:\qs-replay-v4 -Bin C:\N42\N42-gov5\build\bin\n42-v5.7.<latest>.exe
```

### 4.1 Seeding semantics

- A node dir is seeded (full `Copy-Item` of `-Data`) **only if it does not
  exist** — moving old dirs aside is what makes re-seed happen; leaving them
  in place makes the script a pure restart.
- After seed it writes per-node key material: `keystore\bls_<addr>.key`
  (validator BLS privkey from `E:\qs-validators.md`) and `network-keys`
  (fixed libp2p identity so peer IDs are stable across reseeds).
- The replay base carries **no HotStuff journal** ⇒ the fleet starts
  consensus fresh from the replayed head (clean-slate equivalent of
  `hotstuff-reset`; no separate reset step).
- `-Bin` must be passed explicitly (script default is a pinned old binary).

### 4.2 Per-node launch arguments (from `BuildArgs`)

```
--chain mainnet_qmdb_staggered --profile n42
--data.dir E:\qs-node<i>
--engine.miner --engine.etherbase <validator-addr-i>
--p2p.no-discovery --p2p.local-ip 127.0.0.1 --p2p.host-ip 127.0.0.1
--p2p.tcp-port 32000+i --p2p.udp-port 33000+i --p2p.min-sync-peers 0
--p2p.peer /ip4/127.0.0.1/tcp/32000+j/p2p/<peerID-j>   (for every j ≠ i)
--http --http.addr 127.0.0.1 --http.port 20012+i --http.api eth,web3,net
--mobileverify.http 127.0.0.1:21012+i
--pprof --pprof.port 6090+i
```

- Static full mesh (no discovery); loopback-only advertising avoids the
  dual-dial (127.0.0.1 + public IP) libp2p churn seen when auto-advertise
  was on.
- Ports deliberately sit BELOW the Windows ephemeral range (49152+): the
  original 62000/63000 ports were stolen by outbound sockets, costing a
  node on two consecutive starts.

### 4.3 Port matrix

| Port | Purpose |
|---|---|
| 32000-32006 / 33000-33006 | p2p TCP / UDP (node 0..6) |
| 20012-20018 | JSON-RPC HTTP (eth,web3,net) |
| 21012-21018 | mobileverify HTTP (phone-facing verification surface) |
| 6090-6096 | pprof |

## 5. Transaction simulation (`--dev.txgen`)

- Enabled on **node 0 only** (`-TxGenMax 31` default ⇒ up to 31 simulated
  txs/block). One faucet key; parallel senders would race the faucet nonce.
- The txgen key is a dev key (in `E:\deploy-7node.ps1`; derivation note:
  keccak("n42-dev-faucet-v1")). Its address `devFaucetAddress` is credited
  `devBlockReward` = 1 ETH **every block** by consensus — the faucet
  regenerates ~86,400 ETH/day, which bounds sustainable spend for stress
  rounds.
- Heavier load uses the external `txflood` tool against 20012 (`-rpcbatch`
  real batch submission, `-skip-funding` to reuse funded senders,
  `-sender-offset/-shard-senders` for multi-round separation). Fleet-proven
  ceiling on this host: single txflood instance, conc 64 — two instances
  exhaust ephemeral ports and punch nonce holes.
- Economics discipline: pick per-tx value from faucet balance; do not reuse
  depleted sender sets across rounds (a bankrupt faucet manifests as a
  demote spiral that looks like node failure).
- Dial down later by lowering `--dev.txgen.max`, or hand traffic over to
  real devices via `consensus_registerCommitteeValidator`.

## 6. Mobile verification (committee pool + mobileverify)

Two related mechanisms, both wired by `--profile n42`:

- **Simulated mobile-voter committee (chainspec `committeePool`)** —
  in-consensus. 200,000-key BLS pool derived from the published `seedHex`,
  512-member committee per block (ETH sync-committee reference size), active
  set ramping from one committee to full pool over 1 M blocks. Every block
  carries an aggregated BLS committee QC (~114 B on-wire per block); the
  pool is simulated in-process — no devices needed. This is what
  "Mobile-voting committee is simulated automatically" means in the deploy
  banner. To convert a chain's history to committee-sealed form offline, see
  `replay-v2 --bls-reseal` (experimental, not the weekly path).
- **mobileverify (three-layer reporter attestation)** — outside consensus
  (IDC-inside / phones-outside layering). The `--mobileverify.http`
  endpoint (21012+i) is the phone-facing surface for device injection and
  real handsets. Reporter authentication is layered: L1 BLS possession
  proof, L2C f+1 quorum threshold, L2B commit-reveal (anti-grinding). Real
  devices register through this surface and can graduate into committee
  validation via `consensus_registerCommitteeValidator`.

## 7. Acceptance & rollback

Acceptance (first minutes after launch):

- 7/7 processes up; `eth_blockNumber` on 20012..20018 identical at the same
  instant and advancing (~0.8-1.5 blk/s with txgen at 31 under the current
  fastPropose pace; once the tentative 4 s interval lands, expect ~0.25 blk/s
  and adjust the 12 s probe expectation to +3 blocks).
- `hotstuff: commit phases` rolling in node logs; view counter ≈ blocks
  produced (view-per-block, no idle views); zero `view timeout`.
- Zero `ValidateState` / root-mismatch warnings — the QMDB three-state
  self-heal would flag a bad base immediately.

Steady-state health probe (any time): identical `eth_blockNumber` across
nodes, +N blocks over a 12 s window, `grep -c "view timeout"` still 0.

Rollback: stop the fleet, move `E:\qs-nodeN-pre<date>` back into place,
relaunch with the same deploy command (dirs exist ⇒ no re-seed). Keep exactly
one previous generation; delete it only after the NEXT weekly cycle passes
acceptance.

## 8. Disk lifecycle

| Artifact | Policy |
|---|---|
| `E:\qs-replay-v4` | living canonical base; grows with each weekly increment (26 → 47 GB on 2026-08-10) |
| `E:\qs-replay-v3` | frozen 07-27 from-genesis ancestor; rebuild reference — do not extend |
| `E:\qs-nodeN` | working fleet; ~2× base between reseeds |
| `E:\qs-nodeN-pre<date>` | single rollback generation; rotate weekly |
| `replay-inc-<date>.log/.err`, `replay_stats-<date>.json` | keep per-run (dated names — `--output` overwrites silently) |
