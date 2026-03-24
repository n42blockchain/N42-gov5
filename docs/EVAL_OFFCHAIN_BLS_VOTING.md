# 深度评估：链下 BLS 投票聚合 (Off-Chain BLS Vote Aggregation)

> Solana Alpenglow / Votor 模式在 N42 HotStuff-2 中的实施评估
> 评估日期：2026-03-24

---

## 1. 功能概述

**Votor**（Solana Alpenglow 的投票子系统）：将共识投票从链上交易移到链下 BLS 聚合签名，任意节点可聚合形成证书，支持单轮终局。

来源：
- Solana Alpenglow (Anza 2026)
- SIMD-0326 提案
- Votor: off-chain vote certificates + 80% fast-finality path

---

## 2. 关键发现：N42 已经实现了 Votor 的核心能力

### 2.1 N42 投票已经是链下 P2P 消息

| 对比项 | Solana (Tower BFT, pre-Alpenglow) | N42 HotStuff-2 |
|--------|-----------------------------------|----------------|
| 投票方式 | **链上交易**（Ed25519 签名） | **链下 P2P 消息**（BLS 签名） |
| 投票占网络流量 | ~75% | ~5% |
| 验证者月成本 | ~$5,000 投票费 | $0（无费用） |
| 投票聚合 | 无（每票独立验证） | **BLS 聚合签名 → 96 字节 QC** |
| 签名者编码 | 无 | **位图压缩** (ceil(n/8)+2 bytes) |

**结论：Votor 的主要价值——将投票从链上移到链下——在 N42 中从第一天就已实现。** Solana 需要 Votor 是因为 Tower BFT 使用链上 Ed25519 投票交易。N42 的 HotStuff-2 从设计之初就是链下 BLS 投票。

### 2.2 N42 已实现的 Votor 等价功能

| Votor 功能 | N42 实现 | 位置 |
|-----------|---------|------|
| 链下投票传播 | ✅ P2P 消息（unicast/gossip） | `service.go`, `voting.go` |
| BLS 聚合签名证书 | ✅ `AggregateSignatures()` → QC | `quorum.go:130` |
| 签名者位图 | ✅ bool bitmap packed | `codec.go` |
| QC 验证（单次配对） | ✅ `verifyAggregateSignature()` | `quorum.go:340` |
| 单跳中继分发 | ✅ Rotor relay | `rotor.go` |

---

## 3. N42 实际还缺什么？

虽然核心已实现，仍有 3 个增量改进空间：

### 3.1 批量 BLS 签名验证（Tier 1）

**现状**：Leader 逐条验证每个投票的 BLS 签名：
```go
// voting.go - 每票一次配对操作
if !VerifyBLSSignature(vote.Signature, pk, msg) { ... }
```

对 100 个验证者：100 次配对运算 × ~1.5ms = **~150ms CPU 时间/轮**。

**改进**：已有 `VerifyMultipleSignatures()` 和 `SignatureBatch`（`signature_batch.go`），使用 `blst.MultipleAggregateVerify` + 随机标量盲化。**但未在共识热路径中使用**。

| 指标 | 当前 | 批量验证后 |
|------|------|----------|
| 100 验证者 CPU | ~150ms | ~30ms |
| 改善 | — | **60-80%** |
| 风险 | — | 零（纯内部优化） |
| 工期 | — | 1-2 周 |

### 3.2 分布式证书形成（Tier 2）

**现状**：只有 Leader 收集投票并形成 QC。

**改进**：让所有验证者观察投票（广播而非单播给 Leader），任意节点达到法定人数即可形成 QC。消除 Leader 瓶颈。

| 指标 | 当前 | 分布式后 |
|------|------|---------|
| QC 形成节点 | 仅 Leader | 任意节点 |
| Leader 瓶颈 | 有 | 消除 |
| 带宽变化 | — | +30%（投票广播） |
| 风险 | — | 中（equivocation 检测变化） |
| 工期 | — | 2-3 周 |

### 3.3 单轮快速终局（Tier 3）

**现状**：HotStuff-2 固定 2 轮（Prepare + Commit）。

**改进**：≥80% 权益投票时，Round 1 的 QC 直接作为 "Fast Commit Certificate"，跳过 Round 2。

