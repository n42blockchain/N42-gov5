# N42 GAP

> 更新日期：2026-03-18
> 作用：作为 N42 当前仓库核对版 gap 摘要；详细横向功能对比见 [`GAP_ANALYSIS.md`](./GAP_ANALYSIS.md)
> 证据口径：只把本仓源码、自动化回归、`make maturity-baseline`、已通过的 Hive/EEST 定向或分组复测写成“已具备”；仍在运行中的 broad matrix 不写成“已完成”

---

## 一、当前判断

N42 已经不是“功能面窄”的客户端。状态承诺、快照、Witness、GraphQL、Clef、external signer、`Engine API`、`EIP-3668 (CCIP-Read)`、HotStuff、Bundler、MEV、Encrypted TxPool、PeerDAS、并行执行等实现都已落在主仓里。

当前更准确的判断是：N42 已经具备“受控发布候选基线”，并且外部语义已经从“接口存在 + smoke”推进到“多分叉 execution 兼容正在系统收口”；但它还不是“广域互操作与深历史语义都已收齐”的形态。

另一个需要明确分层的点是：N42 自有的 post-quantum 能力和 Ethereum 标准执行层兼容面不是一回事。PQ 交易、PQ 共识和 PQ 预编译可以继续作为 N42 差异化能力存在，但它们不应再混入 Prague / Pectra / Osaka 这类标准 fork 的默认 precompile surface。

剩余主要 gap 已经收敛到：

1. archive / historical proof / 深历史查询与恢复语义。
2. Paris+Shanghai / Cancun / Prague / Osaka 的 broad Hive/EEST 全矩阵仍未收齐，当前只能客观写成“已完成一批定向与分组兼容修复”。
3. 24h / nightly 级资源边界和更长周期 soak。
4. 更大范围的 archive-depth / broad RPC compatibility 互操作矩阵。

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
| 状态与存储 | 3 | JMT / witness / snapshot 路线较强，但 archive / historical proof 仍弱于 geth / Reth / Erigon |
| 同步与恢复 | 3 | full + snap + checkpoint 已有，恢复 smoke 已建立，但缺 staged sync / unwind / archive-depth recovery matrix |
| 执行架构 | 3 | 已有 wave-based Block-STM、并行预热和 zk / witness 路线，但仍缺更成熟的冲突规划与 async I/O |
| 接口与工具 | 4 | GraphQL、Clef、external signer、debug/trace/filter、`Engine API`、`CCIP-Read` 已有真实 smoke，且 `Engine API` 已推进到更接近执行语义的兼容修复 |
| 运行时与运维 | 3 | Prometheus、tracing、ops smoke、runbook、release checklist 已具备最小闭环，但长时间资源红线仍弱 |
| 生产成熟度 | 3 | baseline、ops、interop、soak、release gate 已固定，多分叉 EEST/Hive 兼容显著推进，但还没有 broad full-matrix、archive-depth 和长期连续验证 |
| **总分 / 24** | **19** | 已具备受控发布候选基线，仍未到 archive / 长周期 fully-certified 生产形态 |

---

## 三、已验证的强项

### 3.1 状态、证明与快照

- JMT / 状态承诺：[`../lib/jmt/`](../lib/jmt/)
- witness / stateless 路线：[`../modules/state/witness/`](../modules/state/witness/)
- snapshot / diff layer / journal：[`../modules/state/snapshot/`](../modules/state/snapshot/)

### 3.2 外部接口面

- GraphQL：[`../internal/api/graphql/`](../internal/api/graphql/)
- Clef：[`../cmd/clef/`](../cmd/clef/)
- external signer：[`../accounts/external/`](../accounts/external/)
- `EIP-3668 (CCIP-Read)`：[`../accounts/abi/ccip.go`](../accounts/abi/ccip.go)、[`../accounts/abi/bind/ccip.go`](../accounts/abi/bind/ccip.go)
- `Engine API` 最小真实 round-trip：[`../internal/api/engine_api_v1.go`](../internal/api/engine_api_v1.go)、[`../internal/api/engine_api_blob.go`](../internal/api/engine_api_blob.go)、[`../internal/api/engine_api_v4.go`](../internal/api/engine_api_v4.go)

### 3.3 并行执行与扩展能力

- 并行执行：[`../internal/parallel/`](../internal/parallel/)、[`../internal/parallel_processor.go`](../internal/parallel_processor.go)
- HotStuff / APOA / APOS：[`../internal/consensus/hotstuff/`](../internal/consensus/hotstuff/)
- Bundler / MEV / encrypted txpool：[`../internal/bundler/`](../internal/bundler/)、[`../internal/mev/`](../internal/mev/)、[`../internal/txspool/encrypted/`](../internal/txspool/encrypted/)

