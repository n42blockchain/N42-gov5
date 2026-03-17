# Test Review Master Plan

## Objective

对整个仓库的测试做一次正式、可复现的专项审查，目标不是简单“多跑一些测试”，而是同时完成这 4 件事：

1. 逐文件 review 受版本控制的测试代码，识别错误断言、空断言、占位测试、分析型伪测试、flaky、全局状态污染和资源泄漏。
2. 修复测试本身的问题，并在对应生产代码存在真实缺口时补上回归测试。
3. 删除或降级不适合作为默认 `go test` 套件一部分的过时测试、统计型测试和手工分析型测试。
4. 建立 Execution Layer 外部测试资产的并行分片执行路径，最终把 fork shard 跑法固化到脚本和文档里。

## Formal Scope

本计划采用两层口径，避免把外部镜像仓库和运行时目录混进正式测试代码 review：

- 原始工作区 `*_test.go`：`436`
- 受版本控制正式 `*_test.go`：`431`
- workspace-only 未纳入正式清单的 `*_test.go`：`5`
- 正式 review 主体：主仓库跟踪的 `431` 个测试文件，以及执行中新增并明确纳入版本控制的测试文件
- 外部镜像测试资产：`tests/eth-tests/`、`tests/eth-hive/`、`tests/eth-execution-apis/`、`tests/eth-devp2p/`
- 不纳入正式测试代码 review：运行时目录、链数据、构建产物、虚拟环境，例如 `mainnet/`、`n42data/`、`devtest/`、`tests/n42data/`、`build/`、`.venv/`

正式文件清单和分组顺序见 [TEST_REVIEW_FILE_INVENTORY.md](./TEST_REVIEW_FILE_INVENTORY.md)。

## Current Evidence Baseline

本轮计划只写已经被本地代码和命令验证过的事实：

- 本地存在 `uv 0.10.2`
- `tests/eth-tests/execution-spec-tests` 文档已明确支持：
  - `--collect-only -q`
  - `--sim.limit`
  - `pytest-xdist` 的 `-n auto`
  - Hive 的 `--sim.parallelism`
- 直接运行 `uv run consume engine --help` 时，`uv` 默认选到 Python `3.14`，会因为 `pydantic-core` / `PyO3` 上限问题失败
- 本机存在 `python3.13`
- `uv run --python 3.13 fill --help` 已验证可正常工作
- source/fill 维度 shard collect 已脚本化并跑过一轮：
  - `paris+shanghai`: `49829`
  - `cancun`: `51683`
  - `prague`: `59945`
  - `osaka`: `41843`，但返回码 `rc=5`
  - `rlp`: `49851`
- `consume engine --input stable@latest --collect-only` 已通过最小 Hive `/clients` stub 本地复现：
  - `paris+shanghai`: `3573`
  - `cancun`: `17783`
  - `prague`: `20878`
  - `osaka`: `0`，`rc=5`，`no tests collected (42234 deselected) in 2.45s`
  - `rlp`: `2132`
- `stable@latest` 当前本机缓存版本是：
  - `/Users/jieliu/Library/Caches/ethereum-execution-spec-tests/cached_downloads/ethereum/execution-spec-tests/v5.4.0/fixtures_stable/fixtures`
- 上述 `consume-engine` collect 数已与这份 stable cache 的 `.meta/index.json` 中 `format == blockchain_test_engine` 的 fork 分布逐项核对一致
- 已新增 `scripts/eest_hive_stub.py`
- `scripts/collect_eest_shards.sh` 现支持 `EEST_HIVE_STUB=1` 自启动最小 stub
- `scripts/collect_eest_shards.sh` / `scripts/run_eest_shards.sh` 现已把 `fill` 的 `-k` 表达式与 `consume engine` 的 `--sim.limit` Python regex 分开保存
- `build/bin/n42 init --data.dir <tmp> tests/eth-hive/simulators/ethereum/engine/init/genesis.json` 现已实测成功
- 本地直接运行 `n42` + Hive engine genesis + JWT authrpc 已实测成功：
  - 带 JWT 的 `engine_exchangeCapabilities` 返回完整方法集
  - 无 JWT 访问 authrpc 返回 `403`
  - `engine_getClientCapabilitiesV1` 返回当前本地支持的 forks / methods
- `tests/eth-hive/clients/n42` 容器包装层已实测成功：
  - `scripts/prepare_hive_n42_client.sh` + `docker build -f tests/eth-hive/clients/n42/Dockerfile.local`
  - 挂载真实 Hive genesis 后，容器内 `n42.sh` 可成功 init / start
  - 容器 authrpc 上的 `engine_exchangeCapabilities` / `engine_getClientCapabilitiesV1` 已实测可调用
