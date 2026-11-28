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

- **D1. One rule set for all history; the repair is by balances only, delivered by prelude transfers from one funder** (revised 2026-11-28).
  - Every block executes under profile-1 / Amsterdam rules from block 0. No per-block exception to the source rules exists, no contract is predeployed, and no transaction is re-signed (historic gas limits stay). `--p1-source-tx-rules` stays a diagnostic only.
  - Root cause of the failures before the repair: a 21,000-gas transfer into a fresh account cannot pay the EIP-8037 new-account state gas (183,600). It halts with status 0 and moves no value. The recipient then cannot pay its own transactions (dropped, insufficient funds), and every later transaction of that sender is dropped on its nonce.
  - The repair credits two kinds of balance before history starts:
    - **precreate**: one wei to each fresh recipient of such a transfer. A non-empty account (EIP-161) is charged no new-account state gas.
    - **topup**: the exact shortfall (`want - have`) to each sender that still cannot pay gas * price + value.
  - The genesis stays lean. It gets ONE extra account, the prelude funder (secret key `prelude-funder.key` in the secrets directory), holding the summed credits plus a gas budget. **Prelude blocks** right after the genesis, before the first replayed source block, carry ordinary signed transfers from the funder that credit every delta address with exactly its summed delta. The resulting balances equal those a genesis-alloc credit would give.
  - What the repair cannot fix stays failed:
    - Transactions whose gas limit cannot cover the new storage, contract or access gas are included with status 0. The nonce advances, so no cascade follows.
    - Transactions the protocol refuses (intrinsic gas or calldata floor above the gas limit) are dropped and counted. The protocol cannot include them as failed.

### Repair procedure

1. `replay-v2 --p1-repair-out delta.json` replays on the current genesis. When a transaction would fail for a fixable reason, the pass injects the credit into the state right before it, records it (address, wei, kinds, first block) and continues.
   - The delta is written after every committed batch, and a resumed pass continues it.
   - The delta is public chain-derived data.
2. `replay-p1-genesis ... --prelude-delta delta.json --prelude-funder-key <secrets>/prelude-funder.key` adds the funder to the genesis alloc with balance = sum of the deltas + n * gasPerTx * maxFee * 1.1. `--prelude-delta` is repeatable, and the deltas add up. Without these flags the genesis is unchanged. (`--extra-alloc` still writes the credits into the alloc directly; `--new-prelude-funder-key <file>` generates a funder key, mode 0600, and prints only its address.)
3. `replay-v2 ... --p1-prelude delta.json --p1-prelude-funder-key <secrets>/prelude-funder.key` (same files as step 2) writes the prelude on a fresh target, then replays the source from block 1:
   - one transfer per delta address, sorted by address, nonces 0..n-1, type 2, chain id of the target, one-wei tip (a zero tip deletes the empty coinbase after every transfer, EIP-161, and each deletion drops the replay read cache), fee cap twice the genesis base fee, gas limit 21,000 + 183,600 (`P1PreludeGasPerTx`);
   - packed up to the gas target (gasLimit / 2 by summed gas limits, 73 transfers per 30M block), so the derived base fee never rises during the prelude;
   - sealed like every replayed block (P1Sealer: header, CommitQC, committee evidence; each one slot after its parent). The source blocks keep the slot rule, so the first ones may shift by a few slots;
   - every transfer must succeed, or the run stops;
   - the target records the prelude (block and transfer count, digest of funder, gas and transfer list). A resumed run does not re-emit it and refuses other prelude flags. The stats `p1Diag` holds `prelude_blocks` and `prelude_txs`; prelude transfers are not counted as source transactions.
4. A clean replay with the prelude reproduces the repaired outcomes. A transaction whose logic reads a balance earlier can still diverge: a second repair pass on top (step 1 with the prelude flags) records what is left, and the iteration stops when a pass records nothing new. `replay-v2-p1-audit` audits prelude blocks like any other block.

Trial, blocks 1..1,400,000 (191,911 transactions), 2026-11-28:

| run | ok | status 0 | dropped |
|---|---|---|---|
| original genesis | 400 | 2,950 (2,949 new-account halts) | 188,561 (12,880 root, 175,681 nonce cascade) |
| repair pass 1 | 191,888 | 23 | 0 |
| clean replay, genesis alloc + delta 1 | 191,888 | 23 | 0 |
| repair pass 2 on that genesis | 191,888 (empty delta) | 23 | 0 |
| clean replay, funder genesis + prelude | 191,888 | 23 | 0 |
| repair pass 2 on the prelude | 191,888 (empty delta) | 23 | 0 |

- Delta 1 has 13,478 addresses: 13,344 precreated, and 135 topped up with 2,393.75 ETH in total.
- The prelude for delta 1 is 185 blocks with 13,478 transfers (73 per block); the target head moves from 1,400,592 to 1,400,777, and the outcome counters equal the genesis-alloc variant exactly. The funder genesis is 283,920 bytes against 1,355,721 for the genesis-alloc variant (2,841 alloc accounts instead of 16,228).
- A resumed run neither re-emits the prelude nor accepts a run without the prelude flags.
- The 23 residual status-0 transactions ran out of gas under the new gas schedule. The source had them succeed.
- `replay-v2-p1-audit` passed on the genesis-alloc target (blocks 1-1,400,592) and on the prelude target (blocks 1-1,400,777, 205,389 transactions = 191,911 + 13,478 prelude transfers). On the prelude target the maximum slot gap is 680,424 s: the jump from the genesis time to source block 1's time, which on the genesis-alloc target falls between the genesis and block 1, now falls after the last prelude block.

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
  - The prelude funder key (`prelude-funder.key`, 64 hex, mode 0600) is secret the same way. Its address is public; the genesis holds only the address.
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

# repair: pass, then rebuild the genesis with the funder, then replay with the prelude
n42 replay-v2 ... --p1-genesis <secrets>/p1-genesis-secret.json --p1-repair-out repair/delta1.json
replay-p1-genesis <same flags as above> --prelude-delta repair/delta1.json \
  --prelude-funder-key <secrets>/prelude-funder.key --out <secrets>/p1-genesis-r1.json
n42 replay-v2 ... --p1-genesis <secrets>/p1-genesis-r1.json \
  --p1-prelude repair/delta1.json --p1-prelude-funder-key <secrets>/prelude-funder.key

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
