# N42 分布式基础设施扩展计划

> 日期：2026-03-20
> 方向：从"智能分布式账本"扩展为"分布式存储 + 计算 + 通讯"全栈基础设施
> 方法：基于网络搜索调研 2026 行业最新进展，评估集成路径和优先级

---

## 一、行业现状

### 1.1 分布式存储

| 项目 | 状态 | 核心机制 | 规模 |
|------|------|----------|------|
| **Filecoin** | 主网运行，FVM 支持 EVM 合约 | PoRep + PoSt 存储证明，FOC (Onchain Cloud) 升级中 | 2025 年容量增长 400% |
| **Arweave** | 主网运行，AO 计算层已上线 | 一次付费永久存储，256KB 块的 Merkle 树 | 全球永久数据湖 |
| **EthStorage** | 以太坊主网 Alpha | 扩展 EIP-4844 blob 为长期存储，zk-SNARK 验证 | PB 级 |
| **Walrus** | 2025.3 主网上线 | 纠删编码分片，4-5x 冗余（远优于 Filecoin 5000 副本） | Sui 生态 |
| **Celestia** | 主网运行，50% DA 市场份额 | DAS 数据可用性采样 + 命名空间 Merkle 树 | 160+ GB 已发布数据 |
| **IPFS** | 25 万+ 公共节点 | 内容寻址（CID），libp2p 传输 | 250 万日活用户 |

### 1.2 分布式计算

| 项目 | 状态 | 核心机制 | 定位 |
|------|------|----------|------|
| **Akash** | 主网运行，2026.3 Mainnet 16/17 | Cosmos SDK，K8s 容器编排，反向拍卖 GPU 市场 | 去中心化云计算 |
| **Render** | Solana 主网，市值 >$1B | GPU 渲染子网 + AI/ML 计算子网 (Dispersed) | GPU 渲染 + AI 推理 |
| **Gensyn** | 测试网，以太坊 Rollup | SkipPipe 流水线并行 + Verde 验证协议 | AI 训练 |
| **io.net** | Solana 主网，130+ 国家 | GPU 聚合市场，比 AWS 便宜 70% | AI 推理 |
| **RISC Zero Boundless** | Base 主网（2025.9） | zkVM + STARK→Groth16 递归 + PoVW 代币激励 | 可验证计算市场 |
| **SP1 Hypercube** | 主网运行 | RISC-V zkVM，多线性多项式证明 + Jagged PCS | 实时 ZK 证明 |
| **ZK 协处理器** | 多个项目已上线 | 智能合约将重计算卸载到链下，ZK 证明验证结果 | 链上可验证计算 |

**ZK 协处理器对比：**

| 项目 | 数据覆盖 | 延迟 | 核心优势 |
|------|----------|------|----------|
| Brevis | 以太坊 + EVM L2 | 58-350s | 实时 L1 证明，SDK 电路定义 |
| Axiom | 以太坊历史状态 | 6-12s | 快速窄范围证明 |
| Space and Time | 75 链 + 链下 | 6-12s | SQL 接口，无需写电路 |
| Herodotus | 多链历史状态 | 变化 | 跨链状态证明 |

### 1.3 分布式通讯

| 项目 | 状态 | 核心机制 | 用户规模 |
|------|------|----------|----------|
| **Waku** | 主网运行（Status 使用） | libp2p GossipSub + 8 分片 + ZK 速率限制 (RLN) | Vitalik 认可 |
| **XMTP** | 200 万+ 身份，60+ 应用 | IETF MLS 端对端加密，钱包到钱包消息 | 2026.3 主网预期 |
| **Push Protocol** | 主网运行，正在建 Push Chain L1 | 去中心化通知 + 钱包聊天 + 代币门控 | — |
| **Farcaster** | 主网运行，2026.1 被 Neynar 收购 | 链上身份 (OP Mainnet) + 链下社交 (Snapchain) | 充分去中心化 |
| **libp2p GossipSub** | v1.2 规范，v1.1 广泛使用 | 显式对等 + PX 引导 + IDONTWANT 控制 | N42 已使用 |

---

## 二、N42 集成评估

### 2.1 集成难度分层

```
应用层（无需协议改动）        ← 最容易
  ├── IPFS CID 存储引用
  ├── XMTP / Push Protocol SDK
  ├── Walrus / Arweave 数据引用
  ├── Akash / Render 计算请求 Oracle
  └── Farcaster 身份桥接

预编译/节点配置层              ← 中等
  ├── Waku Relay 集成（N42 已有 libp2p）
  ├── GossipSub v1.2 升级
  ├── ZK 验证预编译增强
  └── Filecoin CCDB 桥接合约

协议层                        ← 较大改动
  ├── 原生 ZK 协处理器支持
  ├── RLN ZK 速率限制
  ├── 原生 Blob 存储扩展
  └── 原生分布式存储原语
```

### 2.2 优先级评估

