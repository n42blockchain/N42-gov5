# QS Linux session handoff — 2026-08-24

This file preserves the operational state of the long-running Linux replay,
fleet benchmark, and witness-replay session. It intentionally contains no BLS
or secp256k1 private keys.

## Scope and terminology

- **QS fleet** is the isolated, self-developed N42 chain:
  `mainnet_qmdb_staggered`, seven local HotStuff validators, QMDB state.
- **witness replay** is a separate Ethereum historical-block workload. Its
  units and concurrency are blocks/workers; N42 faucet, sender, nonce, reward,
  and txpool explanations do not apply to it.
- Do not mix these two workloads or run the witness sweep while the seven-node
  fleet benchmark is consuming the machine.

## Repository and binaries

- Repository: `/home/n42/src/n42/N42-gov5`
- Branch: `main`; at 2026-08-24 05:00 UTC local and `origin/main` were
  `c3d8059e fix(txflood): abort partial sender loads`.
- Relevant pushed commits from this session:
  - `283071f6 build: enforce 5.7.956 version consistency`
  - `468cbe22 build: auto-increment version before compilation`
  - `58b312d9 fix(qs): harden weekly benchmark workflow`
  - `b93c8d35 build: 5.7.958`
  - `65cda179 fix(replay): export target canonical head`
  - `4b9d18fd fix(hotstuff): retry state after failed canonical commit`
  - `510180fb fix(sync): accept legacy protobuf block chunks`
  - `576f5dd7 fix(txindex): synchronize segment reader lifecycle`
  - `00cc2bcc test(params): pin deployed genesis identities`
  - `91012699 docs(witness): define fail-fast replay benchmark`
  - `6537ed32 build: 5.7.959`
  - `58d49032 fix(witness): gate block-worker sweeps`
  - `b8dd1f06 fix(qs): clear persisted pool before benchmarks`
  - `c3d8059e fix(txflood): abort partial sender loads`
- `/data/blockchain/bin/n42` reports `5.7.959-6537ed32`, SHA-256
  `1c0c0dac07b7773f602ddecbe61c43f23b005f0821bd6aee902217d4eaf6ab62`.
- `/data/blockchain/bin/txflood` SHA-256
  `e6deb6849073b362d313afc37b7ee351f4b94be0d6543a07d3c80602eb4cd731`.
- `/data/blockchain/bin/witness-replay` is now a clean VCS build at
  `1ffd1c7064ebb06005a4e813ff511f96c0b93ab6`, SHA-256
  `e979b0dd6ea5e8feb2e576860262e19658fdd0416e7c120e16961806aef6b84d`.
  `b8391c24` makes a populated explicit MDBX Code datadir suppress implicit
  detection of the older codes freezer; an explicit `--codes-freezer` still
  wins.
- `make n42` and `make build` now increment the build version once before
  compilation. Never probe with `n42 version`; that starts a node. Use
  `n42 --version`.

## Chain data completed

- Old N42 mainnet source: `/data/blockchain/mainnet-source`, source head
  `13,497,579`.
- Replay target: `/data/blockchain/qs-replay-linux`, target head `13,536,950`.
  The higher target head is replay-v2 gap fill, not source corruption.
- Replayed source range: `13,204,050..13,497,579`, 293,530 blocks and
  667,334 transactions; `txFailed=0`.
- The 24 receipt mismatches are the accepted EIP-rule behavior of this replay
  design, as confirmed by the operator.
- Target checkpoint was repaired to target canonical head/hash:
  `13,536,950 / 0x9923b24baf104277f88f4dfdfa842c9c94197099d1ad1f02dcac4f60b1bb3414`.
  It now also records source progress separately as `sourceHead=13,497,579`;
  the existing `number`/`hash` pair remains the target canonical identity. The
  updated checkpoint is copied to `qs-era-linux`; both files have SHA-256
  `0775c8e1cb32af05e8c074814ffb3d63be845e9c6cddf4fe366d46c38ac8d158`.
- The replay base had a stale `network.json` claiming mainnet/JMT/APoS even
  though its actual genesis/state are QS/QMDB/HotStuff. It was backed up as
  `network.json.pre-qmdb-fix-20260824` and replaced with the verified node0
  binding. `n42 db stats` now opens it under `mainnet_qmdb_staggered` and sees
  target head `13,536,950`. Replay-v2 now validates an existing target binding,
  persists one for a new target, and fails rather than warning on post-export
  or binding errors.
