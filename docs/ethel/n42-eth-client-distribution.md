# n42-eth Client Distribution

**Status:** Spec / target design — directly models reth's
<https://snapshots.reth.rs/> style with the three-tier
minimal / full / archive contract. Concrete numbers come from
the D:\N42-eth1177 reference build at chain height 25.1 M.

---

## TL;DR

Three sync modes, ordered by storage and capability. **Archive is
the default.** Pick lower tiers only if disk-bound.

| Mode | Disk | Capabilities | Catch-up |
|:--|--:|:--|:--|
| `minimal` | **~39 GB** | Header verification + point queries on current state | minutes |
| `full` | **~682 GB** | minimal + historical state + tx-by-hash + receipts + bodies | tens of minutes |
| `archive` *(default)* | **~849 GB** | full + EVM replay + state proofs at any height + on-demand CS derivation | ~1 hour |

All three modes are produced from the **same underlying archive**
(`docs/ethel/client-server-sync.md`) — choosing a mode just selects
which files to fetch. No re-build per mode.

---

## Mode 1 — `minimal` (~39 GB)

Light-client baseline. Header chain + current-state snapshot. Can
verify header lineage, serve point queries on current state, and
generate EIP-1186 storage proofs at the tip. **Cannot** execute
transactions, **cannot** answer historical queries.

| Section | File(s) | Size | Format | Source |
|:--|:--|--:|:--|:--|
| Chain headers | `chain/freezer/headerc.{cidx,NNNN.cdat}` | **4.6 GB** | N42 columnar (8 K blocks/segment, per-field zstd) | `ethexec header-compact` |
| State — accounts | `snapshot/accounts.[0-H₀].{idx,ef,val.zst}` | **3.9 GB** | RecSplit MPHF (1.71 b/key) + Elias-Fano + zstd values | `cmd/reth-snapshot-export --n42` |
| State — storage | `snapshot/storage.[0-H₀].{idx,ef,val.zst}` | **24 GB** | Same shape; 1.57 B entries | same |
| Code | `chain/freezer/codes.{cidx,NNNN.cdat}` | **6 GB** | Address-indexed (`internal/ethel/codes_freezer_reader.go`); per-entry zstd | `code-import2fz` |
| Manifest | `manifest-minimal.json` | < 1 MB | blake2b-256 per file + segment range | `cmd/n42-eth-manifest` |
| **Total** | | **~39 GB** | | |

### What `minimal` can do

- `eth_blockNumber`, `eth_getBlockByNumber` *headers only*
- `eth_getBalance` / `eth_getCode` / `eth_getStorageAt` / `eth_call`
  at **tip only**
- `eth_getProof` at **tip only** (RB-1..5 path against the
  reth-style `StoragesTrieV2` table when present, see
  `docs/ethel/rb-5a-compression-results.md`)
- Header continuity verification via parent-hash chain

### What `minimal` cannot do

- Anything requiring block bodies (`eth_getBlockByNumber` with
  full=true, `eth_getTransactionByHash`)
- Anything at a historical height (`...At(blockNum)`)
- EVM trace / receipt fetches
- Re-execution / fraud proofs

---

## Mode 2 — `full` (~682 GB)

Full RPC node. Adds bodies, receipts, and the history index that
lets historical queries hit a single block lookup.

Senders are **NOT** in `full` — they're deterministically derivable
from bodies via `ecrecover`. See "Senders are recoverable" below.

| Section | File(s) | Size | Format | Notes |
|:--|:--|--:|:--|:--|
| *(everything from `minimal`)* | | 39 GB | | |
| Bodies | `chain/freezer/bodyc.{cidx,NNNN.cdat}` | **567 GB** | N42 columnar, per-field zstd; ratio 27.5 % vs raw RLP, 65 % vs geth snappy | `ethexec body-compact` |
| Receipts | `chain/freezer/receipts.{cidx,NNNN.cdat}` | **63 GB** | Reth-style Compact, batch=64 zstd; 30 % of geth raw | `ethexec receipt-copy` |
| History — accounts | `chain/freezer/accthist.{cidx,NNNN.cdat}` | **13 GB** | cscompact SegmentStore + RecSplit, 1 M blocks/segment, delta-varint block lists | `ethexec history-build` |
| History — storage | `chain/freezer/storhist.{cidx,NNNN.cdat}` | **28 GB** | same; 52 B addr+slot key with 4 B fingerprint | same |
| Tx-by-hash index | `chain/freezer/txindex.{cidx,NNNN.cdat}` | **13 GB** | RecSplit MPHF over 32 B txHash → (blockNum, txIndex) | `ethexec txlookup-build` |
| Manifest | `manifest-full.json` | < 1 MB | | |
| **Total** | | **~682 GB** | | |

