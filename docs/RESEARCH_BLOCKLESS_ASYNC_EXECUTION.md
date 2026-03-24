# 研究报告：无块异步执行架构 (Blockless Async Execution)

> 突破区块概念的分布式交易处理范式
> 研究日期：2026-03-24

---

## 1. 目标架构描述

一个不依赖传统"区块"概念的分布式网络：

| 属性 | 要求 |
|------|------|
| **无块** | 交易独立异步处理，不打包成块 |
| **无限并行** | 任何人可同时发交易，任何节点可同时处理 |
| **线性+扩展** | 吞吐量随使用量/节点数线性以上增长 |
| **proof-carrying** | 交易携带所依赖数据的有效性证明 |
| **nonce 连续** | 同 nonce 需指数级手续费或拒绝 |
| **后量子安全** | 签名抗量子计算破解 |
| **变长确认** | 单笔确认取决于引用状态的证明验证复杂度 |
| **DAG 或任意结构** | 不限于线性链 |
| **极致状态分片** | 每个账户/合约可独立分片 |

---

## 2. 全球类似论文与研究

### 2.1 DAG / BlockDAG 基础理论

| 论文 | 年份 | 核心贡献 | URL |
|------|------|---------|-----|
| **PHANTOM & GHOSTDAG** (Sompolinsky, Zohar) | 2018 | BlockDAG 排序：将并行区块分为 Blue/Red 集合，贪心多项式近似 | [eprint/2018/104](https://eprint.iacr.org/2018/104) |
| **SPECTRE** (Sompolinsky et al.) | 2016 | DAG 递归投票快速确认，50% 拜占庭容忍 | [eprint/2016/1159](https://eprint.iacr.org/2016/1159) |
| **DAG KNIGHT** (Sutton, Sompolinsky) | 2022 | 无参数 GHOSTDAG，确认速度自适应网络条件 | [eprint/2022/1494](https://eprint.iacr.org/2022/1494.pdf) |
| **Tangle 2.0** (Müller, Penzkofer et al.) | 2022 | 无领导者 Nakamoto 共识 on DAG，权益加权 | [arXiv:2205.02177](https://arxiv.org/abs/2205.02177) |
| **SoK: DAG-based Consensus** (Raikwar et al.) | 2024 | 最全面的 DAG 共识综述 | [arXiv:2411.10026](https://arxiv.org/abs/2411.10026) |

### 2.2 DAG 共识协议

| 论文 | 年份 | 核心贡献 | URL |
|------|------|---------|-----|
| **Narwhal & Tusk** (Danezis et al.) | 2022 | 分离 mempool（Narwhal）和排序（Tusk），160K TPS | [arXiv:2105.11827](https://arxiv.org/abs/2105.11827) |
| **Bullshark** (Spiegelman et al.) | 2022 | DAG BFT "embarrassingly simple"（20 行代码），125K TPS | [arXiv:2201.05677](https://arxiv.org/abs/2201.05677) |
| **Mysticeti** (Babel et al.) | 2023 | 达到理论下界 3 轮消息，390ms 共识延迟 | [arXiv:2310.14821](https://arxiv.org/abs/2310.14821) |
| **Sui Lutris** (Blackshear et al.) | 2024 | 首个亚秒智能合约平台：owned objects 绕过共识 | [arXiv:2310.18042](https://arxiv.org/abs/2310.18042) |
| **Lachesis** (Fantom) | 2021 | aBFT PoS DAG 流，Lamport 时间戳 | [arXiv:2108.01900](https://arxiv.org/abs/2108.01900) |
| **Hashgraph** (Baird) | 2016 | "gossip about gossip" + 虚拟投票，aBFT，Coq 形式验证 | [Swirlds TR](https://www.swirlds.com/wp-content/uploads/2016/06/2016-05-31-Swirlds-Consensus-Algorithm-TR-2016-01.pdf) |
| **Avalanche** (Team Rocket) | 2018 | 概率采样元稳定共识 | [arXiv:1906.08936](https://arxiv.org/pdf/1906.08936) |

### 2.3 并行执行与分片

| 论文 | 年份 | 核心贡献 | URL |
|------|------|---------|-----|
| **Block-STM** (Aptos) | 2022 | 软件事务内存并行执行，170K TPS | [arXiv:2203.06871](https://arxiv.org/abs/2203.06871) |
| **Cerberus** (Radix) | 2020 | 2^256 分片 + 跨分片原子性，最小化 BFT 延迟 | [arXiv:2008.04450](https://arxiv.org/pdf/2008.04450) |
| **OmniLedger** (Kokoris-Kogias et al.) | 2018 | **吞吐量随验证者线性扩展**，6000 TPS/1800 节点 | [eprint/2017/406](https://eprint.iacr.org/2017/406.pdf) |
| **Prism** (Bagaria, Tse et al.) | 2019 | 逼近物理极限的吞吐/延迟，50% 拜占庭容忍 | [arXiv:1810.08092](https://arxiv.org/abs/1810.08092) |

### 2.4 无状态执行 / Proof-Carrying 交易

| 论文 | 年份 | 核心贡献 | URL |
|------|------|---------|-----|
| **不可能性定理** (a16z Crypto) | 2023 | **信息论证明：纯无状态区块链不存在有用的折衷** | [a16z](https://a16zcrypto.com/posts/article/on-the-impossibility-of-stateless-blockchains/) |
| **Piperine** (Lee et al.) | 2020 | 无需复制执行的状态机复制，ZK 证明 | [eprint/2020/195](https://eprint.iacr.org/2020/195) |
| **Verkle Trees** (Ethereum EIP-6800) | 2025 | 多项式承诺 + SNARK 实现无状态验证 | [arXiv:2504.14069](https://arxiv.org/abs/2504.14069) |
| **ACE** (Wüst et al.) | 2020 | 异步并发智能合约执行，链下执行+灵活信任 | [eprint/2019/835](https://eprint.iacr.org/2019/835) |

### 2.5 后量子密码学

| 论文 | 年份 | 核心贡献 | URL |
|------|------|---------|-----|
| **PQ DLT 系统综述** (Nature) | 2023 | Dilithium/Falcon/SPHINCS+ 在分布式账本中的集成 | [Nature](https://www.nature.com/articles/s41598-023-47331-1) |
| **PQ vs Quantum 区块链对比** | 2024 | 全面对比后量子和量子区块链方案 | [arXiv:2409.01358](https://arxiv.org/pdf/2409.01358) |

---

## 3. 现有产品与协议

### 3.1 最接近目标架构的系统

| 系统 | 无块 | per-account 分片 | 线性扩展 | PQ安全 | proof-carrying | 状态 |
|------|------|-----------------|---------|--------|---------------|------|
| **Nano** | ✅ block-lattice | ✅ 每账户一条链 | ❌ 固定容量 | ❌ | ❌ | Mainnet |
| **IOTA Tangle 2.0** | ✅ DAG | ❌ 共享 DAG | 部分 | 曾有(W-OTS) | ❌ | Devnet |
| **Radix Cerberus** | ❌ 有分片块 | ✅ 2^256 分片 | ✅ 声称 | ❌ | ❌ | Mainnet |
| **Sui** | ❌ 有 checkpoint | ✅ per object | 部分 | ❌ | ❌ | Mainnet |
| **TON** | ❌ 有分片块 | ✅ AccountChain | ✅ 动态 | ❌ | ❌ | Mainnet |
| **Holochain** | ✅ agent-centric | ✅ | ✅ 无全局共识 | ❌ | ❌ | Beta |
| **Shardus** | ✅ 无块 | ✅ | ✅ 声称 | ❌ | ❌ | Testnet |
| **Obyte** | ✅ tx=block | ❌ | ❌ | ❌ | ❌ | Mainnet |
| **Kaspa** | ❌ BlockDAG | ❌ | 部分 | ❌ | ❌ | Mainnet |
| **Hedera** | 虚拟(hashgraph) | ❌ | ❌ | ❌ | ❌ | Mainnet |
| **QRL** | ❌ 传统链 | ❌ | ❌ | ✅ XMSS | ❌ | Mainnet |

**没有任何现有系统同时实现全部 9 项属性。**

### 3.2 各系统详解

#### Nano / XNO — Block-Lattice
- 每个账户有自己的区块链（account-chain），全局结构是 DAG（block-lattice）
- 交易两阶段：发送方发 "send" 块，接收方发 "receive" 块
- 共识：Open Representative Voting（委托 PoS 投票解决冲突）
- 零手续费，无矿工
- **代码**: https://github.com/nanocurrency/nano-node

#### Radix DLT — Cerberus
- 2^256 分片，每个组件/资源独立分片
- Cerberus：per-shard BFT + 跨分片原子组合
- 2026年1月公测：**500,000+ 持续 TPS，峰值 800K+ TPS**（128 分片）
- **代码**: https://github.com/radixdlt

#### TON — 无限分片
- 每个账户是自己的 AccountChain，聚合为 ShardChain，再聚合为 WorkChain
- 最多 2^60 分片/工作链，按负载动态分裂/合并
- 异步消息传递跨 AccountChain
- **白皮书**: https://test.ton.org/ton.pdf

#### Sui — Object-Centric
- Owned objects 绕过共识（Byzantine Consistent Broadcast）
- Shared objects 走 Mysticeti DAG 共识
- 120K+ TPS，~400ms 终局
- **论文**: https://docs.sui.io/concepts/research-papers

#### Holochain — Agent-Centric
- 无全局共识，每个 agent 维护自己的签名链 + 共享 DHT
- 验证规则由应用 "DNA" 定义
- 无挖矿，无全局状态，90% 更少计算开销
- **代码**: https://github.com/holochain/holochain

#### Kaspa — BlockDAG
- GHOSTDAG 排序并行区块，不孤立任何块
- PoW，当前 10 块/秒，路线图到 100 块/秒
- 5,705 TPS → 路线图 30,000 TPS
- **代码**: https://github.com/kaspanet/kaspad

---

## 4. 关键研究问题解答

### Q1: 吞吐量真的能随节点数线性扩展吗？

**有，但有上限：**
- **OmniLedger** (2018): 实验证明 600→1800 节点线性扩展（6000 TPS）
- **Radix**: 128 分片 500K+ TPS
- **Zilliqa**: 600→3600 节点近线性扩展
- **TON**: 理论上通过分裂为更多分片来扩展（最多 2^60）

**约束**：跨分片通信开销限制完美线性。无系统展示过**超线性**扩展。

### Q2: "Proof-Carrying 交易" 最新进展？

- **Ethereum Verkle Trees (EIP-6800)**: 最成熟方案。每个区块携带 witness（状态数据+密码学证明）
- **NEAR Nightshade 2.0**: 已上线的无状态验证（验证者用 state witnesses 验证分片块）
- **Piperine**: 不可信证明者产出 ZK 证明，减少 5.4x 成本
- **根本限制 (a16z 2023)**: **信息论不可能**——要么全局状态线性大小，要么 witness 需近线性更新频率

### Q3: DAG 系统如何处理冲突交易（双花）？

| 方法 | 使用者 |
|------|--------|
| 虚拟投票 | Hedera Hashgraph |
| 概率采样 | Avalanche Snowball |
| 权重累积 | IOTA Tangle |
| GHOSTDAG k-cluster | Kaspa |
| Byzantine Consistent Broadcast | Sui（owned objects） |
| Witness/Main chain | Obyte |
| **因果排序**（非冲突无需排序） | Cerberus, Sui, TON |

### Q4: Per-Account 分片的理论极限？

- **TON**: 每账户 = 一个分片（2^60 分片/工作链）→ 理论极限
- **Nano**: 字面实现——每账户一条链
- **Radix**: 2^256 分片
- **根本约束**：跨分片/跨账户通信成为瓶颈。智能放置可减少 34.7% 跨分片交易

### Q5: 后量子签名 + DAG/无块 的组合？

- **IOTA 原始版**: 使用 Winternitz 一次性签名（基于哈希，抗量子），2021 年因实用性切回 ECDSA
- **QRL**: 生产级 XMSS（NIST 批准），但用传统线性链
- **无现有生产系统**同时使用 NIST PQC 标准签名 + DAG/无块架构
- **核心挑战**：PQ 签名体积 10-100x 大于 ECDSA（Dilithium ~2.4KB，Falcon ~666B），在 DAG 中每笔交易独立携带签名，带宽压力巨大

### Q6: 无块/异步执行的未解决问题？

| # | 问题 | 说明 |
|---|------|------|
| 1 | **跨分片原子性** | 交易触及多账户/分片时的原子执行——最难的开放问题 |
| 2 | **状态 witness 不可能性** | a16z 证明纯无状态设计的根本折衷不可避免 |
| 3 | **冲突解决延迟** | DAG 中冲突交易的解决时间不确定且可能很长 |
| 4 | **DAG 中的 MEV** | 并行执行下的 MEV 分析和防护远比线性链复杂 |
| 5 | **智能合约组合性** | DeFi 的原子组合（闪电贷、多步兑换）在 per-account 分片中极难 |
| 6 | **确定性重放** | 并行执行下保证所有节点重放到相同状态——Block-STM 解决块内，跨 DAG 更难 |
| 7 | **状态增长管理** | 无块结构下状态增长不可预测，剪枝比线性链更复杂 |
| 8 | **PQ 签名开销** | NIST PQC 签名 10-100x 大于 ECDSA，在 DAG 中每笔交易一个签名的带宽影响 |

---

## 5. 综合判断

### 5.1 最接近的合成架构

你描述的系统是以下方案的综合体：

```
Nano block-lattice (每账户独立链, 无块)
  + Radix Cerberus (无限并行, 跨分片原子性)
  + Sui object model (非冲突交易绕过共识)
  + Ethereum Verkle/stateless (proof-carrying 交易)
  + NIST PQC (后量子签名)
  + 指数级同 nonce 手续费（全新概念）
```

### 5.2 什么已被证明可行

| 属性 | 已证明可行？ | 最佳实现 |
|------|------------|---------|
| 无块交易处理 | ✅ | Nano, Obyte, Shardus |
| 每账户独立分片 | ✅ | TON, Nano, Radix |
| 线性扩展 | ✅（有上限） | OmniLedger, Radix, Zilliqa |
| 非冲突交易绕过共识 | ✅ | Sui (owned objects) |
| Proof-carrying 交易 | ⚠️ 部分（有不可能性定理） | Ethereum Verkle, NEAR |
| 后量子签名 | ✅（但非 DAG） | QRL (XMSS) |
| 后量子 + DAG | ❌ 无生产实现 | IOTA 曾有但撤回 |
| 指数级同 nonce 费用 | ❌ 全新概念 | 无 |
| 全部 9 项同时 | ❌ | 无 |

### 5.3 根本性挑战

1. **a16z 不可能性定理**：纯无状态区块链存在信息论极限——要么全局状态线性大，要么 witness 更新频率近线性。这意味着 proof-carrying 交易不可能完全消除全局状态依赖。

2. **PQ 签名体积**：NIST 标准签名（Dilithium 2.4KB, Falcon 666B）在 per-tx DAG 中的带宽开销是 ECDSA（64B）的 10-40 倍。1000 TPS × 2.4KB = 2.4 MB/s 仅签名。

3. **跨分片原子组合**：DeFi 的核心模式（闪电贷、多步兑换）要求原子跨分片交易。Cerberus 声称解决了这个问题但规模验证有限。

---

## 6. 关键代码仓库

| 项目 | 仓库 | 语言 |
|------|------|------|
| Kaspa (GHOSTDAG) | https://github.com/kaspanet/kaspad | Go |
| Sui (Mysticeti) | https://github.com/MystenLabs/sui | Rust |
| IOTA | https://github.com/iotaledger | Rust |
| Nano | https://github.com/nanocurrency/nano-node | C++ |
| Narwhal/Tusk | https://github.com/facebookresearch/narwhal | Rust |
| AlephBFT | https://github.com/aleph-zero-foundation/AlephBFT | Rust |
| Holochain | https://github.com/holochain/holochain | Rust |
| QRL | https://github.com/theQRL/QRL | Python |
| Radix | https://github.com/radixdlt | Java/Rust |
| Obyte | https://github.com/byteball | JS |
| Aptos (Block-STM) | https://github.com/aptos-labs/aptos-core | Rust |

---

## 7. 对 N42 的启示

N42 已有的相关能力：Block-STM 并行执行、HotStuff-2 BFT、PQ 密码学（Dilithium/Falcon）、JMT 状态承诺、Tile pipeline。

如果 N42 要向这个方向演进，最可行的路径是：

| 阶段 | 方向 | 参考 | 可行性 |
|------|------|------|--------|
| **近期** | Sui 式 owned-object 共识绕过 | Sui Lutris | ★★★★ |
| **中期** | Cerberus 式 per-shard 并行共识 | Radix Cerberus | ★★★ |
| **远期** | Nano 式 block-lattice + PQ 签名 | Nano + QRL | ★★ |
| **研究** | proof-carrying 交易 (Verkle witness) | Ethereum EIP-6800 | ★★（受不可能性定理限制） |

最大的启示：**不冲突的交易不需要排序**。这是 Sui、Cerberus、TON 的核心洞见。N42 的 Block-STM 已经在块内利用了这个原理，将其扩展到块间（或无块）是自然的演进方向。
