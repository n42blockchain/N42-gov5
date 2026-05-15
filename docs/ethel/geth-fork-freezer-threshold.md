# Patched geth at C:\n42\geth — freezer threshold + offline migrate

We maintain a forked geth at `C:\n42\geth` (branch `master`, parent v1.17.4-unstable @ `31bb68099`). Two patches on top, both 2026-05-15.

## bdd27b47c — decouple FreezerThreshold from FullImmutabilityThreshold

`params/network_params.go` now exports a separate `FreezerThreshold = 1024` (≈3.4 h on mainnet), used **only** by `core/rawdb/chain_freezer.go:freezeThreshold`. `FullImmutabilityThreshold` stays at 90000, so:

- `pathdb.StateHistory` window — unchanged (RPC historical state queries keep their 90 K-block budget)
- `core/blockchain.go` reorg limit — unchanged
- `eth/downloader.fullMaxForkAncestry` — unchanged
- `consensus/clique` snapshot trust — unchanged (mainnet doesn't use clique anyway)

What changes: `chainFreezer.freeze()` migrates blocks at `head - 1024` instead of `head - 90000`. Ancient tracks head much more tightly so offline consumers (N42 sender-recovery / columnar build / history segments) only lag ~1 K blocks instead of 90 K. 1024 is ~16× past PoS finality (64 blocks), safe.

## eb8584fc4 — geth db migrate-to-ancient offline subcommand

```powershell
geth.exe --datadir D:\geth db migrate-to-ancient --to <BLOCK> [--batch 1024]
```

Reads canonical hash + header/body/receipts for each block in `(db.Ancients(), --to]` from the pebble key-value store and appends to the ancient freezer via `ModifyAncients`. Same logic as `chainFreezer.freezeRange`, but runnable offline without starting the full node. Useful when:

- state sync is broken or no CL is connected (so geth itself won't start a normal freeze loop)
- you want to extend the ancient past the running threshold for offline tooling

Resume-safe: starts at the freezer's current item count.

**Implementation gotcha**: uses a local `kvOnlyReader` struct embedding `ethdb.KeyValueStore` and stubbing the ancient methods, so `rawdb.ReadCanonicalHash` / `ReadHeaderRLP` / etc inside the `ModifyAncients` write-locked closure don't try to take `freezer.writeLock.RLock()` (RLock-under-Lock on the same goroutine is a permanent deadlock; Go's `sync.RWMutex` isn't reentrant). Mirrors the unexported `rawdb.nofreezedb` that `chain_freezer.go` uses for the same reason.

## Build & operate

```powershell
cd C:\n42\geth
go build -trimpath -o build\bin\geth.exe .\cmd\geth
# Output: C:\n42\geth\build\bin\geth.exe — 105 MB

# Run with patched freezer threshold
C:\n42\geth\build\bin\geth.exe --datadir D:\geth --syncmode snap --cache 8192

# Force-extend ancient without starting the node
C:\n42\geth\build\bin\geth.exe --datadir D:\geth db migrate-to-ancient --to 25101866
```

## Caveats

- **Build collision on Windows**: rebuilds sometimes leave the old binary as `geth.exe~` and silently skip writing the new `geth.exe` (AV/indexer holding a handle). After a rebuild, always check `Get-Item C:\n42\geth\build\bin\geth.exe` mtime; if missing or stale, kill any geth process and re-run `go build`.

- **The genesis-state-missing wedge**: a half-completed snap sync left at `D:\geth` (May 5) wedged the new binary on startup with `Fatal: missing trie node ... state 0xd7f8974f...` — that's the mainnet genesis state root. `txpool.New` falls back to `chain.StateAt(genesis)` when head state is unavailable, but pathdb without completed snap-sync has no genesis state either, so both fail. Recovery on 2026-05-15:
  1. Move pebble files (1416 entries, 16 GB) from `D:\geth\geth\chaindata\` to `chaindata\pebble.bak\`, leaving `ancient/` intact.
  2. Restart geth → `InitDatabaseFromFreezer` rebuilds pebble from ancient.
  3. **But that nuked all blocks 25,011,748..25,101,866 that were in pebble's hot zone.**
  4. Restored pebble.bak to keep that data.
  5. Did NOT restart geth (pebble would still wedge on the txpool genesis check).
  6. Used the new `migrate-to-ancient` subcommand to extend ancient to 25,101,866 directly. ~2080 blk/s, 43 s for 90 K blocks.

Related: [n42-eth1-freezer-catalogue.md](n42-eth1-freezer-catalogue.md) catalogues the resulting D:\n42-eth1 derived data (freezer table / address-indexed codes / N42 columnar / cscompact RecSplit formats). The geth ancient itself remains read-only by hard rule for any non-geth process.
