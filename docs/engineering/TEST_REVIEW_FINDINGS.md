# Test Review Findings

## Baseline

- 日期：`2026-03-16`
- 原始工作区 `*_test.go`：`436`
- 受版本控制正式 `*_test.go`：`431`
- workspace-only `*_test.go`：`5`
- 正式清单与排序规则：见 [TEST_REVIEW_FILE_INVENTORY.md](./TEST_REVIEW_FILE_INVENTORY.md)

## Findings

### F-001 `tests/eth_state_test.go` contains placeholder tests that silently pass

事实：

- `runStateTest` 目前仍有 `TODO: Implement full state test execution`
- 非异常路径当前直接 `result.Passed = true`
- `TestEIP7702SetCode`、`TestEIP6110Deposits`、`TestEIP7002Withdrawals` 只打印日志
- `TestTransactionValidation` 只遍历文件并打印名称

影响：

- 这类测试会在默认 `go test` 中制造“通过”假象
- 不能反映真实实现质量

处理状态：`partially_resolved`

### F-002 `tests/eth_test_runner_test.go` mixes real execution with count-only tests

事实：

- 同文件既包含真实 state execution，也包含只统计向量数/文件数的 BLS、blockchain、transaction、Prague EIP 测试入口

影响：

- 默认测试语义混杂
- 很难判断单个 `Test*` 是行为验证还是调试/盘点入口

处理状态：`partially_resolved`

### F-003 `tests/full_state_test.go` and `tests/analyze_failures_test.go` are analysis harnesses, not default CI tests

事实：

- 两个文件主要产出统计和失败分析报告
- 默认入口会扫大型 fixture 目录

影响：

- 默认 `go test ./tests` 的行为过重
- 失败与耗时都不够可控

处理状态：`partially_resolved`

### F-004 EEST tooling must pin Python 3.13 in this environment

事实：

- `uv run consume engine --help` 默认使用 Python `3.14` 时构建失败
- 报错来自 `pydantic-core` / `PyO3` 最大支持版本 `3.13`
- `python3.13 --version` 为 `Python 3.13.0`
- `uv run --python 3.13 fill --help` 已验证可工作

影响：

- 并行 shard 方案不能直接假定“本机开箱即用”
- 所有 EEST/Hive 脚本都需要显式固定 Python 版本

处理状态：`confirmed`

### F-005 source/fill shard collect and consume-engine target counts are not the same metric

事实：

- `scripts/collect_eest_shards.sh` 现在已经能在 `fill` 模式下跑出本机 source collect 数
- 当前结果：
  - `paris+shanghai`: `49829`
  - `cancun`: `51683`
  - `prague`: `59945`
  - `osaka`: `41843`，同时 `rc=5`
  - `rlp`: `49851`
- `consume engine stable@latest --collect-only` 也已通过最小 Hive stub 复现：
  - `paris+shanghai`: `3573`
  - `cancun`: `17783`
  - `prague`: `20878`
  - `osaka`: `0`，同时 `rc=5`
  - `rlp`: `2132`
- 上述 `consume-engine` collect 数与本机 `stable@latest` cache
  - `/Users/jieliu/Library/Caches/ethereum-execution-spec-tests/cached_downloads/ethereum/execution-spec-tests/v5.4.0/fixtures_stable/fixtures/.meta/index.json`
  中 `format == blockchain_test_engine` 的计数逐项一致
- 这些数与目标表里的 `~2600 / ~17250 / ~20500 / ~21000` 不在同一口径

影响：

- 不能把 source/fill collect 数直接写成 consume-engine / Hive shard 完成结果
- 已补齐 consume-engine collect-only；wall clock 仍需真实 Hive 执行环境

处理状态：`resolved`

### F-006 consume-engine collect is blocked by missing Hive simulator in the current environment

事实：

- 已实测命令：
  - `uv run --python 3.13 consume engine --input stable@latest --collect-only -q -k "fork_Paris or fork_Shanghai"`
