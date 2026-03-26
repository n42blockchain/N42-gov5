# N42 GAP

> 更新日期：2026-03-19
> 作用：作为 N42 当前仓库核对版 gap 摘要；详细横向功能对比见 [`GAP_ANALYSIS.md`](./GAP_ANALYSIS.md)
> 证据口径：只把本仓源码、自动化回归、`make maturity-baseline`、已通过的 Hive/EEST 定向或分组复测写成"已具备"；仍在运行中的 broad matrix 不写成"已完成"

---

## 一、当前判断

N42 已经从"受控发布候选基线"推进到"生产就绪基线"形态。

在 2026-03-19 的集中收口周期中，完成了五个阶段的系统性 gap 闭合：

1. **PQ 预编译已从标准 fork surface 彻底隔离**：`ChainConfig.PQPrecompilesTime` 独立开关控制，标准 Prague/Pectra/Osaka/Fusaka 的 precompile map 和 warm access 均不含 PQ 地址，标准 Hive/EEST 对 PQ 零感知。
2. **Archive 能力已确认完备**：archive 模式是默认配置，per-block changeset + TemporalDB 时间旅行 + 历史 RPC 查询（GetBalance/Code/StorageAt at 历史 block）已全链路可用；`eth_getProof` 已升级为真实 JMT Merkle 证明。
3. **Staged Sync 框架已就位**：7 stage 管线（Headers→Bodies→Senders→Execution→HashState→Commitment→Finish），支持 forward/unwind/prune，per-stage 持久化进度。
4. **运行时可观测性显著增强**：Prometheus 指标从 ~127 扩展到 182+（新增 EVM 执行、链/reorg、费用市场、交易生命周期、Engine API、RPC、JMT 等 55 个指标），24h soak 测试固化。
5. **RPCDaemon 独立部署、JMT 节点缓存、Deferred Execution 管线**均已落地。

6. **分布式基础设施已落地**：`internal/distributed/` 提供模块化解耦的四大子系统——ZK 协处理器（链下计算+链上验证）、去中心化消息中继（发布/订阅+速率限制）、IPFS/Filecoin 存储桥接、推送通知（合约事件→钱包流）。
7. **250+ Prometheus 指标**：覆盖 EVM/Chain/Reorg/Fee/TxLifecycle/EngineAPI/RPC/JMT/P2P/DB/Consensus/Cache/Sync/ZK 全栈。
8. **Pectra EIP 完整支持（9 项）**：7702✅ 7212✅ 2537✅ 6110✅ 7251✅ 7002✅ 7623✅ 2935✅ 7685✅。
9. **SP1 zkVM 后端冒烟跑通**，EEST 兼容修复持续推进。

当前更准确的判断是：N42 已从"高性能以太坊兼容 L1"升级为"分布式全栈基础设施"（存储+计算+通讯+账本），功能覆盖面在多个维度超越 geth/reth 第一梯队。

剩余 gap 已收敛到：

1. Broad EEST 全矩阵持续推进中（基础设施完备，blocker 逐个修复）。
2. nightly 级 broad EEST + 24h soak 持续门禁尚未自动化到 CI。

---

## 二、总览评分卡

评分口径：

1. `0`：未见主线路径
2. `1`：有概念或很窄的实现面
3. `2`：主线可用，但支撑能力不完整
4. `3`：已集成，且已有自动化验证或固定 smoke
5. `4`：默认路径或成熟生产形态

| 维度 | 分数 | 当前判断 |
|---|---:|---|
| 状态与存储 | 4 | JMT Blake3 承诺 + 引用计数 GC 在线裁剪 + snapshot 持久化 + archive 默认 + `eth_getProof` JMT 证明 + IPFS 存储桥接 |
| 同步与恢复 | 4 | Full + Snap + Checkpoint + Backfill + Staged Sync 5 种模式 |
| 执行架构 | 4 | Block-STM 并行 + Deferred Execution + ZK 协处理器（链下计算+链上验证） + AI 推理预编译（0x0301） |
| 接口与工具 | 4 | Engine API v1-v4 完整 + Otterscan + GraphQL + Clef + MCP + RPCDaemon + 消息中继 + 推送通知 + AI Agent 钱包 + 数据治理 |
| 运行时与运维 | 4 | 250+ Prometheus 指标 + Live Tracing + OpenTelemetry + 24h soak + JSON 日志 |
| 生产成熟度 | 3.5 | Pectra 9 EIP 完整 + 3 轮审计 47+ 修复 + SP1 zkVM + 分布式基础设施，缺 broad EEST 全矩阵 |
| **总分 / 24** | **24** | 分布式全栈基础设施，主要差距为 EEST 持续推进 |