### 3.4 多分叉执行兼容面

- `Engine API` payload 语义已经补到 stateful validation 路径：[`../internal/api/engine_payload_validation.go`](../internal/api/engine_payload_validation.go)、[`../internal/api/engine_payload_stateful.go`](../internal/api/engine_payload_stateful.go)
- 多分叉 blob schedule / BPO 过渡：[`../params/blob_schedule.go`](../params/blob_schedule.go)、[`../conf/genesis_hive.go`](../conf/genesis_hive.go)
- 交易编码、签名和授权列表路径已补足 Ethereum/EIP 兼容语义：[`../common/transaction/ethereum_rlp.go`](../common/transaction/ethereum_rlp.go)、[`../common/transaction/transaction_signing.go`](../common/transaction/transaction_signing.go)、[`../internal/state_transition.go`](../internal/state_transition.go)
- Osaka `P256VERIFY`、`CLZ`、typed tx / access list / intrinsic gas 相关兼容修复已进入主仓测试路径：[`../internal/vm/contracts_p256.go`](../internal/vm/contracts_p256.go)、[`../internal/vm/eips_fusaka.go`](../internal/vm/eips_fusaka.go)、[`../internal/vm/eips_osaka.go`](../internal/vm/eips_osaka.go)

### 3.5 N42 自有 PQ 能力储备

- PQ 预编译实现与独立映射仍在仓内：[`../internal/vm/pq_contracts.go`](../internal/vm/pq_contracts.go)
- PQ 交易 / 签名 / 公钥注册表路径仍在仓内：[`../common/transaction/`](../common/transaction/)、[`../contracts/pqregistry/`](../contracts/pqregistry/)
- PQ 共识模式与 STARK 聚合框架仍在仓内：[`../internal/consensus/apos/pq_stark.go`](../internal/consensus/apos/pq_stark.go)
- 这些能力当前更适合被归类为 “N42 扩展能力储备”，而不是 “已纳入标准 Ethereum EL 兼容面”

---

## 四、当前主要 GAP

### 4.1 外部语义与互操作

1. `Engine API` 已不再只是 Hive `engine-auth` smoke，已经推进到 payload 结构、header 兼容、block RLP size、stateful execution 这一层；但 broad Hive/EEST 全矩阵和 61k+ execution-spec 级别的收口仍未完成。截至 2026-03-18，broad Osaka engine path 已推进到 Frontier scenario 层，最新首个失败已经下沉到更基础的 `INVALID` 场景语义，而不再只是 Osaka 新特性本身。
2. `GraphQL / Clef / external signer / CCIP-Read` 已进入固定回归，但长期外部生态兼容仍需要更重矩阵验证。

### 4.2 恢复性与历史能力

1. `keystore`、`snapshot journal`、`txpool journal`、`checkpoint`、`freezer` 和 `history expiry` 的边界与重启续跑关键路径已进入固定 recovery smoke。
2. archive / historical proof 查询仍未收齐。
3. 缺少 `staged sync + unwind + resume` 这一整套生产级同步恢复控制流。

### 4.3 运行时稳态

1. 已有 metrics / pprof / ops smoke / soak smoke，但 24h / nightly 级 goroutine、heap、队列和连接数红线仍未固化。
2. 当前已形成最小 runbook / checklist / release gate，后续重点变成长期数据和线上阈值回标。

### 4.4 运维拓扑与发布门禁

1. 仍是单进程节点形态，没有 `rpcdaemon/sentry` 级的拆分拓扑。
2. `make maturity-baseline`、`make ops-smoke`、`make interop-smoke`、`make soak-smoke`、`make release-check` 已形成固定 gate，但 nightly interop / 24h soak / broad EEST 持续门禁仍未落地。

### 4.5 PQ 能力放置与启用边界

1. 当前 PQ 预编译代码已经存在，但“存在代码”不等于“应该默认出现在标准 fork map 里”。标准 Prague / Pectra / Osaka / Fusaka 兼容面必须只暴露上游规范和当前 EEST/Hive 所认可的 precompile 集。
2. PQ 预编译应改成显式的 N42 扩展开关，而不是复用标准 fork 名字继承激活。更合适的落点是：
   - 独立 `ChainConfig` 开关，例如 `pqPrecompilesTime` / `pqPrecompilesBlock` / `enablePQPrecompiles`
   - 或仅在 N42 自定义链/自定义 genesis 下启用，而不是跟随 Prague / Osaka 自动打开
