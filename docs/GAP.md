# N42 GAP

> 更新日期：2026-03-17
> 作用：作为 N42 当前仓库核对版 gap 摘要；详细横向功能对比见 [`GAP_ANALYSIS.md`](./GAP_ANALYSIS.md)
> 证据口径：只把本仓源码、测试、`make maturity-baseline` 与已记录的验证结果写成“已具备”

---

## 一、当前判断

N42 已经不是“功能面窄”的客户端。状态承诺、快照、Witness、GraphQL、Clef、external signer、HotStuff、Bundler、MEV、Encrypted TxPool、PeerDAS、并行执行等实现都已落在主仓里。

但 N42 仍不是“生产候选”。当前最主要的问题也不是“缺下一项新功能”，而是：

1. 现有外部 surface 还没有全部达到真实互操作语义，尤其是 `Engine API` 与 Hive / EL 对接。
2. 恢复性已明显改善，但 archive / historical proof、长期重启矩阵仍未收齐。
3. 运行时资源边界、互操作 soak、运维 runbook 和发布门禁还没有形成固定体系。

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
| 同步与恢复 | 2 | full + snap + checkpoint 已有，恢复 smoke 已建立，但缺 staged sync / unwind / archive-depth recovery matrix |
| 执行架构 | 3 | 已有 wave-based Block-STM、并行预热和 zk / witness 路线，但仍缺更成熟的冲突规划与 async I/O |
| 接口与工具 | 3 | GraphQL、Clef、external signer、debug/trace/filter 等面较宽，`Engine API` 只到部分真实闭环 |
| 运行时与运维 | 2 | Prometheus、tracing、MCP 已有，但没有形成资源边界、告警和 release gate 体系 |
| 生产成熟度 | 2 | baseline 与恢复 smoke 已固定，但 interop / soak / ops runbook 仍缺 |
| **总分 / 24** | **15** | 宽度强于传统“实验客户端”，成熟度仍未到生产候选 |

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
- `Engine API` 最小真实 round-trip：[`../internal/api/engine_api_v1.go`](../internal/api/engine_api_v1.go)、[`../internal/api/engine_api_blob.go`](../internal/api/engine_api_blob.go)、[`../internal/api/engine_api_v4.go`](../internal/api/engine_api_v4.go)

### 3.3 并行执行与扩展能力

- 并行执行：[`../internal/parallel/`](../internal/parallel/)、[`../internal/parallel_processor.go`](../internal/parallel_processor.go)
- HotStuff / APOA / APOS：[`../internal/consensus/hotstuff/`](../internal/consensus/hotstuff/)
- Bundler / MEV / encrypted txpool：[`../internal/bundler/`](../internal/bundler/)、[`../internal/mev/`](../internal/mev/)、[`../internal/txspool/encrypted/`](../internal/txspool/encrypted/)

---

## 四、当前主要 GAP

### 4.1 外部语义与互操作

1. `Engine API` 现在已具备 `forkchoiceUpdated -> getPayload -> newPayload -> RPC block lookup` 的最小 round-trip，但还没有形成 Hive / execution-spec 级稳定互操作。
2. `GraphQL / Clef / external signer` 已进入固定 smoke，但仍主要停留在仓库内验证，不等于外部生态长期兼容。

### 4.2 恢复性与历史能力

1. `keystore`、`snapshot journal`、`txpool journal`、`checkpoint`、`freezer` 和 `history expiry` 的边界与重启续跑关键路径已进入固定 recovery smoke。
2. archive / historical proof 查询仍未收齐。
3. 缺少 `staged sync + unwind + resume` 这一整套生产级同步恢复控制流。

### 4.3 运行时稳态

1. 还没有把 goroutine、heap、队列、连接数、sync lag 建成固定基线和红线。
2. 当前更多依赖日志和单次本地验证，仍未形成长期 soak 和自动告警闭环。

### 4.4 运维拓扑与发布门禁

1. 仍是单进程节点形态，没有 `rpcdaemon/sentry` 级的拆分拓扑。
2. `make maturity-baseline` 已能形成本仓固定 gate，但还没有 nightly interop、24h soak、发布 checklist、runbook。

---

## 五、最近已验证的收口

### 5.1 `Engine API`

- `newPayload / forkchoiceUpdated / getPayload / blobs` 已不再只是 stub surface，已有最小真实闭环测试。
- overlay 现在能把 Engine 导入块暴露给普通 RPC 查询路径。

相关文件：

- [`../internal/api/engine_api_v1.go`](../internal/api/engine_api_v1.go)
- [`../internal/api/engine_api_blob.go`](../internal/api/engine_api_blob.go)
- [`../internal/api/engine_api_v4.go`](../internal/api/engine_api_v4.go)
- [`../internal/api/engine_api_blob_test.go`](../internal/api/engine_api_blob_test.go)

### 5.2 恢复性

- keystore watcher 漏事件会补扫刷新：[`../accounts/keystore/account_cache.go`](../accounts/keystore/account_cache.go)
- checkpoint 不再把“只有 canonical hash 的半写入块”误判成已恢复：[`../internal/sync/checkpoint/service.go`](../internal/sync/checkpoint/service.go)
- freezer 重启恢复改为按最小真实表项数恢复，而不是盲信滞后的元数据：[`../modules/rawdb/freezer/freezer.go`](../modules/rawdb/freezer/freezer.go)
- `history expiry` 相关的 `earliest` / `feeHistory` / canonical lookup 现在会对齐最早可用历史，且重启后会从持久化 earliest 继续推进：[`../internal/api/api.go`](../internal/api/api.go)、[`../internal/api/feehistory.go`](../internal/api/feehistory.go)、[`../internal/node/history_expiry_test.go`](../internal/node/history_expiry_test.go)、[`../turbo/rpchelper/helper.go`](../turbo/rpchelper/helper.go)

### 5.3 固定 gate

- 基线脚本：[`./engineering/MATURITY_BASELINE.md`](./engineering/MATURITY_BASELINE.md)
- 最新 full baseline：[`../build/maturity-baseline/20260317-063508Z/summary.md`](../build/maturity-baseline/20260317-063508Z/summary.md)

当前 recovery smoke 已覆盖：

1. keystore reload
2. genesis config
3. checkpoint recovery
4. snapshot journal
5. freezer recovery
6. history expiry recovery
7. txpool journal

---

## 六、当前最值得继续收口的顺序

1. Hive / execution-spec / RPC compatibility 的真实 interop。
2. archive / historical proof / 深历史查询的恢复语义。
3. goroutine / heap / queue / peer 数的资源边界与告警。
4. 24h soak、重启循环、并发 RPC + txpool 压测。
5. runbook、dashboard、release checklist。

---

## 七、相关文档

1. 详细横向对比：[`GAP_ANALYSIS.md`](./GAP_ANALYSIS.md)
2. 生产成熟度计划：[`./engineering/PRODUCTION_MATURITY_PLAN.md`](./engineering/PRODUCTION_MATURITY_PLAN.md)
3. 成熟度基线：[`./engineering/MATURITY_BASELINE.md`](./engineering/MATURITY_BASELINE.md)
4. 测试核对结论：[`./engineering/TEST_REVIEW_FINDINGS.md`](./engineering/TEST_REVIEW_FINDINGS.md)