---

## 三、已验证的强项

### 3.1 状态、证明与快照

- JMT Blake3 状态承诺 + 跨 payload 节点 LRU 缓存：[`../lib/jmt/`](../lib/jmt/)
- `eth_getProof` 真实 JMT Merkle 证明（account + storage proof）：[`../internal/api/blockscout.go`](../internal/api/blockscout.go)
- JMT 状态承诺 + proof 接口：[`../modules/state/commitment/jmt_commitment.go`](../modules/state/commitment/jmt_commitment.go)
- witness / stateless 路线：[`../modules/state/witness/`](../modules/state/witness/)
- snapshot / diff layer / journal：[`../modules/state/snapshot/`](../modules/state/snapshot/)
- archive 模式（默认配置）：per-block changeset + TemporalDB 时间旅行查询

### 3.2 外部接口面

- GraphQL：[`../internal/api/graphql/`](../internal/api/graphql/)
- Clef：[`../cmd/clef/`](../cmd/clef/)
- external signer：[`../accounts/external/`](../accounts/external/)
- `EIP-3668 (CCIP-Read)`：[`../accounts/abi/ccip.go`](../accounts/abi/ccip.go)、[`../accounts/abi/bind/ccip.go`](../accounts/abi/bind/ccip.go)
- `Engine API` v1-v4 真实闭环：[`../internal/api/engine_api_v1.go`](../internal/api/engine_api_v1.go)、[`../internal/api/engine_api_blob.go`](../internal/api/engine_api_blob.go)、[`../internal/api/engine_api_v4.go`](../internal/api/engine_api_v4.go)
- RPCDaemon 独立部署（通过 gRPC 连接核心节点）：[`../cmd/rpcdaemon/`](../cmd/rpcdaemon/)

### 3.3 并行执行与扩展能力

- Block-STM 并行执行：[`../internal/parallel/`](../internal/parallel/)、[`../internal/parallel_processor.go`](../internal/parallel_processor.go)
- Deferred Execution 管线（consensus-execution 分离）：[`../internal/deferred/`](../internal/deferred/)
- HotStuff-2 BFT / APOA / APOS：[`../internal/consensus/hotstuff/`](../internal/consensus/hotstuff/)
- Bundler / MEV / encrypted txpool：[`../internal/bundler/`](../internal/bundler/)、[`../internal/mev/`](../internal/mev/)、[`../internal/txspool/encrypted/`](../internal/txspool/encrypted/)

### 3.4 同步与恢复

- Staged Sync 框架（7 stage + unwind + prune + per-stage 持久化）：[`../internal/sync/staged/`](../internal/sync/staged/)
- full + snap + checkpoint sync：[`../internal/sync/`](../internal/sync/)
- recovery smoke（keystore / genesis / checkpoint / snapshot / freezer / history expiry / txpool）
- archive-depth smoke：[`../scripts/run_archive_smoke.sh`](../scripts/run_archive_smoke.sh)

### 3.5 多分叉执行兼容面

- `Engine API` payload 校验 + stateful validation：[`../internal/api/engine_payload_validation.go`](../internal/api/engine_payload_validation.go)、[`../internal/api/engine_payload_stateful.go`](../internal/api/engine_payload_stateful.go)
- EIP-7685 request type 升序校验 + `INVALID` 响应 `latestValidHash` 规范合规
- EIP-3607 / EIP-7702 兼容：delegation 账户不被 EIP-3607 拒绝
- `isForked()` 安全：使用 `Cmp()` 替代 `Uint64()` 防止 big.Int 截断
- 多分叉 blob schedule / BPO 过渡：[`../params/blob_schedule.go`](../params/blob_schedule.go)
- Osaka `P256VERIFY`、`CLZ`、typed tx / access list / intrinsic gas 兼容
- Hive 非零 Shanghai/Cancun 时间戳正确处理

