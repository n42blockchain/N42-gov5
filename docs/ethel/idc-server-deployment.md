# IDC Server Node — Deployment Guide (ETH EL + CL)

> **Scope.** This document covers the **Ethereum-compatible** side of N42 only:
> the `eth-el` execution layer plus the embedded Caplin consensus layer (CL), and
> the IDC data-distribution services that feed the four client node types.
> The **N42 native chain** (`n42` binary, APoS/HotStuff, AI/distributed runtime)
> is deployed separately — see `docs/ethel/architecture-framework-and-plan.md`
> §A and the N42 operator docs. Do not mix the two on one datadir; they are
> isolated by `--profile` + `--chain` + a `*.datadir.lock`.

---

## 1. What an IDC server node is

An **IDC (Internet Data Center) server node** is a geographically-distinct,
always-at-tip `eth-el` **archive** node that does three jobs:

1. **Produces** verifiable data for every block, in real time (~12 s/slot):
   - per-block **state witness** (~7 KB compressed) → witness freezer,
   - an **MPT stateless anchor** every `K = 1000` blocks (~1 MB compact) → anchor
     freezer,
   - an optional **multi-sig attestation** over `(blockNum, stateRoot,
     receiptRoot)`.
2. **Serves** that data to clients over a hardened, read-only HTTP RPC and over
   **BitTorrent / snapshot mirrors** (for bulk, cacheable, content-addressed
   artifacts).
3. **Anchors the CL** for downstream EL+CL nodes by publishing a beacon
   **checkpoint-sync URL** and **sentinel ENR bootnodes**.

IDC nodes are **redundant and stateless-to-each-other**: a client can fetch the
same artifact from any IDC and cross-check it locally (witnesses are
content-hashed, MPT anchors verify against the header root, code is
keccak-addressed). A faulty or malicious IDC cannot forge data a client accepts.
Run **≥ 3 IDC nodes** in distinct locations for M-of-N attestation and failover.

```
                        ┌──────────────── IDC server node (this guide) ───────────────┐
   mainnet tip ───────▶ │ eth-el archive @ tip  →  witness@12s + MPT anchor@1000       │
                        │ n42-stateless-serve   →  HTTP RPC (:8555) + BT/snapshot mirror│
                        │ caplin sentinel       →  CL checkpoint URL + ENR bootnodes    │
                        │ attestation aggregator→  /attest /attest-status               │
                        └───────┬──────────┬──────────────┬──────────────┬─────────────┘
                                │          │              │              │
                         HTTP+BT │   checkpoint+ENR│  checkpoint+ENR│  snapshot+ENR+devp2p
                                ▼          ▼              ▼              ▼
                            mobile      minimal         full          archive
                         (stateless)  (EL+CL,ckpt)   (EL+CL,ckpt)  (EL+CL,full CL)
```

The four client node types and how they consume an IDC are in §6.

---

## 2. Prerequisites

| Item | Requirement |
|------|-------------|
| OS | Linux x86-64 (production) or Windows 11 (dev). |
| CPU / RAM | ≥ 8 cores / ≥ 32 GB (archive replay + serving). IDC reference: 32 cores / 128 GB. |
| Disk | NVMe. Archive datadir + freezers ≈ 850 GB and growing; reserve ≥ 1.5 TB. |
| Toolchain | Go 1.25.0, CGO enabled (MDBX). |
| Network | Public static IP (or `extip:` NAT). Open ports per §5. |
| Build tags | `n42el,nosqlite,noboltdb` — the `n42el` tag is **required** for the embedded CL. |

```bash
# Build the EL+CL binary and the serving tool
go build -tags "n42el,nosqlite,noboltdb" -o build/bin/eth-el ./cmd/eth-el
go build -tags "nosqlite,noboltdb"        -o build/bin/n42-stateless-serve ./cmd/n42-stateless-serve
```

---

## 3. Stage A — run the IDC as an archive node at tip (the producer)

The producer is a normal `eth-el` **archive** node with the witness + anchor
emit hooks turned on via environment knobs. It catches up to the mainnet tip,
then follows live at 12 s, writing the witness and anchor freezers as it goes.

