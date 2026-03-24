# 深度评估：全流水线 DSMR 出块 (Pipelined Block Building)

> Avalanche Vryx / HyperSDK 模式在 N42 架构中的实施评估
> 评估日期：2026-03-24

---

## 1. 功能概述

**DSMR（解耦状态机复制）**：将区块处理的三个阶段——交易复制、排序共识、执行——分离成独立流水线，使 Block N 的持久化与 Block N+1 的执行并行运行。

| 来源 | 性能 | 状态 |
|------|------|------|
| Avalanche Vryx / HyperSDK | testnet 143K TPS, devnet 100K TPS | 开发中 |
| Monad（解耦执行 + 并行 EVM） | 10,000 TPS, 400ms 出块 | testnet |
| Aptos（Quorum Store + Raptr） | 260K TPS | mainnet |

---

## 2. N42 现有架构分析

### 2.1 关键发现：基础设施已建，但全部未接入生产

| 组件 | 代码位置 | 状态 |
|------|---------|------|
| Tile 流水线（5 级 SPSC 环形缓冲） | `internal/tile/` | ✅ 建成测试 **❌ 未接入生产** |
| DeepPipeline（5 级 channel 流水线） | `internal/deferred/deep_pipeline.go` | ✅ 建成测试 **❌ 未接入生产** |
| Deferred Executor | `internal/deferred/executor.go` | ✅ 接入 node.go **❌ 使用 no-op 适配器** |
| EVM Adapter | `internal/deferred/evm_adapter.go` | **读取已有数据，不执行交易** |
| Block-STM 并行执行 | `internal/parallel/` | ✅ 生产使用 |
| 状态预取 | `internal/prefetcher.go` | ✅ 生产使用 |
| JMT 状态承诺 | `lib/jmt/`, `modules/state/commitment/` | ✅ 生产使用 **❌ 单实例不支持流水线** |

### 2.2 当前区块处理流程（完全串行）

```
Block N 到达
  → 验证 Header（并行，非阻塞）
  → 打开 MDBX RO 事务
  → 创建 IntraBlockState
  → 预取（可选后台）
  → Process() 或 ProcessParallel()（执行所有交易）
  → ValidateState（state root 必须匹配 header）
  → 关闭 RO 事务
  → writeBlockWithState()（打开 RW 事务）
    → 写 TD、receipts、log index、区块数据
    → CommitBlock（IBS → state writer → DB）
    → Flush JMT 节点
    → 写 LtHash digest
    → 更新 Snapshot Tree
    → 提交 RW 事务
  → 刷新 JMT store
  → 更新 head 指针
  → ▶ 处理 Block N+1（重复以上）
```

**瓶颈**：Block N 的整个执行 + 写入必须完成后，Block N+1 才能开始。

### 2.3 瓶颈量化

| 阶段 | 占比 | 可流水线化？ |
|------|------|------------|
| 交易执行 | 60-80% | 可（和前一块持久化重叠） |
| 持久化（RW 事务） | 15-25% | 可（和下一块执行重叠） |
| Header 验证/预取 | 5-10% | 已并行 |

---

## 3. 价值评估

### 3.1 分层收益

| 方案 | 改动量 | 共识变更 | 吞吐提升 | 风险 |
|------|--------|---------|---------|------|
| **Tier A：部分流水线** | 4-6 周 | 无 | 20-30% | 中 |
| **Tier B：完整延迟执行** | 8-12 周 | 硬分叉 | 2-4x | 高 |
| **Tier C：全 DSMR (Vryx)** | 6-12 月 | 硬分叉 + 新协议 | 5-10x | 极高 |

### 3.2 Tier A：部分流水线（最实际）

将 Block N 的持久化与 Block N+1 的预取/执行开始重叠：

```
Block N:    [Execute████████][Persist████]
Block N+1:                   [Prefetch██][Execute████████][Persist████]
Block N+2:                                [Prefetch██][Execute████████]
```

收益：消除持久化等待（15-25%），预取更早开始。无共识变更。

**具体做法**：
1. 接通 `DeepPipeline`，用真实 EVM 执行函数替换 no-op 适配器
2. 持久化阶段用异步 goroutine 运行
3. 下一块的执行通过 `LayeredCache` 读取上一块的未持久化状态
4. 持久化完成后才更新 canonical head

### 3.3 Tier B：完整延迟执行

State root 不在当前块 header，而是在下一块（或 N+2）中包含。需要：
- 修改 Header 格式（`ExecutionStateRoot` 引用前一块）
- 共识接受不含 state root 的块
- `ValidateState` 延迟到下一块验证时

### 3.4 与竞品对比