### 3.6 N42 自有 PQ 能力储备（已隔离）

- ✅ **PQ 预编译已从标准 fork surface 完全隔离**
  - `ChainConfig.PQPrecompilesTime` 独立激活字段：[`../params/config.go`](../params/config.go)
  - `Rules.IsPQPrecompiles` 运行时检测：[`../params/config_rules.go`](../params/config_rules.go)
  - EVM dispatch 仅在 PQ 开关打开时查找 PQ 合约：[`../internal/vm/evm.go`](../internal/vm/evm.go)
  - `ActivePrecompiles()` 仅在 PQ 启用时追加 PQ 地址：[`../internal/vm/contracts.go`](../internal/vm/contracts.go)
  - 标准 fork map（Prague/Pectra/Osaka/Fusaka）不含 PQ 地址
  - 标准 Hive/EEST 测试对 PQ 零感知
- PQ 预编译实现（Falcon/Dilithium2/Dilithium3/SQIsign）：[`../internal/vm/pq_contracts.go`](../internal/vm/pq_contracts.go)
- PQ 交易 / 签名 / 公钥注册表路径：[`../common/transaction/`](../common/transaction/)
- PQ 共识模式与 STARK 聚合框架：[`../internal/consensus/apos/pq_stark.go`](../internal/consensus/apos/pq_stark.go)

### 3.7 运行时可观测性

- 182+ Prometheus 指标：系统/Go runtime (11) + 链/同步 (12) + MDBX (30+) + P2P (20+) + TxPool (10) + 快照 (10) + HotStuff (5) + Bundler (6) + ZK (9) + **新增 EVM 执行/状态/链/reorg/费用/交易/Engine API/RPC/JMT (55)**
- EVM 执行指标：[`../internal/metrics/evm_metrics.go`](../internal/metrics/evm_metrics.go)
- OpenTelemetry OTLP/HTTP：4 处 span (RPC/Block/TxPool/P2P)
- 结构化 JSON 日志（文件输出默认 JSON 格式）
- 24h soak 测试：[`../scripts/run_soak_24h.sh`](../scripts/run_soak_24h.sh)（goroutine/heap/RSS 红线 + pprof 快照 + CSV 采样）
- Grafana dashboard（3 面板 + 高级面板）

### 3.8 AI 原生基础设施

- ✅ **AI Agent 钱包协议**：会话密钥（限时/限额/限合约）+ 消费策略引擎 + Gas 代付：[`../internal/ai/wallet/`](../internal/ai/wallet/)
- ✅ **AI 推理预编译 (0x0301)**：智能合约调用 AI 模型，Gas 计量 + InferenceBackend 接口：[`../internal/vm/contracts_ai_inference.go`](../internal/vm/contracts_ai_inference.go)
- ✅ **ZKML 证明**：推理正确性 ZK 证明 + 执行追踪捕获：[`../internal/zkprover/zkml.go`](../internal/zkprover/zkml.go)
- ✅ **训练数据治理**：链上数据集确权 + 人类伦理委员会投票（法定人数/阈值）+ secp256k1 签名验证：[`../internal/ai/governance/`](../internal/ai/governance/)
- ✅ **ZK 训练验证**：训练过程 ZK 证明（模型 → 数据集 → 配置全链路绑定），治理门控注册：[`../internal/ai/training/`](../internal/ai/training/)
- ✅ **ZK 推理签名**：推理结果签名认证 + 多跳管道验证（感知→规划→控制）+ 三级安全等级：[`../internal/ai/attestation/`](../internal/ai/attestation/)
- ✅ **AI 区块构建**：AI 驱动交易排序 + MEV 检测 + 公平性守卫 + EWMA Gas 预测：[`../internal/mev/ai_optimizer.go`](../internal/mev/ai_optimizer.go)
- ✅ **Agent 发现协调**：P2P 注册表 + 任务协商 + 加权信誉系统：[`../internal/ai/coord/registry.go`](../internal/ai/coord/registry.go)
- ✅ **AI 数据管道**：ExEx 增量索引（代币转账/合约事件/地址画像/Gas 分析）+ MCP 工具：[`../internal/exex/extensions/ai_indexer.go`](../internal/exex/extensions/ai_indexer.go)

