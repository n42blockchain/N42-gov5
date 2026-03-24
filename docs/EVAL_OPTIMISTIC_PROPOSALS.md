# 深度评估：乐观共识提案 (Optimistic Consensus Proposals)

> Aptos Velociraptr / Moonshot 模式在 N42 HotStuff-2 中的实施评估
> 评估日期：2026-03-24

---

## 1. 功能概述

**乐观共识提案**：Leader 在前一轮 QC（Quorum Certificate）形成之前就提出新块，假设父块会被确认。如果假设正确（happy path），减少一个网络 RTT；如果失败（unhappy path），回退到标准流程。

来源：
- Aptos Baby Raptr（2025 mainnet）
- Aptos Velociraptr（2026 进行中）
- Supra Moonshot 论文 (arXiv:2401.01791)
- 形式化验证 (arXiv:2403.16637)

---

## 2. N42 现有共识能力

N42 的 HotStuff-2 **已经实现了多项低垂果实优化**：

| 已实现优化 | 效果 | 位置 |
|-----------|------|------|
| FastPropose（跳过 slot 边界） | 延迟降低 ~72%（1950ms→551ms） | `service.go` |
| Rotor 单跳中继 | 广播延迟降 1 hop | `rotor.go` |
| 乐观投票（不等执行就投票） | 从关键路径移除执行时间 | `proposal.go` |
| QC 背负（PrepareQC 嵌入 Proposal） | 省 1 轮消息 | `proposal.go` |
| 自适应 Pacemaker（EWMA 超时） | 网络自适应 | `pacemaker.go` |

**当前每轮延迟**：4 个网络 hop（Proposal→Vote→QC→CommitVote），FastPropose 模式下约 **500-600ms**。

---

## 3. 价值评估

### 3.1 延迟收益

| 指标 | 当前 (FastPropose) | 乐观提案后 | 提升 |
|------|-------------------|-----------|------|
| 出块周期 | 2δ + overhead | 1δ + overhead | ~40% |
| 提交延迟 | 4δ + overhead | 3δ + overhead | ~25% |
| 预估墙钟时间 | ~500-600ms | ~300-400ms | **100-200ms** |

> δ = 网络半 RTT，洲际网络约 75ms

### 3.2 收益量化

**实际改善：从 ~500ms 降到 ~300-400ms，节省约 100-200ms。**

### 3.3 N42 使用场景匹配度

| 场景 | 对亚秒终局的需求 | 对 <300ms 的需求 |
|------|-----------------|-----------------|
| DeFi / DEX | 高 | 中（RPC/用户延迟是更大瓶颈） |
| AI Agent 操作 | 低（人/秒级操作） | 低 |
| 去中心化消息 | 无（独立于共识） | 无 |
| 通用 L1 使用 | 低（500ms 已比 ETH 快 24x） | 低 |
| 高频交易 (HFT) | 极高 | 极高 |
| 跨链套利 | 高 | 高 |

**结论**：除非 N42 明确定位 HFT / 实时结算赛道，100-200ms 的改善是 "nice to have"，不是 "must have"。

---

## 4. 开发必要性评估

### 4.1 竞品压力

| 竞品 | 当前终局 | 使用乐观提案 |
|------|---------|-------------|
| Aptos (Velociraptr) | <50ms | 是 |
| Sui (Mysticeti V2) | ~500ms | 否（DAG 方式） |
| Solana (Alpenglow) | 100-150ms | 是（Votor+Rotor） |
| N42 (HotStuff-2) | ~500ms | **否** |
| Ethereum | ~12s slot | 否 |

N42 的 500ms 在 L1 中已属第一梯队。Aptos 和 Solana 的优势更多来自其 **DAG/并行共识**架构，而非单纯的乐观提案。

### 4.2 投入产出比

```
投入：4-8 周核心开发 + 3-4 周测试 = 约 2 个月
产出：100-200ms 终局提升
ROI：低-中
```

**对比其他路线图项目的 ROI**：

| 项目 | 投入 | 收益 | ROI |
|------|------|------|-----|
| 乐观共识提案 | 8-12 周 | 终局 -200ms | ★★ 低 |
| io_uring 异步 I/O | 2-4 周 | 写入 +40% | ★★★★ 高 |
| DSMR 全流水线 | 4-8 周 | TPS 2-5x | ★★★★ 高 |
| 内存状态树 SALT | 6-10 周 | 状态访问 5-10x | ★★★★★ 极高 |
| 链下 BLS 投票聚合 | 3-5 周 | 带宽 -70% | ★★★★ 高 |

