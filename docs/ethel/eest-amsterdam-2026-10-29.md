# EEST Amsterdam results — 2026-10-29

Phase D2 of `eip-gap-2026-10-29.md`: the Glamsterdam (Amsterdam) EIPs checked against the
Amsterdam execution-spec-tests fixtures and ported from go-ethereum where N42 was missing them.
ePBS (EIP-7732) is out of scope.

## Fixtures

| Archive | Release | sha256 |
|---|---|---|
| `fixtures.tar.gz` | EEST `tests@v21.0.2` | `e2fa7ea2ea516459f9ab92284b2927892f318f4e1f78ca802cb05a6c3b479ed9` |

Extracted to `D:\eest\v21.0.2\extract\fixtures` (outside the repository). The Amsterdam fixtures
are the `for_amsterdam/` trees of `state_tests`, `blockchain_tests` and `blockchain_tests_engine`:
the new `amsterdam/eip*` directories plus every older test re-filled for the Amsterdam network
(`legacy:*` rows below).

## How to run

The runners are the ones described in `eest-results-2026-10-29.md` (opt-in through
`N42_EEST_DIR`); the Amsterdam network maps to `GlamsterdamTime` in `tests/eest/forks.go`.

```sh
# state tests — ~15 s after the build
N42_EEST_DIR='D:\eest\v21.0.2\extract\fixtures' N42_EEST_FORKS=Amsterdam \
  N42_EEST_FILTER=for_amsterdam/ GOMAXPROCS=8 \
  go test -p 2 -count=1 -tags nosqlite,noboltdb -run TestEESTStateTests ./tests/

# blockchain + engine tests — ~95 min with 4 workers (blockchain alone ~47 min)
N42_EEST_DIR='D:\eest\v21.0.2\extract\fixtures' N42_EEST_FORKS=Amsterdam \
  N42_EEST_FILTER=for_amsterdam/ N42_EEST_WORKERS=4 GOMAXPROCS=8 \
  go test -p 2 -count=1 -timeout 120m -tags nosqlite,noboltdb,n42el \
  -run 'TestEESTBlockchainTests|TestEESTBlockchainEngineTests' ./internal/api/
```

`N42_EEST_REPORT=<file.tsv>` writes one line per case (suite, network, file, test, status,
message). Narrow a rerun with `N42_EEST_FILTER`, e.g. `for_amsterdam/amsterdam/eip7778`.

## Results

Counts are test cases (one state-test post entry, one blockchain fixture), pass / fail.
"Before" is the start of D2 (`72fc014e`, main plus the SLOTNUM push fix); "after" is the final
commit of this phase.

### State tests (`for_amsterdam/`, network Amsterdam)

| Directory | Before (pass / fail) | After (pass / fail) |
|---|---|---|
| eip2780_reduce_intrinsic_tx_gas | 59 / 210 | 269 / 0 |
| eip7708_eth_transfer_logs | 1 / 73 | 74 / 0 |
| eip7843_slotnum | 1 / 17 | 18 / 0 |
| eip7928_block_level_access_lists | 3 / 63 | 66 / 0 |
| eip7954_increase_max_contract_size | 6 / 52 | 58 / 0 |
| eip7976_increase_calldata_floor_cost | 244 / 299 | 543 / 0 |
| eip7981_increase_access_list_cost | 105 / 84 | 189 / 0 |
| eip7997_deterministic_factory_contract | 0 / 40 | 40 / 0 |
| eip8024_dupn_swapn_exchange | 1 / 287 | 288 / 0 |
| eip8037_state_creation_gas_cost_increase | 102 / 395 | 497 / 0 |
| eip8038_state_access_gas_cost_increase | 37 / 226 | 263 / 0 |
| eip8246_selfdestruct_no_burn | 0 / 12 | 12 / 0 |
| legacy:berlin | 478 / 103 | 581 / 0 |
| legacy:byzantium | 13 / 589 | 602 / 0 |
| legacy:cancun | 303 / 949 | 1252 / 0 |
| legacy:constantinople | 0 / 114 | 114 / 0 |
| legacy:frontier | 59 / 1039 | 1098 / 0 |
| legacy:homestead | 0 / 9 | 9 / 0 |
| legacy:istanbul | 0 / 852 | 852 / 0 |
| legacy:london | 6 / 0 | 6 / 0 |
| legacy:mpt | 0 / 56 | 56 / 0 |
| legacy:osaka | 17 / 1179 | 1196 / 0 |
| legacy:ported_static | 3121 / 3321 | 6442 / 0 |
| legacy:prague | 360 / 1884 | 2244 / 0 |
| legacy:shanghai | 10 / 42 | 52 / 0 |
| legacy:spurious_dragon | 3 / 84 | 87 / 0 |
| total | 4929 / 11979 | 16908 / 0 |