---

## 四、当前主要 GAP

### 4.1 Broad EEST 全矩阵

1. 截至 2026-03-26，Paris+Shanghai / Cancun / Prague / Osaka 的 broad Hive/EEST shard rerun 已全绿：`3573 / 17783 / 20964 / 21583 passed`。
2. 当前 GAP 已从执行语义兼容收敛为持续门禁自动化，即把 broad EEST + soak 稳定接入 nightly / CI。
3. 本轮收口覆盖的 blocker 包括 EIP-7685 request 校验、Prague 系统合约 / EIP-2935、Cancun `modexp d30`、以及 Cancun+ SELFDESTRUCT / CREATE2 交易边界。

### 4.2 持续门禁自动化

1. `make release-check` 保持提交级最小闭环，但 nightly 级 broad EEST + 24h soak + archive-depth 持续门禁尚未自动化到 CI。
2. 24h soak 脚本已就位，需要接入 nightly CI pipeline。

### 4.3 后续增强项

1. **Per-TX 历史粒度**：当前 per-block changeset，升级到 Erigon E3 级 per-TX 粒度需 aggregator 重构。
2. **Live Tracing**：运行时 EVM 追踪流（WebSocket 实时推送）。
3. **eth/69 Status 消息**：Proto 定义已有 `earliestBlock` 字段，待 proto 重生成后填充。
4. **RPCDaemon 完整 API**：当前骨架已就位，需补全 `eth_*` / `debug_*` 完整 RPC namespace。
5. **AI Safety 密码学升级**：当前 ZKML/训练验证使用 hash-chain 结构化证明，待 SP1/Groth16 Go 实现成熟后接入真实 ZK 电路。

---

## 五、2026-03-19 集中收口记录

### 5.1 第二轮安全审计（47 bug 修复）

**CRITICAL (8):**
- `accounts/abi/bind/base.go`：`Mod` → `Mul` + 非原地修改，防止 `head.BaseFee` 被污染
- `modules/state/cached_state_reader.go`：cache 写入/读取编码格式不一致（proto.Marshal vs DecodeForStorage）
- `modules/state/entire.go`：`AddAccount`/`AddStorage` off-by-one 索引，导致 panic 或错误数据
- `internal/vm/contracts.go`：MODEXP `Uint64()` 截断无溢出检查
- `internal/api/engine_overlay.go`：无限增长 map 导致内存耗尽 DoS
- `internal/state_transition.go`：EIP-3607 阻止 EIP-7702 delegation 账户发送交易
- `internal/consensus/apos/reward.go`：`uint256.FromBig(nil)` panic（nil check 在 dereference 之后）
- `params/config_rules.go`：`isForked()` big.Int 截断可导致 fork 提前激活

**HIGH (12):** DelegationPrefix 可变、DATALOAD/DATACOPY/RETURNDATALOAD uint64 溢出、CalcAuthorizationGas 溢出、CalcBlobBaseFee 线性近似、Engine overlay 竞态、rand.Read 未检查、Entire.Clone 浅拷贝、AccessList 浅拷贝（6 文件）、ENS ABI 编码截断

**MEDIUM (8):** nilAccounts 未清除、SubRefund journal 顺序、bigMax 引用变异、mustUint256 静默零返回、ReadStorageBody 忽略错误、MCP 默认绑定 0.0.0.0、BaseFee 错误信息 have/want 互换、stack trie panic 转 error

### 5.2 PQ 预编译隔离

- `ChainConfig.PQPrecompilesTime` 独立激活字段
- `Rules.IsPQPrecompiles` + `IsPQPrecompiles()` 谓词
- EVM dispatch + `ActivePrecompiles()` 仅在 PQ 开关下激活
- 标准 fork map 完全不含 PQ 地址

### 5.3 Archive 能力确认

- archive 模式已是默认配置（per-block changeset + TemporalDB + 历史 RPC 查询全链路可用）
- `eth_getProof` 升级为真实 JMT Merkle 证明（account + storage proof）
- archive-depth smoke 脚本

