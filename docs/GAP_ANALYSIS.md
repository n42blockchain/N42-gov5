# N42 vs geth / Reth / Erigon / Monad / Aptos 功能差距分析

> 核对日期：2026-03-16
> 对比对象：go-ethereum (geth)、Reth、Erigon、Monad、Aptos、N42
> 目标：比较“已有功能”的成熟度、完整度和生产可用性，不做宣传式结论
> 证据口径：N42 使用本仓源码、测试和本轮验证；外部项目仅使用官方文档、官方仓库或官方 README

---

## 一、如何阅读这份文档

这版文档恢复为横向功能对比，但评分口径比旧版更收敛：

1. `0`：未见主线路径或没有直接证据
2. `1`：有概念/实验/很窄的实现面
3. `2`：主线可用，但支撑能力不完整
4. `3`：已集成、已文档化、可稳定使用
5. `4`：默认路径或核心架构，成熟度高，具备明显生产导向

额外说明：

- 总分只表示“当前功能成熟度参考”，不等于链安全性、生态规模或商业成功。
- Aptos 不是 Ethereum EL，涉及 `Engine API`、`eth_*`、`GraphQL(EIP-1767)` 等行会标记为 `N/A` 或单独解释，不硬扣分。
- 对 N42，`功能存在` 和 `生产就绪` 明确拆开。尤其 `Engine API` 不能只因 namespace 在就算完成。

---

## 二、先看结论

- 如果以“成熟的 Ethereum EL 基线”看，N42 当前最接近的是“功能面很宽，但成熟度不均衡”的状态：状态承诺、快照、Witness、GraphQL、Clef、HotStuff、Bundler、MEV、Encrypted TxPool 都已经落在主仓里，但不是每一块都到了 production-grade。
- 如果以 `geth / Reth / Erigon` 为参考，N42 当前最大缺口不是“有没有代码”，而是三件事：`Engine API` 真实语义闭环、`staged sync / unwind`、`rpcdaemon/sentry` 这类面向生产运维的进程拆分。
- 如果以 `Monad / Aptos` 为参考，N42 已经进入“并行执行 + BFT + Witness/Stateless”这一代架构，但仍缺少 `async I/O`、更成熟的冲突规划、以及类似 `state sync / streaming` 的恢复能力。
- N42 的方向并不落后，真正的问题是“最后一公里成熟化”：跨客户端互操作、长时间稳定性、故障恢复和运维拓扑。

---

## 三、总览评分卡

| 维度 | geth | Reth | Erigon | Monad | Aptos | **N42** |
|---|---:|---:|---:|---:|---:|---:|
| 状态与存储 | 4 | 4 | 4 | 4 | 4 | **3** |
| 同步与恢复 | 3 | 4 | 4 | 3 | 4 | **2** |
| 执行架构 | 2 | 3 | 3 | 4 | 4 | **3** |
| 共识 / 节点架构 | 2 | 2 | 3 | 4 | 4 | **3** |
| 接口 / 工具 / 运维 | 4 | 3 | 4 | 2 | 3 | **3** |
| 已有功能成熟度 | 4 | 4 | 4 | 3 | 4 | **2** |
| **总分 / 24** | **19** | **20** | **22** | **20** | **23** | **16** |

如何解读：

- `geth`：成熟 EL 基准，架构保守但生产路径最清晰。
- `Reth`：模块化和 staged sync 代表。
- `Erigon`：archive、模块拆分、运维拓扑最强。
- `Monad`：EVM 兼容路线里，异步执行和 async I/O 最激进。
- `Aptos`：非 EVM 参考系里，并行执行、JMT、state sync 很成熟。
- `N42`：广度已经进入第一梯队架构讨论范围，但成熟度仍低于传统强 EL。

---

## 四、详细功能对比

### 4.1 状态管理与存储

