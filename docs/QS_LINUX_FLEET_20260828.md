# QS Linux fleet — 2026-08-28 restart, the receipt-root regression, and Rust members

Operational record for this box. Complements `QS_LINUX_SESSION_HANDOFF_20260824.md`
and `/data/blockchain/HANDOFF.md`.

## What runs now

| Thing | Where | Binary | Ports |
|---|---|---|---|
| qs fleet, 7 HotStuff validators, chain 94 `mainnet_qmdb_staggered`, continuing the existing data (head 13,549,347 at restart) | `/data/blockchain/qs-node0..6` | `/data/blockchain/bin/n42` = main + `fix/qmdb-receipt-root` (backup `n42.pre-fix-receiptroot-20260828` = 5.7.960) | RPC 20012–20018, TCP 32000–32006, UDP **31000–31006** (below the ephemeral range, no sysctl needed), pprof 6090–6096 |
| n42-rs mixed devnet: one QMDB reth EL, three Rust validators, one gov5 member | `/data/blockchain/mixed-fleet/n42-rs` | gov5 member = the same fixed build (`wt-main/build/bin/n42`) | EL 18545/18551, validators 19000–19002, gov5 28545/30393 |
| N42-26 mixed fleet | `/data/blockchain/mixed-fleet/n42-26` | see `N42-26/docs/devlog-141-*.md` | 28501–28506, 29545/29551, 30301–30306, 19780, 9443 |

Start/stop the qs fleet with the scratch wrappers or directly:

```bash
export QS_ROOT=/data/blockchain QS_VALIDATORS=$HOME/qs-validators.md \
       QS_SEED=/data/blockchain/qs-era-linux QS_BASE=/data/blockchain/qs-replay-linux QS_UDP_BASE=31000
set -a; source /data/blockchain/faucet.env; set +a; ulimit -n 65536
cd /data/blockchain/scripts-qs && ./deploy-7node.sh --bin /data/blockchain/bin/n42 --txgen-max 31   # dirs exist → no reseed
./stop-fleet.sh --no-inspect                                                                       # SIGINT, never kill -9
```

Acceptance used: all seven `eth_blockNumber` equal and advancing (~0.5 blk/s under txgen), one hash per
height, no `BAD BLOCK` / `ValidateState` in the logs since the restart.

## The wedge after restart, and the reset

The fleet had been stopped cleanly on 08-24. On restart every leader in turn logged
`consensus parent is not the applied head; re-applying before producing … haveParentHeader:false`
and the view timed out: a proposal (`6916c8…`, view 12434) had been prepare-voted (lockedQC) but never
imported anywhere except its leader, so nobody could extend it. Remedy from the handoff, applied to all
seven after a graceful stop: `hotstuff-reset -datadir <node>/chaindata -apply -force -backup <file>`
(journals backed up as `qs-nodeN/hotstuff-backup-*.json`). That alone was not enough — see below.

## The regression: `invalid receipt root hash` on every sealed block

After the reset the first proposal (13,549,348, 63 txs, node3) was rejected by the other six:
`BAD BLOCK … invalid receipt root hash (remote 007aee… local 648f40…)`. Same binary on both sides.
The n42-rs devnet showed the minimal form: the gov5 member rejected the Rust leader's *empty* block 1
with remote = empty trie root, local = `keccak("")`.

Cause: `cfaa28ec fix(eth-el): allow legacy RLP receipt imports` (08-04, codex branch, merged to main
08-27) made `ValidateState` compute `block.EthereumReceiptRoot` whenever the state scheme is QMDB or
ethereum-mpt, while `NewBlockFromReceipt` — every N42 producer — still seals the native keccak-concat
root. 5.7.960 predates it and produces on the same data (A/B on this box: 13,549,408 → 13,549,423 in 30 s).
The witness-replay gate could not see it (eth-el path, receipts verified against Ethereum headers).

Fix (`fix/qmdb-receipt-root`, `325d88ef`, merged): `ChainConfig.EthereumReceiptEncoding()` — true only
for ethereum-mpt chains and HotStuff chains with `EthELCompat`; the validator derives whatever root the
producer seals. Regression test seals typed-tx blocks with `NewBlockFromReceipt` on QMDB and JMT chains.
Verified: fleet advancing with identical hashes and txgen traffic (12 txs/block at 13,549,584);
n42-rs devnet gov5 member sealing (15 blocks) with the same head as reth at 180, zero errors after the fix.

Lesson: an eth-el import change keyed on the *state scheme* reaches every N42 QMDB chain; the qs fleet
(or the n42-rs `devnet-fleet.sh mixed --gov5`, 3 minutes) is the gate for node-side changes, not the
witness replay.

## Rust members and chain 94

- **n42-rs `h2_observer`** attaches to the qs fleet: status handshake OK, `genesis=0xa2d2ff…`, `[fork OK]`.
  It waits for finality on `/n42/h2/4/ssz_snappy`, which chain 94 does not publish (`interopV4` is not set
  in `mainnet_qmdb_staggered.json`), so it sees no Decide proofs. Enabling `interopV4` is a chainspec
  change for the whole fleet, not something to flip on a running chain from this session.
