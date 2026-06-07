# N42 eth-el — Snapshot Release

Ethereum-mainnet-compatible execution layer. Pick a tier, download over
BitTorrent, catch up via devp2p, stay live at 12 s/block.

- **Head:** block 25,252,185 · **Txs:** 3,509,958,650 · **State snapshot H₀:** 25,188,781
- Sizes are **measured on-disk** (shipped = zstd, no local intermediates).
- All four tier manifests are **built and verified** (`n42-eth-snapshot verify`,
  re-hash every file, 0 mismatch).

## Tiers

| Tier | Download | Files | manifestID | RPC capability |
|:--|--:|--:|:--|:--|
| **mobile** | **4.62 GB** | 5 | `8524ab26…415` | stateless verify; state via verified IDC proofs; no historical tx/log locally |
| **minimal** | **34.13 GB** | 16 | `0cb9b1a2…0ae` | tip state RPC (balance/code/storage/call); no historical bodies |
| **full** (EIP-4444 1yr) | **828.66 GB** | 489 | `1030ae62…c84` | minimal + ~1yr bodies/receipts/logs/tx-by-hash; older bodies 1-of-N from cold seeders |
| **archive** | **1000.29 GB** | 583 | `e22824b4…b54` | full + witness → replay any block, eth_getProof at any height, on-demand changeset/state |

> `manifestID` = sha256 of the sorted file list (blake2b-256 per file inside).
> Full lists: `D:/n42-release/manifest-{mobile,minimal,full,archive}.json`.

### mobile — stateless witness+anchor verifier
Local data is ONLY the signed anchors (BlockProof trust roots, 4.62 GB). Headers,
witness, and code are **streamed from an IDC per block**, the stateRoot is
recomputed and checked against an anchor via the parentHash chain. Account/storage
queries are served by **verified IDC proofs** (`VerifyAccountInclusion` against the
trusted stateRoot). Caches the most recent ~900 blocks + their updated state.

### minimal — snapshot-direct
Compact RecSplit+EF state snapshot at H₀ (≈25.7 GB; **not** the 234 GB MDBX, no
trie) + headers + codes. Serves state RPC at head; no historical bodies.

### full — EIP-4444, 1-year window (production recommended)
minimal + **1 year of bodies** (the 96 GB hot window) + receipts + history
indexes + txindex. Full RPC for ~1 yr; bodies older than the window stay on cold
seeders, fetched 1-of-N on demand via `coldresolve`. EIP-4444 prune runs on a
schedule (`n42-history-expiry --interval 168h`).

### archive — witness-replay (💪)
Ships the **witness stream (184 GB)** instead of changesets (435 GB) — **−250 GB**.
Replay the chain from witness in a few hours (~2,900 blk/s) to materialize state +
changesets + receipts on demand, enabling `eth_getProof` and state at any height.

## Components (measured, shipped/zstd)

| Component | Size | mobile | minimal | full | archive |
|:--|--:|:-:|:-:|:-:|:-:|
| anchors (BlockProof) | 5.0 GB | ✓ | | | ✓ |
| headers (columnar) | 4.9 GB | stream | ✓ | ✓ | ✓ |
| state snapshot — accounts | 4.25 GB | | ✓ | ✓ | ✓ |
| state snapshot — storage | 21.5 GB | | ✓ | ✓ | ✓ |
| codes (by codeHash) | 6.0 GB | | ✓ | ✓ | ✓ |
| bodies — hot 1 yr | 91.5 GB | | | ✓ | — |
| bodies — all | 616 GB | | | cold 1-of-N | ✓ |
| receipts | 182 GB | | | ✓ | ✓ |
| history idx (accthist+storhist) | 41.5 GB | | | ✓ | ✓ |
| txindex (txhash→block) | 13.2 GB | | | ✓ | ✓ |
| witness | 184 GB | stream | | | ✓ |
| changesets (acctcs+storcs) | 435 GB | | | | **rebuilt by replay** |

> archive does **not** download the 435 GB changesets or the 234 GB state MDBX —
> both are rebuilt locally by witness-replay. senders are derivable via ecrecover
> (`ethexec sender-recovery`, ~3 h/16 cores) — optional +38 GB add-on, not shipped.

## Bootstrap

```bash
# 1. fetch + verify a tier manifest (blake2b/file + sha256 manifest_id; reproduced by N producers)
n42-eth-snapshot fetch  --tier full --src https://snapshots.n42 --out D:/n42
n42-eth-snapshot verify --datadir D:/n42 --manifest D:/n42/manifest-full.json

# 2. download the bundle over BitTorrent (1-of-N seeders, resumable; cold bodies → coldresolve)

# 3a. minimal/full: snapshot-direct — open snapshot @ H0 + warm-tier MDBX for H0+1..tip. NO rebuild.
# 3b. archive: witness-replay → materialize state + changesets (~几小时, ~2900 blk/s)
n42 ethexec --witness D:/n42/witness --bodies D:/n42/bodies --datadir D:/n42 --workers 32

# 4. catch up via devp2p, then stay live at 12 s/block
n42-eth-snapshot catch-up --datadir D:/n42
n42 --syncmode full --el.devp2p
#    catchup ~270 Mgas/s → live 12 s; new blocks → MDBX warm overlay above the cold snapshot
```

## Live model
Cold base = immutable snapshot/freezer; every 12 s block writes to the **MDBX warm
overlay** on top. Reads consult overlay → cold. EIP-4444 periodically prunes the
hot-bodies window and relocates aged segments to cold seeders (1-of-N).

## Tooling
- `n42-eth-manifest --mode {mobile,minimal,full,archive}` — producer manifest builder
- `n42-eth-snapshot {fetch,verify,catch-up,follow,upgrade,downgrade}` — client
- `n42-hist-from-freezer` — N42-native accthist/storhist build (sample-verified)
- `n42-history-expiry --interval 168h` — scheduled EIP-4444 prune
- `coldresolve` — 1-of-N cold-segment fetch (BitTorrent), tested

## Remaining before public release
- per-tier `.torrent`/magnet seed-prep (`n42-cold-seed`; archive 850 GB+ piece-hash is heavy I/O)
- manifest signing + hosting
- receipts compression (182 GB → ~63 GB target, batch-64 zstd)

> Web view of this page: `docs/ethel/snapshots/index.html` (reth-snapshots style).
