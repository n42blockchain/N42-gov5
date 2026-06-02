# Minimal / Mobile Client — data formats, direct execution, and RPC capability by data mode

Status: design notes (2026-06-02). Complements
[`idc-stateless-node-architecture.md`](idc-stateless-node-architecture.md)
(producer + minimal/full/archive modes) and
[`stateless-verification.md`](stateless-verification.md) (three trust layers).
This doc nails down: the two per-block state-data formats, the forward/backward
changeset model, mobile direct-execution at the tip, and — the core — **which
JSON-RPC methods each data-availability mode can serve, and which it cannot**.

Guiding principle: **an RPC is serviceable iff the data it reads is recorded.**
A node can only answer queries over data it actually keeps. So the RPC surface is
a direct function of which artifacts a given mode downloads/retains.

---

## 1. Two per-block state-data formats

A block's "state input" can be shipped in two distinct shapes:

### (A) Witness — address-less ordered state-READ stream (layer ②)
- What the EVM *reads* while executing the block, in execution order,
  length-prefixed. **No addresses, no keys, no proof.** (`internal/ethel`
  `WitnessStateReader`; freezer `TableBlockWitness`.)
- ~7–8 KB compressed/block at tip.
- A replayer re-runs the same EVM and dequeues values in lockstep. It is a
  **re-execution read-log, NOT self-verifying** — a wrong stream is caught only
  downstream by the receiptRoot (②) / stateRoot (③) check.
- Needs **codes** (bytecode by keccak) to run contract calls.

### (B) Proof-carrying pre-state + root commitment (layer ③ inputs)
- The pre-state values of the keys the block touches, **with a Merkle proof that
  anchors to block N-1's stateRoot** (committed in `header[N-1].Root`), plus the
  block's changeset. Self-verifying.
- This is what lets a verifier recompute `header[N].Root` from
  (proof + changeset) WITHOUT the full trie — the `stateless.BlockProof` anchor.

### The changeset (CS) — the EVM's output, both directions
- Executing a block (from either format, + codes) yields the **changeset**:
  per touched key, `(oldValue, newValue)`.
- **newValue → forward** (advance state without re-running the EVM).
- **oldValue → backward** (unwind/reorg without re-running the EVM).
- V2 forward changesets (`acctcs`/`storcs`) are how the producer rebuilds state
  cheaply; once a block's CS exists, **no node ever has to re-execute that block**
  to move its state forward or backward.

### Root verification cadence
- **Per block (②):** replay the witness → reproduce `header[N].receiptsRoot` +
  gasUsed (cheap, every block).
