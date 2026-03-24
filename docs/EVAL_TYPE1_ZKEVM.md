# 深度评估：Type-1 zkEVM 全块证明 (Full-Block Prover)

> Polygon Type-1 / SP1 模式在 N42 中的实施评估
> 评估日期：2026-03-24

---

## 1. 功能概述

**Type-1 zkEVM**：对完整 EVM 区块执行生成 ZK 有效性证明。任何有效区块可被数学证明其状态转换正确，无需信任执行者。

| 来源 | 性能 | 成本 | 状态 |
|------|------|------|------|
| Polygon Type-1 | 500tx/84s (FPGA) | $0.002-0.003/tx | Mainnet |
| SP1 Hypercube (Succinct) | 99.7% ETH 块 <12s (16×RTX 5090) | $0.01-0.02/tx | Mainnet |
| RISC Zero Zeth | 区块 44s (R0VM 2.0) | $0.01-0.03/tx | 验证中 |
| Brevis Pico Prism | 99% ETH 块 <6.9s (16 消费级 GPU) | — | 验证中 |

---

## 2. 关键发现：N42 架构已 80% 就绪

### 2.1 现有 ZK 栈状态

| 组件 | 状态 | 问题 |
|------|------|------|
| **Witness 生成**（JMT Merkle proof） | ✅ 功能完整 | — |
| **Guest Input 构建** | ✅ 功能完整 | — |
| **RISC-V Guest 程序**（完整区块重放） | ✅ 编译通过 | State root 计算不匹配 |
| **SP1 客户端架构** | ✅ 接口就绪 | `submitToNetwork()` 是 TODO stub |
| **Miner→Prover 管道** | ✅ 异步接入 | — |
| **Tiered Verification 框架** | ✅ 生产就绪 | ZK tier 用 structural-only 验证 |
| **STARK/SNARK/SP1 后端** | ❌ 全是 stub | `CryptographicReady()` hardcoded `false` |
| **真实 ZK 证明生成** | ❌ 从未执行过 | 零真实密码学证明 |
| **真实 ZK 证明验证** | ❌ structural only | 仅检查公共输入结构 |

### 2.2 Guest 程序已实现的功能

`internal/zkprover/guest/execute.go` 的 `Execute()` 函数：

```
输入: GuestInput (区块头, 父区块头, 交易, witness, fork 配置)
  → 验证 witness Merkle proof vs 父状态根
  → 创建 WitnessStateReader（无状态状态访问）
  → 逐条重放所有交易（完整 EVM）
  → 计算 post-state root
  → 计算 receipts root
输出: GuestOutput {PostStateRoot, ReceiptsRoot, GasUsed, Success}
```

**这已经是完整的区块状态转换证明架构。** 缺的只是：
1. 连接到真实 SP1 证明网络
2. 修复 state root 计算（guest 用 legacy Keccak，链用 JMT Blake3）
3. 加入自定义预编译支持

---

## 3. N42 不是 Type-1（也不需要是）

### 3.1 Type-1 要求 vs N42 现实

| Type-1 要求 | N42 | 差距 |
|-------------|-----|------|
| MPT + Keccak256 状态树 | JMT + Blake3 | **根本不同** |
| 标准以太坊预编译 | 有 + AI/PQ/CAS 扩展 | 扩展但兼容 |
| 标准 EVM gas schedule | 一致 | ✅ |
| 标准区块结构 | 相似但有自定义字段 | 接近 |

**N42 证明的是自己的区块状态转换，不是以太坊的。** 这完全足够用于：
- 无信任轻客户端
- L2 结算（向 L1 证明 N42 区块）
- 跨链桥安全
- 链内安全增强

### 3.2 自定义预编译的影响

| 预编译 | 地址 | Guest 支持 | 问题 |
|--------|------|-----------|------|
| AI 推理 | 0x0301 | ❌ | 需 InferenceBackend（非确定性） |
| PQ Falcon | 0x14 | ❌ | 可加入 guest（确定性） |
| PQ Dilithium | 0x15-0x16 | ❌ | 可加入 guest（确定性） |
| CAS | 0x0303 | ❌ | 需 CAS 后端 |
| 随机数 | 0x0302 | ❌ | 可加入 guest（确定性 per-block） |

PQ 预编译是确定性的，可以加入 guest 程序。AI 推理和 CAS 有外部依赖，需要在 witness 中包含结果。

---

## 4. 价值评估

### 4.1 ZK 证明对 N42 的实际价值

| 用途 | 价值 | 紧迫性 |
|------|------|--------|
| **无信任轻客户端** | 高 — 移动端/浏览器验证区块 | 中 |
| **跨链桥安全** | 高 — 数学证明替代多签 | 中 |
| **L2 结算** | 中 — N42 作为 L2 向 ETH 结算 | 低（N42 定位 L1） |
| **链内安全** | 中 — 乐观接受+延迟 ZK 验证 | 低（HotStuff-2 已有 BFT） |
| **合规/审计** | 高 — 可验证计算证明 | 长期 |

### 4.2 成本 vs 收益

**使用第三方证明服务（推荐路径）**：

| 项目 | 成本 |
|------|------|
| SP1 SDK 集成 | 2-3 人月 |
| State root 修复 | 1-2 人月 |
| Guest 预编译支持 | 1-2 人月 |
| 测试/加固 | 1-2 人月 |
| **总计** | **5-9 人月** |
| 每块证明成本 | $0.01-0.04（Succinct Network） |
| 硬件 | **$0**（外包给证明网络） |

**自建证明基础设施（不推荐）**：