3. 地址空间也需要分层。若目标是保持 Ethereum EL 兼容，PQ 预编译不应长期占用标准低地址段的“看起来像上游 fork 默认表面”的位置。更稳妥的方案是迁移到明确的 N42 扩展地址段；如果短期内保留 `0x14-0x17` 作为兼容别名，也只能放在显式 PQ 扩展开关后面，不能进入标准 precompile warm set。
4. 测试矩阵需要拆开：
   - 标准 Hive / EEST / RPC compatibility 只验证标准 fork surface
   - PQ 预编译、PQ 交易、PQ 共识走 N42 自定义 genesis / 集成测试 / 专项压力测试
5. 启用顺序也应拆层：
   - Falcon：已接近“实现完整但需单独启用”
   - Dilithium2 / Dilithium3：仍需继续做完验证链路、gas 标定和跨模块回归
   - SQIsign：仍是占位，不应进入任何默认生产路径

---

## 五、最近已验证的收口

### 5.1 `Engine API` 与多分叉执行语义

- `newPayload / forkchoiceUpdated / getPayload / blobs` 已不再只是 stub surface，已有最小真实闭环测试。
- overlay 现在能把 Engine 导入块暴露给普通 RPC 查询路径。
- Hive `engine-auth` 现在能稳定通过，且 `RPCMarshalHeader` 已按 Shanghai / Cancun 系列分叉条件输出兼容字段。
- payload 校验已经补到共享 validation + stateful validation 路径，覆盖 `gasLimit`、`baseFee`、typed tx block RLP size、header/parent hash 兼容和按状态执行的无效块判定。
- 近期已落地一批真实 EEST blocker 修复，包括 BPO 时间驱动的 blob schedule、authorization list intrinsic gas、Osaka `P256VERIFY` gas、oversized block RLP 和 access-list/intrinsic gas 兼容。
- 最新一轮兼容修复已经把标准 Osaka precompile surface 和 N42 的 PQ 扩展面重新分开，避免把 PQ 地址误加入标准 fork 的执行与 warm-access 语义中；这类问题现在被明确归类为“扩展能力放置边界”，而不是“上游 fork 兼容项”。

相关文件：

- [`../internal/api/engine_api_v1.go`](../internal/api/engine_api_v1.go)
- [`../internal/api/engine_api_blob.go`](../internal/api/engine_api_blob.go)
- [`../internal/api/engine_api_v4.go`](../internal/api/engine_api_v4.go)
- [`../internal/api/engine_payload_validation.go`](../internal/api/engine_payload_validation.go)
- [`../internal/api/engine_payload_stateful.go`](../internal/api/engine_payload_stateful.go)
- [`../params/blob_schedule.go`](../params/blob_schedule.go)
- [`../conf/genesis_hive.go`](../conf/genesis_hive.go)
- [`../internal/state_transition.go`](../internal/state_transition.go)
- [`../internal/vm/contracts_p256.go`](../internal/vm/contracts_p256.go)
- [`../internal/api/engine_api_blob_test.go`](../internal/api/engine_api_blob_test.go)
- [`../internal/api/engine_payload_validation_test.go`](../internal/api/engine_payload_validation_test.go)
- [`../internal/api/engine_payload_stateful_test.go`](../internal/api/engine_payload_stateful_test.go)

### 5.2 恢复性

- keystore watcher 漏事件会补扫刷新：[`../accounts/keystore/account_cache.go`](../accounts/keystore/account_cache.go)
- checkpoint 不再把“只有 canonical hash 的半写入块”误判成已恢复：[`../internal/sync/checkpoint/service.go`](../internal/sync/checkpoint/service.go)
- freezer 重启恢复改为按最小真实表项数恢复，而不是盲信滞后的元数据：[`../modules/rawdb/freezer/freezer.go`](../modules/rawdb/freezer/freezer.go)
- `history expiry` 相关的 `earliest` / `feeHistory` / canonical lookup 现在会对齐最早可用历史，且重启后会从持久化 earliest 继续推进：[`../internal/api/api.go`](../internal/api/api.go)、[`../internal/api/feehistory.go`](../internal/api/feehistory.go)、[`../internal/node/history_expiry_test.go`](../internal/node/history_expiry_test.go)、[`../turbo/rpchelper/helper.go`](../turbo/rpchelper/helper.go)

### 5.3 固定 gate