- Derived fleet seed: `/data/blockchain/qs-era-linux`; 12 ancient eras,
  sealed end `12,582,912`; deep verification passed 768 sampled blocks.
- Seed txindex covers `12,582,912..13,536,950`.
- Keep `/data/blockchain/qs-replay-v5` and the `.pre-*` node generations until
  the new workflow and benchmarks have passed. Do not delete them casually.

## Current fleet state

- The seven nodes are now paused. All were stopped gracefully by the existing
  `stop-fleet.sh --no-inspect`; no `kill -9` was used. Node 0 last committed
  block `13,539,286`, then logged normal HTTP, txpool, HotStuff and P2P shutdown
  at 2026-08-24 05:04:21 UTC.
- Before the pause, seven nodes ran 5.7.959 from
  `/data/blockchain/qs-node0..6`, RPC ports `20012..20018`, TCP
  `32000..32006`, UDP `31000..31006`. They were launched by the existing
  `bench-7node.sh` with 1,000 ms pacing, `GOMAXPROCS=37` per node and
  `--txgen-max 0`; that phase accumulated the chain-defined 1 ETH/block dev
  faucet reward.
- At 04:56 UTC all seven reported height `13,538,842`, identical hash
  `0xfc904675…217f052`, zero transactions in the latest block, six peers per
  node, and no recent ValidateState/root-mismatch/panic/equivocation logs.
- Faucet balance was 102.023116562 ETH at that height and is now verified to
  rise by exactly 1 ETH/block. The standard 3,000 x 3,000 round needs about
  1,896.93 ETH, so the estimated ready height is `13,540,637` (about 30 minutes
  after the 04:56 sample).
- Root cause of the earlier falling balance was not nonce or P2P: the current
  txpool persists pending transactions inside MDBX `TxPoolJournal`. The old
  benchmark script deleted only a historical file path, before graceful stop,
  so six nodes restored 821 old txgen transactions and gossiped/mined them.
  Those never-committed pending entries were counted, then cleared with the
  existing `txpool-journal-reset` tool while all nodes were stopped. Chain data
  and committed nonces were not changed.
- `bench-run.sh` now stops first, aborts if stop is incomplete, clears each
  stopped MDBX journal with the purpose-built tool, then launches the benchmark.
  This fix is deployed in `/data/blockchain/scripts-qs` and pushed in
  `b8dd1f06`.
- Historical normal mode remains node0-only `--dev.txgen.max 31` at a two-second
  tick. It chooses random 1..31 (mean 16); that light traffic is not a TPS
  benchmark.

## Correct Windows weekly flow and Ubuntu mapping

The Windows source of truth is `docs/QS_WEEKLY_REPLAY_SYNC.md` plus
`docs/QS_TPS_BENCHMARK.md`. The `.ps1` files themselves live on the Windows
operator host, not in this repository.

1. Gracefully stop all seven nodes and verify identical committed QC/head.
2. Run incremental replay-v2 with the **stopped live custom fleet node0 as
   source**, not old mainnet and not Ethereum:
   `E:/qs-node0 -> E:/qs-replay-v4`.
3. Seal eras, emit hot layout, deep-verify, copy the checkpoint.
4. Rebuild the txindex inside the seed artifact.
5. Move old node dirs aside, seed seven clean dirs from the era layout, deploy.
6. Accept only when seven PIDs are alive, same-height hashes are unique and
   identical, heads advance, and there are no root/ValidateState failures.
7. Run the benchmark as a separate launch profile:
   `bench-run.ps1 -> bench-7node.ps1 -> txflood -> measure-tps.ps1 -> stop`.

The Ubuntu equivalents are in `scripts/qs/` and deliberately preserve that
sequence. Future weekly Linux folds must use stopped
`/data/blockchain/qs-node0` as replay source into
`/data/blockchain/qs-replay-linux`. The completed
`mainnet-source -> qs-replay-linux` operation was a one-time Linux bootstrap,
not the normal weekly cycle.

## Benchmark protocol

