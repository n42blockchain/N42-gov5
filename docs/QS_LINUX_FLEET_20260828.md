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
  (Rust node replacing one Go validator, same BLS key + PeerId) — evidence in its devlog-141.

## Also observed

- `txgen` on node0 logged `insufficient funds` while the chain was wedged (its funding transactions could not
  confirm); it recovered by itself once blocks flowed. The faucet key in `faucet.env` matches
  `devFaucetAddress` (10,603 ETH).
- `/tmp` is a 69 GB tmpfs at 77% (other sessions' `n42-go-*` caches and fleet data); Go builds need
  `GOTMPDIR=/home/n42/.gotmp` or the linker fails with "disk quota exceeded". Keep fleet data on `/data`.