| 竞品 | 流水线程度 | 共识变更 | TPS |
|------|-----------|---------|-----|
| geth (Ethereum) | 无流水线 | — | ~100 |
| N42 (当前) | 无流水线（串行） | — | ~1,000 |
| Monad | 完整延迟执行 | 新共识 | ~10,000 |
| Vryx (Avalanche) | 全 DSMR | 新协议 | ~100,000 |
| **N42 + Tier A** | **部分流水线** | **无** | **~1,300-1,500** |
| **N42 + Tier B** | **完整延迟执行** | **硬分叉** | **~3,000-5,000** |

---

## 4. 开发必要性

### 4.1 当前 N42 的性能定位

N42 通过 Block-STM 并行执行已经在 EVM 链中属于第一梯队。但 **所有吞吐提升被串行持久化瓶颈限制**。Block-STM 让执行更快，但快完的执行仍然要等上一块写完数据库。

### 4.2 投入产出比

| 方案 | 投入 | 产出 | ROI |
|------|------|------|-----|
| Tier A 部分流水线 | 4-6 周 | TPS +20-30% | ★★★★ 高 |
| Tier B 延迟执行 | 8-12 周 + 硬分叉 | TPS 2-4x | ★★★ 中 |
| Tier C 全 DSMR | 6-12 月 + 硬分叉 | TPS 5-10x | ★★ 中低（长期战略） |

### 4.3 与其他路线图项对比

| 项目 | 周期 | 收益 | 前置依赖 |
|------|------|------|---------|
| **DSMR Tier A** | 4-6 周 | TPS +30% | 无 |
| io_uring 异步 I/O | 2-4 周 | 写入 +40% | 无 |
| 内存状态树 SALT | 6-10 周 | 状态访问 5-10x | 无 |
| BLS 批量验证 | 1-2 周 | CPU -80% | ✅ 已完成 |

**建议顺序**：io_uring → Tier A 部分流水线 → SALT 内存状态。io_uring 先做因为它直接加速持久化阶段——让 Tier A 的流水线收益更大。

---

## 5. 工作量分析

### 5.1 Tier A 部分流水线（推荐优先实施）

| 文件 | 改动 | 复杂度 |
|------|------|--------|
| `internal/deferred/evm_adapter.go` | 替换 no-op → 真实 EVM 执行 | 中 |
| `internal/deferred/deep_pipeline.go` | 接入真实 commit 函数 | 中 |
| `internal/blockchain.go` InsertChain | 拆分执行和持久化路径 | 高 |
| `internal/blockchain_write.go` | 异步 writeBlockWithState | 高 |
| `internal/node/node.go` | 初始化 DeepPipeline 替代当前路径 | 低 |
| `lib/kv/layered/` | 确保 cache 支持跨块读取 | 中 |
| 测试 | 流水线 stall/resume/error/reorg | 高 |
| **总计** | **~1500-2000 行** | **4-6 周** |

### 5.2 Tier B 完整延迟执行（远期）

在 Tier A 基础上额外需要：

| 文件 | 改动 |
|------|------|
| `common/block/header.go` | 新增 `ExecutionStateRoot` 字段 |
| `internal/block_validator.go` | 延迟 state root 验证 |
| `internal/consensus/` | 接受不含 state root 的块 |
| 共识协议 | Header 格式硬分叉 |
| **额外** | **~1000 行 + 硬分叉** |

---

## 6. 可能带来的问题和负面影响

### 6.1 状态损坏风险 — 评级：高

Block N+1 在 Block N 持久化之前开始执行时，需要读取 N 的未提交状态。

- **现有缓解**：`lib/kv/layered/ShardedCache` 提供 MDBX 上层缓存，`CachedStateReader`/`CachedStateWriter` 已存在
- **剩余风险**：如果 Block N 执行失败但 N+1 已基于 N 的结果开始执行 → N+1 也必须丢弃
- **影响范围**：所有依赖 `IntraBlockState` 的代码

### 6.2 重组处理 — 评级：高

流水线中多个块同时处于不同阶段，重组到达时：
- 必须取消所有 > fork point 的在飞块
- 排空所有 channel/ring buffer
- 作废所有缓存状态快照
- **JMT 回滚**：当前 JMT 的 `FlushTo` 是追加式的，没有独立回滚机制
- `DeepPipeline.Reset(forkBlock)` 和 `TileManager.Reset()` 已处理 channel/ring 排空
- **缺失**：JMT 回滚 + LtHash digest 回滚

### 6.3 JMT 兼容性 — 评级：高（Tier A 的最大挑战）