- **Every K=10000 blocks (③):** the producer emits a **"tries subset"** (the
  pre-state multiproof of that window's touched keys). A client applies the
  window's changeset to the subset, recomputes the state root, and compares it to
  `header.stateRoot`. One strong state-root check per window instead of per block.
  (Cadence is variable: coarse K=10000 historical, fine K=1000 recent.)

---

## 2. Mobile / direct execution at the tip (no catch-up)

A phone does **not** sync from genesis. It:

1. Trusts a recent **checkpoint** header (out-of-band or from CL finality, §6).
2. Pulls the **latest-height** artifacts from an IDC producer: recent headers
   (① chain), per-block witness (②), codes on demand, and the K-anchor tries
   subset (③).
3. Executes/verifies **forward from the checkpoint** — it never replays history.
   State for queries comes from (a) a downloaded compact snapshot + locally
   recorded hot changesets, or (b) on-demand per-account proofs.

So "running on a phone" = verify-and-follow the tip with KB-scale per-block data,
plus whatever state representation the chosen mode keeps. The RPC it can offer is
bounded by that representation — §4.

---

## 3. Data-availability modes

| Mode | Downloads / retains | State it can answer about |
|---|---|---|
| **M0 witness-direct** (mobile minimal) | rolling ~1 wk of {header, body, witness} + K-anchors; codes on demand; **derives + keeps the rolling changeset (account/storage newValues) + replay receipts** | **any key CHANGED in the rolling window** (newValue in the kept changeset) + window receipts/logs; verifies ①②③ |

### M0 daily/weekly footprint (measured, D:/N42-eth1177 freezer, zstd, near tip)
12 s blocks ⇒ 7200 blocks/day. Per block (compressed): witness ~7.8 KB,
changeset acctcs ~5.7 KB + storcs ~10.9 KB (chain-average; recent heavy blocks
~1.5–2.5×), anchor ~1.3 KB amortized.

| | per block | per day | rolling 1 week |
|---|---|---|---|
| witness (download) | ~7.8 KB | **~56 MB** | ~0.4 GB |
| changeset output (acct+storage newValues) | ~16–40 KB | **~120–250 MB** | ~1–2 GB |
| K-anchor (③) | ~1.3 KB | ~9 MB | ~65 MB |

A phone easily holds a 1-week rolling window (~1–2 GB).

### M0 is a "recent-changes RPC source", not just own-account
The changeset M0 derives by replaying the witness carries the **newValue** of
every account/storage key the window touched, and the replay also yields
**receipts**. So M0 can serve, **for any key changed in its rolling window** (not
only the user's own account): `getBalance` / `getTransactionCount` / `getCode` /
`getStorageAt`, plus `getTransactionReceipt` / `getBlockReceipts` for window
blocks, and `getLogs` over the window if it indexes the replayed logs. **Limit:**
only *changed* keys — a key untouched in the window has no value in M0 (fall back
to the M1 snapshot or an on-demand proof).
| **M1 snapshot-compact + hot-delta** | weekly **compact state snapshot** (keccak-keyed, MPHF) + client records **hot** block changesets forward | **full current state** at (snapshot height + hot blocks) |
| **M2 full headers+bodies** | genesis→tip headers + bodies (no state) | none (blocks/txs only) |
| **M2+R full + receipts store** | M2 + **stored receipts + log index** | none (but receipts/logs available) |
| **M3 caplin (CL)** | beacon blocks/state, finality | consensus data only (no EVM state) |

M1's snapshot **must be produced weekly** (rotation) because it is keccak-keyed
state as-of a height; between rotations the client closes the gap with its own
hot-delta changesets.

---

## 4. RPC capability matrix

Legend: ✅ serviceable · ⚠️ serviceable but expensive/slow · ❌ data not present.
"latest" = at/near tip; "historical" = arbitrary old height.

| eth_ method | data class | M0 witness-direct | M1 snapshot+hot | M2 headers+bodies | M2+R (+receipts) | M3 caplin |
|---|---|---|---|---|---|---|
| `blockNumber`,`chainId`,`syncing` | head/meta | ✅ | ✅ | ✅ | ✅ | ✅ (CL head) |
| `getBlockByNumber/Hash` (header) | header ① | ✅ | ✅ | ✅ | ✅ | ❌ |
| `getBlockByNumber` (full txs), `getBlockTransactionCount`, `getTransactionByBlock*Index`, `getUncle*` | body | ✅ recent only | ⚠️ if body kept | ✅ all heights | ✅ | ❌ |
| `gasPrice`,`feeHistory`,`maxPriorityFeePerGas` | recent headers/txs | ✅ | ✅ | ✅ | ✅ | ❌ |
| `getTransactionByHash` | txHash→(blk,idx) index | ❌ (no global tx index) | ❌ | ⚠️ needs tx-hash index built over bodies | ✅ (index) | ❌ |
| `getBalance`,`getCode`,`getStorageAt`,`getTransactionCount` — **latest** | state | ✅ for keys **changed in window** (newValue); else ⚠️ on-demand proof | ✅ | ❌ (no state) | ❌ | ❌ |
| same — **historical** | state-as-of | ❌ | ❌ (only snapshot height + hot) | ❌ | ❌ | ❌ |
| `getProof` — **latest** | trie/proof | ✅ (per-account proof from producer) | ✅ (snapshot trie) | ❌ | ❌ | ❌ |
| `getProof` — **historical (all heights)** | state-as-of trie | ❌ | ❌ | ❌ | ❌ | ❌ |
| `call`,`estimateGas` — **latest** | state+codes+EVM | ⚠️ if it has the touched pre-state (proofs) + codes | ✅ (full current state + codes + EVM) | ❌ | ❌ | ❌ |
| `call`,`estimateGas` — **historical** | state-as-of+EVM | ❌ | ❌ | ❌ | ❌ | ❌ |
| `getTransactionReceipt`,`getBlockReceipts` | receipts | ✅ **window blocks** (replay receipts) | ⚠️ recompute (no stored receipts) | ⚠️⚠️ **re-execute block** (slow) | ✅ (stored) | ❌ |
| `getLogs`,`getFilterLogs`,`*Filter` | receipts + **log index** | ⚠️ **window range** if it indexes replayed logs | ❌ | ❌ (re-exec a range = infeasible) | ✅ (stored + index) | ❌ |
| `sendRawTransaction`,`newPendingTransactionFilter` | mempool + P2P | ❌ (no txpool) | ❌ | ❌ | ❌ | ❌ |
| beacon `eth/v1/beacon/*` (finality, validators, attestations) | CL | ❌ | ❌ | ❌ | ❌ | ✅ |

### Per-mode gap summary (what's MISSING)
- **M0 witness-direct:** serves **recent-changes** state RPC — any account/slot
  changed in the rolling window (newValue from the derived changeset) + window
  receipts (replay) + window logs (if indexed). Missing: arbitrary-account latest
  state for keys NOT changed in the window (fall back to M1 snapshot or on-demand
  proof), no global tx-hash index, no historical state, no mempool. ~56 MB/day
  witness + ~120–250 MB/day changeset; 1-week window ~1–2 GB. The **phone**
  profile (wallet + recent-activity queries, fully verified ①②③).
- **M1 snapshot+hot:** full **current** state → `getBalance/getCode/getStorageAt/
  getProof/call/estimateGas` at tip all ✅. Missing: historical state, receipts/
  logs (no stored receipts; would re-execute), tx-by-hash (no index), mempool.
  This is the **stateful light node** profile.
- **M2 headers+bodies:** all block/tx structural queries ✅; **no state at all**
  (balance/call/proof ❌); receipts only by re-executing (⚠️⚠️).
- **M2+R:** adds receipts/logs/tx-index by **storing** them (see §5).
- **M3 caplin:** consensus/beacon RPC + the **finality anchor** that gives the
  EL minimal client its trusted checkpoint (§6); no eth_ execution RPC.

---

## 5. Why receipts/logs are STORED, not re-executed (Erigon evidence)

`eth_getTransactionReceipt` / `eth_getLogs` need each tx's receipt (status, gas,
logs, bloom). You can recompute them by **re-executing** the block — but:

- One receipt = re-execute the whole block (all prior txs change state).
- `eth_getLogs` over a range = re-execute **every block in the range**.
- This is so slow that **Erigon abandoned on-the-fly receipt regeneration and
  switched to storing receipts** (receipt/log domains + a log index), precisely
  because serving `getLogs`/`getReceipt` by execution is not viable for an RPC
  node. (Erigon 3 keeps receipts/logs as stored, indexed data.)

Consequence for our modes: a node that wants `getReceipt`/`getLogs` must **store
receipts + a log index** (M2+R). A pure witness/changeset node (M0/M1) can
recompute a **single recent** receipt by replaying that one block's witness
(acceptable), but cannot serve range `getLogs` (no index, re-exec infeasible).

---

## 6. Why all-height (historical) proofs are unrealistic — and what Caplin gives

### Historical proofs / historical state-as-of
Serving `getProof`/`getBalance`/`call` at an **arbitrary old height** requires the
**state trie as-of that block** — i.e., per-block historical trie roots +
reconstructable historical state. That is the heaviest archive feature (Erigon's
History domain + a per-block trie-root reconstruction). Producing and serving a
proof for *every* historical height is **not realistic** for a minimal/IDC
distribution model: it would mean keeping (or rebuilding) the full historical
trie at all heights. We deliberately scope proofs to **recent / anchored**
heights (the K-anchor cadence), not all-height.

### Caplin (consensus layer)
Caplin is the embedded **CL**. Its RPC surface is the **beacon API**
(`/eth/v1/beacon/*`: finality checkpoints, validators, attestations, sync
committee, blocks/state) — **not** eth_ execution RPC. Its value to the EL
minimal client is the **trusted checkpoint / finality**: a phone trusts a
CL-finalized header as the ① anchor, then verifies forward. So Caplin provides
*consensus* RPC + the *anchor of trust*, while the EL state RPC comes from the
M0/M1/M2 data modes above.

---

## 7. Recommended client profiles (compose the modes)

| Profile | Modes | RPC surface | Use case |
|---|---|---|---|
| **Phone wallet** | M0 + CL anchor | head/headers, own-account balance/code/proof (on-demand), recent receipt (replay), tx-by-hash for own txs (if it kept the body) | mobile self-custody, verify own state trustlessly |
| **Stateful light** | M1 + M0 follow + CL anchor | + full **current** state: `getBalance/getCode/getStorageAt/getProof/call/estimateGas` at tip | dapp backend at tip, no history |
| **Block/tx explorer (no state)** | M2 (+R) | block/tx structural + receipts/logs (if +R) | indexer/explorer of blocks+events |
| **Archive** | full historical state | everything incl. historical state/proofs/logs | the heavy node; not a phone |

### The hard "cannot" list (any non-archive mode)
- Historical state / historical proof / historical `call` at arbitrary old block.
- Range `getLogs` without a stored, indexed receipt set.
- `getTransactionByHash` / `getTransactionReceipt` by hash without a tx-hash index.
- `sendRawTransaction` without a txpool + P2P (a phone forwards to a producer).

---

## 8b. Size summary across profiles (measured, compressed, near tip)

Per-block artifact sizes (D:/N42-eth1177 + D:/n42-eth1 freezers, zstd):

| artifact | KB/block | role |
|---|---|---|
| header (①) | **0.19** | trust chain / anchor; tiny, everyone keeps |
| K-anchor (③) | **~1.3** amortized | state-root verification every K blocks |
| witness (②) | **7.8** | address-less state-read stream → replay |
| changeset (acct+storage newValues) | **16.6** | **DERIVED** by replay; or downloaded directly to apply forward |
| body (txs) | **23.7** | the transactions — the **biggest** per-block item |
| snapshot (M1 base, whole state @25.19M) | **~22.3 GB** (one-time) | accounts 3.5 GB + storage 18.8 GB (zstd) |

Key download-control fact: the **witness (7.8 KB) is ~3× smaller than the body
(23.7 KB)**, and executing it (with the body + codes) PRODUCES the changeset
(16.6 KB) + receipts locally — so what you download depends on the RPC surface
and trust you need, not on holding state:

### Download tiers (extreme → rich)

| profile | bootstrap (one-time) | steady-state download | local storage | RPC surface |
|---|---|---|---|---|
| **mobile (M0), trust-anchor** | CL checkpoint (~KB) | ① header + ③ anchor only: **~1.5 KB/blk ≈ 11 MB/day** | window headers+anchors (~MB) | follow + verify state-root @anchors (no ② replay) |
| **mobile (M0), verified** | CL checkpoint | + witness + body (+codes) to replay ②: **~33 KB/blk ≈ 240 MB/day** | rolling 1 wk ~1–2 GB (witness+derived changeset+receipts) | recent-changes state + recent receipts/logs, fully verified ①②③ |
| **mobile (M0), changeset-direct** | CL checkpoint | changeset only, apply forward (no EVM, no body): **16.6 KB/blk ≈ 120 MB/day** | rolling changeset+state delta | recent-changes state (no receipts unless also replayed) |
| **minimal (M1)** | **snapshot ~22 GB** (weekly rotation) | hot follow (witness or changeset) ~120–240 MB/day | snapshot 22 GB + hot delta | **full CURRENT state**: getBalance/getCode/getStorageAt/getProof/call @tip |
| **full** | sync headers (4.5 GB) + post-merge bodies | follow bodies+state, ~tens–hundreds MB/day | **trimmed**: latest state + post-merge bodies + recent receipts + indexes (~hundreds GB; vs 246 GB untrimmed) | latest-state + post-merge tx/block + recent receipts/logs |
| **archive** | full sync (all bodies 567 GB + history) | follow | **full historical** (~1.5 TB+) | everything incl. historical state/proof/logs |

Takeaways:
- **bodies dominate** (23.7 KB/blk → 567 GB full chain). Any profile that doesn't
  need tx-level queries (mobile changeset-direct, M1) skips them — the single
  biggest download saving.
- **witness-execution is a compute-for-download trade**: download 7.8 KB witness
  (+ body to replay) and DERIVE the 16.6 KB changeset + receipts, vs downloading
  them. Worth it when you want ② verification + receipts; for pure
  state-following, changeset-direct (16.6 KB, no body, no EVM) is lighter.
- **mobile floor is ~11 MB/day** (headers + anchors, trust-anchor tier) — a phone
  can follow + verify state roots for a year on ~4 GB.
- **M1 one-time 22 GB snapshot** unlocks full current-state RPC; the weekly
  rotation + ~120–240 MB/day hot follow keeps it at tip.

## 8. One-paragraph summary
Ship a block's state as either the address-less **witness** (replay → receipts,
②) or a **proof-carrying pre-state** (recompute root, ③); the EVM's **changeset**
(old=backward, new=forward) means no block is ever re-executed twice. A phone
**verifies-and-follows the tip** (no catch-up) and answers exactly the RPCs whose
data it records: M0 (witness-direct) ⇒ own-account proofs + recent receipts; M1
(snapshot+hot) ⇒ full **current**-state RPC (`call`/`getProof`/`getBalance`); M2
(headers+bodies) ⇒ block/tx structural only; receipts/logs require **storing**
them (re-execution is what Erigon abandoned); all-height historical proofs are an
archive-only feature we don't attempt; Caplin supplies consensus/beacon RPC + the
finality anchor, not EL state RPC.