| 指标 | 当前 | 快速终局后 |
|------|------|----------|
| 终局轮数 | 2 轮 | 1 轮（80%+时） |
| 终局延迟 | ~500ms | ~300ms（快速路径） |
| 拜占庭容忍 | 33% | **20%（快速路径）** |
| 风险 | — | **高（安全模型变更）** |
| 工期 | — | 4-8 周 |

---

## 4. 价值评估

### 4.1 "带宽 -70%" 声明评估

**不准确。** -70% 是相对于 Solana 链上投票的改善。N42 的基线已经是链下投票。

| 优化 | 带宽节省 | N42 已实现？ |
|------|---------|------------|
| 投票链下化 | Solana 的 ~75% | **已实现** |
| BLS 聚合签名 | n×96B → 96B | **已实现** |
| 位图压缩 | n bytes → ceil(n/8) | **已实现** |
| 批量签名验证 | 0%（CPU 收益） | 未实现 |
| 单轮快速终局 | ~30-40%（跳过 Round 2） | 未实现 |

**N42 实际可获得的带宽节省：~30-40%（仅限快速终局路径跳过 Round 2 的情况）**

### 4.2 真实收益量化

| 改进 | 延迟收益 | CPU 收益 | 带宽收益 | 风险 |
|------|---------|---------|---------|------|
| Tier 1: 批量验证 | ~0ms | **-120ms/轮** | 0% | 零 |
| Tier 2: 分布式聚合 | ~20-50ms | ~0 | -30%（反增） | 低 |
| Tier 3: 单轮终局 | **~100-200ms** | ~50% | ~30-40% | **高** |

---

## 5. 开发必要性评估

### 5.1 竞品对比

| 竞品 | 投票方式 | BLS 聚合 | 单轮终局 |
|------|---------|---------|---------|
| Aptos (Jolteon) | 链下 P2P | ✅ | 否（2 轮） |
| Solana (Alpenglow) | 链下（新） | ✅（新） | ✅ 80% fast path |
| Sui (Mysticeti) | 链下 DAG | ✅ | 否 |
| **N42 (HotStuff-2)** | **链下 P2P** | **✅** | **否** |

N42 在投票基础设施上和 Aptos 处于同一水平。Solana 的单轮终局是唯一差异化。

### 5.2 Tier 分级建议

| Tier | 建议 | 理由 |
|------|------|------|
| **Tier 1: 批量验证** | **立即实施** | 零风险，1-2 周，CPU -80% |
| Tier 2: 分布式聚合 | 可选 | 收益小，增加复杂度 |
| Tier 3: 单轮终局 | **推迟** | 高风险（安全模型变更），收益 ~100ms |

---

## 6. 工作量详细分析

### 6.1 Tier 1: 批量 BLS 验证（推荐）

| 项目 | 详情 |
|------|------|
| 文件 | `voting.go` (缓冲投票), `quorum.go` (批量验证) |
| 新增行 | ~100-150 行 |
| 修改行 | ~50 行 |
| 测试 | 批量正确性、混合有效/无效签名、并发安全 |
| 工期 | **1-2 周** |
| 风险 | 零（内部优化，不改协议） |

### 6.2 Tier 3: 单轮快速终局（如果实施）

| 项目 | 详情 |
|------|------|
| 文件 | engine.go, voting.go, round_state.go, quorum.go, codec.go, types.go, service.go |
| 新增行 | ~1000-1500 行 |
| 修改行 | ~500 行 |
| 新类型 | FastCommitCertificate, FastFinalizationMsg |
| 协议变更 | 新消息类型、新 QC 类型、修改安全规则 |
| 安全模型 | 33% → 20%（快速路径），33%（慢速路径） |
| 测试 | 快/慢路径切换、80% 边界、拜占庭 Leader、分区恢复 |
| 工期 | **4-8 周** |
| 风险 | **高** |

---

## 7. 可能带来的问题和负面影响

### 7.1 Tier 1 (批量验证) — 几乎无负面影响

- **延迟增加**：批量验证需要等待积累一定数量的投票才验证，引入 ~10-20ms 缓冲延迟
- **错误定位**：批量验证失败时需逐条排查，增加少量代码复杂度
- **总评**：风险极低，收益确定