- 返回码：`2`
- stderr 明确要求：`HIVE_SIMULATOR environment variable is not set`
- 已新增 `scripts/eest_hive_stub.py`
- `scripts/collect_eest_shards.sh` 现在支持 `EEST_HIVE_STUB=1`
- 在 stub 模式下，`consume engine --collect-only` 已可完成 collect，而不再卡在配置阶段的 `/clients` 请求

影响：

- collect-only 不再被 `HIVE_SIMULATOR` 缺失阻塞
- 真实执行和 wall clock 仍然需要真实 Hive dev/simulator 环境，不应拿 stub 冒充

处理状态：`resolved`

### F-007 BLS precompile gas assertions and implementation drift

事实：

- 本轮已修复 `msm_G1_bls.json` / `msm_G2_bls.json` 的文件名映射，MSM 向量不再被误跳过
- 继续深挖后，确认偏差不止在测试：`params/protocol_params.go` 中 BLS pairing gas 常量和 MSM discount table 与本地 `tests/eth-tests/execution-spec-tests/tests/prague/eip2537_bls_12_381_precompiles/spec.py` 不一致
- `internal/vm/contracts_bls12381.go` 之前还把 G1/G2 MSM 复用了同一张 discount table
- 现在 `tests/bls_precompile_test.go` 和 `TestBLSG1Add` 都会对 gas 做 fail-closed 断言
- `internal/vm/eips_pectra_test.go` 新增了本地 gas 回归，覆盖 G1MSM、G2MSM、pairing 的关键样本点

影响：

- 之前默认 `go test ./tests` 会把错误 gas 计算当作通过
- 现在 BLS gas 偏差会直接阻断测试，并且本地 unit test 不依赖外部 fixture 也能覆盖关键点

处理状态：`resolved`

### F-008 Summary / report style compatibility tests polluted the default suite

事实：

- `tests/dapp_compat_test.go`
- `tests/dapp_compat_phase2_test.go`
- `tests/dapp_compat_phase3_test.go`
- `tests/dapp_compat_phase4_test.go`
- `tests/prediction_market_compat_test.go`
- `tests/zk_evm_compat_test.go`
- 上述文件里的若干 `Summary` / `ImplementationGaps` 入口只打印结论横幅，不验证行为

影响：

- 默认 `go test ./tests` 会混入大量“报告即测试”的伪覆盖
- 输出噪音会掩盖真正失败点

处理状态：`resolved`

### F-009 Some isolated package-level tests were serialized without need

事实：

- `tests/integration_test.go` 和 `tests/refactoring_test.go` 的顶层测试只用本地对象和编译期接口检查，不依赖外部 fixture、共享端口或全局注册表
- `tests/dapp_compat_test.go`
- `tests/dapp_compat_phase2_test.go`
- `tests/dapp_compat_phase3_test.go`
- `tests/dapp_compat_phase4_test.go`
- `tests/prediction_market_compat_test.go`
- `tests/zk_evm_compat_test.go`
- 上述兼容性检查文件里的顶层测试也只读常量、地址、opcode、gas 规则或本地 precompile 实例
- 这些测试之前都没有开启 `t.Parallel()`

影响：

- 默认测试执行时间被不必要地串行化

处理状态：`resolved`

### F-010 `stable@latest` consume-engine input currently exposes no Osaka fixtures to collect

事实：

- 已实测命令：
  - `HIVE_SIMULATOR=http://127.0.0.1:3000 uv run --python 3.13 consume engine --input stable@latest --collect-only -q -k "fork_Osaka"`
- 输出为：`no tests collected (42234 deselected) in 2.45s`
- 返回码：`5`
- 同时，`fill` source collect 口径下 `fork_Osaka` 仍然能扫到 `41843` 个 source tests

影响：

- 目标表中的 `osaka ~21,000` 当前不能在本机 `stable@latest` engine input 上被当作“已复现”
- `osaka` 这一行必须保留为当前 input / fixture release 的客观缺口，而不是把 source count 或目标值伪装成 engine result