---

## 5. 工作量详细分析

### 5.1 需要修改的文件

| 文件 | 改动类型 | 复杂度 |
|------|---------|--------|
| `hotstuff/engine.go` | 并行视图管理、双提案逻辑 | 高 |
| `hotstuff/proposal.go` | 乐观提案生成、回退逻辑 | 高 |
| `hotstuff/voting.go` | 多视图并发投票收集 | 高 |
| `hotstuff/round_state.go` | 投机状态跟踪、2-chain 提交规则 | 高 |
| `hotstuff/pacemaker.go` | 重叠超时处理 | 中 |
| `hotstuff/codec.go` | 新消息类型（OptimisticProposal） | 低 |
| `hotstuff/service.go` | pacemakerLoop 重构 | 中 |
| `hotstuff/rotor.go` | 乐观提案路由 | 低 |
| `consensus/hotstuff/chaos_test.go` | 10+ 新测试场景 | 高 |

**总行数估计**：新增 ~1500-2000 行，修改 ~800-1000 行。

### 5.2 时间分解

| 阶段 | 时间 | 内容 |
|------|------|------|
| 设计 | 1 周 | 状态机重设计、消息格式、安全证明验证 |
| 核心实现 | 3-4 周 | engine/proposal/voting/round_state 改造 |
| 回退路径 | 1-2 周 | 父块拒绝处理、级联回退、持久化恢复 |
| 测试 | 3-4 周 | 单元测试、Chaos 测试、拜占庭场景 |
| 集成调试 | 1 周 | 多节点集群测试、性能基准 |
| **总计** | **9-12 周** | |

---

## 6. 可能带来的问题和负面影响

### 6.1 安全性风险 — 评级：低（但实现敏感）

乐观提案的安全性已被形式化验证（Moonshot 论文），但 **实现正确性是关键**：

- N42 的 `IsSafeToVote()` 当前检查 `justifyQC.View >= lockedQC.View`。乐观提案**没有 JustifyQC**，安全规则必须适配"乐观链"而非具体 QC
- `equivocationTracker` 按视图重置，多视图并发时追踪必须跨视图
- 错误实现可能导致 **分叉**（两个冲突的乐观链同时推进）

### 6.2 活性风险 — 评级：中

- **级联失败**：一个拜占庭 Leader 不仅浪费自己的视图，还浪费下一个 Leader 的乐观提案 → **一个坏 Leader 浪费两个视图**（当前只浪费一个）
- **高丢包率下更慢**：乐观提案依赖及时收到前一个提案。网络不好时频繁回退，可能比标准 HotStuff-2 **更慢**
- N42 自适应 Pacemaker 的 p95 延迟计算假设顺序视图，重叠视图会改变延迟特征

### 6.3 代码复杂度 — 评级：高

当前 HotStuff-2 实现清晰简洁：
- 5 个明确阶段的 `RoundState`
- `advanceToView()` 顺序推进
- 每视图单个活跃提案

乐观提案引入：
- **流水线状态管理**：多个视图同时在飞
- **双提案逻辑**：每个 Leader 准备乐观 + 回退两个提案
- **投机执行**：基于未确认父块构建区块，父块回退时需回滚
- **并发投票路由**：V 视图的投票和 V+1 视图的提案同时处理

### 6.4 测试负担 — 评级：高

10+ 新失败模式需要测试：

1. 父块被拒绝 → 乐观子块必须被丢弃
2. QC 乱序到达（子 QC 比父 QC 先到）
3. N 个连续拜占庭 Leader 级联失败
4. 混合网络：部分节点看到乐观提案，部分看到回退提案
5. 多视图并行投票收集的竞态条件
6. Pacemaker 重叠超时
7. 乐观提案飞行中节点崩溃恢复
8. DA 验证（`onBlockImported`）基于投机父块
9. 乐观链上的重组检测
10. Epoch 边界跨越乐观提案

### 6.5 向后兼容 — 评级：中高

- **线协议变更**：Proposal 需要新字段（`IsOptimistic`、无 QC 的父引用），改变 SSZ/protobuf 编码
- **混合集群**：无乐观提案支持的节点会拒绝缺少 JustifyQC 的提案 → 需要协议协商
- **分阶段部署**：需要 ChainConfig 激活时间戳
- **Epoch 重配置**：`ReconfigurationManager` 假设顺序视图推进