```bash
export N42_STAGED=1                 # staged catch-up (~270 Mgas/s) until at tip
export N42_WITNESS_DIR=/var/lib/n42/freezer       # per-block witness output
export N42_ANCHOR_DIR=/var/lib/n42/freezer        # MPT anchor output (K=1000)
export N42_MPT_VERIFY_INTERVAL=1000               # self-verify anchors as produced
# Optional: sign attestations and post to the aggregator (multi-IDC)
export N42_ATTEST_KEY=/etc/n42/attest.key
export N42_ATTEST_AGGREGATOR=https://idc-agg.example.net/

build/bin/eth-el \
  --datadir /var/lib/n42 \
  --snapshot.mode archive \
  --bootstrap.mode leaves --bootstrap.manifest https://<seed-mirror>/mainnet/manifest.json \
  --eldevp2p.enabled \               # EL block source (devp2p) for catch-up + live
  --engine.enabled=false \           # producer drives itself; no external CL needed here
  --catch-up.mode auto
```

Notes:
- The first cold start downloads the archive (HTTP/BitTorrent — see §4) and runs
  the leaves-journal `RebuildState` (~1 h), then catches up and goes live.
- Witnesses land in the witness freezer table (`TableBlockWitness`); anchors in
  `anchorc.cidx` / `anchorc.NNNN.cdat` (item `n/K-1`).
- For backfilling anchors over a historical range (not live), use
  `cmd/n42-stateless-anchor-produce` (O(state) per anchor; fine offline).

---

## 4. Stage B — serve the data (HTTP RPC + BitTorrent)

### 4.1 Hardened HTTP RPC — `n42-stateless-serve`

Runs read-only over the producer's freezers. Front it with a CDN / reverse
proxy: every artifact except the live head is **immutable + content-addressed**,
so the bulk of traffic is cacheable forever and origin load is bounded by the
1-block/12-s tip rate.

```bash
build/bin/n42-stateless-serve \
  --addr :8555 \
  --headers   /var/lib/n42/freezer \   # columnar headerc
  --bodies    /var/lib/n42/freezer \   # columnar bodyc
  --witness   /var/lib/n42/freezer \   # witness freezer
  --anchors   /var/lib/n42/freezer --anchor-k 1000 \
  --codes     /var/lib/n42/freezer \   # code-by-addr (contract-block ②)
  --chaindata /var/lib/n42/chaindata \ # code-by-hash (kv.Code), optional
  --trie      /var/lib/n42/trie \      # /account-proof, optional
  --chainid 1 \
  --rps 50 --burst 100 --bw-mbps 0 --max-concurrent 1024
```

**Routes** (all read-only; rate-limited per-IP → 429, bandwidth-capped,
max-concurrent → 503, hardened timeouts):

| Route | Purpose | Consumed by |
|-------|---------|-------------|
| `/head` | tip number + hash + finalized anchor | all |
| `/header`, `/headers`, `/full-header` | header(s) by number/range | all |
| `/block` | header + body (faithful columnar codec) | mobile, full, archive |
| `/witness` | per-block state witness (compressed) | mobile, archive |
| `/anchor`, `/anchor-heights` | MPT anchor proof (compact) + attestations | mobile, archive |
| `/account-proof`, `/account-multiproof` | EIP-1186 proofs (needs `--trie`) | mobile, dapps |
| `/code` | bytecode by keccak hash (content-addressed) | mobile, archive |
| `/health` | liveness / metrics probe | ops / CDN |
| `/attest`, `/attest-status` | attestation aggregator (§7) | IDC peers |

### 4.2 BitTorrent / snapshot mirror (bulk download)

Bulk artifacts (full archive, per-mode snapshots, deltas) are distributed over
**HTTP + BitTorrent v1/v2 + WebRTC + libp2p**, multi-source, with **blake2b
manifest verification**. Build and publish per-mode releases + deltas:

```bash
# Publish a snapshot release per client mode (minimal / full / archive)
n42-eth-manifest   --archive /var/lib/n42 --mode minimal --out /pub/mainnet/<height>/minimal
n42-eth-manifest   --archive /var/lib/n42 --mode full    --out /pub/mainnet/<height>/full
n42-eth-manifest   --archive /var/lib/n42 --mode archive --out /pub/mainnet/<height>/archive
# Incremental delta between two heights (≈ <1% of a full release)
n42-eth-delta-build --from-archive H0 --to-archive H1 --mode <mode> --out /pub/...
```

Clients point `--snapshot.source` (and, for BT, `--torrent.enabled`) at the
mirror; the manifest's blake2b hashes make the source untrusted.

> Mode selectors (validated): **minimal** excludes bodies/witness/senders;
> **full** = minimal + bodies/history/txindex (excludes witness + senders);
> **archive** ⊃ full and is the only mode carrying witness.

---

## 5. DNS, domains, and ports

