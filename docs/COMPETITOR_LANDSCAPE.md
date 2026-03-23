# 公链竞品全景分析

> 日期：2026-03-23
> 范围：按市值/开发活跃度排序的主要公链技术分析

---

## 已深度分析并实现的竞品

| 公链 | 分析结果 | N42 实现 |
|------|---------|---------|
| **geth v1.16** | EIP-7904 Glamsterdam, EraE, eth_getStorageValues | ✅ 全部实现 |
| **reth v1.11** | JMT 缓存, Overlay Warmup, Stateless Validation | ✅ 全部实现 |
| **Erigon 3.3** | Storage Tiering, OtterSync, Haystack Archive | ✅ 全部实现 |
| **Sei v3** | Dependency Prediction, Prefetch Predictor | ✅ 全部实现 |
| **Monad** | Async I/O, Deep Pipeline, PooledDBStore | ✅ 全部实现 |
| **Aptos** | Code Cache, Randomness Precompile, DA Merge | ✅ 全部实现 |
| **Solana/Firedancer** | Rotor Relay, LtHash, Tile Architecture | ✅ 全部实现 |

详见 `docs/GAP_ANALYSIS.md`

---

## Avalanche (AVAX)

### 现状
AvalancheGo v1.12+，Avalanche9000 (Etna) 已于 2024.12 上线主网。HyperSDK 活跃开发中。

### 三大创新

**1. HyperSDK — 高性能 VM 框架 (Go)**
- Action-based 执行模型：开发者定义 Action 类型 + `StateKeys()` 声明访问的状态键
- 声明式状态访问使得运行时自动并行化无冲突 Action
- 多维费率：计算/存储读/存储分配/存储写/带宽各自独立 EIP-1559 定价
- Merkle 基数树 + Pebble (LevelDB 后继)
- 基准: TokenVM 50K-100K+ TPS

**2. Vryx — 解耦交易传播**
- 交易传播与共识解耦：任何节点都可以分块广播交易
- 提案者仅在区块头中引用 chunk hash（不含完整交易）
- 共识最终化时，大部分验证者已有交易数据
- 从"提案者带宽瓶颈"转为"聚合网络带宽"

**3. Avalanche9000 (Etna)**
- Subnet → L1 迁移：子网验证者无需再验证主网，门槛从 2000 AVAX 降至约 1.33 AVAX/月
- P-Chain 变为轻量级验证者注册表
- ACP-77: 验证者管理合约（自定义质押逻辑）
- ACP-118: Warp Messaging 改进（BLS 多签跨 L1 消息）

### 对 N42 的参考价值
| 技术 | 可行性 | 价值 |
|------|--------|------|
| 多维费率市场 | 高 — 扩展现有 gas_predictor | 改善资源定价准确性 |
| Vryx 解耦传播 | 中 — 需改造 P2P 和 miner | 降低区块传播延迟 |
| 声明式状态访问 | 低 (EVM 不兼容) — 可用于 AVM | Block-STM 已部分覆盖 |

---

## Sui

### 现状
Sui v1.x 主网运行中，Mysticeti BFT 已替代 Narwhal/Bullshark。终局性 ~390ms。

### 三大创新

**1. Mysticeti BFT (DAG 共识)**
- DAG 结构：所有验证者同时出提案，消除 leader 瓶颈
- 去除块级认证：包含未认证块，缩短关键路径
- 通用提交规则：容忍缺失块，单轮多 leader 提交
- 终局性从 2-3s (Narwhal/Bullshark) 降至 390ms
- 流水线：验证者持续出提案，不等前轮完成

**2. 对象级并行执行**
- 基本单元是 Object（非 Account）：每个对象有唯一 ID、所有者、版本
- Owned objects → 无需共识（拜占庭一致广播，~200ms）
- Shared objects → 需共识 (~390ms)
- 交易声明输入对象 → 调度器可直接并行无冲突交易（零推测执行）
- 热点问题：shared object 高争用时退化为串行