### What `full` adds vs `minimal`

- `eth_getBlockByNumber` (full=true), `eth_getBlockReceipts`
- `eth_getTransactionByHash`, `...Receipt`
- `eth_getBalance/Code/Storage` at **any historical height** via
  the cscompact history index (`docs/ethel/history-build-v1-design.md`)
- `eth_getLogs` (via receipts + per-block log indexer)

### What `full` still cannot do

- `debug_traceTransaction`, `trace_*` — require executing the
  block, which needs witness or full pre-state reconstruction
- `eth_getProof` at historical heights — needs the trie nodes that
  existed at that block (only `archive` ships these)
- Re-derive a CS/changeset audit log

### Senders are recoverable

`senders.NNNN.cdat` is NOT shipped in any mode — it's
deterministically derivable from `bodyc` via `ecrecover`.
`ethexec sender-recovery --ancient <bodyc>` produces it in
~3 hours on 16 cores (or ~12 hours single-threaded) on 25 M
blocks. Once built it lives at `chain/freezer/senders.*` and
the executor skips ecrecover in the hot loop.

Operators wanting the pre-built senders pack can opt in via
`--include-senders` during snapshot fetch; the publisher
maintains it as an optional add-on (~38 GB).

---

## Mode 3 — `archive` (~849 GB) — **default**

Adds the per-block witness stream. With witness, EVM execution is
replayable from any historical block, which means **changesets
(acctcs / storcs) and arbitrary historical state proofs are
derivable on demand** rather than shipped.

| Section | File(s) | Size | Format | Notes |
|:--|:--|--:|:--|:--|
| *(everything from `full`)* | | 682 GB | | |
| Witness | `chain/freezer/witness.{cidx,NNNN.cdat}` | **167 GB** | Stream/length-prefixed v1 (`docs/ethel/devlog-eth-el-node.md`; `memory/feedback_witness_stream_v1.md`); set of state slots touched per block | `ethexec` executor with `--witness` |
| **Total** | | **~849 GB** | | |

### Why archive ships witness and not raw CS

| What we **could** ship | Size | What we ship | Size | Saving |
|:--|--:|:--|--:|--:|
| acctcs + storcs (Erigon V2 changesets, old→new per slot) | **397 GB** | witness (touched-slot set per block) | **167 GB** | **−230 GB** |

CS is `(blockNum, key, oldValue, newValue)` for every state write.
Witness is `(blockNum, {keys touched})`. Witness is ~2.4 × smaller
because:

- It records the **access set**, not the value diff
- Touched-but-unchanged slots count once (CS would record both
  values)
- No value bytes — just keys

Trade-off: producing CS from the archive requires a re-execution
pass. On the reference machine (Ryzen 9 9950X, NVMe), full 25 M
blocks: ~2–4 hours for `acctcs+storcs` derivation.

### What `archive` adds vs `full`

- `debug_traceTransaction`, `trace_block`, `trace_replayTransaction`
- `eth_getProof` at **any** historical block
- Audit / fraud proof generation
- CS rebuild via `ethexec witness-replay --derive-cs`

### `archive` is the default

The default mode is `archive` because:

1. It's the only mode that retains full L1-equivalent EVM
   semantics (replay + state proofs at any height).
2. The marginal cost over `full` (+167 GB / +23 %) is small versus
   the capability gap.
3. CS-on-demand is fast enough (single-digit hours) that not
   shipping raw CS is a clean architectural win.

Operators not running `debug_*` / `trace_*` and not generating
historical proofs can save 167 GB by choosing `full`.

---

## Mode comparison matrix