| 能力 | geth | Reth | Erigon | Monad | Aptos | **N42** | N42 判断 |
|---|---|---|---|---|---|---|---|
| 状态承诺结构 | Hex-MPT | Hex-MPT + sparse trie crate | Flat KV + trie commitment | Persistent Patricia trie in MonadDb | JMT | JMT + Blake3 | 方向先进，证明路径有特色 |
| 读放大优化 | Snapshot + path-based storage | Flat state | Segment / compressed layout | DB-native persistent versions | Scratchpad + versioned state | Snapshot diff layers + sharded cache | 这块已经不弱 |
| 历史状态 / archive 模型 | Hash archive + path archive | Archive + static files | 压缩 archive / 历史分层很强 | 有界历史 + compaction | AptosDB versioned state | Freezer + history expiry；无 PBSS 级 archive 路径 | 这是存储侧主要差距 |
| 裁剪 / 过期 | Path-based pruning | Pipeline/static-file aware | full/minimal/archive 模式 | Inline compaction | Ledger / state / merkle pruner | Snapshot-aware pruning + EIP-4444 风格 expiry | 有实现，但证据还偏仓库内 |
| 冷数据布局 | Ancients / freezer | Static-file crates | Segments + files | File / block-device database | AptosDB 多 schema | 5 表冷存储 + layered MDBX | 可用，但不如 Reth/Erigon 模块化 |
| Proof / witness / stateless path | MPT proof | MPT proof | Historical proof 强 | Delayed-root proof path | JMT / accumulator proof | Block witness + stateless verify + zk guest | 这是 N42 的亮点 |

小结：

- N42 在状态承诺、快照和 witness 路线上的设计并不保守，甚至比 geth 更“新”。
- 但如果把目标换成“长期 archive 节点、历史查询、proof 服务、运维可控性”，N42 仍明显落后于 geth / Reth / Erigon。

### 4.2 同步与恢复

| 能力 | geth | Reth | Erigon | Monad | Aptos | **N42** | N42 判断 |
|---|---|---|---|---|---|---|---|
| 主同步模式 | Snap 默认 + full/archive | Full + staged pipeline | Staged sync + modular downloader | Statesync + Blocksync 文档化 | State sync | Full + snap + checkpoint | 主路径齐了，但恢复面不够深 |
| Staged / segmented sync | 无 | 有 | 有 | 无公开 EL staged 等价物 | 不是 EL staged 模型 | 无 | 对比 Reth / Erigon 的清晰缺口 |
| Checkpoint / bootstrap | 通过 CL checkpoint | 通过 EL/CL 栈 | Caplin 路径 | Statesync / bootstrap | State sync bootstrap | Trusted-hash checkpoint | 有，但更像轻量版本 |
| State sync / 数据流服务 | Snap state heal | Pipeline-oriented | OtterSync / segmented 分发 | Statesync | Driver + streaming + storage service | 仅 snap sync | 缺少独立 state-sync 服务 |
| 缺块恢复 / 回退 | Reorg / state heal | Backfill + unwind | Unwind + staged retry | Blocksync | Continuous syncer | Downloader + checkpoint / snap | 无 stage-level unwind 能力 |
| 进程拆分 | Monolith + 外部 CL | 模块化 crate，常见仍单节点 | Sentry + rpcdaemon + Caplin | 高性能一体化栈 | consensus / execution / storage / mempool 明确分层 | 单进程节点 | 生产运维面落后于 Erigon / Aptos |

小结：

- N42 不是“不会同步”，而是缺“生产级同步控制流”。
- `staged sync + unwind + resume + process decomposition` 这一套，是 N42 目前最需要向 Reth / Erigon 补课的部分。

### 4.3 执行、并行化与共识