- 基线脚本：[`./engineering/MATURITY_BASELINE.md`](./engineering/MATURITY_BASELINE.md)
- 运维手册：[`./engineering/OPERATIONS_RUNBOOK.md`](./engineering/OPERATIONS_RUNBOOK.md)
- 发布清单：[`./engineering/PUBLISH_CHECKLIST.md`](./engineering/PUBLISH_CHECKLIST.md)
- 最新 full baseline：[`../build/maturity-baseline/20260317-074829Z/summary.md`](../build/maturity-baseline/20260317-074829Z/summary.md)
- 最新 ops smoke：[`../build/ops-smoke/20260317-075716Z/summary.md`](../build/ops-smoke/20260317-075716Z/summary.md)
- 最新 interop smoke：[`../build/interop-smoke/20260317-075747Z/summary.md`](../build/interop-smoke/20260317-075747Z/summary.md)
- 最新 soak smoke：[`../build/soak-smoke/20260317-075856Z/summary.md`](../build/soak-smoke/20260317-075856Z/summary.md)
- 最新 release gate：[`../build/release-check/20260317-075540Z/summary.md`](../build/release-check/20260317-075540Z/summary.md)

当前 recovery smoke 已覆盖：

1. keystore reload
2. genesis config
3. checkpoint recovery
4. snapshot journal
5. freezer recovery
6. history expiry recovery
7. txpool journal

---

## 六、细化收口计划

### 6.1 标准 EL 兼容层

1. 继续把 Paris+Shanghai / Cancun / Prague / Osaka 的 broad Hive/EEST 全矩阵跑到新的首个失败，并逐个把 blocker 压缩到可重复的 selector。
2. 每修一个 blocker，都补三层证据：
   - 对应模块单测
   - 精确 EEST/Hive selector 复测
   - broad rerun 证明断点向后推进
3. 标准兼容层的退出标准不是“某个 EIP 已有代码”，而是“标准 fork surface 不再混入 N42 自定义扩展，并且 broad matrix 持续向后推进”。

### 6.2 PQ 预编译与 N42 扩展层

1. 把 PQ 预编译、PQ 交易和 PQ 共识明确写成 N42 扩展能力，不再默认算入标准 Prague / Osaka 兼容面评分。
2. 为 PQ 预编译补一份单独的放置方案：
   - 激活条件：独立链配置开关或自定义 fork，而不是复用标准 fork
   - 地址策略：独立 N42 扩展地址段；如保留旧地址别名，只能放在显式扩展开关后
   - 兼容策略：标准 Hive/EEST 不感知 PQ 地址，PQ 专项测试使用独立 genesis
3. 为 PQ 能力补分层 gate：
   - 算法单测
   - VM 预编译回归
   - PQ 交易与公钥注册表集成测试
   - PQ 共识模式的专用回归
4. 在 Dilithium / SQIsign 没进入可验证完成态之前，不把它们写成默认生产能力。

### 6.3 历史与恢复能力

1. 把 archive / historical proof / 深历史查询与恢复语义补进固定 gate。
2. 明确区分：
   - `history expiry` 之后的最早可用历史语义
   - archive 节点的深历史查询能力
   - historical proof / witness 查询能力
3. 补齐深历史重启恢复矩阵，避免“功能存在但重启后边界漂移”。

### 6.4 运行时稳态与持续门禁

1. 把 24h / nightly 级 goroutine / heap / queue / peer 数红线收成持续 gate，而不是短时 smoke。
2. 把 broad EEST、archive-depth、broad RPC compatibility 和 soak 分出层级：
   - 提交级 smoke
   - nightly interop
   - 周期性长稳
3. 把 dashboard 阈值、告警和 runbook 回写到发布 gate，避免“文档写了但 release-check 不拦”。

### 6.5 当前推荐执行顺序

1. 先继续推进 broad Hive/EEST matrix，尤其是当前正在运行的 Osaka engine path。
2. 再把 PQ 预编译从“仓内实现存在”收口成“扩展层放置和启用边界清晰”。
3. 然后补 archive / historical proof / 深历史恢复。
4. 最后固化 nightly / 24h 级资源边界与更大范围 interop gate。

---

## 七、相关文档

1. 详细横向对比：[`GAP_ANALYSIS.md`](./GAP_ANALYSIS.md)
2. 生产成熟度计划：[`./engineering/PRODUCTION_MATURITY_PLAN.md`](./engineering/PRODUCTION_MATURITY_PLAN.md)
3. 成熟度基线：[`./engineering/MATURITY_BASELINE.md`](./engineering/MATURITY_BASELINE.md)
4. 测试核对结论：[`./engineering/TEST_REVIEW_FINDINGS.md`](./engineering/TEST_REVIEW_FINDINGS.md)
5. PQ 升级路线：[`./engineering/POST_QUANTUM_UPGRADE_PLAN.md`](./engineering/POST_QUANTUM_UPGRADE_PLAN.md)
