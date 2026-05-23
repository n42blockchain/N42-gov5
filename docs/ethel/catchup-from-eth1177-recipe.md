# Catch-up Recipe — D:/N42-eth1177 → tip → 12s live

**Date:** 2026-05-23
**Goal:** Take the existing D:/N42-eth1177 datadir (PlainState at
block 25,101,866 + freezer at item 25,101,867), catch up the ~55K
blocks to current ETH mainnet tip, then 12-second live loop.

## State of D:/N42-eth1177 (verified 2026-05-23)

```
mdbx.dat (278 GB)
  Account                 386,292,128 entries / 24.1 GB
  Storage               1,570,290,387 entries / 125.9 GB
  Code                      2,368,843 entries / 15.7 GB
  TrieOfAccounts/Storage     30,264 each / 118.9 MB each
  HashedAccount/Storage         0 (HPH hashing pass crashed —
                                  not needed for live sync)
  DbInfo                          1 entry

chain/freezer
  acctcs.cidx + .NNNN.cdat   25,101,867 items (blocks 0..25,101,866)
  storcs.cidx + .NNNN.cdat   25,101,867 items
  witness.cidx + .NNNN.cdat  25,101,867 items
  senders.cidx + .NNNN.cdat  25,101,867 items
```

## Progress markers

```
DbInfo/ethel_progress              = 12,501,823  ← STALE (rebuild crashed)
SyncStageProgress/ethel-last-block = 25,101,866  ← AUTHORITATIVE (executor wrote per-batch)
```

The stale `DbInfo/ethel_progress` was a known footgun in
`cmd/ethexec rebuild-state` auto-resume — fixed in this commit
to prefer the larger of the two markers + log a Warn on
mismatch.

## What "ready" means here

- PlainState IS at block 25,101,866 (matches freezer last block)
- The ~50% appearance from DbInfo/ethel_progress was a stale-marker
  illusion; actual state is complete
- No further RebuildState run needed before catch-up
- Trie tables (HashedAccount/Storage) are empty — fine for execution
  (Engine API doesn't require them; only HPH state-root verify does)

## Catch-up: 25,101,866 → tip (~25,156,141 = ~55K blocks)

```bash
# Build cmd/eth-el with embedded Caplin (n42el tag)
cd C:/N42/N42-gov5
go build -tags n42el -o /c/tmp/eth-el.exe ./cmd/eth-el

# Start eth-el against the existing datadir
/c/tmp/eth-el.exe \
    --datadir D:/N42-eth1177 \
    --bootstrap.enabled=false \           # PlainState already populated
    --caplin.enabled \
    --caplin.network mainnet \
    --caplin.checkpoint.url <BEACON_URL>  \  # operator-supplied
    --catch-up.mode auto                  # G4 strategy selector
```

eth-el will:
1. Open the rebuilt PlainState (bootstrap skipped)
2. Caplin checkpoint-syncs the beacon chain side
3. Engine API receives `engine_newPayload` for blocks
   25,101,867..tip
4. Each payload runs EVM, mutates PlainState, advances
   SyncStageProgress
5. At tip → 12-second slot live loop

## Time estimate for catch-up

55K blocks × ~3-5 s EVM exec (mainnet post-merge density) ≈
3-5 hours on the reference machine, dominated by EVM. Bodies
likely fetched from peers in parallel.

## Interrupt-safe restart

The executor writes `SyncStageProgress/ethel-last-block` per
batch (atomic with freezer commits per `output_batcher.Sync`).
On ctrl-C or crash:
- Last few batches up to the most recent durable checkpoint
  are kept
- Restart eth-el with the same flags → resumes from
  SyncStageProgress + 1

NOTE: cmd/eth-el's own progress reading (NOT cmd/ethexec's)
needs to be audited similarly. The bootstrap service detects
"already populated" but the catchup service's resume semantics
should be verified before relying on automatic continuation.

## Companion docs

- `docs/ethel/rebuild-state-resume-recipe.md` — RebuildState
  resume mechanics (separate flow from this catch-up)
- `docs/ethel/real-chain-three-mode-runbook.md` — three-mode
  bootstrap design
- `memory/project_eth_el_bootstrap_paths.md` — terminology
- `cmd/n42-read-ethel-progress` — helper to inspect markers