| 能力 | geth | Reth | Erigon | Monad | Aptos | **N42** | N42 判断 |
|---|---|---|---|---|---|---|---|
| 执行策略 | 顺序 EVM | 顺序 EVM + parallel prewarming | 优化后的顺序执行 | 乐观并行执行 | 并行执行 / BlockExecutorTrait | Wave-based Block-STM + fallback | 已经进入并行执行路线 |
| 冲突处理 | N/A | 预热 / 预取 | Pipeline / layout 优化 | Optimistic re-exec | Scratchpad + speculative execution | MVS + wave validate / re-exec | 强于传统 EL，弱于 Monad / Aptos |
| Async I/O | 无 | 无 | 本轮未见 async DB 核心证据 | `io_uring` + block-device | 不是公开主卖点 | 无 | 对比 Monad 的明确差距 |
| 内建共识路径 | 外部 CL | 外部 CL | Caplin 可选 | MonadBFT | AptosBFT | APOA / APOS / HotStuff | N42 这块广度不错 |
| 共识与执行解耦 | 无 | 无 | 有限 | 有，且是核心设计 | 组件分层明显，但不是 Monad 式 delayed-root pipeline | 无 | 未来若追高性能，应重点演进 |
| Engine API / EL 兼容性 | 完整 | 完整 | 完整 | EVM / RPC 兼容，但不是公开 EL/CL 分体重点 | N/A | surface 在，但语义仍部分未闭环 | 当前最大生产阻断项 |

小结：

- N42 不是“只有传统串行 EVM”；`internal/parallel/` 和 `internal/parallel_processor.go` 已经有实质并行执行代码。
- 但从成熟度看，N42 目前更像“第一代可用的 Block-STM 落地”，还不是 Monad/Aptos 那种完整执行体系。

### 4.4 RPC、工具链与运维

| 能力 | geth | Reth | Erigon | Monad | Aptos | **N42** | N42 判断 |
|---|---|---|---|---|---|---|---|
| 标准接口面 | 强 `eth/debug/txpool/admin` | 强 `admin/trace/txpool` | 强，且 `rpcdaemon` 独立 | 强调 Ethereum RPC 兼容 | 原生 REST / RPC，不是 ETH namespace | `eth/debug/trace/filter/graphql/witness` 都在 | 功能面已经很宽 |
| GraphQL / 灵活查询 | EIP-1767 | 主线不强调 GraphQL | `rpcdaemon` 路线下查询能力强 | 本轮未见重点证据 | 原生 API 体系 | EIP-1767 GraphQL | N42 在这点上并不弱 |
| 外部签名 / 账户工具 | Clef | 不以 signer 为重点 | 不以 signer 为重点 | 本轮未见重点证据 | 原生账户模型 | Clef + external signer client | 运维体验加分项 |
| AA / 私有订单流 / MEV | 外部生态强 | 外部 builder / txpool 生态强 | RPC / OTS 生态强 | 不是公开主卖点 | 交易模型不同 | Bundler + encrypted txpool + mev relay | 有实现，但生态验证不足 |
| 可观测性 | 成熟 metrics / ops | metrics crate 完整 | 模块化运维面最强 | 官方文档有节点运维，但广度有限 | 成熟节点运维体系 | Prometheus + tracing + MCP | 能力在，但还缺长期运维证据 |
| 既有功能生产成熟度 | 高 | 高 | 高 | 中高，受主网时间影响 | 高 | 中 | N42 现在更适合写“具备能力”，不适合写“批准生产” |

---

## 五、Ethereum EL 参考系下，N42 还差什么

这张表只看 `geth / Reth / Erigon / N42` 所共享的 Ethereum EL 参考系。