**3. 可编程交易块 (PTB)**
- 单交易含多个异构操作（Move 调用、转账、拆分/合并）原子执行
- 操作间可传递数据（前一步输出作为后一步输入）
- 无需路由合约，无重入风险
- 一次签名、一次共识 = N 个操作

### 对 N42 的参考价值
| 技术 | 可行性 | 价值 |
|------|--------|------|
| DAG 共识 | 中 — HotStuff-2 可扩展为 DAG 提案 | 消除 leader 瓶颈 |
| 对象并行 | 低 (EVM 不兼容) — AVM 可用 | Block-STM 已覆盖 |
| PTB 批量操作 | 高 — AI Agent 多步工作流 | 减少交易数和 Gas |

---

## CometBFT (Cosmos)

### 现状
CometBFT v1.0 稳定版，Cosmos SDK v0.50.x。Hub 出块从 6-7s 降至 4-5s，dYdX 链 <2s。

### 三大创新

**1. ABCI 2.0 — 精细区块生命周期**
- `PrepareProposal`: 提案者控制区块内容（重排序/插入/移除交易，MEV 拍卖）
- `ProcessProposal`: 每个验证者投票前验证提案（拒绝恶意区块）
- `FinalizeBlock`: 替代 BeginBlock+DeliverTx+EndBlock（原子化，减少 ABCI 往返）
- 吞吐提升 ~15-20%

**2. Vote Extensions — 投票附加数据**
- 验证者在 pre-commit 投票中附加应用定义数据
- 下一个提案者聚合这些数据用于 PrepareProposal
- 实际应用:
  - 预言机价格: 验证者提交价格证明 → 聚合为中位数（无需外部预言机网络）
  - MEV 拍卖: 密封出价通过投票扩展提交
  - 阈值签名: IBC 轻客户端更新分布在投票扩展中

**3. 乐观执行 + IBC v2**
- 乐观执行: 投票阶段即开始执行提案块，接受则完成，拒绝则丢弃
- 执行重负载链延迟降低 20-40%
- IBC v2 Eureka: 取消通道握手（4 tx → 1 tx），多跳路由，IBC-over-Ethereum (ZK 轻客户端)

### 对 N42 的参考价值
| 技术 | 可行性 | 价值 |
|------|--------|------|
| Vote Extensions | 高 — 扩展共识接口 | 原生预言机、MEV 拍卖 |
| PrepareProposal/ProcessProposal | 高 — 扩展 Engine 接口 | 应用层提案验证 |
| 乐观执行 | 高 — deferred pipeline 已就绪 | 共识+执行重叠 |

---

## Near Protocol

### 现状
4 分片主网运行，800-1200 TPS，出块 ~1.2s，终局 ~2s。Stateless validation 已上线。

### 三大创新

**1. 无状态验证 + 状态见证**
- 验证者不再存储分片完整状态
- Chunk 生产者生成状态见证（所有访问的 trie 节点 + Merkle 证明）
- 见证大小 1-5 MB/chunk，Zstandard 压缩 + Reed-Solomon 纠删编码
- 验证者可验证任意分片（无需存储其状态）
- 动态验证者-分片分配：每个 epoch 重新分配，攻击者无法预测

**2. Chain Signatures (MPC 跨链签名)**
- 约 30 个 MPC 节点（从验证者中选出）集体持有密钥份额
- NEAR 智能合约调用 `sign(payload, path)` → MPC 网络产生有效签名
- 确定性密钥派生：`path` + 合约 account ID → 每个合约在每条目标链上有唯一密钥
- 无桥梁、无流动性池、无中继欺诈风险
- 已部署: Sweat Wallet (BTC), Defuse (跨链 DEX)

**3. 动态重分片**
- 分片 Gas 使用率 >80% 持续多个 epoch → 自动分裂
- 相邻分片均 <30% → 合并
- 结合无状态验证：验证者无需迁移状态
- 目标: 4 → 10+ 分片，5000+ TPS

