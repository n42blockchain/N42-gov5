# EEST results — 2026-10-29

Phase B of `eip-gap-2026-10-29.md`: the Ethereum execution-spec-tests (EEST) fixtures for
Cancun → Osaka (and the BPO transitions) run against the eth-el code paths.

## Fixtures

| Archive | Release | sha256 | Used for |
|---|---|---|---|
| `fixtures_stable.tar.gz` | EEST v5.4.0 (2025-12-06) | `92cf1b47ad12fb27163261fc3c1cea5df72439cab507983d06b56c94f8741909` | Cancun, Prague, ShanghaiToCancun, CancunToPrague |
| `fixtures_develop.tar.gz` | EEST v5.4.0 (2025-12-06) | `3e2b02d49fe903eda4fd8caca5cbf0d139c470e97e1de9a85299b1b034f97099` | Osaka, PragueToOsaka, OsakaToBPO1, BPO1ToBPO2 … BPO3ToBPO4 |

Both from <https://github.com/ethereum/execution-spec-tests/releases/tag/v5.4.0>; the sha256
matches the digest GitHub lists. `stable` stops at Prague, so the Osaka/BPO networks come from
`develop` of the same release. The develop state tests carry Osaka only (no BPO post keys).
The fixtures stay outside the repository (here `D:\eest\v5.4.0\extract\fixtures` and
`D:\eest\v5.4.0\extract_develop\fixtures`).

## Runners

All opt-in: they skip unless `N42_EEST_DIR` points at an extracted `fixtures` directory.

| Test | Package | Path exercised |
|---|---|---|
| `TestEESTStateTests` | `./tests/` | `state_tests`: each post entry's `txbytes` decoded and applied with `internal.ApplyTransaction` + `ethel.NewEthReplayEngine` on an in-memory state; MPT post root and logs hash compared; `expectException` cases must be rejected and leave the pre-state root |
| `TestEESTBlockchainTests` | `./internal/api/` | `blockchain_tests`: each block RLP via `ethel.DecodeRawBlock` → `EngineAPIV1.ImportSyncedBlock` (eth-el `EngineStateAdapter`, MDBX chaindata, MPT state) + forkchoice; invalid blocks must be rejected; final head and post state compared |
| `TestEESTBlockchainEngineTests` | `./internal/api/` | `blockchain_tests_engine`: `engine_newPayloadV1..V4` + `forkchoiceUpdated` as the fixture lists them, expected status/validation error checked; final head and post state compared |

Fork names map to explicit Ethereum chain configs in `tests/eest/forks.go` (mainnet-like: ethash
marker, MPT state, mainnet deposit contract; `XToYAtTime15k` activates Y at 15000; BPO1–5 from the
fixture blob schedule). Pre-merge networks are skipped by the blockchain runners (eth-el imports
post-merge blocks only).

Env vars: `N42_EEST_DIR` (required), `N42_EEST_FORKS` (comma list; default Cancun and later
including transitions), `N42_EEST_FILTER` (substring of the fixture path), `N42_EEST_WORKERS`
(parallel fixtures for the blockchain runners, default 1), `N42_EEST_REPORT` (TSV, one line per
case: suite, network, file, test, status, message).

### Commands

```sh
# state tests, stable (Cancun, Prague, transitions) — ~15 s
N42_EEST_DIR='D:\eest\v5.4.0\extract\fixtures' GOMAXPROCS=8 \
  go test -p 2 -count=1 -tags nosqlite,noboltdb -run TestEESTStateTests ./tests/

# state tests, Osaka from develop — ~10 s
N42_EEST_DIR='D:\eest\v5.4.0\extract_develop\fixtures' N42_EEST_FORKS=Osaka GOMAXPROCS=8 \
  go test -p 2 -count=1 -tags nosqlite,noboltdb -run TestEESTStateTests ./tests/

# blockchain + engine tests, stable — ~50 min each with 8 workers
N42_EEST_DIR='D:\eest\v5.4.0\extract\fixtures' N42_EEST_WORKERS=8 GOMAXPROCS=8 \
  go test -p 2 -count=1 -timeout 600m -tags nosqlite,noboltdb \
  -run 'TestEESTBlockchainTests|TestEESTBlockchainEngineTests' ./internal/api/

# blockchain + engine tests, Osaka/BPO from develop
N42_EEST_DIR='D:\eest\v5.4.0\extract_develop\fixtures' N42_EEST_WORKERS=8 GOMAXPROCS=8 \
  N42_EEST_FORKS='PragueToOsakaAtTime15k,Osaka,OsakaToBPO1AtTime15k,BPO1,BPO1ToBPO2AtTime15k,BPO2,BPO2ToBPO3AtTime15k,BPO3,BPO3ToBPO4AtTime15k,BPO4,BPO5' \
  go test -p 2 -count=1 -timeout 600m -tags nosqlite,noboltdb \
  -run 'TestEESTBlockchainTests|TestEESTBlockchainEngineTests' ./internal/api/
```

(`GOMAXPROCS=8`, 8 workers: the machine also runs the 7-node fleet.)

## Results

Counts are test cases (one state-test post entry, one blockchain fixture), as pass / fail.
"After fixes" is a full rerun of every suite at the final commit.

Summary after fixes: blockchain and engine suites 120,776 / 0 across all networks; state tests
55,218 / 15, all 15 in the three `preCheck` categories under "Remaining".