| Capability | `minimal` | `full` | `archive` |
|:--|:-:|:-:|:-:|
| `eth_call` (tip) | ✓ | ✓ | ✓ |
| `eth_getProof` (tip) | ✓ | ✓ | ✓ |
| `eth_getBlockByNumber` (full=true) | – | ✓ | ✓ |
| `eth_getTransactionByHash/Receipt` | – | ✓ | ✓ |
| `...At(historicalBlock)` | – | ✓ | ✓ |
| `eth_getLogs` | – | ✓ | ✓ |
| `debug_traceTransaction`, `trace_*` | – | – | ✓ |
| `eth_getProof` (historical) | – | – | ✓ |
| Re-derive CS / audit log | – | – | ✓ |
| **Disk** | **39 GB** | **682 GB** | **849 GB** |

---

## File-format quick reference

| Format family | Files | Spec |
|:--|:--|:--|
| **Standard freezer** | `senders`, `receipts`, `witness`, … | `modules/rawdb/freezer` — `.cidx` 6 B/item, `.NNNN.cdat` ≤2 GB rotation, per-batch zstd |
| **Address-indexed (codes)** | `codes.cidx`/`cdat` | `internal/ethel/codes_freezer_reader.go` — 26 B/entry sorted by address, binary searched |
| **N42 columnar** | `headerc`, `bodyc` | `internal/ethel/{header,body}_compact.go` — 8 192 blocks/segment, per-field zstd |
| **cscompact (history)** | `accthist`, `storhist`, `txindex` | `internal/cscompact/segment_store.go` — 1 M blocks/segment, RecSplit MPHF + delta-varint |
| **Snapshot (state)** | `accounts.*`, `storage.*` | `cmd/reth-snapshot-export` — RecSplit MPHF + Elias-Fano + zstd values |
| **MDBX (live tip)** | `chain/mdbx.dat` | Hot table for blocks within the warm-CS window (last ~7 days) |

Per-table source code / writer mapping: `docs/ethel/freezer-tables.md`.
Per-file inventory with byte ranges: `docs/ethel/n42-eth1-freezer-catalogue.md`.

---

## Distribution server layout

Mirrors reth's <https://snapshots.reth.rs/> directory tree. Each
**network × mode × release** is a directory of files plus a
manifest. Files are content-addressed by blake2b-256 so deltas can
be diffed.

```
https://snapshots.n42.io/
├── mainnet/
│   ├── 25100000/                          ← release tag = chain height
│   │   ├── minimal/
│   │   │   ├── headerc.0000.cdat
│   │   │   ├── …
│   │   │   ├── snapshot/accounts.0-25099999.idx
│   │   │   ├── …
│   │   │   └── manifest-minimal.json
│   │   ├── full/
│   │   │   └── …                          ← superset of minimal
│   │   └── archive/
│   │       └── …                          ← superset of full
│   └── 25200000/
│       └── …
└── sepolia/
    └── …
```

### manifest format

```json
{
  "network": "mainnet",
  "height": 25100000,
  "mode": "archive",
  "block_hash": "0x…",
  "state_root": "0x…",
  "created_at": "2026-05-22T18:27:00Z",
  "files": [
    {
      "path": "chain/freezer/headerc.cidx",
      "size": 24520,
      "blake2b256": "…",
      "segments": [0, 3065]
    },
    {
      "path": "chain/freezer/headerc.0000.cdat",
      "size": 1610612736,
      "blake2b256": "…",
      "segments": [0, 1024]
    },
    …
  ]
}
```

`blake2b-256` is fixed for content-addressed integrity. The
optional `segments` field is the inclusive segment-id range in
the file, which lets a client know exactly which file to fetch
to extend a given block range.

---

## Client CLI

```bash
# Fetch the default archive of mainnet
n42-eth snapshot fetch --network mainnet --mode archive \
    --to /var/lib/n42/

# Same with explicit height (otherwise picks latest)
n42-eth snapshot fetch --network mainnet --mode archive \
    --height 25100000 --to /var/lib/n42/

# Switch tiers in place (idempotent — re-fetches only what's missing)
n42-eth snapshot upgrade --to full
n42-eth snapshot upgrade --to archive
n42-eth snapshot downgrade --to minimal --delete-extra

# Verify
n42-eth snapshot verify --to /var/lib/n42/      # blake2b every file
n42-eth snapshot verify --to /var/lib/n42/ --deep   # + replay sample blocks

# Inspect what mode we have
n42-eth snapshot mode --to /var/lib/n42/
# > mode=archive  height=25100000  intact=true
```

### Catch-up after snapshot

