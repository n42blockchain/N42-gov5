# Test Code Review Plan

## 目标

对仓库内全部测试代码文件做一次逐文件审查，修正现有测试中的错误、不稳定点和弱断言，补齐高价值缺口，并在每个阶段完成后用包级和仓库级验证闭环。

## 范围

- 纳入：`rg --files -g '*_test.go'` 命中的全部测试文件
- 当前正式口径：`429` 个 `*_test.go`
- 不纳入：`.codex-cache/`、运行时数据目录、构建产物目录

## 基线分布

按顶级目录统计：

- `internal`: `173`
- `lib`: `109`
- `common`: `68`
- `modules`: `33`
- `tests`: `15`
- `accounts`: `8`
- `cmd`: `5`
- `utils`: `4`
- `api`: `3`
- `conf`: `3`
- `log`: `3`
- `contracts`: `2`
- `params`: `1`
- `pkg`: `1`
- `tools`: `1`

当前高密度子模块：

- `internal/avm`: `28`
- `internal/vm`: `27`
- `common/crypto`: `23`
- `internal/api`: `22`
- `internal/consensus`: `18`
- `lib/kv`: `18`
- `lib/crypto`: `13`
- `modules/rawdb`: `13`
- `internal/p2p`: `12`
- `internal/sync`: `12`
- `modules/state`: `12`
- `common/transaction`: `11`
- `lib/state`: `10`

## 总体策略

执行顺序遵循“先高风险主路径、再底层库、最后慢集成”的原则。每个阶段内按包路径字典序逐文件处理，避免来回切换上下文。

每个测试文件都按同一套检查框架处理：

1. 读测试文件和对应生产代码。
2. 判断测试目标是否仍然有效，是否已经偏离当前实现。
3. 识别并修正以下问题：
   - 错误断言、空断言、只测 happy path
   - 依赖睡眠、时间、随机数、全局变量导致的不稳定
   - fixture 过重、重复 setup、清理不完整
   - nil、空输入、边界值、错误输入、回归路径缺失
   - channel/goroutine/context 清理不完整
   - 永久 `t.Skip`、条件跳过说明不足
   - benchmark/fuzz/helper 组织混乱
4. 补充高价值缺口：
   - 已修 bug 的回归测试
   - API/序列化 round-trip
   - 数据结构不变量
   - 并发/生命周期错误路径
5. 运行最小必要验证：
   - 先跑文件级或 `-run` 子集
   - 再跑包级
   - 阶段结束后跑阶段汇总验证

## 分阶段计划

### Phase 0: 基线与清单

目标：

- 固化测试文件总量、目录分布、阶段顺序
- 建立测试审查 findings 记录格式

动作：

- 生成测试文件正式清单
- 记录每阶段包列表和预期验收命令
- 建立 `TEST_CODE_REVIEW_FINDINGS.md` 作为执行记录

验收：

- 计划文件落盘
- 测试文件统计口径固定

### Phase 1: 入口层与基础设施测试

优先包：

- `cmd/evmsdk`
- `conf`
- `api/protocol`
- `accounts/keystore`
- `log`
- `params`
- `pkg/errors`
- `tools/tpsbench`
- `utils`

重点检查：

- CLI/SDK 生命周期测试是否覆盖启动、停止、重启、错误输入
- 配置默认值和解析测试是否覆盖非法配置
- 基础工具测试是否存在平台依赖、路径依赖、环境污染

阶段验收：

- `go test -count=1 ./cmd/evmsdk ./conf ./api/protocol/... ./accounts/... ./log/... ./params ./pkg/errors ./tools/... ./utils/...`

### Phase 2: 核心执行、状态与存储测试

优先包：

- `common/block`
- `common/transaction`
- `common/types`
- `common/rlp`
- `modules/state/...`
- `modules/rawdb/...`
- `internal/blockchain*`
- `internal/block_validator*`
- `internal/evm*`
- `internal/forkchoice*`
- `internal/state_processor*`
- `internal/vm/...`
- `internal/avm/...`
- `internal/txspool`
- `internal/snapshot`
- `lib/state`
- `lib/kv/...`
- `lib/commitment`
- `lib/jmt`

重点检查：

- 状态转换和区块处理断言是否覆盖错误路径和边界高度
- DB 读写对称性、索引、snapshot、rollback 回归是否到位
- VM/AVM opcode、gas、EOF、precompile 测试是否缺少边界和负例
- 测试是否偷偷依赖全局链状态或执行顺序

