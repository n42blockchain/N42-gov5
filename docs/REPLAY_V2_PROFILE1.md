# Replay v2 into a profile-1 chain

`n42 replay-v2 --p1-genesis` replays the old N42 main chain (chain 94) into a
new chain that follows the mixed production standard, profile 1, from block 1.
Every replayed block is a profile-1 block. A live profile-1 validator set can
continue the chain from its head.

## What each block carries

- **Header** (`internal/replay/profile1.go`, `P1Sealer`):
  - view = number; coinbase = leader of the view.
  - Timestamp on the genesis 8 s slot grid: the slot a leader would stamp at the source block's time, never before the parent's slot + 1.
  - gasLimit = `hotstuff.gasLimit`; EIP-1559 base fee from the parent header.
  - Deferred fields carry the parent's executed result (depth 1, `requestsHash` included).
  - Zero constants and blob fields; element 22 absent; `slotNumber` = number; registry root always present.
- **extraData**: the parent's CommitQC, signed by every genesis validator in the H2-v4 commit domain, plus the leader's BLS seal.
- **Committee evidence**: 512 of the `committeePool` (200,000 keys, ramp 1M), byte-identical to what the live committee pool builds. The next header's `parentBeaconRoot` links it.
- **Execution** is that of the live `StateProcessor`: EIP-4788, EIP-2935, the transactions, EIP-7002/7251 requests, then `Finalize` (dev block reward).

## Owner decisions (2026-11-26)

- **D1. One rule set for all history.** Every block executes under profile-1 / Amsterdam rules from block 0, and no source-rule exception exists.
  - Historic transactions that fail under those rules are accepted as failed (status 0) or dropped.
  - Example: 21,000-gas transfers to fresh accounts run out of gas under the new-account state gas.
  - `--p1-source-tx-rules` exists only as a diagnostic that measures the difference. It stays off for a real replay.
- **D2. Base-fee price changes are accepted.** The base fee re-derived from the parent header decays to 7 wei. No transaction falls below it, and about 1.5k dynamic-fee transactions pay a different effective price.
- **D3. Historic rewards become a genesis pre-fund.** The source chain's body rewards (historic APoS rewards: 666 blocks, 2,274 entries, 104 addresses, about 1,372.7 ETH) cannot be credited per block without a history-specific rule.
  - `replay-p1-genesis --source` sums them per address into the genesis alloc instead.
  - `--prefund-out` writes the per-address table. The table is public chain data.
- **D4/D5. Secret keys.** The validator BLS keys and the committee-pool seed are secret and local only. The genesis embeds the seed (`committeePool.seedHex`), so a genesis built with it is secret too.
  - `replay-p1-genesis --pool-seed-file --validator-secrets-file` reads them from files, never prints them, and writes the genesis mode 0600.
  - Never commit, log or copy them into shared directories. A replay target built from them is not to be shared either.

## Commands

```bash
# genesis (secret inputs, secret output)
replay-p1-genesis --std internal/testdata/profile1/standard_genesis.json \
  --pool-seed-file <secrets>/committee-pool-seed.hex \
  --validator-secrets-file <secrets>/validator-bls-secrets.json \
  --source <chain-94 datadir copy> --source-to <head> \
  --prefund-out reward-prefund.json --out <secrets>/p1-genesis-secret.json

# replay (QMDB; the target is bound as chain "private", profile n42)
n42 replay-v2 --source <src> --target <dst> --tree qmdb --chain mainnet_qmdb_staggered \
  --p1-genesis <secrets>/p1-genesis-secret.json \
  --p1-validator-keys <secrets>/validator-bls-secrets.json --from 1 --to <head>

# follower audit of every block
n42 replay-v2-p1-audit --target <dst> --p1-genesis <genesis> --workers 16
```

The audit checks every block for:

- header round-trips in the RLP, proto and compact codecs;
- hotstuff `VerifyHeader` (profile-1 rules, CommitQC, seal, committee-evidence link) and the Round-1 header gate;
- the transactions and rewards roots;
- the committee evidence, verified against the pool public keys and compared byte for byte with the live pool's output.

## Live continuation

Each batch writes the consensus state a validator resumes from: view = head + 1, and locked QC = committed QC = the head's CommitQC. It also writes the head's executed result.

To continue, start the genesis validators on copies of the target with `--chain private --profile n42` and their BLS keys in the keystore. They continue at the next 8 s slot, with committee evidence and QCs. `replay-v2-p1-audit --from <head+1>` then checks the new blocks.

Continuing requires the hotstuff fix that counts the recovered committed head as imported. Without it, a validator set restarted together from its committed head never votes.