### 6.6 浪费的工作量 — 评级：低-中

父块被拒绝时：
- **矿工工作**：构建了一个扩展未确认父块的区块 → CPU 浪费
- **带宽**：乐观提案已广播给所有验证者 → 一个提案大小的消息浪费
- **正常情况**：诚实 Leader + 稳定网络下极少发生
- **对抗情况**：最多 1/3 提案失败

### 6.7 MEV 影响 — 评级：低（偏正面）

- 更快出块 = 更小的 MEV 窗口 → 对用户有利
- `AIBlockOptimizer` 优化时间减少 → 优化质量可能降低
- `FairnessGuard` 三明治检测窗口缩小 → 更难检测
- 整体：更快出块**减少** MEV 提取机会，净正面

### 6.8 网络放大 — 评级：低

- 每视图多一个提案消息（~50% 更多提案），投票数量不变
- 总带宽增加约 10-20%（提案占比低，投票占主导）

---

## 7. 替代方案对比

| 方案 | 延迟改善 | 复杂度 | N42 现状 |
|------|---------|--------|---------|
| FastPropose（跳 slot 边界） | 72% | 低 | **已实现** |
| Rotor 中继（单跳广播） | 1 hop | 中 | **已实现** |
| 乐观投票（不等执行） | 执行时间 | 低 | **已实现** |
| QC 背负 | 1 轮消息 | 低 | **已实现** |
| **乐观提案（本评估）** | **~40% 出块周期** | **高** | **未实现** |
| 并行投票（多播给所有人） | 减少 Leader 瓶颈 | 中 | 未实现 |
| DAG 共识（Narwhal/Mysticeti） | 吞吐量为主 | 极高 | 未实现 |

---

## 8. 综合评估

### 评分卡

| 维度 | 分数 (1-5) | 说明 |
|------|-----------|------|
| 价值/收益 | ★★★ | 100-200ms 改善有意义但非决定性 |
| 必要性 | ★★ | 500ms 终局在 L1 中已属顶尖 |
| 可行性 | ★★★★ | 技术上可行，有形式化证明 |
| 实施风险 | ★★★ | 活性退化风险 + 复杂度高 |
| 投入产出比 | ★★ | 9-12 周换 100-200ms，ROI 偏低 |

### 结论

**建议：降为 P1 优先级，推迟到 DSMR 流水线和 SALT 内存状态树之后实施。**

理由：
1. N42 已经通过 FastPropose + Rotor + 乐观投票将延迟从 ~2s 优化到 ~500ms，已经吃掉了 80% 的优化空间
2. 剩余 100-200ms 的改善需要重构共识核心，风险与收益不成正比
3. DSMR 全流水线（吞吐量 2-5x）和 SALT 内存状态（状态访问 5-10x）对 N42 的**实际性能感知**影响更大
4. 如果未来 N42 明确进入 HFT / 实时结算赛道，再重新评估

### 如果决定实施，推荐路径

1. 实现 Moonshot "双提案"模式（乐观 + 回退），而非 Velociraptr 的完整前缀共识
2. 添加 `ChainConfig` 激活标志（类似 `FastPropose`）
3. 保持现有顺序路径为默认，乐观模式为可选
4. 从 2-chain 提交规则开始（比前缀共识简单）
5. 在 mainnet 激活前进行广泛的 Chaos 测试

---

## 参考文献

1. [Raptr: Prefix Consensus for Robust High-Performance BFT (arXiv:2504.18649)](https://arxiv.org/abs/2504.18649)
2. [Moonshot: Optimizing Chain-Based Rotating Leader BFT via Optimistic Proposals (arXiv:2401.01791)](https://arxiv.org/abs/2401.01791)
3. [Formally Verifying the Safety of Pipelined Moonshot (arXiv:2403.16637)](https://arxiv.org/html/2403.16637v1)
4. [Velociraptr: Towards Faster Block Time (Aptos Labs)](https://medium.com/aptoslabs/velociraptr-towards-faster-block-time-for-the-global-trading-engine-b7579d27fd1a)
5. [Baby Raptr Lands on Mainnet (Aptos)](https://aptosnetwork.com/currents/baby-raptr-lands-on-mainnet)
6. [Supra Moonshot Whitepaper](https://supra.com/documents/Moonshot-Whitepaper.pdf)