```bash
n42-eth sync --datadir /var/lib/n42/
# pulls blocks 25100001..tip from peers via libp2p; appends to all freezer tables
```

Bodies, receipts, and witness for each new block are produced by
the executor and appended in lockstep (`outputBatcher.Sync()` —
`memory/project_freezer_mdbx_atomicity.md`). State stays in the
live MDBX warm tier until the next snapshot rollover.

### Mode upgrade flow

Upgrade walks the manifest of the target mode, downloads any
missing file, blake2b-verifies it, and atomically moves into
place. Existing files are kept iff their hash matches.

Downgrade removes any file in the current manifest that the
target manifest doesn't reference — strictly subtractive.

---

## Producing distributables

All three modes are bytes-identical functions of an `archive`
build. The publisher pipeline is:

```
ethexec replay (produces archive freezer + warm MDBX)
   │
   ├── ethexec history-build      → accthist + storhist + txindex
   ├── reth-snapshot-export --n42 → snapshot/accounts.* + storage.*
   ├── code-import2fz             → codes.cidx + cdat
   ├── ethexec header-compact     → headerc.*
   ├── ethexec body-compact       → bodyc.*
   └── (witness, receipts, senders are direct ethexec outputs)
   │
   ▼
n42-eth-manifest --mode {minimal,full,archive}
   │
   ▼
manifest-{mode}.json + content-addressed blob index
   │
   ▼
upload to snapshots.n42.io
```

Single source of truth → three subsetted manifests → atomic
publish.

---

## Storage savings vs reth equivalent

For the same 25.1 M block chain state:

| Mode | n42-eth | reth equivalent | Saving |
|:--|--:|--:|--:|
| minimal | 39 GB | reth has no equivalent — closest is `--full` ≈ 1.2 TB | — |
| full | 682 GB | reth `--full` ≈ 1.2 TB | **−518 GB (−43%)** |
| archive | 849 GB | reth `--archive` ≈ 2.5 TB | **−1.65 TB (−66%)** |

n42-eth's archive at 849 GB vs reth's 2.5 TB comes from:

- Reth's `StoragesTrie` 31.4 GB → n42 packed-subkey 26 GB
  (`docs/ethel/rb-5a-compression-results.md`)
- Reth `Bytecodes` 17.6 GB → address-indexed codes 6 GB
- Reth ships full per-block changesets via MDBX history; n42 ships
  witness + on-demand CS derivation (167 GB vs 397 GB)
- Snapshot tier replaces hot MDBX `PlainAccountState` 23.1 GB +
  `PlainStorageState` 129.5 GB ≈ 152 GB with the
  RecSplit/MPHF-indexed 28 GB

---

## Roadmap

| Phase | Outcome | Status |
|:--|:--|:--|
| Phase A — write specs | this doc + companion docs | ✓ |
| Phase B — single-source archive build | `ethexec` replay → all freezer tables | ✓ in production |
| Phase C — snapshot exporter | `reth-snapshot-export --n42` (accounts 3.92 GB / storage 24 GB) | accounts ✓, storage in progress |
| Phase D — history index | `accthist` / `storhist` / `txindex` | ✓ at 24M blocks |
| Phase E — manifest tool | `n42-eth-manifest` produces per-mode manifests + content-addressed indexes | ✓ |
| Phase F — client snapshot CLI | `n42-eth snapshot {fetch,verify,upgrade,downgrade}` | TODO |
| Phase G — public distribution server | snapshots.n42.io + per-region mirrors | TODO |
| Phase H — delta updates | weekly incremental snapshots since H₀ — see `client-server-sync.md` | TODO |

---

## Companion documents

- `docs/ethel/freezer-tables.md` — every freezer table and what it stores
- `docs/ethel/n42-eth1-freezer-catalogue.md` — current on-disk layout
- `docs/ethel/client-server-sync.md` — sync flow + weekly delta tarballs
- `docs/ethel/archive-reduction-honest-targets.md` — per-component sizes and reduction ladder
- `docs/ethel/state-storage-tiered-design.md` — Erigon-E3-style tiered state plan
- `docs/ethel/history-build-v1-design.md` — cscompact + MPHF+fp history index
- `docs/ethel/rb-5a-compression-results.md` — packed-subkey StoragesTrie (this session)
- `docs/ethel/devlog-eth-el-node.md` — `cmd/eth-el` node architecture
