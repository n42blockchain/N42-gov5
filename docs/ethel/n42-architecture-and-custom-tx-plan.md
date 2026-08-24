# N42 Architecture & Custom Transaction Plan

Status: plan (2026-06). Grounded in a code survey of `lib/qmdb`, `common/transaction`,
`crypto/{bls,dilithium,falcon}`, `internal/{vm,txspool,ethel}`, `contracts/pqregistry`,
`params`. Companion docs: [[datc-segment-format]] (delivery), [[datc-proof-serving]]
(serving). File:line refs are to the surveyed tree.

---

## 0. The two pillars

1. **State**: N42 commits to **QMDB** (binary twig forest, Blake3, append-only,
   O(1) index) as the **native/default** state tree — `lib/qmdb`, already wired
   as `--tree qmdb`. zkEVM tooling, however, expects **Keccak 16-ary MPT** proofs;
   N42 supplies that via **DATC** (full-history EIP-1186 archive — "archive plus",
   since geth/reth/erigon archives don't serve full-history proofs).
2. **Transactions**: custom types beyond the standard 0x00–0x04 —
   **(A) post-quantum signed** (`0x05`, *already largely built*; Feature A
   gating+gas now landed) and **two** ultra-compressed batch types that each pack
   ~10⁴ txs at ~12 B/tx:
   - **(B1) single-signature batch** (`0x06`) — one signer authorizes the WHOLE
     bundle with ONE secp256k1 ECDSA signature (same-sender / single-authority,
     e.g. an exchange/payment-processor doing thousands of its own ops).
   - **(B2) aggregate-signature batch** (`0x07`) — N *different* senders, each
     signs its own sub-tx; the N BLS signatures aggregate into ONE (96 B), so the
     batch authenticates many distinct users.
   They are different trust models (one authority vs many users), not two impls of
   the same thing — both are wanted.

Guiding principle throughout: **minimize changes on the consensus/execution hot
path.** Both tx types decode/expand into ordinary messages before execution, so
the EVM, state writer, and root computers are untouched.

---

## 1. State layer — QMDB native + MPT proof sidecar (zkEVM)

### 1.1 What exists
- **QMDB** (`lib/qmdb`): append-only positional log, per-key O(1) index
  (flatIndex / MDBX-backed), Blake3 binary twig forest, sharded lock-free writes,
  3-tier bounded memory. ~52× smaller than JMT, sequential writes. **Root is NOT
  ETH-MPT compatible.** Already an option in the replay/commitment pipeline.
- **DATC** (`cmd/n42-datc`): replays changesets → maintains the Erigon-layout
  Keccak MPT incrementally → emits temporal records (leaf history, node records,
  change index) → serves `proof-as-of(addr,slot,block)`. The full genesis→tip
  build is in progress (`--end 25311094`).
- Five commitment engines already exist behind a `RootComputer` interface (MPT/
  JMT/BMT/Verkle/LtHash), and LtHash already **runs in parallel with JMT for
  cross-check** — i.e. computing two roots per block is an established pattern.

### 1.2 Dual-root model (the design decision)
Header commits **two** roots:
- `WorldRoot` = QMDB twig-forest root → **consensus state root** (fast, append-only).
- `MPTStateRoot` = Keccak MPT root → **for zkEVM / eth-compat proofs**.

Both are computed per block. Cost asymmetry is the crux:
- **Live (tip)**: at 12 s/block there is ample budget to compute *both* roots even
  though MPT is read-bound (one block's incremental MPT root is ms–seconds; 12 s
  budget absorbs it). So the live node commits both; zkEVM can prove against the
  fresh `MPTStateRoot`.
- **Archive (history)**: DATC backfills the MPT temporal records genesis→tip
  (the slow part is *replaying millions of blocks back-to-back*, not per-block
  cost) and serves historical proofs. Delivered as the time-segmented,
  BT-seedable artifact in [[datc-segment-format]], served under the limiter in
  [[datc-proof-serving]].

### 1.3 zkEVM anchoring
A zkEVM proof verifies an EIP-1186 Merkle path to `MPTStateRoot`. Trust chain:
`MPTStateRoot` in the (QMDB-consensus) header → DATC segment `segment_root`
on-chain commitment ([[datc-segment-format]] §8) → a downloaded/served proof is
verifiable without trusting the server. **No change to QMDB**; the MPT is a
read-only compatibility projection.

### 1.4 Open decisions
- Whether the live node always computes `MPTStateRoot` or only DATC does (live =
  fresh zkEVM proofs at tip; DATC-only = proofs lag by the archive cadence). Recommend
  **live-dual at tip + DATC for history**, since 12 s easily covers it.
- Header layout for the second root (extra 32 B; the header already carries
  multiple tree roots per `project_header_extra_layout`).

---

## 2. Transaction subsystem — how a new type plugs in

Existing types (`common/transaction/transaction.go:34`): Legacy `0x00`,
AccessList `0x01`, DynamicFee `0x02`, Blob `0x03`, SetCode `0x04`,
**PostQuantum `0x05`**. Next free: **`0x06`**.

Three dispatch centers a new type must touch:
1. **Eth RLP wire** — `common/transaction/ethereum_rlp.go` (`DecodeEthereumTransaction`
   switch on type byte; `EncodeEthereumTransaction`). *Note: today this switch only
   covers 0x01–0x04 — 0x05 is NOT wired here (protobuf-only); see §3.*
2. **Protobuf storage/wire** — `transaction.go` `txDataFromProtoMessage` /
   `toProtoFields` (covers 0x05).
3. **Signer** — `transaction_signing.go` (`londonSigner.Sender/Hash`); custom
   schemes add their own `Signer` (e.g. `pq_signer.go`).

Plus: a `TxData` struct file, txpool acceptance (`internal/txspool/txs_pool.go`
`validateTx`), and fork gating (`params/config.go` time field + `config_rules.go`).
**Execution needs no per-type change**: `tx.AsMessage()` (`message.go`) is
type-agnostic and the executor consumes a `Message`. This is the lever for
keeping both new types off the hot path.

---

## 3. Feature A — Post-quantum signed transaction (`0x05`): finish & harden

**Status: mostly built.** `pq_transaction.go` (`PostQuantumTx`), `pq_signer.go`
(`PostQuantumSigner`), `pq_optimization.go`, `contracts/pqregistry/registry.go`
exist. Pure-Go crypto present: Falcon-512, Dilithium2, Dilithium3 (Dilithium5
present-not-wired; SQIsign defined-not-verified).

Sizes (pubkey+sig total): secp256k1 129 B · BLS 144 B · **SQIsign 241 B** ·
**Falcon-512 1563 B** · **Dilithium2 3732 B** · **Dilithium3 5245 B**. The
`pqregistry` + `PubKeyModeHash` lets a first tx carry the full pubkey and later
txs reference a 32 B hash (saves ~865 B Falcon / ~1280 B Dilithium2 per later tx).

### Gaps → work
| gap | status |
|-----|--------|
| **fork gating** | ✅ DONE — `ChainConfig.PQTxTime` + `Rules.IsPQTx` + `IsPQTx(time)`; txpool rejects `0x05` until active (commit 667f7fbe) |
| **gas table** | ✅ DONE — `common/transaction/pq_gas.go`: per-algo verify gas + per-byte sig/pubkey cost, wired into txpool pre-check AND consensus (`Message.intrinsicGasExtra` → `StateTransition`). Per-algo constants are calibration placeholders — **benchmark before mainnet** |
| `0x05` not in the **Eth RLP wire** switch (protobuf-only) | TODO — add decode/encode case in `ethereum_rlp.go` *iff* PQ txs must traverse the eth-el/devp2p wire; otherwise document N42-native-only |
| precompiles `0x14–0x17` reserved, **unimplemented** | TODO — implement Falcon/Dilithium2/Dilithium3/SQIsign verify precompiles in `internal/vm/precompiles` (gated by existing `PQPrecompilesTime`) — lets *contracts* verify PQ sigs; independent of the tx type |
| SQIsign verify TODO | TODO — integrate a vetted SQIsign lib or drop `0x01` until then |

### Notes
- PQ address = `Keccak256(pubKey)[12:]` (distinct from secp256k1 accounts) — decide
  coexistence (a PQ account is just another 20-byte address; fine).
- PQ is **security**, not throughput: signatures are large and **not aggregatable**
  with current schemes. Keep it orthogonal to Feature B.

---

## 4. Feature B — Ultra-compressed batch transactions (`0x06`, `0x07`)

**Goal**: pack up to ~10⁴ txs at ~12 B/tx (ledger fields, Vitalik
rollup-compression model), with the txpool accepting and expanding batches. Two
types with different signature/trust models:

### 4.0 Two types, and the ECDSA-vs-Ed25519 question

| | **B1 single-sig batch `0x06`** | **B2 aggregate-sig batch `0x07`** |
|---|---|---|
| signature | ONE ECDSA over the whole bundle (merkle root) | N BLS sigs → one 96 B aggregate |
| who signs | one signer (same-sender / single authority) | N different senders, each signs own sub-tx |
| authenticates | the batch submitter for all sub-txs | each distinct user independently |
| verify cost | **one** ecrecover, amortised → ~0/tx | O(N) pairings (size win, not CPU) |
| use case | exchange / processor batching its own ops | general cross-user mempool compression |
| crypto status | reuse secp256k1 (no new crypto) | `VerifyMultipleSignatures` exists (HotStuff) |

**ECDSA vs Ed25519 for B1** (your question — amortised they're ~equal, correct):
one signature over the whole batch → verify cost ÷ N ≈ 0 either way, so raw
per-verify speed is irrelevant. The real difference is **recovery**: secp256k1
has `ecrecover` (pubkey from sig) → the signer is a plain N42 account with **zero
pubkey bytes/storage**; Ed25519 has **no recovery** → must carry/register a 32 B
pubkey and adds a new key/address scheme. **Decision: B1 uses secp256k1 ECDSA**
(reuses ecrecover, signer = normal account, nothing new). Ed25519 only wins when
batch-verifying *many independent* sigs — not the case here (it is *one* sig).

The rest of this section (4.1–4.6) details the columnar encoding, pubkey-registry,
txpool expand, and execution — **shared by both B1 and B2**; only the signature
field + acceptance check differ (B1: one ecrecover; B2: one aggregate verify).

### 4.1 Crypto choice — BLS distinct-message aggregation
`crypto/bls` (blst, BLS12-381) already provides **`VerifyMultipleSignatures` /
`VerifyMultipleSignaturesVarMsg`** — randomized (Boneh-Gorbunov-Venkatesan)
**distinct-message** aggregate verify, in production use by HotStuff. This is
exactly "N txs, each its own hash, one aggregate signature."
- Aggregate sig: **96 B total** (vs N×65 B secp256k1).
- Verify is **O(N) pairings** (size win, not CPU win) — so pricing must charge
  per sub-tx (see §4.6).
- **No pubkey recovery** (unlike ecrecover) → the verifier must obtain N pubkeys.

### 4.2 The pubkey problem → reuse the registry pattern
To avoid 48 B/tx of pubkeys (which would erase the aggregation win), **accounts
register a BLS pubkey once on-chain** (reuse the `contracts/pqregistry` design:
`address → pubkey`). A batch then references each sender **by account index/
address**, and the verifier looks the pubkey up from state. → **0 pubkey bytes
per sub-tx.** A sender without a registered BLS key cannot be in a BLS batch
(falls back to a normal tx).

### 4.3 Wire format (columnar, reusing `body_compact` codecs)
```
BatchTx (0x06) {
  ChainID, BatchGasFeeCap, BatchGasTipCap            # trimmed uint256
  SubTxCount  (uint16/varint)
  # columnar sub-tx fields (reuse encodeTrimmedU256 / addr-dict / varint / bitpack)
  senders[]   : account-index (varint) → pubkey via registry      # ~2 B
  to[]        : addr-dict index OR 20 B                            # ~2 B
  values[]    : sci-notation mantissa×10^exp (planned codec)       # ~2 B
  nonces[]    : per-sender; elide when sequential                  # 0–1 B
  gas[], data_len[] : varint ; calldata[] concatenated
  aggSig      : 96 B BLS  (covers all N distinct sub-tx hashes)
}
```
Reusable as-is from `internal/ethel/body_compact.go`: columnar layout, trimmed
uint256, varint, adaptive address dictionary, bitpacking, conditional columns,
zstd. Add the `value` scientific-notation codec (already planned, −39%).

### 4.4 Byte budget (honest)
Per sub-tx, ledger fields only: sender ~2 + to ~2 + value ~2 + gas ~2 +
fees ~2 (batch-shared → ~0) + nonce ~0 + **aggSig 96 B ÷ N ≈ 0 for large N**
→ **~10–13 B/tx ledger** ✓ (matches the rollup target). **Calldata is
unavoidable in a full-node ledger** (can't move to a DA blob like an L2) — so a
batch of *transfers* (no calldata) hits ~12 B/tx; a batch of *contract calls*
adds the (zstd-compressed) calldata on top. State the limit clearly: 12 B/tx is
a **ledger-field** figure, realized for transfer-heavy batches.

### 4.5 Txpool: accept + expand (no bundle primitive exists today)
`internal/txspool` has **no** bundle/batch type — pending/queue are per-account
nonce lists. Plan:
- `validateTx` (`txs_pool.go`): new branch for `0x06` — decode header, fetch each
  sender's registered BLS pubkey, **`VerifyMultipleSignatures(subHashes, pubkeys,
  aggSig)` once**, check per-sub-tx nonce/gas/fee/intrinsic-gas.
- **Expand**: insert each sub-tx into its sender's `pending[from]` list as an
  ordinary `DynamicFeeTx`-shaped message (carrying a back-reference to the batch
  for the receipt/whole-batch hash). The batch object itself is not stored.
- `Pending()` returns expanded sub-txs in per-account nonce order → **miner /
  executor unchanged**.
- Cross-account batches are fine here (each sub-tx lands in its own account list);
  the aggregate verify already covered all of them at acceptance.

### 4.6 Execution & block encoding
- Execution: **unchanged** — sub-txs flow through `AsMessage` → `ApplyMessage`.
  State (QMDB + MPT) sees normal txs → no state-layer change.
- Block body: either (a) expand batches before `body_compact` encoding (MVP,
  simplest), or (b) add a batch-aware columnar variant later. Receipts: per
  sub-tx, plus a batch hash for `eth_getTransactionByHash` (sub-tx hashes are
  derivable; or expose only the batch hash, F2-style).
- **Gas/DoS pricing**: charge per sub-tx (verify is O(N)); reject batches whose
  aggregate verify or expansion would exceed a per-block budget. A bad aggregate
  sig fails the whole batch atomically at acceptance (cheap — one VerifyMultiple).

### 4.7 Build order (two types, shared plumbing)
- **B1 `0x06` first (no new crypto)**: single-signature batch — one secp256k1
  ECDSA over the bundle (same-sender / single authority); decode + columnar
  encode + txpool accept(one ecrecover)+expand. Lands the shared
  encode/expand/pricing plumbing. (~2 weeks per the survey effort map.)
- **B2 `0x07` next (headline)**: aggregate-signature batch — reuses B1's plumbing;
  swaps the acceptance check to one `VerifyMultipleSignatures` over N distinct
  sub-tx hashes, adds the **on-chain BLS pubkey registry** (reuse the `pqregistry`
  pattern) so sub-txs reference sender by index → 0 pubkey bytes/tx, plus
  per-sub-tx O(N) pricing. This is the "10⁴ txs, one signature" cross-user feature.

---

## 5. "Minimize changes for performance" — the discipline

- **Execution hot path untouched**: both new types expand/decode to a `Message`
  before `ApplyMessage`; the EVM, state writer, QMDB & MPT root computers never
  branch on the new types.
- **Reuse, don't rebuild**: `body_compact` codecs (columnar/trim/dict/varint/
  bitpack/zstd) for batch encoding; `pqregistry` pattern for BLS pubkey registry;
  `VerifyMultipleSignatures` for aggregation; the segment format + proof limiter
  docs for DATC delivery.
- **Acceptance-time work, once**: a batch is verified once (one aggregate
  pairing-check) at pool acceptance, not per sub-tx at execution.
- **Gating keeps mainnet surface clean**: new types behind time-fork flags
  (`PQTxTime`, `BatchTxTime`), like the existing Prague/PQPrecompiles pattern.

---

## 6. Cross-cutting

| concern | decision |
|---------|----------|
| Gas/DoS | PQ: per-algo verify+size surcharge. Batch: per-sub-tx gas; O(N) verify priced; per-block batch budget. |
| Fork gating | `PQTxTime`, `BatchTxTime` in `ChainConfig` + `IsX` rules + `validateTx` gates. |
| Wire | decide per type whether it must traverse eth-el devp2p RLP (then wire `ethereum_rlp.go`) or stays N42-native protobuf. |
| RPC | batch & F2 sub-txs: expose batch hash; sub-tx-hash lookup optional (MPHF index, like F2). |
| zkEVM | proves against `MPTStateRoot` (DATC), anchored via segment_root; QMDB untouched. |

---

## 7. Roadmap

1. **DATC** (in flight): finish genesis→tip build → verify/proof acceptance →
   land segment format + proof RPC (with limiter). *Gate before 1.x below.*
2. **Feature A (PQ 0x05)**: ✅ fork gating + gas table landed (667f7fbe);
   remaining = (optional) RLP wire + precompiles 0x14–0x17 + SQIsign.
3. **Feature B1 (`0x06` single-sig ECDSA batch)**: txpool accept(one ecrecover)/
   expand, columnar encode. Lands shared batch plumbing.
4. **Feature B2 (`0x07` aggregate-sig BLS batch)**: reuse B1 plumbing + on-chain
   BLS pubkey registry + `VerifyMultipleSignatures` acceptance + per-sub-tx
   pricing. The "10⁴ txs / one signature" cross-user headline.
5. **State dual-root**: commit `MPTStateRoot` alongside QMDB `WorldRoot` live;
   wire zkEVM proof anchoring.

Each step ships behind a fork flag, with the execution path unchanged.

## 8. Key risks / honest limits

- **12 B/tx is a ledger-field figure**; calldata-heavy batches are larger
  (full-node can't externalize calldata to DA).
- **BLS aggregate verify is O(N)** — saves bytes, not verify CPU; must be priced.
- **PQ and aggregation don't combine** with current crypto (PQ sigs aren't
  aggregatable); they are separate types for separate goals (security vs throughput).
  PQ-aggregate (lattice aggregation) is a research item, not planned.
- **MPT live-dual cost**: computing `MPTStateRoot` at tip is read-bound; fine at
  12 s/block but adds I/O — measure before enabling live-dual vs DATC-only.
- **SQIsign** verify is unimplemented; don't ship `0x01` PQ algo until integrated.