- 当前真实 blocker 已收敛到 Engine API 执行语义本身：
  - `NewPayloadV1/V2/V3/V4` / `ForkchoiceUpdatedV1/V2/V3/V4` 当前仍是输入校验 + `SYNCING`
  - 这意味着 auth / capabilities / boot 路径已打通，但 full shard / wall clock 不能据此宣称完成

这些数分成两种不同口径：`fill` 是 source collect，`consume engine` 是 remote fixture / engine collect。下面这张表现在记录的是目标值和本机两套已复现口径，不再把它们混成同一结果。

## Target Shard Goals

| Shard | Selector | Target ~Tests | Current local evidence | Status |
|-------|----------|---------------|------------------------|--------|
| paris+shanghai | `.*/.*fork_(Paris\|Shanghai)` | ~2,600 | `fill` => `49829`; `consume engine stable@latest` => `3573` | 两种 collect 均已复现，engine collect 高于目标表 |
| cancun | `.*/.*fork_Cancun` | ~17,250 | `fill` => `51683`; `consume engine stable@latest` => `17783` | 两种 collect 均已复现，engine collect 略高于目标表 |
| prague | `.*/.*fork_Prague` | ~20,500 | `fill` => `59945`; `consume engine stable@latest` => `20878` | 两种 collect 均已复现，engine collect 略高于目标表 |
| osaka | `.*/.*fork_Osaka` | ~21,000 | `fill` => `41843` (`rc=5`); `consume engine stable@latest` => `0` (`rc=5`, no tests collected) | 当前 stable input 下 engine collect 未复现到 Osaka fixture |
| rlp | `.*eip2930_access_list.*` | unchanged | `fill` => `49851`; `consume engine stable@latest` => `2132` | 两种 collect 均已复现，engine collect 与 source count 不同 |

目标 wall clock `74 min` 当前保留为目标值，不在本轮文档里伪装成“已确认现状”。要把它写成正式结果，必须至少补齐：

1. 固定 Python `3.13`
2. 固化 `fill` / `consume engine` / `hive` 的调用脚本
3. 生成或定位 fixture index
4. 复现 shard collect 数
5. 跑出真实 wall clock
6. 具备真实 payload / forkchoice 执行语义，而不只是 authrpc 和 capability surface

## Progress Snapshot

- Phase 0：已完成
- Phase 1：已完成第一轮入口层收口，包括：
  - `tests/eth_state_test.go`、`tests/eth_test_runner_test.go`、`tests/full_state_test.go`、`tests/analyze_failures_test.go` 的手工/分析入口降级
  - `tests/bls_precompile_test.go` 从 gas mismatch 仅日志升级为 fail-closed 断言
  - `params/protocol_params.go`、`internal/vm/contracts_bls12381.go`、`internal/vm/eips_pectra_test.go` 的 BLS gas 公式与本地 execution-spec-tests 对齐
  - `tests/dapp_compat*_test.go`、`tests/prediction_market_compat_test.go`、`tests/zk_evm_compat_test.go` 中 summary/report 型伪测试降为显式手工入口
  - `tests/integration_test.go`、`tests/refactoring_test.go` 的隔离型测试已增加 `t.Parallel()`
- Phase 6：`tests/dapp_compat*_test.go`、`tests/prediction_market_compat_test.go`、`tests/zk_evm_compat_test.go` 的只读兼容性检查已增加 `t.Parallel()`
- Phase 7：source/fill shard collect、consume-engine collect-only 和 dry-run runner 已完成；真实 Hive 执行 wall clock 仍未在本机确认
- Phase 7 补充：Hive client bootstrap / JWT auth / engine capability surface 已在本地二进制和容器包装层实测打通；full engine execution 仍未完成
- Phase 7 再补充：真实 `hive --sim ethereum/engine --sim.limit 'engine-auth/'` 已跑通到 simulator 级执行；当前结果是 `1` 个 suite、`8` 个 tests、`7` 个 failed，失败点已从“启动/鉴权”收敛到 `forkchoiceUpdated/getPayload/newPayload` 语义与外部 Ethereum hash 兼容面
- 当前 `consume-engine` 的最新 dry-run 目录：
  - `tests/results/eest-shards/20260316-203112Z`

## Review Order

执行顺序不是简单按目录字母序，而是按“先去掉假测试和入口噪音，再收核心路径，再收慢集成和外部分片”的原则：

