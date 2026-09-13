# Native asset module: USDT transfer without the interpreter (2026-09-12)

Low-priority experiment. The question from the stablecoin study
(`cmd/stablecoin-stats`): pure USDT/USDC transfers are 19.1% of mainnet
transactions over the last year, and an isolated benchmark put the interpreter
at 15-30 us per transfer. What does executing USDT's `transfer` natively buy in
a real replay, and can it be made exactly equivalent?

Scope as decided: USDT only (69% of pure transfers, no proxy), built as a
native asset module behind a fork-time switch rather than a dedicated
transaction type, with a full-year differential test.

## What was built

| Piece | Where |
|---|---|
| Module | `internal/vm/native_asset.go` |
| Hook | `internal/vm/evm.go` — `typ == CALL && rules.IsNativeAsset && addr == USDT`, in place of `run` |
| Switch | `ChainConfig.NativeAssetTime` / `Rules.IsNativeAsset` (N42 extension; nil on every Ethereum chainspec) |
| Replay flag | `witness-replay --native-asset off\|shadow\|on` |
| Tests | `internal/vm/runtime/native_asset_test.go`, real bytecode in `internal/vm/testdata/usdt_runtime.hex` (keccak-checked) |

The module is bytecode-equivalent: same state, logs, refund counter, access
list, remaining gas **and state-read order** as the TetherToken bytecode. So
enabling it changes no consensus result, and it applies at any call depth, not
only to top-level transactions. Anything outside the modeled shape — paused,
blacklisted, deprecated, fee enabled, insufficient balance, overflow, odd
calldata, value attached, static context, tracer, not enough gas — goes to the
interpreter.

## Two equivalence conditions that are not obvious

The first version compared gas, storage and logs, passed its unit tests, and
wedged the replay on the first block.

1. **Read order is consensus-adjacent.** A witness stream
   (`internal/ethel/witness.go`) is one `[len][value]` entry per read against
   the underlying state reader, in execution order, with no keys. The replay
   serves reads by position. The module skipped `maximumFee` (its value cannot
   matter with a zero fee), so its fifth read returned maximumFee's zero as the
   sender balance, and every later value in the block shifted — surfacing as
   `nonce too high ... state 0` on an unrelated transaction. Any path that
   replaces EVM execution (precompile, native module, parallel executor) must
   reproduce the first-load order, including loads whose values it ignores.
2. **A hand-back must have read only a prefix of what the interpreter reads —
   gas included.** A gas-limited inner call may run out after three slots in the
   recorded execution. A module that loads all seven and then decides gas is
   short has consumed four entries the witness never had. The fix charges the
   bytecode's static gas segment by segment (measured per opcode with a tracer:
   `[654 201 62 184 277 230 193 101 207 1892]`, segment 4 is 211 for a zero
   amount) plus the exact SLOAD/SSTORE costs, and stops before any load the
   interpreter would not reach.

Neither shows up in a test that compares outcomes only. The tests now wrap the
state reader and compare the read sequence, and sweep every gas value from 0 to
what each transfer needs (≈142k values). Both checks were confirmed to fail on
the version before each fix.

## Full-year differential test

`witness-replay --native-asset shadow` over 2025-09-10 .. 2026-09-10: for every
call the module accepts it runs the module, records the effects, rolls back,
runs the interpreter, records again and compares; the interpreter's result is
kept.

| | |
|---|---|
| blocks | 23,329,031 .. 25,943,310 (2,614,280) |
| transactions | 743,490,150 (matches `stablecoin-stats` exactly) |
| replay | 46m40s, `failed=0` (GasUsed + ReceiptHash per block) |
| **calls compared** | **219,938,560** |
| **mismatches** | **0** |
| handed back: shape (other selectors, value, static, calldata) | 75,715,514 |
| handed back: balance / overflow | 360,087 |
| handed back: interpreter would run out of gas | 161,506 |
| handed back: paused / blacklisted / deprecated / fee | 76,216 |

219.9M accepted calls is 2.3x the 97.5M top-level pure USDT transfers: most
USDT transfers happen inside routers, wallets and bridges, and the module takes
those too.

## CPU A/B

Same binary, the latest 200,000 blocks (25,743,311 .. 25,943,310, 57,853,388
txs), 30 workers, `off` and `on` alternated twice. CPU-seconds are the measure,
not wall clock.

| mode | run 1 | run 2 | mean CPU-s | within-mode spread |
|---|---|---|---|---|
| off | 6,893.7 | 6,909.5 | 6,901.6 | 0.23% |
| on | 6,809.1 | 6,814.5 | 6,811.8 | 0.08% |

**−89.8 CPU-s, −1.30%** (paired: −84.6 and −95.0, about 6x the spread); wall
clock 228.8 s → 225.5 s. About 16.7M calls were taken in the window, so the
real saving is ~5.4 us per call — a third of the isolated benchmark's 15.3 us.
The replay's interpreter is already cheaper per call (JUMPDEST cache, warm
state caches), and the module's plan is not free (three keccaks, ordered reads,
closures).

## Assessment

- Exactness: demonstrated on a year of mainnet at every call depth.
- Benefit on eth-el replay: ~1.3% CPU for USDT alone. USDC would add less than
  proportionally (proxy + delegatecall make its path longer, but it is 31% of
  pure transfers).
- The real prize remains on the n42 native chain, where a native asset does not
  have to reproduce bytecode gas or read order: pre-declared read/write sets for
  scheduling, a two-slot witness, compact encoding. That is a consensus change
  and a separate decision.
- Plan overhead is the obvious next lever if this is pursued: the closures and
  per-call keccaks of sender/recipient keys are a visible share of the 5.4 us.