- Standard Linux command, with a fresh offset every round:

  ```bash
  cd /data/blockchain/scripts-qs
  set -a
  source /data/blockchain/faucet.env
  set +a
  QS_SEED=/data/blockchain/qs-era-linux \
  QS_BASE=/data/blockchain/qs-replay-linux \
  QS_UDP_BASE=31000 \
  QS_MAXCPU=37 \
    ./bench-run.sh --offset 3000000 --tag linux-v959-c37-shard --decay-sec 90
  ```

- The script stops the normal fleet, clears journals, launches the benchmark
  fleet, verifies its arguments, waits for all RPCs, decays baseFee, starts
  txflood, opens measurement windows only after flooding starts, then stops all
  nodes gracefully.
- Baseline: 480M gas, 1,000 ms pacing, pool 300k/100k,
  `N42_TXINDEX_TAIL=1`, `N42_MAX_GOSSIP_MB=8`,
  `N42_STRESS_GASLIMIT=1`, shard senders, 3,000 senders x 3,000 tx,
  10 gwei, RPC batch 100, concurrency 32.
- Windows used 5 threads/node on 32 threads. The equivalent initial Linux
  allocation is 37/node on 256 threads; tune only after a valid baseline.
- Windows best comparable first window: 22,487 TPS, 98.4% occupancy, 55/60
  full blocks. Compare win1 to win1; later windows are affected by baseFee.
- Fresh sender offset and 90-second decay are mandatory. Occupancy below about
  95% means the supply harness, not the chain, limited that result.
- Funding's contiguous nonce chain goes to node0; the existing P2P mesh
  propagates it. Do not submit the identical chain to all seven RPCs.
- Every derived sender's complete nonce chain is routed to one RPC. If any
  sender's bounded nonce probes fail, txflood now aborts instead of submitting
  a partial load with empty raw slots (`c3d8059e`).

## Completed audit validation

The replay, HotStuff, legacy sync, txindex lifecycle and deployed-genesis fixes
were committed separately and pushed. The risky temporary mainnet genesis
change was rejected: deployed old-mainnet identity remains `0x594aad…`, while
the QS fleet keeps its separate `0xa2d2ff…` genesis.

Validation completed successfully:

```bash
GOCACHE=/tmp/n42-go-cache go test -race \
  ./internal/txlookup ./internal/txindexer ./internal/sync \
  ./internal/consensus/hotstuff ./internal/replay ./internal/node ./params \
  ./cmd/txflood ./internal/txspool
git diff --check
```

The intentionally untracked documents are this temporal handoff and
`docs/QS_LINUX_OPEN_QUESTIONS_20260824.md`. The latter is the complete review
question list for the other developer; do not commit either until the operator
decides how to incorporate the answers.

## Remaining order

1. Obtain answers to `docs/QS_LINUX_OPEN_QUESTIONS_20260824.md`; do not resume
   the fleet or witness sweep before the operator reviews them.
2. Apply the agreed fixes using the existing Windows/QS toolchain as the source
   of truth, then revalidate the scripts without deleting retained generations.
3. If confirmed, resume the empty benchmark-profile fleet until the faucet has
   enough funding, then run one valid baseline with a fresh sender offset.
4. Stop the fleet gracefully before any exclusive witness hardware sweep.
5. Witness format smoke already passed 0–200,000 with 121,793 tx and
   `failed=0`; this is not a performance result. `witness/senders` indexes both
   cover 25,765,566. `/data/blockchain/witness/MANIFEST.txt` now records 488/488
   transferred files matching by size and head/tail 4 MiB MD5.
6. `/data/blockchain/code-mdbx` now contains 2,673,190 Code rows, but the
   single-block W4 gate at 24,000,022 still fails with the exact old gas
   mismatch even when the incomplete codes freezer is not opened. The old
   `5ccc9bb9` binary and an on-the-fly ecrecover run reproduce the same result.
   See the appended Linux result in `docs/QS_LINUX_ANSWERS_20260824.md` and the
   three `w4-block-24000022-*.log` files under `/data/blockchain/wr-logs`.
7. Do not run the dense gate or any witness performance sweep until the second
   W4 cause is identified. The earlier `24,980,000–24,990,000` range has no
   canonical basis; the eventual correctness gate must start in the known
   failing 24,000,000 range and must finish with `failed=0` before performance
   is counted.
