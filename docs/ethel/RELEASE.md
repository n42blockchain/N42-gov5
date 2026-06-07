# N42 eth-el — Snapshot Release

Ethereum-mainnet-compatible execution layer. Pick a tier, download over
BitTorrent, catch up via devp2p, stay live at 12 s/block.

- **Head:** block 25,252,185 · **Txs:** 3,509,958,650 · **State snapshot H₀:** 25,188,781
- Sizes are **composition targets** (post-rescope selectors). The shipped bundle
  is the cold/immutable data only; everything derivable (state from snapshot,
  receipts/changesets/history from witness) is rebuilt locally, not downloaded.
- Tier file selection lives in `cmd/n42-eth-manifest/manifest/selector.go`
  (mobile / minimal / full / archive). Manifests are regenerated from the live
  datadir per release (`n42-eth-manifest` → `n42-eth-snapshot verify`).

## Tiers

| Tier | Download | Bundle | RPC capability |
|:--|--:|:--|:--|
| **mobile** | **~MB (app + config)** | no file bundle — app binary + checkpoint config `{block, hash, IDC URL}` | stateless verify; state via verified IDC proofs + a rolling 900-block in-memory overlay; no historical tx/log locally |
| **minimal** | **~25.7 GB** | compact state snapshot ONLY | tip state RPC (balance/code/storage/call); headers/bodies/codes for snapshot→tip are fetched live, not bundled |
| **full** (EIP-4444 1yr) | **~137 GB** | snapshot + headers + codes + 1-yr hot bodies + 1-yr txindex | minimal + ~1 yr bodies/tx-by-hash; older bodies 1-of-N from cold seeders; receipts/logs serve the latest window |
| **archive** | **~809 GB** | raw materials: headers + bodies + codes + witness + txindex + anchors | replay witness → materialize state + changesets + receipts; `eth_getProof` / state at any height |