### 对 N42 的参考价值
| 技术 | 可行性 | 价值 |
|------|--------|------|
| 状态见证 | 高 — JMT 已支持 Merkle 证明 | 轻量验证者 |
| MPC 跨链签名 | 中 — 需新增阈值 ECDSA | 原生跨链操作 |
| 动态负载缩放 | 中 — 协处理器可动态调度 | 自适应吞吐 |

---

## MegaETH

### 现状
公共测试网 2025 年底启动。宣称 100K+ TPS，<10ms 出块。L2 架构（Ethereum 结算）。

### 三大创新

**1. 全内存状态**
- 排序器将完整链状态放入 RAM（512GB+），零磁盘 I/O
- 执行时无 Merkle trie 遍历，状态存储为扁平 hash map
- Merkle root 在执行后异步计算
- NUMA 感知内存布局：按地址前缀分区跨 NUMA 节点
- 简单转账 ~2μs，Uniswap V2 Swap ~10μs

**2. 异构节点架构**
- 排序器: 高端服务器（100+ 核，512GB+ RAM）—— 唯一活跃排序器
- 全节点: 接收状态差异（非完整区块），不重执行
- 证明节点: 异步生成 ZK 有效性证明
- 状态差异传播比完整区块小 10-100x

**3. EVM AOT 编译**
- 热合约从 EVM 字节码编译为原生 x86-64 机器码
- 消除解释器循环开销
- JIT 预编译：利用 AES-NI/AVX-512 硬件加速
- 推测执行：前一交易未完全提交即开始执行下一交易

### 对 N42 的参考价值
| 技术 | 可行性 | 价值 |
|------|--------|------|
| 异步状态根计算 | 高 — deferred pipeline 已就绪 | 降低出块延迟 |
| 状态差异同步 | 高 — changeset 基础设施已有 | 减少同步带宽 |
| AOT/JIT EVM | 中 — 需大量工程 | 热合约 10-50x 加速 |

---

## TON (The Open Network)

### 现状
Telegram 9 亿用户驱动，Mini Apps 生态活跃。基准 50K-100K+ TPS。

### 两大创新

**1. 无限分片范式**
- 动态分裂/合并至 2^60 个分片，每个账户实质上有自己的"账户链"
- 跨分片异步消息传递 + 自动路由
- 合约天然分片感知——无需特殊适配

**2. TVM + FunC/Tact**
- Cell 架构：1023 位数据 + 4 引用（非 EVM 的 256 位字）
- Tact 语言 (2024): TypeScript 风格语法
- TON Storage: 类 BitTorrent 分布式文件存储
- TON DNS: 链上名称解析

### 对 N42 的参考价值
异步消息传递模式与 N42 的分片 GossipSub relay 概念一致。Cell 数据模型可参考用于 CAS 存储。

---

## Polkadot/Substrate

### 现状
Async Backing 已上线，Agile Coretime 已上线。JAM 处于测试网/实现阶段。

### 两大创新

**1. JAM (Join-Accumulate Machine)**
- 替代 Relay Chain 的通用计算引擎
- Join 阶段: 验证者跨 ~340 核并行执行工作包
- Accumulate 阶段: 结果确定性折叠进共享状态
- PVM (基于 RISC-V 的 VM) 替代 Wasm
- 5+ 团队竞争实现（JAM Implementer's Prize）

**2. Elastic Scaling**
- 单平行链可同时消费多个核心
- 按需购买计算时间（非 2 年拍卖）
- 动态资源分配

### 对 N42 的参考价值
JAM 的 Join-Accumulate 模式与 N42 的 MapReduce batch compute (`distributed/compute/batch/`) 直接对应。分层验证与 N42 的 TieredVerifier 同构。

---

## Cardano

### 现状
Chang 硬分叉完成（2024），链上治理 (CIP-1694) 生效。Ouroboros Leios 研发中。

### 两大创新

**1. Ouroboros Leios (输入代言人)**
- 三种区块类型: Input Blocks (频繁，任何池) → Endorsement Blocks → Ranking Blocks (排序)
- 交易吞吐与网络带宽成正比（非出块间隔瓶颈）
- 目标 10-100x 吞吐提升

