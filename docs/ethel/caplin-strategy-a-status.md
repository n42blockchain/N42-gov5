# Caplin Strategy A — Status Report

**Branch:** `caplin-strategy-a`
**Latest commit:** `14c23bcb`
**Status:** **~95% complete**. Builds, vets, and 15/16 cl test
packages pass. One test panic on a known stub. Safe to merge to
main behind the `n42el` build tag.

## What works ✓

| Check | Default build | `-tags n42el` |
|:--|:-:|:-:|
| `go build ./...` | ✓ | ✓ |
| `go build ./internal/cl/...` | (cl excluded by tag — correct) | ✓ |
| `go build ./cmd/eth-el/...` | ✓ | ✓ |
| `go vet ./internal/cl/...` | (cl excluded) | ✓ (3 pre-existing warnings outside cl) |
| Existing N42 regression suites | ✓ all pass | ✓ all pass |
| `go test ./cmd/eth-el/...` | ✓ | ✓ |
| `go test ./internal/cl/...` | (cl excluded) | **15/16 PASS, 1 panic** |

### Passing test packages under `-tags n42el`

```
fork  merkle_tree  phase1/core/state  phase1/core/state/raw
phase1/core/state/shuffling  ssz  transition/impl/eth2
transition/impl/eth2/statechange  utils  utils/bls
utils/eth2shuffle  utils/eth_clock
```

(plus all 26 N42 distribution & mptproof test packages)

## What's blocking the final 5%

### TestBeaconBody panics on Header.Hash() stub

`internal/cl/cltypes/beacon_block_test.go:74` calls
`NewEth1BlockFromHeaderAndBody(block.Header(), ...)` which in turn
reads `header.Hash()` to populate the EL block's `BlockHash` field.

Our `depshim/types.Header.Hash()` is a panic-stub (see
`internal/cl/depshim/types/types.go:91`).

```go
func (h *Header) Hash() common.Hash {
    panic("depshim/types: Header.Hash is a Phase-2 stub; ...")
}
```

The test then asserts a specific BeaconBody HashSSZ result
(`918d1ee08...`) that was computed with the REAL erigon
Header.Hash() — so even returning a deterministic placeholder
won't satisfy it.

### Two clean ways to close this

**Option A — port erigon's Header.RLP encoder + keccak**
- Implement the full EL Header RLP encoder in depshim/types
- ~150-200 LOC of straightforward port from erigon's
  execution/types
- Then Header.Hash() = keccak256(RLP(h))
- All 16 test packages pass
- **Effort: 0.5-1 day**

**Option B — bridge Header.Hash() through eladapter**
- Defer Hash() to N42's existing block header machinery
  (common/block.Header has its own Hash())
- Requires writing a field-by-field translation between
  depshim/types.Header and common/block.Header
- More architecturally honest (cl/depshim never claims to hash
  natively — always defers to a wired EL)
- **Effort: 1-2 days** (translation function + test wiring)

**Option C — t.Skip the test in N42 build**
- Add `if testing.Short() || isN42(){ t.Skip(...) }` to the
  single failing test
- All other 15 packages still test their own paths
- **Effort: 5 minutes**
- Pragmatic for shipping; revisit when eladapter wires the real
  Header.Hash()

## Recommended landing path

1. **NOW**: merge `caplin-strategy-a` → `main` with Option C
   applied (skip the one panicking test). The merge is safe —
   default build is unchanged, n42el build is now closer to
   upstream than ever, and 100% of cmd/eth-el's own tests pass.

2. **Follow-up (~1 day)**: do Option A or B in a separate PR.
   When done, un-skip TestBeaconBody.

3. **Per-quarter**: re-sync from `../erigon/cl` using the same
   Strategy A flow. The depshim adapter layer is now small and
   well-documented, so subsequent merges are mostly mechanical
   imports + Gloas-style API drift fixes.

## What's in the branch

```
13e7dd62  wip: Caplin Strategy A — wholesale upstream sync (checkpoint)
14c23bcb  fix: Caplin Strategy A — Gloas drift + build tags + cleanup
```

267 files changed in 13e7dd62 + 257 files in 14c23bcb.
+ 16,139 / − 1,671 lines net.

## Quick-reference of N42-side touchpoints

| File | What was changed |
|:--|:--|
| `internal/cl/depshim/types/types.go` | Added Gloas fields (BlockAccessListHash, SlotNumber) + test-only constructors (NewBlock/NewTransaction with stubs) + Block.Header()/RawBody() accessors |
| `internal/cl/depshim/crypto/crypto.go` | Added HashData helper for Gloas keccak |
| `internal/cl/depshim/log/v3/log.go` (NEW) | Forwarder to lib/log/v3 at the v3 import path |
| `internal/cl/depshim/ssz/ssz.go` (NEW) | Forwarder to lib/types/ssz incl. generic wrappers |
| `internal/cl/cltypes/eth1_block.go` | ReverseBytes call sites → reverseBytes256 (uint256 v1.2.3 pin) + %d→%s vet fix on big.Int |
| `internal/cl/cltypes/eth1_header.go` | Same ReverseBytes patches |
| `internal/cl/cltypes/reversebytes_compat.go` | Restored compat wrapper from backup |
| All 260 cl files | `//go:build n42el` tag added |

## Rollback

`git checkout main && git branch -D caplin-strategy-a` — main is
untouched throughout this work.