> Consensus (Sync-to-tip / Consensus-validation): embedding caplin checkpoint-sync
> adds only **~150 MB runtime** beacon state (fetched from a checkpoint URL, **not**
> in the torrent). It is a code-merge item (#31), not a download-size item — see
> `docs/ethel/caplin-merge-plan.md` (2026-06-06 (c) evaluation). EL tier bundles
> are unaffected by it.

### mobile — stateless witness+anchor verifier (no file bundle)
The phone ships the **app + a checkpoint config blob** (`MobileConfig{IDC,
CheckpointBlk, CheckpointHash, Retention}`); there is nothing to download/seed.
Per block it streams header/body/witness/code from an IDC, recomputes the
stateRoot, and checks it against the ①-trusted header chain (parentHash from the
socially-trusted checkpoint). Each verified block's post-state changeset is
folded into a **rolling 900-block in-memory overlay** (`MobileFollowTick`), so
`MobileBalanceOf` answers from RAM (`source:overlay`) and only falls back to a
verified EIP-1186 proof (`source:proof`) on a miss. Contract bytecode is a small
hot-only LRU (re-fetched from IDC `/code` on eviction). Nothing is written to
disk.

### minimal — snapshot-direct
Compact RecSplit+EF state snapshot at H₀ (≈25.7 GB; **not** a 234 GB MDBX, no
trie). Headers/bodies/codes for the snapshot→tip catch-up are fetched live from
the IDC/peers; older data missing locally is requested from peers on demand.
Serves state RPC at head.

### full — EIP-4444, 1-year window (production recommended)
snapshot + headers + codes + **1 year of hot bodies** (the ~97 GB EIP-4444
window) + 1-yr txindex. Full RPC for ~1 yr; bodies older than the window stay on
cold seeders, fetched 1-of-N on demand via `coldresolve`. Receipts/history are
**not** shipped — they serve the latest window or are rebuilt. EIP-4444 prune
runs on a schedule (`n42-history-expiry --interval 168h`).

### archive — witness-replay (💪)
Ships **raw materials only** — headers + bodies(all) + codes + witness + txindex
+ anchors. State, receipts, changesets, and history are **regenerated locally**
by replaying the witness from genesis (~2,900 blk/s, a few hours) — saving the
~640 GB those derived datasets would otherwise add. Enables `eth_getProof` and
state at any height.

## Components (composition targets)

| Component | Size | mobile | minimal | full | archive |
|:--|--:|:-:|:-:|:-:|:-:|
| anchors (BlockProof) | 5 GB | stream | | | ✓ |
| headers (columnar) | 5 GB | stream | live | ✓ | ✓ |
| state snapshot — accounts (RecSplit+EF) | 4.25 GB | | ✓ | ✓ | rebuilt |
| state snapshot — storage | 21.5 GB | | ✓ | ✓ | rebuilt |
| codes (by codeHash) | 6 GB | stream(hot) | live | ✓ | ✓ |
| bodies — hot 1 yr (EIP-4444) | 97 GB | | | ✓ | — |
| bodies — all | 601 GB | | | cold 1-of-N | ✓ |
| txindex (txhash→block) | 13 GB | | | 1-yr | ✓ |
| witness | 179 GB | stream | | | ✓ |
| receipts | 182 GB | | | latest-window | **rebuilt by replay** |
| history idx (accthist+storhist) | 41.5 GB | | | rebuilt | **rebuilt by replay** |
| changesets (acctcs+storcs) | 435 GB | | | | **rebuilt by replay** |
| **bundle total** | | ~MB | **~25.7 GB** | **~137 GB** | **~809 GB** |

> `full` = 25.7 + 5(headers) + 6(codes) + 97(1-yr bodies) + ~3(1-yr txindex) ≈ 137 GB.
> `archive` = 5 + 601 + 6 + 179 + 13 + 5 ≈ 809 GB; it does **not** download the
> 435 GB changesets, 182 GB receipts, 41.5 GB history, or the state snapshot —
> all rebuilt locally by witness-replay. senders are derivable via ecrecover
> (`ethexec sender-recovery`, ~3 h/16 cores) — optional +38 GB add-on, not shipped.
> The 20 GB full beacon-block archive is a separate optional consensus add-on, not
> part of any EL tier.

## Bootstrap

```bash
# 1. fetch + verify a tier manifest (blake2b/file + sha256 manifest_id; reproduced by N producers)
n42-eth-snapshot fetch  --tier full --src https://snapshots.n42 --out D:/n42
n42-eth-snapshot verify --datadir D:/n42 --manifest D:/n42/manifest-full.json

# 2. download the bundle over BitTorrent (1-of-N seeders, resumable; cold bodies → coldresolve)

# 3a. minimal/full: snapshot-direct — open snapshot @ H0 + warm-tier MDBX for H0+1..tip. NO rebuild.
# 3b. archive: witness-replay → materialize state + changesets + receipts (~几小时, ~2900 blk/s)
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
- `n42-eth-torrent --datadir <d> --manifest <m> [--tracker ..] [--webseed ..] --update-manifest`
  — builds ONE multi-file `.torrent` over a tier's files, streaming piece hashes
  from disk (no in-RAM load); infohash is reproducible (info-dict only), trackers/
  webseeds attach outside it. Writes the magnet + infohash back into the manifest.
- `n42-eth-snapshot {fetch,verify,catch-up,follow,upgrade,downgrade}` — client
- `n42-hist-from-freezer` — N42-native accthist/storhist build (sample-verified)
- `n42-history-expiry --interval 168h` — scheduled EIP-4444 prune
- `coldresolve` — 1-of-N cold-segment fetch (BitTorrent), tested

### Per-tier torrent seed-prep
```bash
n42-eth-manifest --datadir D:/n42-release --mode minimal --network mainnet --height 25252185
n42-eth-torrent  --datadir D:/n42-release --manifest D:/n42-release/manifest-minimal.json \
    --tracker udp://tracker.n42:6969/announce --webseed https://snapshots.n42/mainnet/ \
    --update-manifest        # → minimal.torrent + manifest.torrent{infohash,magnet}
```
**minimal tier built + verified** (D:/n42-release, 23.94 GB / 7 files): infohash
`f644e8ff…`, 49026 pieces @ 512 KiB, round-trips through `metainfo.LoadFromFile`.
full/archive use the same path once their manifests are regenerated.

## Remaining before public release
- regenerate the full/archive manifests from the rescoped selectors (full needs a
  hot-only-bodies datadir) + build their torrents (archive 800 GB+ piece-hash is heavy I/O)
- caplin: produce the `beacon-checkpoint` seed (minimal/full) + extreme-compressed
  `beacon-archive` (archive) so those sections populate (#31)
- manifest signing + hosting

> Web view of this page: `docs/ethel/snapshots/index.html` (reth-snapshots style).