Use one subdomain per service so clients have stable, CDN-frontable endpoints.

| Service | Suggested domain | Default port | Proto | Notes |
|---------|------------------|--------------|-------|-------|
| Stateless RPC | `data.idc.example.net` | 8555 | HTTPS (behind CDN) | `/head /header /block /witness /anchor /code /account-proof /health` |
| Snapshot mirror | `cdn.idc.example.net` | 443 | HTTPS + BitTorrent | static manifests/segments/deltas; cache-forever |
| CL checkpoint | `cl.idc.example.net` | 443 | HTTPS | beacon checkpoint-sync endpoint (finalized state) |
| CL sentinel (ENR) | publish ENR, not a domain | 9000 | TCP **and** UDP | libp2p TCP 9000 + discv5 UDP 9000 (split-capable) |
| Attestation agg. | `agg.idc.example.net` | 443 | HTTPS | `/attest /attest-status` |
| Engine API | `127.0.0.1` only | 20014 | HTTP+JWT | **never expose**; loopback only |

Firewall:
- **Open inbound**: 443 (RPC/mirror/checkpoint/agg via reverse proxy), TCP 9000
  + UDP 9000 (sentinel), and (if the IDC also seeds EL devp2p) TCP/UDP 30303.
- **Never open**: Engine API (20014) and JSON-RPC (20012/20013) to the public.

NAT / Docker: set the publicly reachable IP explicitly so the sentinel ENR is
correct, otherwise inbound beacon peers cannot reach you:

```bash
--caplin.nat extip:<public-ip>      # only extip:<ip> | none are supported
--caplin.discovery.addr 0.0.0.0     # bind all interfaces
```

To get the IDC's sentinel ENR (to hand to archive clients as a bootnode), read
it from the startup log line `[Sentinel] Sentinel started enr=...`.

---

## 6. Configuring the four client node types against this IDC

All four are **ETH-side** nodes. The N42 native chain is a separate binary/doc.

### 6.1 mobile (stateless light) — HTTP/BT, zero local state

The lightest client: no local state DB. Fetches header + witness (+ anchor at
anchor heights) per block and **verifies locally** (① header chain, ② witness
replay → receiptRoot, ③ MPT anchor → header root). Code is fetched by keccak
hash on demand and cached (content-addressed → a lying IDC cannot inject wrong
code; the client checks `keccak256(code) == hash`).

```
IDC endpoint:  https://data.idc.example.net   (the /head /header /witness /anchor /code RPC)
client flag:   --idc https://data.idc.example.net
```

Reference driver / conformance surface: `cmd/n42-stateless-e2e --idc <url>`
(pure-online mode: `--datadir "" --codes ""`). The production mobile SDK uses the
same `serve.HTTPSource` (`Source`/`FullSource`/`ArchiveSource`) under the hood.
For trust, point it at **≥ 2 IDC URLs** so `stateless.MultiSource` outvotes a
lone liar on head/header/anchor.

### 6.2 minimal — EL + CL(checkpoint), snapshot-direct