| 项目 | geth | Reth | Erigon | **N42** | 当前结论 |
|---|---|---|---|---|---|
| JWT / authrpc / capability surface | 完整 | 完整 | 完整 | 已打通 | 不是当前 blocker |
| `NewPayload*` 真实语义 | 完整 | 完整 | 完整 | 部分路径仍返回 `SYNCING` | blocker |
| `ForkchoiceUpdated*` 真实语义 | 完整 | 完整 | 完整 | 部分路径仍返回 `SYNCING` | blocker |
| `GetPayload*` payload lifecycle | 完整 | 完整 | 完整 | 仍可能 `payload not found` | blocker |
| Hive / execution-spec 互操作 | 高 | 高 | 高 | 未闭环 | blocker |
| 历史 archive / proof 服务 | 强 | 强 | 很强 | 中低 | gap |
| `rpcdaemon/sentry` 级进程拓扑 | 无 / 弱 | 模块化但常见仍单节点 | 强 | 无 | gap |

直接证据：

- [`../internal/api/engine_api_v1.go`](../internal/api/engine_api_v1.go)
- [`./engineering/TEST_REVIEW_FINDINGS.md`](./engineering/TEST_REVIEW_FINDINGS.md)

这里要特别区分两件事：

1. `Engine API namespace 已存在`
2. `Engine API 已经达到真实外部 EL 兼容`

N42 现在已经完成的是前者的较大一部分，离后者还有关键语义差距。

---

## 六、N42 现有功能的成熟度判断

### 6.1 已有功能里，成熟度相对较高的部分

- 状态承诺和 proof 路径：[`../lib/jmt/`](../lib/jmt/)、[`../modules/state/witness/`](../modules/state/witness/)
- 快照与状态读优化：[`../modules/state/snapshot/`](../modules/state/snapshot/)
- Snap sync 和 checkpoint 基础：[`../internal/sync/snapsync/`](../internal/sync/snapsync/)、[`../internal/sync/checkpoint/`](../internal/sync/checkpoint/)
- 内建共识路径：[`../internal/consensus/hotstuff/`](../internal/consensus/hotstuff/)、`apoa`、`apos`
- 接口层：[`../internal/api/graphql/`](../internal/api/graphql/)、[`../cmd/clef/`](../cmd/clef/)、[`../accounts/external/`](../accounts/external/)
- 高阶功能面：[`../internal/bundler/`](../internal/bundler/)、[`../internal/mev/`](../internal/mev/)、[`../internal/txspool/encrypted/`](../internal/txspool/encrypted/)

### 6.2 有实装价值，但还不能写成“生产稳态”的部分

- 并行执行：[`../internal/parallel/`](../internal/parallel/) 和 [`../internal/parallel_processor.go`](../internal/parallel_processor.go)
- PeerDAS：[`../internal/peerdas/`](../internal/peerdas/)
- MCP / tracing / Prometheus：[`../internal/mcp/`](../internal/mcp/)、[`../internal/tracing/`](../internal/tracing/)、[`../internal/metrics/prometheus/`](../internal/metrics/prometheus/)
- History expiry / witness RPC / zk guest 这类前沿功能

### 6.3 当前最影响生产就绪判断的阻断项

1. `Engine API` 真实 payload / forkchoice / payload retrieval 语义仍未完全闭环。
2. 缺少 `staged sync + unwind + resume` 这一整套恢复控制流。
3. 缺少 `rpcdaemon/sentry` 级别的进程拆分和大规模运维拓扑。
4. archive / historical proof 查询路径还没有达到 geth / Reth / Erigon 水平。
5. 并行执行已落地，但还缺少更成熟的冲突规划、async I/O 和更长期的实网证据。

---

## 七、优先级建议

如果目标是“把 N42 从功能很宽，推进到生产就绪候选”，建议按下面顺序收口：

1. 先补 `Engine API` 真实语义和 Hive 互操作闭环，这一步优先级最高。
2. 引入 `staged sync / unwind / resume`，把同步链路从“能跑”升级到“可恢复、可观测、可回退”。
3. 明确 archive 战略：是靠 PBSS 类路径、static-file 类路径，还是 Erigon 风格 segment/history 模型。
4. 把 `RPC / P2P / sync` 服务边界拆开，至少具备向 `rpcdaemon/sentry` 类拓扑演进的能力。
5. 升级并行执行，从当前 wave-based Block-STM 继续往“更稳的冲突规划 + 更少重试 + 更强 I/O”推进。
6. 对已有高级功能补真实生产证据：长时间 soak、故障恢复、跨客户端互操作、运维压测。