### State tests

| Network | Before fixes | After fixes |
|---|---|---|
| Cancun | 16842 pass / 5 fail | 16842 / 5 |
| Prague | 18862 / 7 | 18864 / 5 |
| Osaka (develop) | 19510 / 7 | 19512 / 5 |

### Blockchain tests (`ImportSyncedBlock` path)

| Network | Before fixes | After fixes |
|---|---|---|
| Cancun | 17640 / 45 | 17685 / 0 |
| Prague | 20766 / 93 | 20859 / 0 |
| ShanghaiToCancunAtTime15k | 57 / 3 | 60 / 0 |
| CancunToPragueAtTime15k | 98 / 0 | 98 / 0 |
| Osaka (develop) | 21464 / 94 | 21558 / 0 |
| PragueToOsakaAtTime15k | 85 / 1 | 86 / 0 |
| OsakaToBPO1, BPO1ToBPO2, BPO2ToBPO3, BPO3ToBPO4 | 23 / 0 | 23 / 0 |

### Blockchain engine tests (`engine_newPayload` path)

| Network | Before fixes | After fixes |
|---|---|---|
| Cancun | 17685 / 0 | 17685 / 0 |
| Prague | 20878 / 0 | 20878 / 0 |
| ShanghaiToCancunAtTime15k | 60 / 0 | 60 / 0 |
| CancunToPragueAtTime15k | 98 / 0 | 98 / 0 |
| Osaka (develop) | 21577 / 0 | 21577 / 0 |
| PragueToOsakaAtTime15k | 86 / 0 | 86 / 0 |
| OsakaToBPO1, BPO1ToBPO2, BPO2ToBPO3, BPO3ToBPO4 | 23 / 0 | 23 / 0 |

## Failures found

### Fixed

| Category | Cases | Where | Fix |
|---|---|---|---|
| Harness: `MemoryState` returned stale code after an EIP-7702 delegation was cleared | 4 state (`test_delegation_clearing` delegated_account, Prague + Osaka) | `tests/memory_state.go` | code resolved by code hash |
| `INCORRECT_BLOB_GAS_USED` block accepted | 222 blockchain (`cancun/eip4844_blobs/test_invalid_blob_gas_used_in_header`) | wire import skipped newPayload's pre-execution body checks | `validateWireBlockBody` in the adapter (per-tx shape, blob count, `blobGasUsed`) |
| `INVALID_WITHDRAWALS_ROOT` block accepted | 9 blockchain (`shanghai/eip4895_withdrawals/test_withdrawals_root` valid_False) | `ImportSyncedBlock` never compared the body withdrawals with the header root | `validateWireBlockForkFields` |
| `INCORRECT_BLOCK_FORMAT` (blob fields before Cancun) accepted | 3 blockchain (`test_invalid_pre_fork_block_with_blob_fields`, ShanghaiToCancun) | same | `validateWireBlockForkFields` |
| `RLP_BLOCK_LIMIT_EXCEEDED` (EIP-7934) accepted | 2 blockchain (`osaka/eip7934_block_rlp_limit`) | `ImportSyncedBlock` had no size check (devp2p and batch replay already did) | `validateExecutionPayloadBlockRLPSize` in `ImportSyncedBlock` |

Related, found while reading the paths: devp2p `executeRange` checked the body's transactions
against `transactionsRoot` but executed the withdrawals unchecked, so a peer sending wrong
withdrawals got a valid block reported invalid via its state root. Bodies whose withdrawals miss
`withdrawalsRoot` are now refetched (`eldevp2p`, unit test `TestWireWithdrawalsMatchHeader`).
The adapter deliberately does not reject on withdrawalsRoot: on the devp2p path that is a peer
fault, not an invalid block.

### Remaining (not fixed here)

All in the state tests, 5 per fork, same three categories:

| Category | Cases per fork | Cause |
|---|---|---|
| `INITCODE_SIZE_EXCEEDED` creation tx accepted (EIP-3860) | 3 (`test_contract_creating_tx` over_limit_zeros/ones, `creationTxInitCodeSizeLimit` invalid) | shared `internal/state_transition.go` `preCheck` has no tx-level initcode cap (geth `CheckMaxInitCodeSize` in `preCheck`) |
| `TYPE_4_TX_CONTRACT_CREATION`, `TYPE_4_EMPTY_AUTHORIZATION_LIST` accepted (EIP-7702) | 2 (Prague, Osaka) | `preCheck` lacks geth's set-code tx shape checks |
| `TYPE_4_TX_PRE_FORK` accepted | 2 (Cancun only) | same: a type-4 tx before Prague is not rejected in `preCheck` |

These are rejected on every eth-el block import path (newPayload already, wire imports now via
`validateWireBlockBody`), which is why the blockchain suites pass them; only bare transaction
application (the state-test harness) still accepts them. Moving the checks into the shared
`preCheck` changes N42's own chains too (N42 mainnet chainspecs activate set-code via
`pectraTime` without `pragueTime`), so per the plan it needs a qs-history check first. Left for a
follow-up.

### Other notes

- The old hand-rolled runner `tests/eth_test_runner_test.go` (ethereum/tests layout) is unchanged
  and still skips.
- Blockchain runners skip pre-merge networks; Shanghai-only networks are not in the default set.
- `blockchain_tests_engine_x` and `transaction_tests` are not wired.
