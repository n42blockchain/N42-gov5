# Tester Guide — Four ETH Node Types + Last-Month Features

> **Scope.** Testing the **Ethereum-compatible** side: the four `eth-el` client
> node types (mobile / minimal / full / archive) against an IDC server, plus the
> features merged in the last ~30 days. The **N42 native chain** is tested
> separately — see the N42 test plan and
> `docs/ethel/architecture-framework-and-plan.md` §A. Server-side deployment is
> in `docs/ethel/idc-server-deployment.md`.

---

## 0. Test environment

| Need | Value |
|------|-------|
| Build | `go build -tags "n42el,nosqlite,noboltdb" -o build/bin/eth-el ./cmd/eth-el` (CGO on, Go 1.25) |
| Tools | `n42-stateless-serve`, `n42-stateless-e2e`, `n42-stateless-client-test`, `n42-datc`, `n42-verify-root`, `n42-witness-size` |
| IDC under test | one or more reachable IDC URLs (see deployment guide); record the sentinel ENR |
| Network | mainnet (`--caplin.network mainnet`, chainid 1) unless testing a private net |

Conventions used below:
- `IDC_RPC=https://data.idc.example.net` (stateless RPC)
- `IDC_CDN=https://cdn.idc.example.net` (snapshot/BT mirror)
- `IDC_CL=https://cl.idc.example.net` (checkpoint)
- `IDC_ENR=enr:-...` (archive bootnode)

> **Golden rule (project discipline): every block must verify its state root.**
> A run that imports blocks without per-sub-batch / per-block root verification
> is **not** a pass. Treat any `state root mismatch`, `receipt root mismatch`,
> or silent skip as a failure and capture logs.

---

## 1. Pre-flight: is the IDC healthy?

```bash
curl -s $IDC_RPC/health                 # expect 200
curl -s $IDC_RPC/head                   # tip number+hash+finalized anchor; advances ~every 12s
curl -s "$IDC_RPC/header?num=20000000"  # a known header; sanity-check the number echoes back
curl -s $IDC_RPC/anchor-heights | head  # anchor cadence K=1000 heights present
curl -s "$IDC_CL/"                       # checkpoint endpoint reachable (finalized state)
```

Pass criteria: `/head` advances over a minute; `/header?num=` returns the
**exact** requested number (a mismatch means the server is returning a
nearest-neighbor — fail and report). Always re-compute hex↔dec yourself before
asserting a block number.

---

## 2. Node-type test matrix

Run each node type pointed at the IDC, let it reach tip, and verify. Keep each
node on its **own datadir** (datadir lock enforces isolation).

### 2.1 mobile (stateless light) — trustless, zero local state

This is the strongest correctness test because it has **no local state to hide
behind** — every value is verified from the witness/anchor.

```bash
# Pure-online minimal client: no local state, no local code; everything from IDC.
build/bin/n42-stateless-e2e \
  --idc $IDC_RPC \
  --datadir "" --codes "" \
  --from <H> --count 100 --k 1000
```

Expect: 100 contiguous real blocks pass **① header chain + ② witness EVM replay
+ ③ MPT anchor** at the anchor height, with code fetched by keccak from the IDC
and keccak-verified + cached. Record: blocks verified, distinct code fetched vs
cache hits, wall time.

Negative tests (must FAIL closed):
- Point at an IDC that serves a **tampered witness** → ② must reject (receiptRoot
  mismatch), not accept.
- Point at **two IDCs that disagree** on a header/anchor → `MultiSource` must
  outvote the liar (use `--idc url1,url2,...` with one bad source) and report no
  quorum rather than silently pick one.
- Request code whose `keccak256(code) != hash` → client must reject.

### 2.2 minimal — EL + CL(checkpoint), snapshot-direct (#94)

```bash
build/bin/eth-el --datadir /t/min \
  --snapshot.mode minimal --snapshot.source $IDC_CDN --torrent.enabled \
  --bootstrap.mode snapshot \
  --eldevp2p.enabled \
  --caplin.enabled --caplin.checkpoint.url $IDC_CL \
  --caplin.sentinel.discovery.port 0 \
  --engine.jwt /t/jwt.hex
```

Verify:
- Cold start does **snapshot-direct** (no `RebuildState`; log shows snapshot
  reader + warm overlay, not a ~1 h rebuild).
