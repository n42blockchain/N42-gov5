# Phase H — Delta Updates (Weekly Incremental Snapshots)

**Status:** Design + skeleton tool (`cmd/n42-eth-delta-build`).
First-class follow-on to Phase E (manifest) and Phase F (client CLI).

## Why

A full `archive` download is **849 GB**. At 100 Mbit/s that's
~18 hours; at 1 Gbit/s it's ~2 hours. Users who already have an
archive at height H₀ want to step forward to H₀+1w (one week of
new blocks) without re-downloading the 849 GB they already have.

Empirical compression spikes confirm that encoding-level tweaks on
bodies / witness / trie nodes give single-digit percent savings —
the **real** lever is _not redownloading what you already have_.

Goal: a client at any height H₀ can move to the latest published
release H₁ by downloading **only the new blocks plus any updated
snapshot sections**, never the full archive.

## Definitions

A **delta** is the file-level difference between two manifests:
- `manifest-archive-25100000.json` (baseline release)
- `manifest-archive-25200000.json` (next release)

A file is in the delta iff either:
1. It exists in the new manifest but not in the old (a new freezer
   `.cdat` segment, a new snapshot file), OR
2. It exists in both but with a different `blake2b256` (a mutated
   tip file).

For N42's append-only freezer (`headerc`, `bodyc`, `receipts`,
`witness`, `senders`, `accthist`, `storhist`, `txindex`,
`codes`), category 1 dominates: most existing `.cdat` files are
immutable past their rotation boundary. The exceptions are:
- the **head `.cdat`** of each table (still being appended to)
- the table's `.cidx` (grows with every new item)

Snapshot tier (RecSplit MPHF, Elias-Fano) is rebuilt at each
release and lands in category 2 — those files always change.

## Expected delta sizes

For an `archive` archive growing 1 week (~50K blocks):

| Section | New bytes |
|---|--:|
| `bodyc` rotated `.cdat`s | ~1.0 GB  (50K blocks × ~20 KB/block) |
| `headerc` rotated `.cdat`s | ~12 MB |
| `receipts` rotated `.cdat`s | ~125 MB |
| `witness` rotated `.cdat`s | ~330 MB |
| `accthist`/`storhist` rebuilt (1 M block segment) | ~80 MB |
| `txindex` rebuilt segment | ~25 MB |
| `codes` updated `.cdat` (new contracts only) | ~5 MB |
| snapshot/accounts.* | ~3.9 GB (full rebuild — see RFC notes) |
| snapshot/storage.* | ~24 GB (full rebuild — see RFC notes) |
| `manifest-*.json` | < 1 MB |
| **Worst case (with full snapshot rebuild)** | **~30 GB** |
| **Best case (segment-incremental snapshot)** | **~2 GB** |

Worst case requires re-downloading both snapshot tier files — they
are RecSplit MPHFs rebuilt from scratch per release.

Best case requires either:
- the snapshot to be split into 1-M-block segments, like the
  history index already is (`docs/ethel/history-build-v1-design.md`),
  or
- a content-addressed segment scheme so old segments are reused

The history-build v1 spec already publishes per-segment files. We
extend the same per-segment shape to snapshot accounts/storage in
Phase H.2.

## Wire format

A delta release is a directory tree mirroring the source archive,
containing **only the changed/new files** plus a delta manifest:

```
delta-archive-H₀+1w/
├── chain/
│   └── freezer/
│       ├── bodyc.0335.cdat                       ← rotated, new
│       ├── bodyc.0336.cdat                       ← head (truncated)
│       ├── bodyc.cidx                            ← always changes
│       ├── headerc.*.cdat / cidx                 ← same shape
│       ├── witness.*.cdat / cidx
│       └── …
├── snapshot/
│   ├── accounts.25100000-25199999.{idx,ef,val.zst}
│   └── storage.25100000-25199999.{idx,ef,val.zst}
└── delta-manifest-archive.json
```

The **delta manifest** lists the same files as a normal manifest
plus a `from_height` field pointing at the baseline release:

```json
{
  "network": "mainnet",
  "from_height": 25100000,
  "to_height":   25200000,
  "mode":        "archive",
  "based_on_manifest_id": "<sha256 of the baseline manifest>",
  "created_at": "2026-05-29T00:00:00Z",
  "manifest_id": "<sha256 of this delta's file list>",
  "files": [ { … same FileEntry shape … } ]
}
```