- **Validation of chain 94 blocks by a Rust EL is blocked** (n42-rs `docs/N42_26_PORT.md`, "what is still
  missing"): `MobileRegistryRoot` (21st header field under `mobileAnchor`, active since 2026-07-18) has no
  reth/Engine-API spelling; EOF (`eofTime` active) has no revm implementation. Until those land, Rust nodes
  can follow chain 94's consensus but cannot execute or vote on its blocks.
- **Consensus participation therefore runs on the checked-in devnet genesis** (`interopV4: true`,
  `epochLength 0`, rewards + committee pool in production shape): n42-rs `scripts/devnet-fleet.sh mixed
  120 --gov5` with `N42_GOV5=<gov5 checkout whose build/bin/n42 is the fixed build>` and
  `N42_FLEET_DIR=/data/blockchain/mixed-fleet/n42-rs`; N42-26 via `scripts/gov5-interop-qualification.sh`
  (Rust node replacing one Go validator, same BLS key + PeerId). Result on this box (N42-26
  `docs/devlog-141-linux-mixed-fleet-20260829.md`, commit `56f43d0` on `perf/erigon-borrow-20260828`):
  the macOS runtime's key material is not reproducible, so an equivalent 6-validator static chain
  (chainId 1143) was generated with the same script (constants made overridable); five Go nodes on the
  fixed build plus the Rust node as validator 5 (gov6's slot, its BLS key and PeerId). Six RPC heads
  identical over 182 s (98→301, max lag 0, hash/stateRoot/receiptsRoot equal per sample), Rust
  `last_voted_view` advancing (111→624), Rust leading every 6th view (17 blocks byte-identical on all
  endpoints, commit latency 24–32 ms), and during a gov5 restart the chain kept committing views
  403–411 with 4 Go + Rust = exactly quorum, so every QC carried the Rust vote; gov5 then pulled the
  missed blocks from the Rust node over `bodies_by_range`. Runtime under
  `/data/blockchain/mixed-fleet/n42-26/runtime` (`source runtime/env.sh && scripts/gov5-interop-qualification.sh stop`).

## Also observed

- `txgen` on node0 logged `insufficient funds` while the chain was wedged (its funding transactions could not
  confirm); it recovered by itself once blocks flowed. The faucet key in `faucet.env` matches
  `devFaucetAddress` (10,603 ETH).
- `/tmp` is a 69 GB tmpfs at 77% (other sessions' `n42-go-*` caches and fleet data); Go builds need
  `GOTMPDIR=/home/n42/.gotmp` or the linker fails with "disk quota exceeded". Keep fleet data on `/data`.

## 2026-08-29: both Rust clients are validators of chain 94

The "blocked" list above was worked through on the Rust branches (`perf/erigon-borrow-20260828` in each repo):

| | n42-rs (slot 6, replaces Go node6) | N42-26 (slot 5, replaces Go node5) |
|---|---|---|
| Header | 23-field gov5 header (`0x80` placeholders for nil optionals + `MobileRegistryRoot`, present in every block, value 0) carried through the Engine API (`ExecutionPayloadV1.mobileRegistryRoot`), blocks sealed with the gov5 hash; pinned on real block 13,560,300 | `Gov5NativeHeader` raw-encoding registry, round-trips real block 13,560,375 to `0x0e37dae9…` |
| Rewards / committee | rewards as withdrawals (already there); committee evidence checked | rewards parsed from the wire, keccak-concat root checked, credited as EIP-4895 withdrawals (13,540,000 reproduced); committee evidence only written (EIP-4788), not rebuilt |
| Signatures | `Gov5Legacy` signing profile (`interopV4` is off on chain 94) | same |
| State | leaf-form QMDB export v2 (gov5 `n42-qmdb-export --leaf-form`, `lib/qmdb/portable_v2.go`, 19 s / 2.35 GB / 30,933 twigs) rebuilt in Rust and checked against the header root; `n42-init-snapshot init` 7m57s, 5.0 GB datadir; forest in memory (EL 9.2 GB RSS) with a delta log instead of a 2.3 GB snapshot per block | reth initialised from `n42-reth-state-dump` (6.12M accounts, 17 s); **stateRoot trusted, not recomputed** (`N42_GOV5_STATE_ROOT_TRUST=1`) — the v1 slot log cannot be exported from a pruned fleet node and the Rust tree cannot yet hold chain 94 |
| Consensus | votes: 121 of the last 126 committed QCs carry slot 6 while Go node6 is stopped; **leads**: 25 of 151 blocks (coinbase 0x1ccd…, up to 150 txs), identical on every node | votes: 106 execution-validated votes in 10 min, 82 of the last 100 QCs carry bit 5; does not lead by design (trusted state root → `with_leader_disabled`, its view times out like an absent validator). A stall at 06:27 was an H2 rebroadcast storm (forwarded Timeouts echoing with gov5's rotor, ~700 msg/s, inbound streams at capacity, block pushes blocked); fixed by 750 ms byte-identical dedup and no Timeout forwarding under the gov5 profile (`85b9499`), verified lag 0 and 24/34 QCs with bit 5 afterwards |
| Found on the way | a same-block storage slot written back to its original value is re-written by gov5 (QMDB appends) but dropped by revm's bundle → state root mismatch at 13,561,251, fixed by recording per-tx writes | observer identity gets RST after identify (unexplained) |
| Runtime | `/data/blockchain/mixed-fleet/n42-rs-qs` (`start-el.sh`, `start-validator.sh`, `qc-evidence.py`); doc `n42-rs/docs/chain94-validator-20260829.md` | `/data/blockchain/mixed-fleet/n42-26-qs` (`run-node.sh`, `stop-node.sh`, `tools/qc-bitmaps.py`); doc `N42-26/docs/devlog-142-chain94-participant-20260829.md` |

Committee now: Go 0–4 + N42-26 (5) + n42-rs (6). Returning a slot to Go: stop the Rust holder first
(never two holders of one key), then `roll-one-node.sh --node N --bin /data/blockchain/bin/n42 --txgen-max 0`
with the qs env exported. Not covered on either side: EOF (no revm implementation), Prague/7702 details,
and for N42-26 the state root.