**2. Plutus V3 + Hydra**
- Plutus V3: BLS12-381 (链上 ZK-SNARK 验证)，SoP 编码减少脚本成本 30-50%
- Hydra: 同构状态通道（完整 Plutus 执行），亚秒终局

### 对 N42 的参考价值
Leios 的交易传播-排序分离与 N42 的 deferred pipeline 概念对齐。BLS12-381 链上 ZK 验证与 N42 的 zkverifier 平行。

---

## Celestia

### 现状
主网 2023.10 启动，已成为主流模块化 DA 层。Blob 空间从 2MB 扩至 8MB+。

### 两大创新

**1. 数据可用性采样 (DAS)**
- 2D Reed-Solomon 编码矩阵 (k×k → 2k×2k)
- 轻节点随机采样 ~75 个 cell → 99%+ 置信度
- 区块大小随轻节点数量扩展（非全节点）

**2. Namespace Merkle Tree (NMT)**
- 叶子按 namespace ID 排序
- Rollup 仅下载自己 namespace 的数据
- 支持包含证明 + 完整性证明（无遗漏）

### 对 N42 的参考价值
DAS 与 N42 的 PeerDAS (EIP-7594) 直接对应。NMT 概念可增强 CAS 存储的命名空间隔离。

---

## StarkNet

### 现状
v0.13+，生产 10-50 TPS。Cairo 2.x 稳定，Stwo 下一代证明器开发中。

### 两大创新

**1. Cairo 2.x → Cairo Native**
- Sierra 安全中间表示：保证程序终止
- Cairo Native: Sierra → LLVM → 原生 x86（排序器速度 10-100x）
- 证明仍走 STARK 电路

**2. 递归 STARK + Stwo 证明器**
- 递归证明组合：多个交易证明聚合为单个证明
- Stwo: Circle STARKs over Mersenne31 (2^31-1) 域
- 单指令乘法 → 证明速度 10-100x 提升
- SHARP: 跨部署共享证明成本

### 对 N42 的参考价值
递归 STARK 聚合可应用于 N42 的 ZKML 证明。Cairo Native 的 LLVM 编译思路可启发 AVM/EVM JIT。Sierra 安全 IR 与 N42 的 WASM fuel 计量解决同一问题（停机问题）。

---

## 跨链共性主题

所有 2025-2026 年公链共享一个核心主题：**关注点分离**

| 分离维度 | 代表项目 | N42 对应 |
|---------|---------|---------|
| 数据可用性 ↔ 执行 | Celestia | PeerDAS (`internal/peerdas/`) |
| 共识 ↔ 执行 | Cardano Leios, CometBFT | Deferred Pipeline (`internal/deferred/`) |
| 出块 ↔ 证明 | StarkNet | ZK Prover (`internal/zkprover/`) |
| 计算 ↔ 验证 | Polkadot JAM | Coprocessor (`internal/distributed/coprocessor/`) |
| 交易传播 ↔ 排序 | Avalanche Vryx | Rotor + HotStuff (`internal/consensus/hotstuff/`) |

---

## Top 5 深度评估结论

> 日期：2026-03-23 | 方法：源码审计 + 生产数据 + N42 代码库交叉验证

### 评估标准
- 是否有生产验证（非 testnet/devnet/paper）
- 实际采用率（多少链/用户在用）
- N42 是否已有等价或更好的实现
- 实施成本 vs 收益比

### 逐项评估

**1. Vote Extensions (CometBFT) — SKIP**

| 维度 | 评估 |
|------|------|
| 生产验证 | 仅 4-6 条 Cosmos 链 (<10% 采用率) |
| 事故记录 | dYdX 2025.10 因 oracle sidecar 故障停链 8 小时，赔偿 $462K |
| 维护状态 | Skip Protocol 已于 2025.1 停止维护 Slinky/Connect |
| N42 已有 | HotStuff-2 Proposal 已携带 piggybacked QC + TxRootHash（同模式） |
| 不实施原因 | N42 无需亚区块价格更新的 DEX 场景；如需预言机用预编译更安全 |

