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

- **D1. One rule set for all history, plus a genesis alloc repair** (revised 2026-11-28).
  - Every block executes under profile-1 / Amsterdam rules from block 0. No per-block exception to the source rules exists.
  - Only the genesis alloc changes, so that most historic transactions still succeed. `--p1-source-tx-rules` stays a diagnostic only.
  - Root cause of the failures before the repair: a 21,000-gas transfer into a fresh account cannot pay the EIP-8037 new-account state gas (183,600). It halts with status 0 and moves no value. The recipient then cannot pay its own transactions (dropped, insufficient funds), and every later transaction of that sender is dropped on its nonce.
  - The repair adds two kinds of genesis credit:
    - **precreate**: one wei to each fresh recipient of such a transfer. A non-empty account (EIP-161) is charged no new-account state gas.
    - **topup**: the exact shortfall (`want - have`) to each sender that still cannot pay gas * price + value.
  - What the repair cannot fix stays failed:
    - Transactions whose gas limit cannot cover the new storage, contract or access gas are included with status 0. The nonce advances, so no cascade follows.
    - Transactions the protocol refuses (intrinsic gas or calldata floor above the gas limit) are dropped and counted. The protocol cannot include them as failed.

### Repair procedure

1. `replay-v2 --p1-repair-out delta.json` replays on the current genesis. When a transaction would fail for a fixable reason, the pass injects the credit into the state right before it, records it (address, wei, kinds, first block) and continues.
   - The delta is written after every committed batch, and a resumed pass continues it.
   - The delta is public chain-derived data.
2. `replay-p1-genesis ... --extra-alloc delta.json` adds the credits to the genesis alloc. The flag is repeatable, and the deltas add up.
3. A clean replay on the new genesis reproduces the repaired outcomes. Balances are additive, so a genesis credit leaves each account with the balance the injection gave it at the point of use.
   - A transaction whose logic reads a balance earlier can still diverge. A second repair pass on the new genesis catches it, and the iteration stops when a pass records nothing new.

Trial, blocks 1..1,400,000 (191,911 transactions), 2026-11-28:

| run | ok | status 0 | dropped |
|---|---|---|---|
| original genesis | 400 | 2,950 (2,949 new-account halts) | 188,561 (12,880 root, 175,681 nonce cascade) |
| repair pass 1 | 191,888 | 23 | 0 |
| clean replay, genesis + delta 1 | 191,888 | 23 | 0 |
| repair pass 2 on that genesis | 191,888 (empty delta) | 23 | 0 |

- Delta 1 has 13,478 addresses: 13,344 precreated, and 135 topped up with 2,393.75 ETH in total.
- The 23 residual status-0 transactions ran out of gas under the new gas schedule. The source had them succeed.
- `replay-v2-p1-audit` passed on the clean target: blocks 1-1,400,592.

The stats JSON key `p1Diag` holds the outcome diagnostics:

- `tx_ok`, `tx_status0`, `tx_dropped`;
- status against the source receipt: `status_match`, `status_src_ok_now_failed`, `status_src_failed_now_ok`, `dropped_src_ok`;
- `root_drop_<class>` (the first drop of a sender) and `cascade_drop_<class>` (later drops, out of nonce step);
- `status0_newaccount_state_gas` and the drops of its recipients, `root_drop_*_after_newaccount_halt`;
- `repair_precreate`, `repair_topup`, `repair_topup_unresolved`.

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

# genesis alloc repair: pass, then rebuild the genesis with the delta
n42 replay-v2 ... --p1-genesis <secrets>/p1-genesis-secret.json --p1-repair-out repair/delta1.json
replay-p1-genesis <same flags as above> --extra-alloc repair/delta1.json --out <secrets>/p1-genesis-r1.json

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