### 7.2 Tier 3 (单轮终局) — 多项负面影响

#### 安全模型退化
- 快速路径仅容忍 20% 拜占庭（标准 HotStuff-2 容忍 33%）
- 如果保持 33% 容忍，阈值需 90%+，快速路径很少触发
- **质疑**：为了 ~100ms 延迟改善，是否值得降低安全假设？

#### 慢速路径退化
- 当快速路径不触发（<80% 投票），自动回退到 2 轮
- 但回退本身需要额外逻辑判断，增加 ~10-20ms 开销
- 不稳定网络下频繁在快/慢之间切换，可能比纯 2 轮更慢

#### Epoch 边界风险
- 验证者集合变更时，跨 epoch 的快速证书验证更复杂
- 需要在 epoch 边界强制使用慢速路径

#### 与现有优化的冲突
- FastPropose 已将提案延迟压到 200ms。叠加单轮终局的边际收益递减
- Pacemaker 的 EWMA 延迟采样需要区分快/慢路径的不同延迟分布

#### 地理中心化压力
- 80% 阈值要求大多数验证者在 ~100ms 内投票
- 高延迟地区的验证者可能永远无法参与快速路径
- 这与去中心化目标相矛盾

---

## 8. 替代方案

| 方案 | 效果 | 复杂度 | 推荐 |
|------|------|--------|------|
| **Tier 1: 批量 BLS 验证** | CPU -80% | 极低 | **强烈推荐** |
| Tier 2: 分布式聚合 | Leader 瓶颈消除 | 中 | 可选 |
| Tier 3: 单轮终局 | 延迟 -100ms | 高 | 推迟 |
| 并行投票处理（多 goroutine 验证） | CPU -50% | 低 | 推荐 |
| 投票压缩（Delta 编码） | 带宽 -20% | 低 | 可选 |

---

## 9. 综合评估

### 评分卡

| 维度 | 分数 (1-5) | 说明 |
|------|-----------|------|
| 价值/收益 | ★★ | 核心能力已实现，增量改进有限 |
| 必要性 | ★★ | N42 投票已是链下 BLS，非紧迫需求 |
| 可行性 | ★★★★★ | Tier 1 零风险可立即实施 |
| 实施风险 | ★★（Tier 3）/ ★★★★★（Tier 1） | 分层差异大 |
| 投入产出比 | ★★★★★（Tier 1）/ ★★（Tier 3） | Tier 1 是低垂果实 |

### 结论

**路线图中 "链下 BLS 投票聚合" 的描述具有误导性。N42 的投票从未上链，BLS 聚合签名从第一天就存在。** Votor 解决的是 Solana 特有的链上投票问题，不是 N42 的问题。

**实际建议**：

1. **立即做**：Tier 1 批量 BLS 验证（1-2 周，CPU 收益 60-80%，零协议风险）
2. **重新命名**：路线图中此项应改为 "批量 BLS 签名验证优化" 而非 "链下 BLS 投票聚合"
3. **降低优先级**：从 P0 降为 P1，Tier 3 单轮终局排在 DSMR/SALT 之后
4. **更新预期**：带宽收益从 "-70%" 修正为 "CPU -80%（Tier 1）, 带宽 -30%（Tier 3 可选）"

---

## 参考文献

1. [Alpenglow: A New Consensus for Solana (Anza)](https://www.anza.xyz/blog/alpenglow-a-new-consensus-for-solana)
2. [Alpenglow: Solana's Great Consensus Rewrite (Helius)](https://www.helius.dev/blog/alpenglow)
3. [Solana's Alpenglow: A Faster Consensus with New Trade-Offs (Sei Research)](https://blog.sei.io/research/solanas-alpenglow-a-faster-consensus-with-new-trade-offs/)
4. [SIMD-0326: Alpenglow Proposal](https://github.com/solana-foundation/solana-improvement-documents/blob/main/proposals/0326-alpenglow.md)
5. [Alpenglow Upgrade Targets 100-150ms Finality (Yellow.com)](https://yellow.com/news/solana-alpenglow-upgrade-targets-100-150-millisecond-finality-through-consensus-overhaul)
6. N42 源码分析: `internal/consensus/hotstuff/`, `common/crypto/bls/`