1. `tests/` 入口层和手工分析型测试
2. 入口配置与工具层：`cmd/`、`accounts/`、`api/protocol`、`conf/`、`log/`、`params/`、`pkg/`、`tools/`、`utils/`
3. 执行、状态、存储主路径：`common/`、`modules/state`、`modules/rawdb`、`internal/vm`、`internal/avm`、`internal/txspool`、`internal/snapshot`、`internal/blockchain*`、`lib/state`、`lib/kv`、`lib/jmt`
4. 共识、网络、节点、RPC：`internal/api`、`internal/consensus`、`internal/p2p`、`internal/sync`、`internal/node`、`internal/network`、`internal/miner`、`modules/rpc`、`modules/event`
5. 密码学、ZK、合约：`common/crypto`、`lib/crypto`、`internal/zkprover`、`internal/zkverifier`、`contracts/`
6. 低层库和慢速集成：其余 `lib/`、`tests/`
7. 外部测试资产并行化：`tests/eth-tests`、`tests/eth-hive`

## Review Rules

每个测试文件都按同一套规则处理：

1. 先读测试文件，再读对应生产代码，不凭测试名称推断测试价值。
2. 识别并处理这几类问题：
   - 只打印日志、不做稳定断言
   - 依赖 `Sleep`、随机数、系统时间、全局注册表、共享端口
   - “先写 TODO，再默认 pass”
   - 只统计文件数/向量数、不执行核心行为
   - fixture 清理不完整，或测试间共享状态
   - goroutine / channel / context 没有退出路径
   - 一跑就是整套外部资产，但没有显式 opt-in
3. 对测试保留或移除的标准：
   - 能稳定验证行为的，保留并补断言
   - 本质是调试脚本、统计脚本、失败分析脚本的，迁出默认测试路径或显式改为手工入口
   - 与当前实现重复、过时或误导的，删除
4. 对并行化的标准：
   - 只有无共享链数据、无共享端口、无全局状态污染的测试，才启用 `t.Parallel()`
   - 大目录 walker 用 worker 池或 shard runner，不直接在默认测试里串行扫整棵外部树

## Phase Plan

### Phase 0: Baseline And Inventory

动作：

- 固化正式 `*_test.go` 清单
- 生成按功能模块排序的 inventory
- 建立 findings 文档
- 记录外部测试资产与本地环境前置条件

验收：

- [TEST_REVIEW_MASTER_PLAN.md](./TEST_REVIEW_MASTER_PLAN.md) 落盘
- [TEST_REVIEW_FILE_INVENTORY.md](./TEST_REVIEW_FILE_INVENTORY.md) 可重建
- [TEST_REVIEW_FINDINGS.md](./TEST_REVIEW_FINDINGS.md) 建立

### Phase 1: `tests/` Entry Layer Cleanup

优先文件：

- `tests/eth_state_test.go`
- `tests/eth_test_runner_test.go`
- `tests/full_state_test.go`
- `tests/analyze_failures_test.go`
- `tests/bls_precompile_test.go`
- `tests/integration_test.go`

目标：

- 去掉默认 pass 的占位测试
- 把分析型/统计型测试从默认 suite 剥离或显式 opt-in
- 建立轻量、稳定、可断言的单元级回归
- 收紧外部 fixture 扫描入口，避免默认 `go test ./tests` 直接变成大规模资产遍历

阶段验收：

- `go test -count=1 ./tests -run 'Test(EthState|RunState|FullState|Analyze|EIP|TransactionValidation|BLS|Integration)'`

### Phase 2: Entry / Config / Tooling Packages

优先包：

- `cmd/evmsdk`
- `accounts/...`
- `api/protocol/...`
- `conf`
- `log/...`
- `params`
- `pkg/errors`
- `tools/...`
- `utils/...`

目标：

- 修 CLI/SDK 生命周期测试
- 去除时间等待和全局状态污染
- 给解析、编码、版本和错误路径补断言

阶段验收：

- `go test -count=1 ./cmd/evmsdk ./accounts/... ./api/protocol/... ./conf ./log/... ./params ./pkg/errors ./tools/... ./utils/...`

### Phase 3: Execution / State / Storage

优先包：

- `common/...`，排除 `common/crypto/...`
- `modules/state/...`
- `modules/rawdb/...`
- `modules/ethdb`
- `internal/block*`
- `internal/evm*`
- `internal/vm/...`
- `internal/avm/...`
- `internal/txspool`
- `internal/snapshot`
- `lib/state`
- `lib/kv/...`
- `lib/commitment`
- `lib/jmt`

目标：