**2. 多维费率市场 (Avalanche HyperSDK) — SKIP**

| 维度 | 评估 |
|------|------|
| 生产验证 | **零** — HyperSDK README 标注 "ALPHA, not safe for production" |
| 实际部署 | 无任何 Avalanche L1 在生产环境使用 HyperSDK |
| N42 已有 | EIP-1559 + EIP-4844 blob gas = 双维费率（已完整实现） |
| 不实施原因 | 5 维费率影响交易格式/区块头/钱包/SDK，N42 规模下无多维争用问题 |

**3. Vryx 解耦传播 (Avalanche) — SKIP**

| 维度 | 评估 |
|------|------|
| 生产验证 | **零** — 仅 devnet 基准（100K TPS 合成负载） |
| N42 已有 | Deferred execution pipeline（共识-执行分离 = Vryx 核心思想） |
| 不实施原因 | N42 的 7-100 验证者集，1MB 区块 GossipSub 传播 ~0.8ms/节点，Vryx 解决的带宽瓶颈**在此规模不存在** |

**4. 状态差异同步 (MegaETH) — DEFER（长期路线图）**

| 维度 | 评估 |
|------|------|
| 生产验证 | ✅ MegaETH 主网 2026.2 上线，35K TPS 压测通过 |
| N42 已有 | 70% 基础设施：DiffLayer + DiffCollector + StateDiffClient (gRPC) |
| 缺少部分 | P2P 差异传播 + 非执行副本的证明验证 (~30% 工作量) |
| 判定 | **当 N42 需支撑大量 RPC 副本节点时实施**，当前规模无需 |

**5. PTB 批量操作 (Sui) — SKIP**

| 维度 | 评估 |
|------|------|
| 生产验证 | ✅ Sui 原生交易格式（100% 使用，但多操作 PTB 占比未公开） |
| N42 已有 | ERC-4337 Bundler + AI Agent 会话密钥 + PaymasterService（已覆盖核心场景） |
| 不实施原因 | 需新交易类型 → **破坏 EVM 兼容性**（N42 核心价值主张） |

### 汇总

| # | 功能 | 判定 | N42 已有等价实现 |
|---|------|------|----------------|
| 1 | Vote Extensions | **SKIP** | HotStuff-2 piggybacked QC/TxRootHash |
| 2 | 多维费率 | **SKIP** | EIP-1559 + blob gas (双维) |
| 3 | Vryx 解耦传播 | **SKIP** | Deferred execution pipeline |
| 4 | 状态差异同步 | **DEFER** | DiffLayer + StateDiffClient (待升级) |
| 5 | PTB 批量操作 | **SKIP** | ERC-4337 Bundler + AI Agent session keys |

### 结论

**N42 的现有架构已覆盖以上功能的核心价值。** 4 项 SKIP 因为：
- 竞品功能未经生产验证（HyperSDK alpha、Vryx devnet）
- 存在严重事故（dYdX 停链 8 小时）
- N42 已有等价或更优实现（deferred pipeline、ERC-4337）
- 实施会破坏 EVM 兼容性（PTB）

唯一保留的"状态差异同步"已有 70% 基础设施就位，列入长期路线图等需求驱动。

---

## 未评估但值得关注

以下功能尚未深度评估，未来可按需分析：

| 技术 | 来源 | 关注理由 |
|------|------|---------|
| DAG 共识 (Mysticeti) | Sui | 消除 leader 瓶颈，但需重写共识层 |
| MPC 跨链签名 | Near | 原生跨链操作，但需新密码学组件 |
| 异步状态根计算 | MegaETH | deferred pipeline 已部分覆盖 |
| 递归 STARK 聚合 | StarkNet | ZKML 证明成本优化 |
| EVM JIT/AOT 编译 | MegaETH | 热合约 10-50x 加速，工程量大 |
| JAM Join-Accumulate | Polkadot | 与 N42 MapReduce batch compute 同构 |
| 动态重分片 | Near | 当前非分片架构不适用 |