处理状态：`confirmed`

### F-011 The table's `paris+shanghai` selector is not directly valid as a Python regex

事实：

- 目标表写的是：`.*/.*fork_(Paris\|Shanghai)`
- `consume` 的 `--sim.limit` 在本地源码里明确按 Python regex 解析
- 在 Python regex 下，`\|` 会匹配字面量 `|`，不会做 alternation
- 当前脚本已保留表里的展示 selector，但实际传给 `consume engine --sim.limit` 的表达式会规范化成：
  - `.*fork_(Paris|Shanghai).*`

影响：

- 如果直接把表里的 `\|` 原样传给 `consume engine --sim.limit`，`paris+shanghai` 的 collect 结果会是 `0`
- shard 脚本必须区分“文档展示 selector”和“实际执行 regex”

处理状态：`resolved`

### F-012 Hive engine genesis and authrpc bootstrap were incomplete for a real N42 client wrapper

事实：

- 之前 `tests/eth-hive/clients/n42/n42.sh` 写入的 `/jwtsecret` 是原始 ASCII，而节点读取逻辑要求十六进制字符串
- Hive 标准 engine genesis 不带 `config.consensus`，旧路径下 `n42 init` 后重启仍会读到空 consensus，节点直接报 `invalid engine name`
- 现在已补齐：
  - `conf/genesis_config.go`
  - `params/config.go`
  - `modules/rawdb/accessors_metadata.go`
  - `internal/node/node.go`
  - `tests/eth-hive/clients/n42/n42.sh`
- 已实测本地直接运行：
  - `build/bin/n42 init --data.dir <tmp> tests/eth-hive/simulators/ethereum/engine/init/genesis.json`
  - 随后启动 `n42` 并用 JWT 调用 `engine_exchangeCapabilities`
  - 无 JWT 调用 authrpc 返回 `403`
- 已实测容器包装层：
  - `scripts/prepare_hive_n42_client.sh`
  - `docker build -f tests/eth-hive/clients/n42/Dockerfile.local ...`
  - 挂载真实 Hive genesis 后，容器内 authrpc 上 `engine_exchangeCapabilities` 和 `engine_getClientCapabilitiesV1` 均可调用

影响：

- N42 现在已经从“只做 collect-only/stub 脚本”推进到“真实 EL 客户端可启动、可鉴权、可响应 Engine API capability 探针”
- 这一步为后续真实 Hive 执行提供了必要前置条件

处理状态：`resolved`

### F-013 Full Hive shard execution is still blocked by missing Engine API execution semantics

事实：

- 当前本地代码里：
  - `internal/api/engine_api_v1.go`
  - `internal/api/engine_api_blob.go`
  - `internal/api/engine_api_v4.go`
- `NewPayloadV1/V2/V3/V4` 目前是输入校验后返回 `SYNCING`
- `ForkchoiceUpdatedV1/V2/V3/V4` 目前也是输入校验后返回 `SYNCING`
- `GetPayloadV1/V2/V3/V4` 目前仍返回 `payload not found`
- 这意味着虽然 `engine_exchangeCapabilities` / `engine_getClientCapabilitiesV1` 已能调用，但 execution-spec-tests 的真实 payload / forkchoice 语义还没落地
- 在真实 Hive 运行里，`./build/bin/hive --client-file ./n42-clients.yaml --client n42_local --sim ethereum/engine --sim.limit 'engine-auth/' --sim.parallelism 1`
  已经跑到 simulator 级执行，结果是：
  - `suites=1`
  - `tests=8`
  - `failed=7`
- 第一批失败的共同触发点是 `CLMocker` 的 `ProduceSingleBlock()`：
  - `engine_forkchoiceUpdatedV1` 期望 `{payloadStatus:{status:"VALID", latestValidHash:headBlockHash}, payloadId:...}`
  - 当前 N42 返回 `{payloadStatus:{status:"SYNCING"}, payloadId:null}`