The `based_on_manifest_id` is the integrity anchor: the client
refuses to apply a delta unless its current archive's manifest_id
matches the delta's `based_on_manifest_id`.

## Client flow

```
$ n42-eth-snapshot mode --datadir /var/lib/n42
mode=archive  height=25100000  intact=true

$ n42-eth-snapshot delta apply \
    --source https://snapshots.n42.io/mainnet \
    --datadir /var/lib/n42
detected current height=25100000
fetching delta-archive-25200000.json
delta from 25100000 to 25200000 (mode=archive)
  files to fetch  : 412
  bytes to xfer   : 2.13 GB
fetching 412/412 ...
verifying …
moving into place atomically
✓ updated archive to height=25200000  intact=true
```

The client:
1. Reads its own `manifest-archive.json`, gets the current
   `manifest_id` and `height`.
2. Fetches `delta-archive-<latest>.json` from the publisher.
3. Validates the delta is applicable: `based_on_manifest_id`
   matches the local manifest_id. If not, falls back to a full
   `fetch` of the target tier.
4. Downloads + verifies each delta file (same blake2b path as
   regular fetch).
5. Replaces the local manifest with the new one atomically.

## Server pipeline

```
ethexec extend (1 week of new blocks)
   → archive grows; new .cdat files written; head .cdat extended
   → snapshot exporter re-runs (or runs only for changed
     account/storage segments — Phase H.2)
   → n42-eth-manifest --mode archive   → manifest-archive-H₁.json
   → n42-eth-delta-build --from H₀ --to H₁ --mode archive
        compares two manifests, copies/links only changed files
        into a delta-H₀-H₁/ tree, writes delta-manifest-*.json
   → publish to snapshots.n42.io/<network>/<H₁>/
```

## Phase H sub-tasks

| Sub | Outcome | Effort | Status |
|---|---|---|---|
| **H.1 delta builder** | `cmd/n42-eth-delta-build` compares two manifests and produces a delta tree + delta-manifest | 3-5 days | **skeleton ✓** (this PR) |
| **H.2 delta client** | `n42-eth-snapshot delta apply` flow in the existing CLI | 3-5 days | TODO |
| **H.3 segment-incremental snapshot** | snapshot/accounts.* split into 1-M-block segments so unchanged segments are reused across releases | 1-2 weeks | TODO — needs RecSplit segment design |
| **H.4 publication tooling** | upload to S3/CDN, prune old deltas per retention policy | 1 week | TODO |
| **H.5 client release cadence** | weekly snapshot rollover by default; semver for breaking-format changes | ongoing | TODO |

## Open design questions

1. **How many deltas to keep on the server?**
   Probably K rolling weekly releases + monthly anchor. If a
   client's H₀ is older than the oldest cached delta, they fall
   back to full-fetch.

2. **Should we chain deltas?**
   A client at H₀=25M wanting H₃=25.3M could either:
   (a) download one delta `25M → 25.3M` (publisher pre-merges),
   or (b) apply three deltas `25M → 25.1M → 25.2M → 25.3M`.
   (a) is simpler for the client but means more server-side work
   (publish per-anchor deltas as well as per-week). Likely (a).

3. **Snapshot tier handling.**
   The accounts/storage snapshot files are large (~28 GB combined)
   and currently rebuilt monolithically per release. Without H.3,
   every delta carries the full snapshot. With H.3, only changed
   account/storage **segments** ship. The size win is huge but
   touches the snapshot writer (`cmd/reth-snapshot-export --n42`).

4. **Cross-mode deltas?**
   Should a `delta-archive-*.json` carry the files for `full`
   and `minimal` too, with a per-mode subset?  Or one delta per
   mode? One-per-mode is simpler and matches the per-mode
   manifest contract.

## Companion documents

- `docs/ethel/n42-eth-client-distribution.md` — minimal/full/archive spec
- `docs/ethel/client-server-sync.md` — overall sync flow (incl. weekly delta tarballs)
- `docs/ethel/archive-reduction-honest-targets.md` — per-component sizes