---

## 八、主要证据

### 8.1 N42（本仓）

- [`../lib/jmt/`](../lib/jmt/)
- [`../modules/state/snapshot/`](../modules/state/snapshot/)
- [`../modules/state/witness/`](../modules/state/witness/)
- [`../internal/sync/snapsync/`](../internal/sync/snapsync/)
- [`../internal/sync/checkpoint/`](../internal/sync/checkpoint/)
- [`../internal/parallel/`](../internal/parallel/)
- [`../internal/consensus/hotstuff/`](../internal/consensus/hotstuff/)
- [`../internal/api/graphql/`](../internal/api/graphql/)
- [`../cmd/clef/`](../cmd/clef/)
- [`../accounts/external/`](../accounts/external/)
- [`../internal/mev/`](../internal/mev/)
- [`../internal/bundler/`](../internal/bundler/)
- [`../internal/txspool/encrypted/`](../internal/txspool/encrypted/)
- [`../internal/api/engine_api_v1.go`](../internal/api/engine_api_v1.go)
- [`./engineering/TEST_REVIEW_FINDINGS.md`](./engineering/TEST_REVIEW_FINDINGS.md)

### 8.2 外部官方资料

- geth
  - https://geth.ethereum.org/docs/fundamentals/archive
  - https://geth.ethereum.org/docs/fundamentals/sync-modes
  - https://geth.ethereum.org/docs/interacting-with-geth/rpc/graphql
  - https://geth.ethereum.org/docs/tools/clef/introduction
- Reth
  - https://raw.githubusercontent.com/paradigmxyz/reth/main/README.md
  - https://raw.githubusercontent.com/paradigmxyz/reth/main/docs/repo/layout.md
  - https://raw.githubusercontent.com/paradigmxyz/reth/main/Cargo.toml
- Erigon
  - https://docs.erigon.tech/fundamentals/modules/rpc-daemon
  - https://docs.erigon.tech/fundamentals/modules/sentry
  - https://docs.erigon.tech/interacting-with-erigon/interacting-with-erigon
  - https://docs.erigon.tech/staking/caplin
- Monad
  - https://docs.monad.xyz/
  - https://docs.monad.xyz/monad-arch/execution/parallel-execution
  - https://docs.monad.xyz/monad-arch/execution/monaddb
  - https://docs.monad.xyz/monad-arch/consensus/asynchronous-execution
  - https://docs.monad.xyz/monad-arch/consensus/blocksync
- Aptos
  - https://aptos.dev/en/network/blockchain/blockchain-deep-dive
  - https://raw.githubusercontent.com/aptos-labs/aptos-core/main/state-sync/README.md
  - https://github.com/aptos-labs/aptos-core/tree/main/state-sync
  - https://github.com/aptos-labs/aptos-core/tree/main/storage/jellyfish-merkle
  - https://github.com/aptos-labs/aptos-core/tree/main/aptos-move/aptos-vm/src/block_executor

---

## 九、最终判断

如果只问一句话：

**N42 现在更像“架构方向对、实现面很宽、但生产级收口还没完成”的客户端。**

它已经不适合再被写成“功能缺失明显”；更准确的说法是：

- 对比 geth / Reth / Erigon，N42 还没有把 EL 语义、同步恢复和运维拓扑收齐。
- 对比 Monad / Aptos，N42 已进入并行执行和 BFT 时代，但还没有到那种系统级成熟度。
- 对 N42 最客观的评价不是“已经生产就绪”，而是：**已经有资格进入第一梯队架构对照，但尚未完成第一梯队的生产化收口。**
