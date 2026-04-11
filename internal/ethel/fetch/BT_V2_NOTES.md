# BT v2 (BEP 52) support — evaluation

Reference for future work on Step 8. This file documents what it would
take to light up `SourceBTV2` in `TorrentFetcher.Kinds()` and why we
have not done it yet.

## Current state

```
go.mod:
    github.com/anacrolix/torrent v1.61.0
    replace github.com/anacrolix/torrent => github.com/erigontech/torrent v1.54.2-alpha-8
```

The repo is pinned to Erigon's fork `v1.54.2-alpha-8` (approximately
late-2023 upstream). BT v2 / BEP 52 support in the upstream anacrolix
library stabilised around `v1.50` and was fully usable by `v1.55`. The
Erigon fork stopped tracking upstream before that.

## What BT v2 actually unlocks

For eth-el the benefits are modest but real:

1. **SHA256 piece hashes** instead of SHA1. Relevant only for content
   integrity paranoia — HTTPFetcher already hashes the full file, so
   this is belt-and-braces.
2. **Merkle-tree piece layout** — resumable downloads can verify any
   piece without the whole `pieces` array. Not a win for a single-file
   asset, where we already verify the full file at the end.
3. **Hybrid v1+v2 magnets** — a single magnet link can advertise both
   `urn:btih:` (v1) and `urn:btmh:` (v2), so one swarm serves every
   client generation. **This is the main reason to care** — public
   seeders are starting to publish hybrid magnets exclusively.

## What it costs to land it

1. **Unpin `anacrolix/torrent`** from the Erigon fork. Upstream
   `v1.61.0` (the module declaration already targets this) is the
   natural landing zone. Risk: the Erigon fork likely has private
   patches. Audit with `git log` on the fork repo before swapping.

2. **Audit every importer of `anacrolix/torrent`** in this repo:

   ```
   internal/distributed/storage/torrent/*.go   (5 files, the client
                                                 wrapper we use here)
   internal/ethel/fetch/torrent_fetcher*.go    (our new code)
   lib/downloader/**/*.go                      (~20 files, Erigon's
                                                 snapshot downloader
                                                 — LARGE surface)
   ```

   `lib/downloader` is the blast radius. It is Erigon's legacy
   snapshot pipeline: `mainloop.go`, `downloader.go`,
   `downloader_grpc_server.go`, `webseed.go`,
   `mdbx_piece_completion.go`, etc. It is deeply tied to the
   `v1.54.2-alpha-8` API surface — piece completion tracking,
   metainfo tracing, stats. An API change in
   `torrent.ClientConfig` or `torrent.Torrent` ripples through every
   file.

3. **Rewrite or retire `lib/downloader`.** Three options:

   a. **Lift and shift**: fix every compile error in `lib/downloader`
      after unpinning. Realistic effort: 1–2 days for a focused
      engineer who knows the package.

   b. **Delete `lib/downloader` entirely.** It is Erigon-snapshot
      infrastructure that N42 does not use at runtime (only `cmd/n42`
      imports it — and only from `internal/node/node.go` stage
      plumbing). If N42 has its own segment distribution (OtterSync,
      CAS bridge), this package is dead weight. **This is the highest-
      value cleanup.** Worth checking usage count before upgrading.

   c. **Vendor a second torrent client.** Add a new dependency on
      `github.com/anacrolix/torrent v1.61.0` under a different import
      alias and use it only in `internal/ethel/fetch`. Two clients in
      one binary is a waste of DHT sockets and memory but
      architecturally isolates the upgrade. Not recommended unless (a)
      and (b) are both infeasible.

4. **Add `SourceBTV2` to `TorrentFetcher.Kinds()`**. Trivial once the
   client actually supports v2.

## Recommendation

**Do not upgrade BT v2 in isolation.** Pair it with a decision on
`lib/downloader`:

- If `lib/downloader` is unused at runtime, delete it first and then
  the upgrade is a 30-line diff.
- If `lib/downloader` is still live, escalate the upgrade to a proper
  project: pin upstream, rewrite the fork-specific parts, run the
  existing snapshot-download integration tests.

Until then, BT v1 magnets with `ws=` WebSeed fallback cover 95 % of
the "serve N42 assets over BT" use case. anacrolix v1.54 already
drains WebSeeds automatically (BEP 19), so manifests can advertise
both BT v1 magnets AND HTTPS mirrors in the same `Sources` list and
the `MultiSourceFetcher` will fall through correctly — BT v2 is not on
the critical path for eth-el to ship.

## Pre-flight check — results (2026-04-10)

```bash
grep -rln "lib/downloader" cmd/ internal/ --include="*.go"
# (no results)
```

**`lib/downloader` has zero callers outside of itself.** Every file
that imports it is inside `lib/downloader/**` or `lib/chain/snapcfg/`
(which is only imported by `lib/downloader` — the two form a
self-contained island). Neither `cmd/n42`, `cmd/ethexec`, `cmd/eth-el`,
`internal/node`, nor any other live code path uses it at runtime.

This makes **option (b) trivial**: the entire `lib/downloader` tree
plus `lib/chain/snapcfg` can be deleted with a `rm -rf`, and the only
import cycle to break is `internal/distributed/storage/torrent/` which
already has its own anacrolix client wrapper that does not touch
`lib/downloader`.

**Revised recommendation:** when BT v2 becomes a hard requirement,
execute in this order:

1. `rm -rf lib/downloader lib/chain/snapcfg`
2. In `go.mod`, remove the `replace github.com/anacrolix/torrent =>
   github.com/erigontech/torrent` line
3. `go mod tidy` — upstream `v1.61.0` takes over
4. Fix any API drift in `internal/distributed/storage/torrent/client.go`
   (the wrapper surface is ~200 lines; expect a handful of signature
   changes)
5. Add `SourceBTV2` to `TorrentFetcher.Kinds()` and extend
   `normaliseMagnet` to recognise `urn:btmh:` magnets

Realistic effort after (1): **half a day**. The blast radius turned
out to be far smaller than initially feared.

Still **not doing it now** because:
- No published N42 asset currently uses a v2-only magnet
- BT v1 + WebSeed + HTTPS mirrors cover every test scenario
- Removing `lib/downloader` is a visible refactor that deserves its
  own commit and PR review, not a drive-by inside a fetcher change