Intermediate points: with the VM changes but the old transaction path (`23f6dd7e`) 6625 / 10283;
after the Amsterdam transaction path (`f260d6c2`) 16903 / 5; after the total gas cap
(`aac13fc3`) 16908 / 0.

Before D2 almost every Amsterdam transaction above 2^24 gas was rejected by the Osaka cap, the
state gas was charged from execution gas, intrinsic and floor costs used the Prague formulas, and
the EVM never saw the slot number — which is why the re-filled legacy tests failed as well.

### Blockchain tests (`ImportSyncedBlock` path)

Before: every Amsterdam fixture failed with `genesis hash mismatch` (checked on the first 1,250
cases of a full run): the Ethereum-compatible header hash and the genesis header lacked
`blockAccessListHash` and `slotNumber`.

After (`8b3d8a8b`): **26247 / 102** of 26349 cases.

| Directory | After (pass / fail) |
|---|---|
| eip2780_reduce_intrinsic_tx_gas | 287 / 0 |
| eip7708_eth_transfer_logs | 77 / 0 |
| eip7778_block_gas_accounting_without_refunds | 117 / 0 |
| eip7843_slotnum | 20 / 0 |
| eip7928_block_level_access_lists | 1126 / 100 |
| eip7954_increase_max_contract_size | 58 / 0 |
| eip7976_increase_calldata_floor_cost | 543 / 0 |
| eip7981_increase_access_list_cost | 189 / 0 |
| eip7997_deterministic_factory_contract | 40 / 0 |
| eip8024_dupn_swapn_exchange | 288 / 0 |
| eip8037_state_creation_gas_cost_increase | 826 / 0 |
| eip8038_state_access_gas_cost_increase | 263 / 0 |
| eip8246_selfdestruct_no_burn | 663 / 0 |
| eip8282_builder_execution_requests | 153 / 0 |
| legacy:berlin | 581 / 0 |
| legacy:byzantium | 602 / 0 |
| legacy:cancun | 6470 / 0 |
| legacy:constantinople | 115 / 0 |
| legacy:frontier | 1161 / 1 |
| legacy:homestead | 9 / 0 |
| legacy:istanbul | 852 / 0 |
| legacy:london | 7 / 0 |
| legacy:mpt | 116 / 0 |
| legacy:osaka | 1357 / 1 |
| legacy:paris | 1 / 0 |
| legacy:ported_static | 6442 / 0 |
| legacy:prague | 2915 / 0 |
| legacy:shanghai | 110 / 0 |
| legacy:spurious_dragon | 87 / 0 |
| legacy:tangerine_whistle | 772 / 0 |
| total | 26247 / 102 |

Intermediate point: at `bf6ea505` (before the two start-of-block system-call fixes) the run
gave 26239 / 110; the 8 extra failures were the `eip8037 state_gas_system_calls` beacon-root
and history cases (state root mismatch), fixed by `95cfd8ed` and `8b3d8a8b` (30 / 30 on a
filtered rerun, then the full rerun above).

### Engine tests (`engine_newPayload*` path)

All 3,183 Amsterdam engine fixture files (26,392 payload cases) are **skipped**: they are
delivered through `engine_newPayloadV5`, which neither N42's engine API nor the harness
implements yet (`skip: unsupported newPayloadVersion 5`). Nothing in this phase changes that;
the same block contents pass through the `ImportSyncedBlock` path above.

## Remaining failures

State tests: none.