- **单实例**：`bc.jmtCommitment` 是全局单例。两个流水线阶段不能同时修改
- **FlushTo 破坏性**：移动 dirty nodes 到 store 后清空 dirty set。如果 N+1 已开始修改 tree，N 的 FlushTo 会丢 N+1 的脏数据
- **解决方案**：per-block JMT overlay 或 copy-on-write JMT fork（需扩展 `lib/jmt/`）

### 6.4 内存压力 — 评级：中高

| 资源 | 单块 | 4 块流水线 | 风险 |
|------|------|----------|------|
| IntraBlockState | ~10-50 MB | ~40-200 MB | 大块时可能 OOM |
| JMT dirty nodes | ~1-5 MB | ~4-20 MB | 低 |
| Block-STM MVS | ~5-20 MB/block | ~20-80 MB | 并行执行叠加 |
| Receipts/Logs | ~1-5 MB | ~4-20 MB | 低 |

总计峰值：~100-300 MB 额外内存。对 32 GB+ 的验证者节点可接受。

### 6.5 MEV 影响 — 评级：中

- 完整 DSMR (Tier C) 中，交易在排序前被复制，验证者可利用时间差
- Tier A 部分流水线不改变交易可见性时序，MEV 风险不变
- N42 现有 `FairnessGuard` 和 `AIBlockOptimizer` 不受 Tier A 影响

### 6.6 测试复杂度 — 评级：高

新增故障模式：
1. 持久化失败时执行回滚
2. 流水线积压（backpressure）导致死锁
3. 重组时多块在飞取消
4. JMT 一致性验证（流水线 reset 后）
5. Block-STM + 流水线交互（MVS 跨块读取）
6. 非确定性 channel 调度竞态

现有 `chaos_test.go` 需要扩展。`DeepPipeline` 和 `TileManager` 的测试已覆盖单元行为，但缺少和真实区块链状态机的集成测试。

### 6.7 向后兼容 — 评级：Tier A 零 / Tier B 硬分叉

- **Tier A**：纯内部优化，不改区块格式和共识协议。完全向后兼容。
- **Tier B/C**：修改 Header 格式和 state root 包含规则，需要硬分叉。

---

## 7. 综合评估

### 评分卡

| 维度 | Tier A（部分流水线） | Tier B（延迟执行） | Tier C（全 DSMR） |
|------|---------------------|-------------------|------------------|
| 价值/收益 | ★★★ (TPS +30%) | ★★★★ (TPS 2-4x) | ★★★★★ (TPS 5-10x) |
| 必要性 | ★★★★ (消除明确瓶颈) | ★★★ (需要更高 TPS 时) | ★★ (竞争驱动) |
| 可行性 | ★★★★ (基础设施已建) | ★★★ (需硬分叉) | ★★ (全新协议) |
| 风险 | ★★★ (JMT 兼容是关键) | ★★ (重组 + JMT) | ★ (全面风险) |
| ROI | ★★★★ | ★★★ | ★★ |

### 结论

**Tier A 部分流水线应列为 P1 优先实施。**

理由：
1. 基础设施已建好 80%（Tile pipeline、DeepPipeline、Block-STM 全部就位），缺的是**最后一公里接线**
2. 不需要共识变更和硬分叉
3. 消除明确的持久化瓶颈，让 Block-STM 的并行收益不被串行 I/O 抵消
4. 为未来 Tier B（硬分叉延迟执行）铺路

**建议实施顺序**：
1. io_uring 异步 I/O（加速持久化，2-4 周）
2. Tier A 部分流水线（接通 DeepPipeline，4-6 周）
3. SALT 内存状态树（消除状态访问瓶颈，6-10 周）
4. Tier B 延迟执行（需硬分叉，待 Shanghai fork 时机）

---

## 参考文献

1. [Vryx: Fortifying Decoupled State Machine Replication (HackMD)](https://hackmd.io/@patrickogrady/rys8mdl5p)
2. [Processing 5B Micropayments at 100K TPS with Vryx and Vilmo](https://hackmd.io/@patrickogrady/vryx-poc)
3. [HyperSDK 143K TPS on Testnet (CoinTelegraph)](https://cointelegraph.com/news/avalanche-hyper-sdk-blockchain-upgrade-hits-143000-tps-on-testnet)
4. [Monad: Parallel Execution + Deferred Execution](https://docs.monad.xyz/monad-arch/execution/parallel-execution)
5. [Narwhal and Tusk: DAG-based Mempool (arXiv:2105.11827)](https://arxiv.org/abs/2105.11827)
6. N42 源码分析: `internal/tile/`, `internal/deferred/`, `internal/blockchain.go`
