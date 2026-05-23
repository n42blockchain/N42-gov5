# Caplin Strategy A — Progress Checkpoint

**Branch:** `caplin-strategy-a` (NOT merged into main)
**Date:** 2026-05-22
**Status:** in-progress, mechanical merge mostly done, upstream API drift surfacing

## What's done ✓

1. **Backup** of all N42-local files at `/c/tmp/n42-caplin-backup/`
   (depshim/, eladapter/, kvadapter/, service.go, cltypes/reversebytes_compat.go,
   cltypes/builder.go, transition/impl/funcmap/impl.go)

2. **Wholesale copy** `../erigon/cl/` → `internal/cl/` (558 files → trimmed)

3. **N42-only adapter overlay restored** verbatim:
   - depshim/ (25 files / 1912 LOC) — type aliases for erigon→N42 deps
   - eladapter/ (163 LOC) — execution-layer adapter
   - kvadapter/ (199 LOC) — Caplin-owned MDBX env
   - service.go — service wrapper

4. **ReverseBytes patch** — uint256 v1.2.3 pin preserved:
   - 4 call sites in `cltypes/eth1_block.go` + `eth1_header.go`
   - patched `.ReverseBytes(...)` → `reverseBytes256(..., ...)`
   - compat file `cltypes/reversebytes_compat.go` restored

5. **Drop duplicate** `builder.go` (upstream now defines
   ValidatorRegistration in `cltypes/mev_builder.go`)

6. **Trim 12 erigon-only top packages** N42 never imported:
   ```
   aggregation/ antiquary/ clstages/ das/ gossip/
   persistence/ pool/ rpc/ sentinel/ spectest/ validator/
   p2p/
   ```
   + beacon subdirs (beaconhttp/builder/building/handler/beacontest)
   + phase1/forkchoice, phase1/network, phase1/stages

7. **Drop heavy `execution_client/`** files that depend on erigon's
   execmodule/gointerfaces/txpool/rpc; keep `interface.go` + `types.go`
   so N42's eladapter remains the implementation

8. **Bulk import rewrite** — every erigon path mapped:
   ```
   github.com/erigontech/erigon/cl/X         → internal/cl/X
   github.com/erigontech/erigon/common/X     → internal/cl/depshim/X
   github.com/erigontech/erigon/db/kv*       → internal/cl/depshim/kv
   github.com/erigontech/erigon/execution/*  → internal/cl/depshim/*
   github.com/erigontech/erigon/p2p/event    → internal/cl/depshim/event
   github.com/erigontech/erigon/node/gointerfaces/typesproto
                                              → internal/cl/depshim/typesproto
   ```

9. **New depshim subpackages added**:
   - `depshim/log/v3/log.go` — forwarder to N42's `lib/log/v3`
   - `depshim/ssz/ssz.go` — forwarder to N42's `lib/types/ssz`,
     including generic wrappers (EncodeDynamicList, DecodeStaticList,
     DecodeDynamicList)

10. **Bulk alias** every `import "...depshim/log/v3"` →
    `import log "...depshim/log/v3"` (12 files affected)

11. **File count** trimmed from 558 (erigon raw) → 260 (N42 subset
    + adapters)

## What builds ✓

```
go build -tags n42el ./internal/cl/depshim/...  # ✓ green
go build -tags n42el ./internal/cl/ssz/...      # ✓ green
go build -tags n42el ./internal/cl/cltypes/solid/...  # ✓ green
go build -tags n42el ./internal/cl/merkle_tree/...     # ✓ green
go build -tags n42el ./internal/cl/utils/...           # ✓ green
go build -tags n42el ./internal/cl/monitor/...         # ✓ green
go build -tags n42el ./internal/cl/abstract/...        # ✓ green
go build -tags n42el ./internal/cl/clparams/...        # ✓ green
go build -tags n42el ./internal/cl/fork/...            # ✓ green
go build -tags n42el ./internal/cl/transition/...      # ✓ green
go build -tags n42el ./internal/cl/phase1/core/...     # ✓ green
```

## What's remaining ⚠ (1-3 days est)

### Gloas API drift in cltypes

Upstream master has Gloas (EIP-7928/7843) types on `*types.Header`:
- `SlotNumber`
- `BlockAccessListHash`
- `crypto.HashData` helper

These aren't on N42's `depshim/types.Header`. Two options per call site:
- **(a) Add** the fields to `depshim/types` (pulls in Gloas semantics)
- **(b) Drop** Gloas paths (cltypes/eth1_block.go has the Gloas branches —
  they're likely guarded by fork version, can be left compiled-out)

Locations:
```
internal/cl/cltypes/eth1_block.go:155  header.SlotNumber
internal/cl/cltypes/eth1_block.go:524  crypto.HashData
internal/cl/cltypes/eth1_block.go:526  header.BlockAccessListHash
internal/cl/cltypes/eth1_block.go:528  header.SlotNumber
```

### Other packages not yet built

```
beacon/                        — likely depends on phase1/forkchoice (dropped)
phase1/execution_client        — N42 keeps only interface.go + types.go
phase1/core/state              — interface compatibility with new Caplin
service.go                     — top-level wrapper; needs API alignment
```

### Build-tag hygiene

Erigon's files don't have `//go:build n42el` but N42 requires it.
Currently 260 cl files mixed — need a one-shot sweep:
```
find internal/cl -name "*.go" -not -path "*/depshim/*" \
  -not -path "*/eladapter/*" -not -path "*/kvadapter/*" \
  -exec sh -c 'grep -q "//go:build" {} || sed -i "1i //go:build n42el\n" {}' \;
```

### Integration with cmd/eth-el

After internal/cl/... builds, need:
```
go build -tags n42el ./cmd/eth-el/...
```
plus running the existing test suite. There may be N42-side API drift
in eladapter (`Backend` interface needs methods Caplin now expects).

## Punch list for next session (in order)

1. Resolve Gloas API drift in `cltypes/eth1_block.go` (decide a/b per site)
2. Build cltypes top package green
3. Build beacon (drop refs to removed beacon/handler)
4. Build phase1 fully (skipping the dropped forkchoice/network/stages)
5. Build transition/{impl,machine}
6. Update eladapter Backend interface for new ExecutionEngine methods
7. Compile cmd/eth-el with -tags n42el
8. Run go vet + go test for the n42el path
9. Merge `caplin-strategy-a` → main once green

## Rollback

If we decide to abandon Strategy A:
```bash
git checkout main
git branch -D caplin-strategy-a
```
The main branch is untouched.

## Companion docs

- `docs/ethel/caplin-merge-plan.md` — the original Strategy A plan
- `docs/ethel/devlog-eth-el-node.md` — cmd/eth-el architecture