| 方向 | 项目 | 优先级 | 理由 | 工作量 |
|------|------|--------|------|--------|
| **计算** | ZK 协处理器原生支持 | **P0** | N42 已有 SP1/STARK/SNARK 验证器；直接价值：智能合约可调用链下重计算 | S |
| **通讯** | Waku 消息协议集成 | **P1** | N42 已用 libp2p，Waku 是自然扩展；RLN 可做 P2P 防 DoS | M |
| **存储** | Filecoin CCDB 桥接 | **P1** | 纯合约层，N42 不需改动；用户可直接在 N42 上发起 Filecoin 存储 | S |
| **通讯** | Push 通知合约 | **P2** | 智能合约事件→钱包通知，提升 dApp 用户体验 | S |
| **存储** | IPFS CID 原生支持 | **P2** | 在合约中存储和验证 IPFS 内容哈希 | S |
| **计算** | 可验证计算市场接口 | **P2** | 连接 Boundless/SP1 Prover Network | M |
| **存储** | 原生 Blob 存储扩展 | **P3** | EthStorage 模式：扩展 blob 为长期存储 | L |
| **通讯** | RLN ZK 速率限制 | **P3** | P2P 层 ZK 防 DoS，需链上注册合约 | L |

---

## 三、实施计划

### Phase A：ZK 协处理器原生支持（1-2 周）

**目标**：智能合约可将重计算卸载到链下，链上用 ZK 证明验证结果。

**N42 已有基础**：
- `internal/zkverifier/` — STARK/SNARK/SP1 验证
- `internal/vm/contracts.go` — 预编译合约框架
- BN256 pairing 预编译（地址 0x08）已存在

**需要做的**：
1. 部署标准 Groth16 Verifier 合约模板（~200 LOC Solidity）
2. 添加 `ZKCoprocessorRegistry` 预编译或合约，允许注册可信计算提供者
3. 标准化 `ComputeRequest → Proof → Verify` 接口
4. 文档：如何接入 Boundless / SP1 Prover Network

**关键文件**：
- `internal/vm/contracts.go` — 预编译注册
- `contracts/` — Verifier 合约模板

### Phase B：Waku 消息协议集成（2-3 周）

**目标**：N42 节点可作为 Waku 中继节点，支持去中心化消息传递。

**N42 已有基础**：
- `internal/p2p/` — 完整 libp2p 网络栈
- GossipSub 已用于区块/交易传播

**需要做的**：
1. 引入 `go-waku` 库作为可选依赖
2. 在 `internal/p2p/` 中添加 Waku relay 协议处理器
3. 添加 `waku_*` RPC 命名空间（publish/subscribe/filter）
4. 配置项：`conf/waku_config.go`（Enabled, Shards, RLN）
5. RLN 注册合约部署在 N42 上（ZK 速率限制凭证）

**关键文件**：
- `internal/p2p/` — 协议扩展
- `conf/waku_config.go` — 新配置
- `internal/api/waku_api.go` — RPC 接口

### Phase C：Filecoin CCDB 桥接（1 周）

**目标**：N42 用户可通过智能合约直接发起 Filecoin 存储交易。

**架构**：
```
N42 用户 → OnRamp 合约 (N42) → 中继 Agent → Filecoin SP
                                    ↓
                        Oracle 合约 (N42) ← Prover 合约 (Filecoin)
```

**需要做的**：
1. 部署 Filecoin CCDB 的 OnRamp 合约到 N42
2. 部署 Oracle 合约接收 Filecoin 存储证明
3. 文档：操作指南

**关键**：纯合约层，N42 客户端无需改动。

### Phase D：Push 通知 + IPFS 引用（1 周）

**目标**：dApp 可发送钱包通知；合约可存储和引用 IPFS 内容。

**需要做的**：
1. 部署 Push Protocol 通知合约
2. 提供 IPFS CID 验证库合约（Solidity）
3. 文档和示例

---

## 四、N42 差异化优势

与纯 L1 账本相比，集成这三个方向后 N42 将具备：

| 能力 | 传统 L1 | N42 (扩展后) |
|------|---------|-------------|
| 价值转移 | ✅ | ✅ |
| 智能合约 | ✅ | ✅ |
| 去中心化存储 | ❌ (需外部桥接) | ✅ Filecoin CCDB + IPFS CID + Blob 扩展 |
| 可验证计算 | ❌ | ✅ ZK 协处理器 + SP1/Boundless |
| 去中心化消息 | ❌ | ✅ Waku Relay + RLN |
| dApp 通知 | ❌ | ✅ Push Protocol |
| PQ 安全 | ❌ (除 N42 外无) | ✅ PQ-STARK + Blake3 |
| 实时 ZK 证明 | ❌ | ✅ SP1 Hypercube 后端 |

### 定位升级

```
之前：高性能以太坊兼容 L1（智能分布式账本）
之后：分布式全栈基础设施（存储 + 计算 + 通讯 + 账本）
```

---

## 五、相关资料来源

### 分布式存储
- Filecoin Onchain Cloud — Messari 报告
- Filecoin Cross-Chain Data Bridge (CCDB) — Filecoin 文档
- EthStorage 2025 年报 — PB 级去中心化存储
- Walrus 技术概述 — Nansen 分析
- Celestia Matcha 升级 — 128 MB 区块
- IPFS 250K 节点 + libp2p 12 实现

### 分布式计算
- Akash Homenode — 2026.2 发布
- RISC Zero Boundless — Base 主网 2025.9
- SP1 Hypercube — 10.8s 以太坊区块证明
- ZK 协处理器对比 — Space and Time 报告
- Gensyn SkipPipe — 训练时间降低 55%

### 分布式通讯
- Waku 技术概述 — RLN ZK 速率限制
- XMTP 200 万+ 身份 — MLS 端对端加密
- Push Protocol → Push Chain L1
- Farcaster 架构 — 混合链上/链下设计
- libp2p GossipSub v1.2 规范