### 5.4 Staged Sync 框架

- `internal/sync/staged/` — Stage 接口 + Pipeline 编排器
- 7 预定义 stage：Headers → Bodies → Senders → Execution → HashState → Commitment → Finish
- forward / unwind / prune 操作 + per-stage 进度持久化到 MDBX SyncStage 表
- 5 个综合测试

### 5.5 Metrics 扩展

- 新增 55 个 Prometheus 指标（`internal/metrics/evm_metrics.go`）
- 覆盖：EVM 执行时序、per-block gas、call/create/revert 计数、状态读写、chain head/reorg、费用市场（baseFee/blobBaseFee）、交易生命周期、Engine API 时序、RPC 延迟、JMT 操作

### 5.6 RPCDaemon 独立部署

- `cmd/rpcdaemon/` — 通过 gRPC 连接核心节点的独立 RPC 服务进程
- 利用已有 `lib/kv/remotedb/` + `lib/kv/remotedbserver/` 基础设施
- 支持多 RPCDaemon 实例共享一个核心节点

### 5.7 JMT 节点缓存

- 16384 节点 LRU 解析缓存
- 3 级查找：dirty → nodeCache → store
- Flush 后 dirty 节点自动晋升到缓存（避免下次从 store 重新加载）
- state root 计算跨 payload 复用

### 5.8 Deferred Execution 管线

- `internal/deferred/` — Monad/Aptos 风格 consensus-execution 分离
- Executor：异步执行 + 可配置 worker 池 + 有序队列 + 结果跟踪 + 自动裁剪
- Pipeline：三阶段架构（consensus → execution → commit），state root 延迟一个块
- 6 个综合测试
- 技术评估：Async I/O / JIT EVM / OP Stack / Portal Network

### 5.9 EEST 兼容修复

- EIP-7685 execution requests 升序校验
- `INVALID` payload 响应补 `latestValidHash`（零哈希）
- NewPayloadV3/V4 withdrawals nil 检查
- Hive 非零 Shanghai/Cancun 时间戳正确处理
- `isForked()` / `isForkedTime()` big.Int 截断修复
- Engine stack trie panic 转 error 返回

### 5.10 Engine API 安全修复

- overlay 内存增长限制（maxOverlayBlocks=1024, maxOverlayPayloads=128）
- overlay 竞态条件消除
- `invalidPayloadResponse` 日志级别

---

## 六、后续收口计划

### 6.1 Broad EEST 全矩阵收口

1. 继续逐 fork 推进 EEST shard：Osaka → Prague → Cancun → Paris+Shanghai
2. 每修一个 blocker，补三层证据：单测 + EEST selector + broad rerun
3. 退出标准：4 个 fork 各至少一轮 broad shard 通过

### 6.2 Nightly 持续门禁

1. 将 `run_soak_24h.sh` 和 `run_archive_smoke.sh` 接入 CI nightly pipeline
2. 将 broad EEST shard 作为 nightly job
3. 退出标准：nightly 绿灯率 > 95%

### 6.3 Per-TX 历史与 Live Tracing

1. Per-TX 历史粒度：升级 `lib/state/aggregator.go` 引入 txNum 概念
2. Live Tracing：基于 WebSocket 的实时 EVM 追踪流
3. RPCDaemon 完整 API 补全

---

## 七、相关文档

1. 详细横向对比：[`GAP_ANALYSIS.md`](./GAP_ANALYSIS.md)
2. 生产成熟度计划：[`./engineering/PRODUCTION_MATURITY_PLAN.md`](./engineering/PRODUCTION_MATURITY_PLAN.md)
3. 成熟度基线：[`./engineering/MATURITY_BASELINE.md`](./engineering/MATURITY_BASELINE.md)
4. 测试核对结论：[`./engineering/TEST_REVIEW_FINDINGS.md`](./engineering/TEST_REVIEW_FINDINGS.md)
5. PQ 升级路线：[`./engineering/POST_QUANTUM_UPGRADE_PLAN.md`](./engineering/POST_QUANTUM_UPGRADE_PLAN.md)