阶段验收：

- `go test -count=1 ./common/block ./common/transaction ./common/types ./common/rlp ./modules/state/... ./modules/rawdb/... ./internal/... ./lib/state ./lib/kv/... ./lib/commitment ./lib/jmt`

### Phase 3: 共识、网络、节点与 RPC 测试

优先包：

- `internal/api`
- `internal/consensus/...`
- `internal/p2p/...`
- `internal/sync/...`
- `internal/node`
- `internal/network/...`
- `internal/miner/...`
- `modules/rpc/jsonrpc`
- `modules/event/v2`
- `internal/tracers/...`
- `internal/metrics/prometheus`

重点检查：

- 订阅、取消订阅、断连、重连、超时、错误码路径
- 共识签名、validator 集、header/block number 边界
- p2p/编码/解码测试是否覆盖坏包和畸形输入
- metrics/tracing 测试是否污染全局注册表或默认 handler

阶段验收：

- `go test -count=1 ./internal/api ./internal/consensus/... ./internal/p2p/... ./internal/sync/... ./internal/node ./internal/network/... ./internal/miner/... ./modules/rpc/jsonrpc ./modules/event/v2 ./internal/tracers/... ./internal/metrics/prometheus`

### Phase 4: 密码学、ZK、合约与工具链测试

优先包：

- `common/crypto/...`
- `lib/crypto/...`
- `internal/zkprover/...`
- `internal/zkverifier`
- `contracts/...`

重点检查：

- 序列化/反序列化、零值、nil、非法字节流、长度边界
- 签名验证、批处理验证、proof 结构不变量
- guest/prover/verifier 输入输出的一致性
- 合约事件、订阅、错误输入和 no-op 降级路径

阶段验收：

- `go test -count=1 ./common/crypto/... ./lib/crypto/... ./internal/zkprover/... ./internal/zkverifier ./contracts/...`

### Phase 5: 基础库与慢速集成测试

优先包：

- `lib/common`
- `lib/seg`
- `lib/recsplit`
- `lib/rlp`
- `lib/rlp2`
- `lib/downloader`
- `lib/diagnostics`
- `lib/log`
- `lib/txpool`
- `lib/types`
- `tests`

重点检查：

- 低层库测试是否存在实现细节绑死、跨平台假设
- 慢集成测试是否存在资源泄漏、外部依赖不透明、失败信息不足
- 兼容性测试是否明确预期、是否能稳定复现失败

阶段验收：

- `go test -count=1 ./lib/...`
- `go test -count=1 ./tests`

### Phase 6: 统一回归与收口

动作：

- 回跑全部触及包
- 运行 `go vet ./...`
- 运行 `go test ./...`
- 视需要运行 `make build`

收口标准：

- 全部阶段完成
- 无未记录的测试 debt
- 所有新增修复都有对应回归
- 文档、findings、执行命令齐全

## 逐文件执行规则

每个文件固定按这套顺序处理：

1. 记录文件路径和对应生产代码路径
2. 判断测试类型：
   - 单元测试
   - 集成测试
   - benchmark
   - fuzz
   - 测试辅助文件
3. 标记问题类型：
   - `broken`
   - `flaky`
   - `weak_assertion`
   - `missing_negative_case`
   - `missing_regression`
   - `global_state_leak`
   - `cleanup_missing`
   - `slow_or_redundant`
4. 修正或补全
5. 运行最小验证并记录命令

## 产出物

1. `docs/engineering/TEST_CODE_REVIEW_PLAN.md`
2. `docs/engineering/TEST_CODE_REVIEW_FINDINGS.md`
3. 相关测试修复和补全代码
4. 阶段性验证记录

## 当前状态

- `2026-03-16` 已完成测试专项执行首轮收口
- `429` 个 `*_test.go` 已纳入正式清单与模式扫描
- 已落修复：
  - `cmd/evmsdk/common_test.go`
  - `accounts/keystore/account_cache_test.go`
  - `accounts/keystore/keystore_test.go`
  - `utils/safego_test.go`
  - `utils/async_test.go`
  - `log/root_test.go`
  - `internal/zkprover/service_test.go`
  - `internal/sync/state_machine_test.go`
- 专项 findings 已写入 `docs/engineering/TEST_CODE_REVIEW_FINDINGS.md`
- 外部夹具依赖、平台特定 skip 和旧库历史 debt 已归档，不再算作未记录项