- 补边界输入、rollback、snapshot、索引和 round-trip 回归
- 清掉顺序依赖和共享状态
- 在可隔离的地方增加并行子测试

阶段验收：

- `go test -count=1 ./common/... ./modules/state/... ./modules/rawdb/... ./modules/ethdb ./internal/... ./lib/state ./lib/kv/... ./lib/commitment ./lib/jmt`

### Phase 4: Consensus / Network / RPC / Node

优先包：

- `internal/api`
- `internal/consensus/...`
- `internal/p2p/...`
- `internal/sync/...`
- `internal/node`
- `internal/network/...`
- `internal/miner/...`
- `modules/rpc/...`
- `modules/event/...`
- `internal/tracing`
- `internal/tracers/...`
- `internal/download`
- `internal/metrics/...`
- `internal/mcp`

目标：

- 修订阅/取消订阅/超时/断连测试
- 修默认全局 metrics/tracing 注册污染
- 给坏包、nil、畸形 proto、错误码路径补断言

阶段验收：

- `go test -count=1 ./internal/api ./internal/consensus/... ./internal/p2p/... ./internal/sync/... ./internal/node ./internal/network/... ./internal/miner/... ./modules/rpc/... ./modules/event/... ./internal/tracing ./internal/tracers/... ./internal/download ./internal/metrics/... ./internal/mcp`

### Phase 5: Crypto / ZK / Contracts

优先包：

- `common/crypto/...`
- `lib/crypto/...`
- `internal/zkprover/...`
- `internal/zkverifier`
- `contracts/...`

目标：

- 补零值、nil、非法字节流、错误 proof 结构、批处理边界
- 修未初始化对象在测试路径里的 panic 和静默降级

阶段验收：

- `go test -count=1 ./common/crypto/... ./lib/crypto/... ./internal/zkprover/... ./internal/zkverifier ./contracts/...`

### Phase 6: Base Libraries And Slow Integration

优先包：

- 其余 `lib/...`
- `tests/*compat*`
- `tests/*integration*`

目标：

- 收慢速集成测试的稳定性、清理和失败信息
- 识别哪些测试应迁出默认 `go test ./...`

阶段验收：

- `go test -count=1 ./lib/...`
- `go test -count=1 ./tests`

### Phase 7: EEST / Hive Parallel Sharding

动作：

- 固定 Python `3.13`
- 建立 collect 脚本和 execute 脚本
- 分离 source collect、fixture collect、consume-engine collect 三种口径
- 复现 shard 数量
- 增加 `xdist` 或 Hive parallelism
- 固定结果输出目录和汇总表

最小交付：

- 可复现 shard collect 脚本
- 可复现 shard 执行脚本
- shard 结果文档，包含真实数量、失败数和 wall clock

阶段验收：

- `scripts/collect_eest_shards.sh`
- `scripts/run_eest_shards.sh`
- 产出 shard 结果 markdown 或 JSON

### Phase 8: Full Regression Sweep

动作：

- 回跑所有触及包
- 运行 `go vet ./...`
- 运行 `go test ./...`
- 需要时运行 `make build`

收口标准：

- 所有变更有明确 findings 记录
- 默认测试套件不再包含“静默通过的占位测试”
- 受影响包验证通过
- EEST/Hive shard 路径至少完成脚本化和 collect 复现，执行结果按证据记录

## Deliverables

- [TEST_REVIEW_MASTER_PLAN.md](./TEST_REVIEW_MASTER_PLAN.md)
- [TEST_REVIEW_FILE_INVENTORY.md](./TEST_REVIEW_FILE_INVENTORY.md)
- [TEST_REVIEW_FINDINGS.md](./TEST_REVIEW_FINDINGS.md)
- `scripts/generate_test_review_inventory.sh`
- `scripts/collect_eest_shards.sh`
- `scripts/run_eest_shards.sh`

## Current Step

当前本地可落地部分已经推进到 `Phase 7/8` 收口：

- 默认 `go test ./tests` 已清掉一批静默通过的占位 / summary 型入口
- 兼容性检查中可隔离的顶层测试已并行化
- EEST 的 source collect 和 consume-engine collect-only 都已脚本化并复现
- 真实 Hive harness 现在已经能启动并进入 `engine-auth` suite，不再停在 proxy / client bootstrap
- 当前剩余主线工作是 Engine API block-building/import 语义，以及对外 Ethereum header-hash 兼容
- `74 min` wall clock 仍不能宣称完成，因为 `engine-auth` 都还没有跑绿，更高阶 shard 不具备真实完成条件