| 项目 | 成本 |
|------|------|
| 自定义 prover 开发 | 12-18 人月 |
| 硬件（16×RTX 5090） | $80,000-100,000 |
| 运维 | 持续 |
| **总计** | **远超 N42 当前团队规模** |

---

## 5. 工作量分析

### 5.1 推荐路径：连接 Succinct Prover Network

| 步骤 | 文件 | 工作量 |
|------|------|--------|
| SP1 Go SDK/FFI 集成 | `zkprover/sp1_client.go` | 2-3 周 |
| 修复 guest state root 计算 | `zkprover/guest/execute.go` | 2-3 周 |
| PQ 预编译加入 guest | `zkprover/guest/apply_tx.go` | 2 周 |
| 完善 BLOCKHASH（256 ancestors） | `zkprover/guest/execute.go` | 1 周 |
| 系统交易支持（beacon root） | `zkprover/guest/execute.go` | 1 周 |
| 真实 ZK 验证后端 | `zkverifier/verifier.go` | 2 周 |
| P2P 证明传播 | `internal/sync/` | 2 周 |
| Tiered Verifier 接入 | `distributed/coprocessor/` | 1 周 |
| 测试/集成 | 多文件 | 3 周 |
| **总计** | | **~16-18 周（4-5 人月）** |

### 5.2 不推荐路径：Type-1 以太坊等价

额外需要：
- MPT + Keccak256 状态树支持（替代或双支持 JMT）：6-9 人月
- 完整以太坊共识规则适配：3-4 人月
- **总计额外 9-13 人月，且破坏 N42 独有优势**

---

## 6. 可能带来的问题和负面影响

### 6.1 State Root 不匹配 — 评级：高（但可修复）

Guest 程序的 `IntermediateRoot()` 使用 legacy Keccak 增量哈希，而链上使用 JMT Blake3（或 pre-Shanghai 的 legacy GenerateRootHash）。证明输出的 state root 和链上不匹配。

**修复方案**：在 guest 中使用和链上相同的 root 计算逻辑（`GenerateRootHash` for pre-Shanghai）。

### 6.2 证明延迟 — 评级：低（架构已处理）

ZK 证明生成需要秒到分钟。但 N42 的 tiered verification 已经处理：
- 区块立即以乐观模式接受（TierOptimistic）
- ZK 证明异步生成后升级到 TierZK
- 不阻塞共识

### 6.3 外部依赖 — 评级：中

依赖 Succinct Network 的可用性和定价。缓解：
- 支持多个证明后端（SP1 + RISC Zero）
- 本地 simulation 模式作为 fallback
- 证明是可选的，非共识必需

### 6.4 证明成本 — 评级：低

$0.01-0.04/块。N42 8 秒出块 = $0.1-0.4/分钟 = $150-600/天。
对于有价值的跨链桥或企业用户，完全可接受。

### 6.5 竞争劣势 — 评级：高（如果自建 prover）

Polygon/Succinct/zkSync 各投入数亿美元。N42 不应在 prover 技术上竞争。
**但使用第三方证明网络完全规避了这个问题。**

### 6.6 AI 推理预编译不确定性 — 评级：中

AI 推理结果依赖模型版本。在 guest 中复现需要：
- Witness 包含推理结果（而非在 guest 中重新执行）
- 或限定确定性推理模型

### 6.7 EEST 影响 — 评级：零

ZK 证明是附加功能，不改变区块执行逻辑。EEST 测试不涉及 ZK。

---

## 7. 综合评估

### 评分卡

| 维度 | 自建 Prover | 第三方证明网络 |
|------|-----------|--------------|
| 价值 | ★★★★ | ★★★★ |
| 必要性 | ★★（BFT 已有安全保障） | ★★★（桥/轻客户端需要） |
| 可行性 | ★（团队/资金不足） | ★★★★（架构已 80% 就绪） |
| 风险 | ★（和行业巨头竞争） | ★★★★（外部依赖可控） |
| ROI | ★ | ★★★★ |

### 结论

**N42 应该走第三方证明网络路径，不应自建 prover。**

1. **架构已 80% 就绪**：RISC-V guest 程序、witness 生成、tiered verification、miner 管道全部存在
2. **缺的只是 "最后一公里"**：SP1 SDK 集成 + state root 修复 + 预编译支持
3. **4-5 人月** 可获得真实 ZK 证明能力
4. **$0.01-0.04/块** 证明成本完全可接受
5. **不追求 Type-1 以太坊等价**——证明 N42 自己的区块就够了

### 建议路线图位置

从 P2 远期 → **P1 中期**（因为架构已就绪，投入产出比高于预期）。

建议实施顺序：
1. SP1 SDK 集成（连接 Succinct Network）
2. Guest state root 修复
3. PQ 预编译加入 guest
4. 真实 ZK 验证后端
5. P2P 证明传播

---

## 参考文献

1. [Polygon Type-1 Prover](https://polygon.technology/blog/upgrade-every-evm-chain-to-zk-introducing-the-type-1-prover)
2. [SP1 Hypercube: Real-Time Proving](https://blog.succinct.xyz/sp1-hypercube/)
3. [SP1 Reth: Type-1 zkEVM](https://blog.succinct.xyz/sp1-reth/)
4. [Succinct Prover Network](https://www.theblock.co/post/365606/succinct-mainnet-prove-token)
5. [RISC Zero Zeth](https://github.com/risc0/zeth)
6. [Vitalik's zkEVM Types](https://vitalik.ca/general/2022/08/04/zkevm.html)
7. [Ethereum L1-zkEVM Roadmap 2026](https://ethereum-magicians.org/t/l1-zkevm-roadmap-2026)
8. N42 源码: `internal/zkprover/`, `internal/zkverifier/`, `cmd/zkguest/`