- 从同一轮日志还能直接看到一个额外兼容面：
  - `eth_getBlockByNumber("latest")` 返回的创世 `hash` 字段是 `0x99396d2a...ce23d12b`
  - Hive client 随后基于返回的 header 字段计算并发给 `engine_forkchoiceUpdatedV1` 的 `headBlockHash` 是 `0xfb8b0d00...5ae58c16`
  - 这说明对外 Ethereum header-hash 兼容也还没有对齐，至少当前 RPC 输出和 Hive/go-ethereum 的 header hash 计算结果不一致

影响：

- 不能把当前状态写成“已完成 Hive shard wall clock 74 min”
- 可以客观确认 auth、bootstrap、capability surface、真实 simulator 启动链路都已完成，但 full shard 仍被 execution semantics 和外部 hash 兼容阻塞

处理状态：`confirmed`

### F-014 Hive proxy address discovery was incomplete on Docker Desktop style networking

事实：

- `tests/eth-hive/internal/libdocker/container.go` 之前只读取 `container.NetworkSettings.IPAddress`
- 在当前本机 Docker 环境里，普通 `docker inspect` 的有效容器地址在 `NetworkSettings.Networks.bridge.IPAddress`
- 真实 Hive 首跑时，proxy 地址被组成了 `http://:8081/testsuite`，直接导致：
  - `Post "http://:8081/testsuite": dial tcp :8081: connect: connection refused`
- 现已补成：
  - 优先读取 `NetworkSettings.IPAddress`
  - 为空时按网络名排序后回退到 `NetworkSettings.Networks[*].IPAddress`
- 回归测试已加在：
  - `tests/eth-hive/internal/libdocker/container_test.go`

影响：

- 真实 Hive 运行不再卡在 proxy / simulator API 入口
- 这一步把 Phase 7 从“只有本地 curl 与 collect-only”推进到了“真实 simulator 已执行”

处理状态：`resolved`

## Execution Log

### 2026-03-16 Phase 0

- 建立测试专项总计划
- 建立正式 inventory 生成器
- 固化 `431` 正式测试文件口径
- 确认外部镜像测试资产与主仓库测试代码需要分开处理
- 确认 EEST 本地环境需固定 Python `3.13`

### 2026-03-16 Phase 1

- 进入 `tests/` 入口层清理
- 第一批文件：`tests/eth_state_test.go`
- `tests/eth_state_test.go` 中 6 个默认 pass / 仅日志入口已改成显式 `t.Skip`
- 补充 `tests/eth_state_unit_test.go`
- 修复 `ParseBigInt("xyz")` 之前静默返回 `0,nil`
- 修复 `ParseHexOrDecimal("42")` 之前误按十六进制解析成 `66`
- `tests/eth_test_runner_test.go` 中 4 个 count-only / inventory-only 入口已改成显式 `t.Skip`
- 新增 `scripts/collect_eest_shards.sh`
- 新增 `scripts/run_eest_shards.sh`
- 已实测 `fill` 口径 shard collect：
  - `paris+shanghai=49829`
  - `cancun=51683`
  - `prague=59945`
  - `osaka=41843 (rc=5)`
  - `rlp=49851`
- 已验证 `run_eest_shards.sh` 的 dry-run 结果目录：
  - `tests/results/eest-shards/20260316-192414Z`
- 已确认 `consume engine --input stable@latest --collect-only` 当前还缺 `HIVE_SIMULATOR`
- `tests/full_state_test.go` 和 `tests/analyze_failures_test.go` 的分析型入口已改成显式 `t.Skip`
- 补充 `tests/full_state_unit_test.go`
- `tests/bls_precompile_test.go` 修复了 `msm_G1_bls.json` / `msm_G2_bls.json` 被误跳过的问题
- 已把 `tests/bls_precompile_test.go` 的 gas 检查升级成 fail-closed，并修复 `params/protocol_params.go` / `internal/vm/contracts_bls12381.go` 的 BLS gas 偏差
- `internal/vm/eips_pectra_test.go` 新增 BLS MSM / pairing gas 回归
- `tests/dapp_compat*_test.go`、`tests/prediction_market_compat_test.go`、`tests/zk_evm_compat_test.go` 中 summary/report 型入口已改成显式 `t.Skip`
- `tests/zk_evm_compat_test.go` 的 rollup gas 测试现在会对超阈值 gas 直接 fail
- `tests/integration_test.go` 和 `tests/refactoring_test.go` 的隔离型测试已增加 `t.Parallel()`