- Catches up from snapshot H₀ to tip; **state root verified per sub-batch**.
- CL log: `Caplin lightweight EL-follower enabled` (NOT independent fork choice).
- `eth_getBalance`/`eth_getBlockByNumber` at tip return correct values.
- No tx history / no proofs expected at this tier.

### 2.3 full — EL + CL(checkpoint), snapshot-direct

```bash
build/bin/eth-el --datadir /t/full \
  --snapshot.mode full --snapshot.source $IDC_CDN --torrent.enabled \
  --bootstrap.mode snapshot --history.mode full \
  --eldevp2p.enabled \
  --caplin.enabled --caplin.checkpoint.url $IDC_CL \
  --caplin.sentinel.discovery.port 0 \
  --engine.jwt /t/jwt.hex
```

Verify (in addition to minimal's checks):
- Historical **state** queries work (`eth_getBalance` at an old height).
- **tx lookup + receipts** work (`eth_getTransactionByHash`,
  `eth_getTransactionReceipt`).
- No historical proofs/traces at this tier.
- Serves headers/bodies to a peer (point a minimal node's source at this node).

### 2.4 archive — EL + CL(comprehensive / independent fork choice, #34)

```bash
build/bin/eth-el --datadir /t/arc \
  --snapshot.mode archive --snapshot.source $IDC_CDN --torrent.enabled \
  --bootstrap.mode leaves --bootstrap.manifest $IDC_CDN/mainnet/manifest.json \
  --caplin.enabled --caplin.checkpoint.url $IDC_CL \
  --caplin.sentinel.discovery.port 9000 --caplin.sentinel.port 9000 \
  --caplin.bootnodes "$IDC_ENR" --caplin.nat extip:<this-ip> \
  --engine.jwt /t/jwt.hex
```

Verify:
- CL log: `Caplin independent fork choice enabled` →
  `anchor loaded` → `sentinel started` →
  `drove EL to attestation-weighted head`.
- The node acquires **beacon peers** (`GetPeers` > 0) and the EL head advances
  driven by the CL (not by eldevp2p; eldevp2p is off here).
- Full semantics: arbitrary-height **`eth_getProof`** (EIP-1186), **trace**, and
  **unwind** work.
- Catch-up reaches tip then switches to **12 s live**.

> **CL selection gotcha to test:** `--caplin.sentinel.discovery.port` defaults to
> 9000. Confirm minimal/full with `... .discovery.port 0` log the *follower* and
> archive with `>0` logs *independent fork choice*. A node that logs the wrong
> driver is a config/regression bug.

---

## 3. Last-month feature tests (~30 days)

Map each area to a concrete check. Run with the node from §2 unless noted.

### 3.1 Caplin embedded CL — independent fork choice (#34)

The headline feature. Beyond §2.4:

- **Adversarial head guarantee** (unit): `go test -tags "n42el,nosqlite,noboltdb"
  ./internal/cl/ -run TestResolveHeadExec` — head is sourced **only** from the
  attestation-weighted fork choice, never the EL tip; finalized stays **zero**
  when unknown (never substituted with head); GetHead errors propagate.
- **head/finalized arg order** (regression of the recent swap fix):
  `go test -tags "n42el,nosqlite,noboltdb" ./internal/cl/eladapter/ -run
  TestAdapter_ForkChoiceUpdate` — asserts canonical `(finalized, safe, head)`
  routing. While live, confirm the EL's `finalizedBlockHash` lags `headBlockHash`
  (finality behind head), **not** the reverse.
- **Live gossip path:** with the archive node connected, a freshly proposed
  beacon block should reach `OnBlock` within its slot (sub-slot head latency),
  and the block-sync loop backstops it. Watch for
  `subscribed to beacon_block gossip`.
- **Production hardening (#34):** split sentinel TCP/UDP ports honored; `--caplin.nat
  extip:` makes the published ENR carry the public IP (read the
  `[Sentinel] Sentinel started enr=` line and decode it); extra
  `--caplin.bootnodes` are appended, not replacing the preset.
- **Shutdown (recent fix):** stop the node (SIGINT) and confirm the sentinel and
  libp2p host close — no leaked listener on TCP/UDP 9000 on restart.

### 3.2 Caplin EL-follower (#31)

- minimal/full nodes (§2.2/2.3) log `lightweight EL-follower`, pin finality from
  the checkpoint, and follow the EL devp2p tip. Kill mid-run and restart →
  finality re-pins, no datadir split (state vs head reconcile).

### 3.3 minimal/full snapshot-direct (#94)

- Cold-start a minimal and a full node from the IDC snapshot mirror; confirm
  **no `RebuildState`** runs (snapshot-direct via warm overlay) and catch-up from
  H₀ verifies per sub-batch. Tamper a storage slot in the warm tier and confirm
  the tombstone path: a slot cleared post-H₀ must read as absent (not the stale
  snapshot value).

### 3.4 Stateless pipeline + serving (stateless/*)

- **Three trust layers** end-to-end: `cmd/n42-stateless-e2e` over real freezers,
  layers ①header chain ②witness replay ③anchor — all PASS for a 100-block window
  aligned to data coverage.
- **Serve hardening:** hammer `$IDC_RPC` past `--rps/--burst` → expect 429 +
  `Retry-After`; exceed `--max-concurrent` → 503; oversized range/`count` →
  rejected by request caps.
- **MultiSource M-of-N:** §2.1 negative tests.
- **Bench sanity:** `n42-witness-size` ≈ 7 KB compressed / ~37 KB raw at tip;
  MPT anchor ≈ 1 MB / 1000 blocks.

### 3.5 eth_getProof / MPT proof system (mptproof)

- On the archive node: `eth_getProof(addr, [slots], "latest")` returns a valid
  EIP-1186 bundle; verify it independently (`VerifyStandardProof` oracle).
- State-as-of: `eth_getProof(addr, [slots], <oldBlock>)` returns a historical
  proof that verifies against that block's stateRoot.

### 3.6 Snapshot/BT distribution (n42-eth-*)

- Publish a release per mode + a delta (`n42-eth-manifest`,
  `n42-eth-delta-build`); a client `--snapshot.source` (with `--torrent.enabled`)
  downloads and **blake2b-verifies** from the untrusted mirror. Mode selectors:
  minimal excludes bodies/witness/senders; full excludes witness/senders;
  archive ⊃ full (witness is archive-only). See
  `docs/ethel/n42-eth-distribution-test-plan.md` for IT-1..IT-7.

### 3.7 EIP-4444 / body compression / F2 (ethel)

- `--history.f2dir <dir>`: the node serves **ledger bodies** (from/to/value/
  nonce/gas/input, no signatures) ~45% smaller; confirm `eth_getBlockByNumber`
  ledger fields are correct while signatures are absent on the F2 path.
- Cold-segment expiry + relocate + torrent 1-of-N reseed (per
  `docs/ethel/body-compression-design.md`); confirm an expired cold body is
  re-fetchable and content-verifies.

### 3.8 txindex compression (txindex)

- `eth_getTransactionByHash` on a full/archive node resolves via the segmented
  txhash→block index (LFP + self-verify + mmap); confirm lookups are correct and
  the lookup heap stays flat (mmap, no per-query alloc spike).

### 3.9 devp2p (devp2p)

- `--eldevp2p.enabled` on minimal/full: the EL acquires eth/68-69 peers and the
  tip advances; `--eldevp2p.bootnodes` append (or `--eldevp2p.bootnodes-replace`
  replaces) the mainnet defaults.

### 3.10 DATC — full-history EIP-1186 proofs (`cmd/n42-datc`, 2026-06)

DATC produces an EIP-1186 proof for **any historical height** (contrast §3.5
mptproof: latest + state-as-of from the live trie; DATC covers all of history
in its own compact store). Build keys records by a per-depth epoch schedule and
gold-checks each E₁ window against the real `header.Root`.

```bash
# Build (mainnet source; W=E_1 window batch root; leaf history → zstd segments)
n42-datc build --src mainnet --out <d> --leaf-seg --window --cbar 0.25
# Verify: 100 random historical-height roots rebuilt BYTE-EXACT from DATC records
n42-datc verify --out <d> --samples 100
# Proof: EIP-1186 result, self-verified by an independent hash-chain walk to the
# real header.Root (only emitted if it verifies)
n42-datc proof  --out <d> --addr 0xde0b295669a9fd93d5f28d9ec85e40f4cb697bae \
  --slots 0x0,0x1 --at <historical-height>
```

- Spot-check EF multisig `0xde0b29…bae` and USDT `0xdac17f…ec7` slot `0x0` at
  several heights; each must print `proof VERIFIED against header root …`.
- **Partial-window edge**: if `--end` is not a multiple of W, the last `<W`
  blocks are a partial window — verify a few `--at` heights inside that range
  explicitly (chg-replay must reconstruct them correctly).
- **Known gap**: DATC historical proofs are **not yet wired to a serve
  endpoint** (a phone trusts historical roots via the anchor; `/account-proof`
  is latest-only). See `idc-server-deployment.md` §9.

### 3.11 Parallel EVM + lazy-coinbase (`--parallel-evm`, 2026-06)

Block-STM intra-block parallel execution, opt-in (default off). lazy-coinbase
defers the per-tx coinbase tip to one Σtip credit, removing the universal
per-tx conflict; an observer guard trips on any tx that reads the coinbase
balance (BALANCE/SELFBALANCE/`Empty`) → re-runs the block non-lazy.

- Unit: `go test -tags 'nosqlite,noboltdb' ./internal/ethel/ -run Coinbase` and
  `./modules/state/ -run BalanceReadHook`.
- **Deployment rule (must enforce):** do **NOT** stack `--parallel-evm` on a
  block-level parallel pipeline (`witness-replay --workers N`) — the two tiers
  oversubscribe the cores and both slow down. parallel-evm is for SINGLE-STREAM
  execution only (live tip-following / single sequential replay).

### 3.12 Storage footprint + switching to fresh data

- Per-mode download/on-disk budget: `docs/ethel/storage-matrix-2026-06.md`
  (mobile ~MB / minimal ~30 GB / full ~126 GB download / archive ~1.1–1.4 TB
  post-squeeze). Hot-segment-only except headerc; senders/codes come from the F2
  body / on-demand IDC, not downloaded.
- Switching the IDC/clients to a fresh replay + the run-after-switch checklist
  A–D: `idc-server-deployment.md` §9-10.

### 3.13 Regression suite (run before any data switch / release)

```bash
go test -tags 'nosqlite,noboltdb' \
  ./internal/ethel/stateless/... ./internal/ethel/ \
  ./modules/state/ ./modules/state/commitment/ \
  ./cmd/n42-datc/ ./lib/qmdb/ -count=1
```
Covers: stateless ①②③ + serve→client roundtrip incl. the 2026-06
`TestHTTPStateAnchorEndToEnd` (real-state anchor + code over HTTP),
lazy-coinbase observer guard, DATC build/verify equivalence, QMDB
sharding/SIMD/batched-fold root-identity. **Any red ⇒ do not ship.**

---

## 4. Cross-node consistency (the real acceptance gate)

At the same tip height `H`, all four node types must agree:

```bash
# same block hash + stateRoot from each node's RPC (and the IDC)
for n in mobile minimal full archive; do report eth_getBlockByNumber(H) hash+stateRoot; done
```

Pass: identical `hash` and `stateRoot` across mobile (verified), minimal, full,
archive, and the IDC `/head`/`/header`. Any divergence is a **consensus bug** —
capture the height, the diverging field, and per-tx receipts to localize.

---

## 5. Reporting template

For each failure capture:
1. Node type + exact command line (all flags).
2. Block height (hex **and** decimal, recomputed), the diverging field.
3. Relevant log lines (the CL driver line, the mismatch line).
4. Whether it reproduces on a fresh datadir.
5. Build commit (`git rev-parse HEAD`) + build tags.

Never trust a tool's stdout `PASS`/`OK` alone if its stdout could be polluted —
confirm with idempotent queries (`/header?num=`, `eth_getProof`, a re-run on a
fresh datadir).

---

## 6. Related docs

- `docs/ethel/idc-server-deployment.md` — server side (this guide's counterpart).
- `docs/ethel/caplin-independent-forkchoice-runbook.md` — archive CL (#34).
- `docs/ethel/caplin-el-follower-runbook.md` — minimal/full CL (#31).
- `docs/ethel/real-chain-three-mode-runbook.md` — snapshot modes.
- `docs/ethel/stateless-verification.md` — trust layers ①②③.
- `docs/ethel/storage-matrix-2026-06.md` — per-mode footprint + squeeze queue.
- `docs/ethel/eip1186-mpt-proof-storage-research.md` — DATC design (§3.10).
- `docs/ethel/n42-eth-distribution-test-plan.md` — distribution IT cases.
- `docs/ethel/architecture-framework-and-plan.md` — **N42 native chain** + overview.