Blockchain tests, 102 cases, all one gap — the spec EIP-7928 block-level access list is not
built or verified on import (N42 keeps its own BAL format; the header only carries the
fixture's `blockAccessListHash`, which goes into the header hash but is not recomputed):

| Expected exception | Cases | Where |
|---|---|---|
| `INVALID_BLOCK_ACCESS_LIST` (block accepted) | 91 | eip7928 |
| `BLOCK_ACCESS_LIST_GAS_LIMIT_EXCEEDED` (block accepted) | 6 | 5 eip7928, 1 `frontier/validation/header/block_gas_limit_below_minimum[minimum_minus_one]` |
| `INCORRECT_BLOCK_FORMAT` (block accepted) | 2 | eip7928 |
| `INVALID_BAL_HASH\|INVALID_BLOCK_HASH` | 1 | eip7928 |
| `INVALID_BLOCK_ACCESS_LIST\|INVALID_GAS_USED` | 1 | eip7928 |
| `RLP_BLOCK_LIMIT_EXCEEDED` (block accepted) | 1 | `osaka/eip7934 block_at_rlp_size_limit_boundary[max_rlp_size_plus_1_byte]`: the Amsterdam block RLP includes the BAL, which N42 does not carry, so the size is short by its encoding |

Other known gaps, not covered by these fixtures:

- `engine_newPayloadV5` / `engine_getPayloadV6` (engine fixtures skipped, see above).
- `lib/txpool` still applies the Osaka 2^24 per-transaction gas cap under Amsterdam rules
  instead of `TX_MAX_TOTAL_GAS_LIMIT`.
- Block production (miner/worker) does not build Amsterdam blocks: no two-dimensional
  `gasUsed`, no spec BAL / `blockAccessListHash`, no slot number.
- The legacy `TransitionDb` path keeps unreachable Glamsterdam branches of the old EIP-7778
  draft; Amsterdam transactions now go through `state_transition_amsterdam.go`.

## Changes

| Commit | Subject |
|---|---|
| `72fc014e` | fix(vm): SLOTNUM pushes the slot instead of overwriting the stack top |
| `6a509e50` | test(eest): report a panicking case as a failure instead of aborting the run |
| `ae9488ba` | style: gofmt params/protocol_params.go and internal/vm/instrumented.go |
| `93c5d4a8` | test(eest): pass the state-test env slotNumber into the block header |
| `61c1f3c2` | fix(core): give the EVM block context the header slot number on Amsterdam |
| `4356064a` | fix(vm): read a missing EIP-8024 immediate as zero |
| `cf05a522` | refactor(common): make GasPool a struct |
| `88b9a0b0` | feat(common): two-dimensional block gas accounting for EIP-8037 |
| `6b663471` | feat(vm): EIP-8037 state-gas reservoir and EIP-8038 state access costs |
| `23f6dd7e` | fix(state): EIP-8246 keeps a destructed account's balance at tx end |
| `f260d6c2` | feat(core): Amsterdam transaction execution with the EIP-8037 reservoir |
| `aac13fc3` | fix(core): cap an Amsterdam transaction's gas at TX_MAX_TOTAL_GAS_LIMIT |
| `354a241e` | fix(api): Amsterdam headers commit to the BAL hash and the slot number |
| `31dd3fd1` | fix(core): an Amsterdam header's gas used is the EIP-8037 block figure |
| `f60460a3` | fix(api): leave Amsterdam intrinsic and floor checks to the state transition |
| `f578fdcf` | feat(core): EIP-8282 builder deposit and exit request queues |
| `8bc9147d` | feat(conf): genesis slot number for an Amsterdam genesis |
| `97a66c03` | test(eest): pass the fixture genesis slotNumber into the genesis spec |
| `bf6ea505` | fix(core): give Amsterdam system calls an EIP-8037 state-gas reservoir |
| `98a5597b` | fix(vm): keep the N42 Fusaka SLOTNUM semantics frozen |
| `629627d3` | test(vm): re-pin the Glamsterdam jump tables after EIP-8037/8038 |
| `95cfd8ed` | fix(core): give the Amsterdam beacon-root system call its state-gas reservoir |
| `8b3d8a8b` | fix(core): run the Amsterdam EIP-2935 history update as a system call |

### Shared code touched (all chains compile through it)

- `common.GasPool` is a struct (was a `uint64` alias); `AddGas`/`SubGas`/`Gas` behave as before.
  The two-dimensional counters are only touched by Glamsterdam code.
- `internal/vm` `call`/`create`: Glamsterdam-only reservoir hand-off and code-deposit pricing;
  the non-Glamsterdam branches compute the same gas as before. `VMInterpreter` gained four
  state-gas methods.
- `IntraBlockState.FinalizeTx` runs `preserveDestructedBalances` only when
  `rules.IsGlamsterdam`; `Selfdestruct8246` is only reached from Glamsterdam code.
- `ethCompatibleHeaderHash`, the genesis header, `HeaderGasUsed`, the payload pre-checks and the
  system-call reservoir all branch on `IsGlamsterdam`.
- `conf.Genesis` gained an optional `slotNumber` (omitted when zero).

### N42-native risk

No N42 chainspec sets `glamsterdamTime`, so none of the Amsterdam paths run on N42 chains.
One earlier change did reach them: `72fc014e` changed `opSlotNum`, which the N42 Fusaka table
shares, and N42 chainspecs activate Fusaka (`params/chainspecs/mainnet.json` `fusakaTime`
1774656000). `98a5597b` restores the original SLOTNUM for the Fusaka table (it writes the slot
into the stack top and panics on an empty stack); only the Amsterdam table pushes. Whether N42
wants the corrected opcode is a fork decision left open here.