### 2026-03-16 Phase 6/7 Follow-up

- 新增 `scripts/eest_hive_stub.py`，提供最小 `/clients` 和 `/hive` API，仅用于 `consume engine --collect-only`
- `scripts/collect_eest_shards.sh` 现在支持 `EEST_HIVE_STUB=1`
- 修复 `scripts/collect_eest_shards.sh` 把 `no tests collected` 的输出行数误算成 count 的问题
- 修复 `scripts/collect_eest_shards.sh` 误把 `consume --sim.limit collectonly` 的最终 pytest summary 当成 shard count 的问题；现在会读取 `pytest-regex selected N tests to run for regex: ...`
- `scripts/collect_eest_shards.sh` / `scripts/run_eest_shards.sh` 已切到正式的 `--sim.limit` 口径，不再把 `-k` 混进 `consume engine`
- 已实测 `consume engine stable@latest --collect-only`：
  - `paris+shanghai=3573`
  - `cancun=17783`
  - `prague=20878`
  - `osaka=0 (rc=5, no tests collected)`
  - `rlp=2132`
- 已实测本地直接运行的 Hive authrpc 探针：
  - 带 JWT 的 `engine_exchangeCapabilities` 返回完整方法集
  - 无 JWT 返回 `403`
  - `engine_getClientCapabilitiesV1` 返回 `supportedForks=["paris","shanghai","cancun","prague","pectra"]`
- 已实测 `tests/eth-hive/clients/n42` 容器包装层：
  - 重新生成 `n42-local` 源码快照后，Docker 容器能正常 init / start
  - 容器日志不再出现 `invalid engine name`
  - 容器 authrpc 上的 `engine_exchangeCapabilities` 已返回预期结果
- 已确认本机 `stable@latest` cache 版本是 `v5.4.0`
- 已确认以上 `consume-engine` collect 数与 `v5.4.0` stable cache 的 engine fixture index 一致
- `tests/dapp_compat*_test.go`、`tests/prediction_market_compat_test.go`、`tests/zk_evm_compat_test.go` 的顶层只读检查已增加 `t.Parallel()`
- `go test -count=1 ./tests` 当前结果：`ok`，耗时 `0.802s`
- `scripts/run_eest_shards.sh` 已重新验证 dry-run，结果目录：
  - `tests/results/eest-shards/20260316-195807Z`
  - `tests/results/eest-shards/20260316-203112Z`
- `tests/eth-hive/internal/libdocker/container.go` 已修复 Docker proxy 地址发现只读 `NetworkSettings.IPAddress` 的问题
- `tests/eth-hive/internal/libdocker/container_test.go` 已新增 network fallback 回归
- 已重编译 `tests/eth-hive/build/bin/hive`
- 已实测真实 Hive suite：
  - `./build/bin/hive --client-file ./n42-clients.yaml --client n42_local --sim ethereum/engine --sim.limit 'engine-auth/' --sim.parallelism 1 --results-root ../results/hive-auth-20260316-233355Z --docker.output`
  - 结果：`suites=1 tests=8 failed=7`
  - 结果目录：`tests/results/hive-auth-20260316-233355Z`
- 已确认真实 Hive 不再卡在 proxy / client bootstrap，而是进入 Engine API 真实失败面：
  - `engine_forkchoiceUpdatedV1` 仍返回 `SYNCING`
  - `CLMocker` 因拿不到 `VALID + payloadId` 而失败