`eth-el` in `--snapshot.mode minimal` (snapshot-direct via warm overlay, #94),
with a **lightweight checkpoint follower** CL: it pins finality from the IDC
checkpoint and follows the EL's own devp2p tip. The follower is selected by
setting the sentinel **discovery port to 0** (no beacon P2P mesh).

```bash
build/bin/eth-el \
  --datadir /var/lib/n42-min \
  --snapshot.mode minimal \
  --snapshot.source https://cdn.idc.example.net/ --torrent.enabled \
  --bootstrap.mode snapshot \
  --eldevp2p.enabled \                       # EL block source for live tip
  --caplin.enabled \
  --caplin.checkpoint.url https://cl.idc.example.net/ \
  --caplin.sentinel.discovery.port 0 \       # 0 ⇒ lightweight checkpoint follower
  --engine.jwt /etc/n42/jwt.hex
```

### 6.3 full — EL + CL(checkpoint), snapshot-direct

Same CL wiring as minimal; `--snapshot.mode full` so the node also stores
headers + bodies + history index + txindex + receipts (no witness, no full
state). Serves headers/bodies to peers.

```bash
build/bin/eth-el \
  --datadir /var/lib/n42-full \
  --snapshot.mode full \
  --snapshot.source https://cdn.idc.example.net/ --torrent.enabled \
  --bootstrap.mode snapshot \
  --history.mode full \
  --eldevp2p.enabled \
  --caplin.enabled \
  --caplin.checkpoint.url https://cl.idc.example.net/ \
  --caplin.sentinel.discovery.port 0 \       # checkpoint follower
  --engine.jwt /etc/n42/jwt.hex
```

### 6.4 archive — EL + CL(comprehensive, independent fork choice, #34)

The full consensus node. `--snapshot.mode archive` (full historical state via
self-execution), driven by the **independent fork-choice CL**: real
attestation-weighted LMD-GHOST + Casper FFG over a diverse beacon peer set,
which drives the EL to the independently-chosen head (adversarial-environment
safe). Selected by a **non-zero sentinel discovery port** plus IDC bootnodes.

```bash
build/bin/eth-el \
  --datadir /var/lib/n42-arc \
  --snapshot.mode archive \
  --snapshot.source https://cdn.idc.example.net/ --torrent.enabled \
  --bootstrap.mode leaves --bootstrap.manifest https://cdn.idc.example.net/mainnet/manifest.json \
  --caplin.enabled \
  --caplin.checkpoint.url https://cl.idc.example.net/ \
  --caplin.sentinel.discovery.port 9000 \    # >0 ⇒ independent fork choice + beacon P2P
  --caplin.sentinel.port 9000 \
  --caplin.bootnodes "enr:-<IDC sentinel ENR>" \
  --caplin.nat extip:<this-node-public-ip> \
  --caplin.max-peer-count 64 \
  --engine.jwt /etc/n42/jwt.hex
```

> **CL selection rule (important):** the embedded CL chooses its driver from the
> sentinel discovery port — `> 0` ⇒ **independent fork choice** (archive); `= 0`
> ⇒ **checkpoint follower** (minimal/full). The flag **defaults to 9000**, so a
> minimal/full node *must* pass `--caplin.sentinel.discovery.port 0` explicitly
> to get the lightweight follower. (`internal/cl/service.go` Start.)

### 6.5 Quick matrix

| Node | binary / mode | EL block source | CL driver | IDC endpoints used |
|------|---------------|-----------------|-----------|--------------------|
| mobile | stateless lib / `--idc` | n/a (witness replay) | n/a | RPC `/header /witness /anchor /code` (+BT none) |
| minimal | `eth-el --snapshot.mode minimal` | eldevp2p | checkpoint follower (`discovery.port 0`) | snapshot mirror + `cl.` checkpoint |
| full | `eth-el --snapshot.mode full` | eldevp2p | checkpoint follower (`discovery.port 0`) | snapshot mirror + `cl.` checkpoint |
| archive | `eth-el --snapshot.mode archive` | beacon P2P + leaves/snapshot | independent fork choice (`discovery.port>0` + bootnodes) | snapshot mirror + `cl.` checkpoint + sentinel ENR |

---

## 7. Multi-IDC attestation (defence in depth)

Each IDC signs `(blockNum, stateRoot, receiptRoot)` and posts to a shared
aggregator; clients count **distinct** signers and accept an anchor only at
M-of-N. The aggregator is `n42-stateless-serve` with `/attest` + `/attest-status`
enabled, behind `agg.idc.example.net`.

- **Producer side** (each IDC): `N42_ATTEST_KEY=<signer key>` +
  `N42_ATTEST_AGGREGATOR=https://agg.idc.example.net/`. Signing is best-effort
  (5 s timeout, never halts sync).
- **Aggregator**: finalizes per anchor by distinct-signer threshold with a
  rolling window and fork-split guard.
- **Clients**: `stateless.MultiSource` + the `AttestationPool` aggregation; a
  lone liar is outvoted and reported (never silently resolved).

---

## 8. Hardening & operations checklist

- [ ] Engine API bound to loopback only; JWT secret 0600, not world-readable.
- [ ] Public RPC + mirror behind a CDN/reverse proxy; origin only serves the
      live tip + cache misses.
- [ ] Per-IP rate limit (`--rps/--burst`), bandwidth cap (`--bw-mbps`), and
      `--max-concurrent` tuned to the box.
- [ ] Sentinel: correct `--caplin.nat extip:<ip>`; TCP+UDP 9000 reachable;
      publish the ENR for archive clients.
- [ ] ≥ 3 IDC nodes in distinct locations; attestation key per node; aggregator
      reachable.
- [ ] Prometheus scraping the `internal/metrics` endpoint + `/health`.
- [ ] Snapshot publisher retention: keep the latest N releases + M deltas per
      mode (see distribution test plan).
- [ ] Back up the attestation signer keys; rotate on compromise.

---

## 9. Switching to fresh replay data + verification checklist

**Support check (2026-06-12).** The serve/produce/client tools all take data
directories as command-line flags (§3/§4); the replay output formats are
stable — headerc (columnar Marshal), witness (proto v1, length-prefixed),
anchorc (compact-wire `BlockProof`), codes (keccak content-addressed) — so
switching to a fresh replay is a **path change, not a code change**. The
producer-side gold gate (`header.Root`, checked per block in bpp / per E_1
window in n42-datc) means data is verified before it ever reaches an IDC.

> **Gap — historical proofs not yet served.** `n42-datc proof` produces
> full-history EIP-1186 proofs at any height, but `/account-proof` (§4.1) only
> serves *latest* via the MDBX trie. A phone verifying a **historical-height**
> account/slot proof needs a `/datc-proof?addr=&slot=&at=N` endpoint wired to
> the DATC store (TODO, after the 25M build completes). Today: latest proof +
> historical anchor-root trust.

**Run after every data switch (any red ⇒ do not ship):**

| # | Check | Command |
|---|-------|---------|
| A | Code regression (synthetic, seconds, CI) | `go test -tags 'nosqlite,noboltdb' ./internal/ethel/stateless/...` |
| B | Real-freezer smoke (①header + ②real witness replay + ③anchor) | `n42-stateless-e2e --headers <d> --bodies <d> --witness <d> --codes <d> --datadir <mdbx\|""> --anchors <d> --from <recent> --count 100 --k 1000` |
| C | Live IDC over HTTP (transport stack) | start `n42-stateless-serve` then `n42-stateless-client-test --idc <url> --from N --count 50` (pure-online `--datadir "" --codes ""` = phone shape) |
| D | DATC full-history proof (after 25M build) | `n42-datc verify --out <d> --samples 100` + `n42-datc proof --out <d> --addr 0x.. --slots 0x.. --at <historical>` (self-verifies via an independent hash-chain walk to the real header root) |

Check B note: keep witness/codes/senders heights **≥** the test block height
(codes-freezer is complete by-addr; MDBX by-hash is not). Failing blocks at the
data's coverage edge are alignment issues, not verification bugs.

## 10. Test-coverage matrix & gaps

| Verification path | Coverage | Test |
|---|---|---|
| ① HeaderChain extend/prune/gap | ✅ unit | `headerchain_test.go` |
| ③ real anchor produce + verify | ✅ unit | `anchor_test.go`, `producer_proof_test.go` |
| ③ anchor compact-wire codec | ✅ unit | `proofwire_test.go` |
| serve→HTTP→client roundtrip (empty ①③) | ✅ unit | `serve/http_test.go::TestHTTPEndToEnd` |
| serve→HTTP→client roundtrip (real ③ + code) | ✅ unit | `serve/state_e2e_test.go::TestHTTPStateAnchorEndToEnd` |
| `/code` roundtrip + keccak self-check | ✅ unit | `serve/server_test.go` + above |
| multi-sig aggregation finalize/fork-split | ✅ unit | `aggregator_test.go` |
| ② witness replay (synthetic empty block) | ✅ unit | `minimal_verify_test.go` |
| ② witness replay (real mainnet freezer) | ⚠️ E2E tool only | `cmd/n42-stateless-e2e` (no go-test regression) |
| code tiers (cold→hot→IDC fetch) | ⚠️ E2E tool only | same |
| DATC historical proof → serve endpoint | ❌ unwired | TODO (§9 gap) |
| multi-IDC network producer→aggregator | ⚠️ single-node aggregation tested | no distributed E2E |
| anchorc.blocks sidecar fault tolerance | ⚠️ path exists | no corruption test |

TODO priority: (1) wire DATC proof to serve `/datc-proof` (phone verifies
historical heights); (2) real-witness-replay go-test regression (needs a small
fixed freezer fixture); (3) multi-IDC network E2E.

---

## 11. Related docs

- `docs/ethel/idc-stateless-node-architecture.md` — full architecture + roles.
- `docs/ethel/caplin-independent-forkchoice-runbook.md` — archive CL (#34).
- `docs/ethel/caplin-el-follower-runbook.md` — minimal/full CL (#31).
- `docs/ethel/real-chain-three-mode-runbook.md` — snapshot modes.
- `docs/ethel/stateless-verification.md` — the three trust layers ①②③.
- `docs/ethel/n42-eth-distribution-test-plan.md` — snapshot/BT distribution.
- `docs/ethel/architecture-framework-and-plan.md` — **N42 native chain** + overview.
