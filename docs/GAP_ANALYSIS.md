# N42 全局功能缺失深度对比分析

> 对比对象：go-ethereum (geth) v1.16+、reth v1.11+、Sei v2/v3、Monad、Grevm 2.1、Aptos
> 分析日期：2026-03-09
> 范围：以太坊及高性能公链客户端全局功能模块

---

## 目录

- [一、状态管理与存储](#一状态管理与存储)
- [二、同步机制](#二同步机制)
- [三、执行层与 EVM](#三执行层与-evm)
- [四、P2P 网络层](#四p2p-网络层)
- [五、共识层与区块构建](#五共识层与区块构建)
- [六、RPC API 完整性](#六rpc-api-完整性)
- [七、交易池](#七交易池)
- [八、开发者工具与 CLI](#八开发者工具与-cli)
- [九、安全与稳定性](#九安全与稳定性)
- [十、MEV 与经济模型](#十mev-与经济模型)
- [十一、可观测性与运维](#十一可观测性与运维)
- [十二、L2/Rollup 与扩展框架](#十二l2rollup-与扩展框架)
- [十三、前沿路线图对齐](#十三前沿路线图对齐)
- [十四、性能工程](#十四性能工程)
- [十五、跨链与互操作性](#十五跨链与互操作性)
- [十六、综合评分与优先级建议](#十六综合评分与优先级建议)

---

## 一、状态管理与存储

### 1.1 各链方案概览

| 功能 | geth | reth | Sei v2/v3 | Monad | Grevm 2.1 | Aptos | **N42** |
|------|------|------|-----------|-------|-----------|-------|---------|
| **MPT 状态树** | ✅ Hex-MPT | ✅ Hex-MPT | ❌ IAVL→SeiDB | ❌ 自研 | N/A (库) | ❌ JMT | ❌ 增量 Keccak |
| **Path-Based Storage (PBSS)** | ✅ v1.13+ 默认 | ✅ flat state | ❌ | ❌ | N/A | N/A | ❌ |
| **Verkle Tree** | 🔧 Fusaka 已含 | 🔧 跟进中 | ❌ | ❌ | N/A | ❌ | ❌ |
| **State Pruning** | ✅ PBSS 在线裁剪 | ✅ 多模式 | ✅ SeiDB | ✅ | N/A | ✅ | ✅ `pruner.go` |
| **Snapshot / Flat State** | ✅ 完整快照层 | ✅ flat state 核心设计 | ✅ SeiDB SS | ✅ MonadDB | N/A | ✅ | ⚠️ 逻辑快照 |
| **Ancient/Freezer DB** | ✅ 5 表冷存储 | ✅ static files | ❌ | ❌ | N/A | ✅ | ✅ P1-8 |
| **State Expiry** | 🔧 2026 路线图 | 🔧 跟进中 | ❌ | ❌ | N/A | ❌ | ❌ |
| **History Expiry** | ✅ eth/69 支持 | ✅ | ❌ | ❌ | N/A | ❌ | ❌ |
| **DB Inspection 工具** | ✅ | ✅ | ❌ | ❌ | N/A | ✅ | ✅ P3-3 |
| **Sparse Trie (内存缓存)** | ❌ | ✅ v1.2+ 核心优化 | ❌ | ❌ | N/A | ❌ | ❌ |

### 1.2 关键差距分析

**Path-Based Storage (PBSS)**：geth v1.13+ 和 reth 均采用路径索引替代哈希索引存储状态节点，实现在线裁剪（不再需要离线 prune）。N42 使用 MDBX 的 key=address 方案本质上类似 flat state，但缺乏等价的在线 trie 裁剪机制。

**Verkle Tree**：以太坊 Fusaka (2025.12.3 主网激活) 硬分叉已部分引入 Verkle Tree，geth v1.16.7 支持。Verkle tree 将 proof 大小从 ~4KB 降至 ~150B，是实现无状态客户端的关键。Glamsterdam (2026 H1) 将完善 Verkle 迁移。N42 使用增量 Keccak 方案，无法生成标准以太坊兼容的 Verkle proof。

**Snapshot Layer**：geth 的 snapshot 层提供 O(1) 状态读取（非遍历 trie），reth 的 flat state 设计从一开始就内建此能力。N42 的 `internal/snapshot/manager.go` 仅提供逻辑快照点（用于裁剪恢复），不是性能加速层。

**Sparse Trie**：reth v1.11 的核心优化，将 state root 计算延迟降低 25-27%，吞吐量提升 33%（700M→1G gas/s）。通过跨 payload 复用内存中的 trie 节点，避免每次重建。

### 1.3 N42 优势

- MDBX 作为底层 KV 存储具有优秀的读写性能（memory-mapped B+tree）
- `lib/kv/layered/` LayeredDB 分层存储（State DB + History DB）是合理的架构选择
- 增量 Keccak 方案比 MPT 简单得多，适合非以太坊兼容场景

---

## 二、同步机制

| 功能 | geth | reth | Sei v2/v3 | Monad | Grevm 2.1 | Aptos | **N42** |
|------|------|------|-----------|-------|-----------|-------|---------|
| **Snap Sync** | ✅ 默认模式 | ✅ | ❌ Cosmos 快照 | ❌ 自研 | N/A | ✅ state sync | ✅ P1 |
| **Full Sync** | ✅ | ✅ | ✅ | ✅ | N/A | ✅ | ✅ |
| **Staged Sync** | ❌ | ✅ 核心创新 | ❌ | ❌ | N/A | ❌ | ❌ |
| **Checkpoint Sync** | ✅ | ✅ | ✅ (Cosmos) | ❌ | N/A | ✅ | ❌ |
| **Backfill Sync** | ❌ | ✅ | ❌ | ❌ | N/A | ❌ | ❌ |
| **Light Client** | ✅ LES | ❌ | ✅ IBC light | ❌ | N/A | ✅ | ❌ |
| **Portal Network** | 🔧 实验 | ❌ | ❌ | ❌ | N/A | ❌ | ❌ |
| **Beam Sync** | 🔧 实验 | ❌ | ❌ | ❌ | N/A | ❌ | ❌ |
| **State Sync (应用层)** | ❌ | ❌ | ✅ Cosmos | ❌ | N/A | ✅ | ❌ |

### 关键差距

**Staged Sync**：reth/Erigon 的核心创新，将同步过程分解为独立的阶段（headers→bodies→senders→execution→hashing→trie→finish），每个阶段可以独立重试、恢复和监控。显著提高了同步的可靠性和可观测性。

**Checkpoint Sync**：从可信检查点快速启动，跳过历史验证。对于新节点加入网络极其重要，可将启动时间从数天缩短到数小时。

---

## 三、执行层与 EVM

### 3.1 并行执行对比

| 功能 | geth | reth | Sei v2/v3 | Monad | Grevm 2.1 | Aptos | **N42** |
|------|------|------|-----------|-------|-----------|-------|---------|
| **并行执行引擎** | ❌ 顺序 | 🔧 prewarming | ✅ 乐观并行 | ✅ 乐观并行 | ✅ hint-DAG | ✅ Block-STM | ✅ Block-STM |
| **并行策略** | - | prefetch+warmup | Block-STM 变体 | 乐观调度 | 预分析 DAG | 原生 Block-STM | Wave Block-STM |
| **状态预取** | ✅ prefetcher | ✅ parallel prewarming | ✅ | ✅ async I/O | N/A | ✅ | ✅ P1-11 |
| **TX 依赖分析 (DAG)** | ❌ | ❌ | ✅ | ❌ 乐观重试 | ✅ 核心特性 | ❌ 乐观重试 | ❌ |
| **Async I/O** | ❌ | ❌ | ❌ | ✅ 核心特性 | ❌ | ❌ | ❌ |
| **JIT/AOT EVM 编译** | ❌ | 🔧 实验 (revmc) | ❌ | ❌ | ❌ | N/A (Move) | ❌ |
| **SIMD 优化** | ❌ | 🔧 实验 | ❌ | ❌ | ❌ | ❌ | ❌ |

### 3.2 EVM 兼容性与 EIP 支持

| EIP/功能 | geth | reth | Sei | Monad | **N42** | 说明 |
|----------|------|------|-----|-------|---------|------|
| **EIP-1153 (TLOAD/TSTORE)** | ✅ | ✅ | ✅ | ✅ | ✅ | 瞬态存储 |
| **EIP-4844 (Blobs)** | ✅ | ✅ | ❌ | ✅ | ✅ | Proto-Danksharding |
| **EIP-5656 (MCOPY)** | ✅ | ✅ | ✅ | ✅ | ✅ | 内存复制 |
| **EIP-7516 (BLOBBASEFEE)** | ✅ | ✅ | ❌ | ✅ | ✅ | Blob 基础费 |
| **EIP-3855 (PUSH0)** | ✅ | ✅ | ✅ | ✅ | ✅ | 零值推入 |
| **EIP-7702 (Pectra AA)** | ✅ | ✅ | ❌ | 🔧 | ✅ | 委托账户代码 |
| **EIP-2537 (BLS12-381)** | ✅ | ✅ | ❌ | 🔧 | ✅ | BLS 预编译 |
| **EOF (EVM Object Format)** | 🔧 Glamsterdam | 🔧 | ❌ | ❌ | ✅ | N42 已提前实现 |
| **EIP-7212 (P-256)** | ✅ Pectra | ✅ | ❌ | 🔧 | ✅ | secp256r1 验证 |
| **ERC-4337 (AA)** | 部分 | 部分 | ❌ | ❌ | ✅ pre-Pectra | 账户抽象 |
| **PeerDAS** | ✅ Fusaka | ✅ | ❌ | ❌ | ❌ | 数据可用性采样 |

### 3.3 关键差距

**Transaction DAG 分析**：Grevm 2.1 的核心创新 — 在执行前通过模拟执行结果（hints）构建交易依赖 DAG，使用 Lock-Free DAG（2.1 新增，调度开销降低 60%）和 Task Groups（强依赖交易归组同线程顺序执行）。性能数据：Uniswap 场景 11.25 gigagas/s，30% hot-ratio 混合场景 2.96 gigagas/s（5.5x 提升），不可并行化场景比 Block-STM 减少 **95% CPU 使用**。N42 的 Block-STM 采用纯乐观方式（execute→validate→retry），在高冲突场景下效率显著低于 DAG 方案。

**Async I/O (MonadDB)**：Monad（2025.11 主网上线）的核心差异化特性 — MonadDB 使用 Linux `io_uring` 内核技术，执行线程发起 I/O 请求不阻塞，可直接在块设备（block device）上运行绕过文件系统。多 VM 实例 + 异步 I/O 允许一个交易等待磁盘加载时继续处理其他交易，实现真正的流水线执行。

**JIT/AOT EVM 编译**：reth 的 `revmc` 将 EVM 字节码编译为本机机器码，JIT 模式计算密集场景最高 6.9x 提升（Fibonacci 19x），AOT 模式预编译热门合约（USDC、WETH）消除预热延迟。LLVM 后端自动向量化（SIMD），目标集成后整体 ~2x EVM 执行提升。

**PeerDAS (Data Availability Sampling)**：Fusaka 硬分叉引入的核心特性，允许验证节点通过采样而非下载完整 blob 数据来验证数据可用性。对 L2 Rollup 生态至关重要。

---

## 四、P2P 网络层

| 功能 | geth | reth | Sei | Monad | Aptos | **N42** |
|------|------|------|-----|-------|-------|---------|
| **协议栈** | DevP2P | DevP2P | libp2p (Tendermint) | 自研 | 自研 | libp2p |
| **eth/68 (TX announce)** | ✅ | ✅ | N/A | N/A | N/A | ✅ |
| **eth/69 (history expiry)** | ✅ v1.16 | ✅ | N/A | N/A | N/A | ❌ |
| **Snap Protocol** | ✅ | ✅ | N/A | N/A | N/A | ✅ P1 |
| **Blob Sidecar P2P** | ✅ | ✅ | N/A | N/A | N/A | ❌ |
| **Witness Protocol** | 🔧 | 🔧 | N/A | N/A | N/A | ❌ |
| **PeerDAS 网络层** | ✅ Fusaka | ✅ | N/A | N/A | N/A | ❌ |
| **Portal Network** | 🔧 | ❌ | N/A | N/A | N/A | ❌ |
| **Gossip 优化** | ✅ | ✅ | ✅ GossipSub | ✅ | ✅ | ✅ |
| **连接管理/门控** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Kademlia DHT** | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ |

### 关键差距

**eth/69 协议**：EIP-7642，增加了 `earliestBlock` / `latestBlock` 字段到 Status 消息，新增 `BlockRangeUpdate` 消息。支持 history expiry — 客户端可在 2025.5 后丢弃 pre-merge 历史数据。N42 使用 libp2p 而非 DevP2P，协议不直接兼容，但概念可借鉴。

**Blob Sidecar P2P**：EIP-4844 的 blob 数据通过独立的 gossip 通道传播。虽然 N42 支持 blob 交易处理，但缺乏 blob sidecar 的 P2P 传播协议。

---

## 五、共识层与区块构建

| 功能 | geth | reth | Sei | Monad | Aptos | **N42** |
|------|------|------|-----|-------|-------|---------|
| **共识引擎** | PoS (Beacon) | PoS (Beacon) | Tendermint/CometBFT | MonadBFT | AptosBFT (Jolteon) | APoA/APoS |
| **Engine API v1-v4** | ✅ 完整 | ✅ 完整 | N/A | N/A | N/A | ⚠️ v4 存在 |
| **Proposer-Builder Separation** | ✅ MEV-Boost | ✅ | ❌ | ❌ | ❌ | ❌ |
| **Slot-based 出块** | ✅ 12s | ✅ 12s | ✅ ~400ms (Giga: sub-400ms) | ✅ 400ms | ✅ ~160ms (Raptr) | ✅ 8s (period) |
| **Finality 速度** | ~15min (2 epoch) | ~15min | ~400ms 即时 | ~800ms | <800ms (Raptr) | 取决于 epoch |
| **Deferred Execution** | ❌ | ❌ | ❌ | ✅ 核心特性 | ✅ | ❌ |
| **流水线共识** | ❌ | ❌ | ✅ Twin-Turbo → Autobahn (Giga) | ✅ 完整流水线 | ✅ Raptr (Prefix Consensus) | ❌ |
| **PQ-STARK 验证** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ 独有 |
| **Withdrawal 处理** | ✅ | ✅ | N/A | N/A | N/A | ❌ deposit 合约 |

### 关键差距

**Deferred Execution（延迟执行）**：Monad 和 Aptos 的核心创新 — 将区块执行与共识解耦。共识先对交易排序达成一致（区块 = 交易排序服务），执行在后台异步进行，执行可利用完整区块时间平滑突发负载。Sei Giga（2026 年上线）也采用此模式，共识仅排序不包含状态变更结果。

**流水线共识（Pipelining）**：Monad 实现了共识 → 执行 → I/O 的完整流水线重叠（Block N 共识 | Block N-1 执行 | Block N-2 提交），Aptos Raptr（2025.6 Baby Raptr 主网上线）使用 Prefix Consensus 将网络跳数从 6 减至 4，延迟降低 20%（100-150ms），目标 250k TPS。Sei Giga 引入 **Autobahn 共识 + 多提议者（Multi-Proposer）** 模型，多个验证者并行出块消除单提议者瓶颈。

**N42 优势**：PQ-STARK 后量子签名验证是 N42 的独有特性，以太坊 2026 路线图才将量子抗性列为核心优先事项。

---

## 六、RPC API 完整性

| API | geth | reth | Sei | Monad | Aptos | **N42** |
|-----|------|------|-----|-------|-------|---------|
| **eth_* 标准** | ✅ 完整 | ✅ 完整 | ✅ 部分 | ✅ | N/A | ✅ 大部分 |
| **eth_getProof** | ✅ MPT proof | ✅ | ✅ | ✅ | N/A | ✅ 增量 Keccak |
| **eth_createAccessList** | ✅ | ✅ | ❌ | ✅ | N/A | ✅ P3-1 |
| **eth_simulateV1** | ✅ | ✅ | ❌ | ❌ | N/A | ✅ |
| **eth_getBlockReceipts** | ✅ | ✅ | ❌ | ✅ | N/A | ✅ |
| **debug_* 命名空间** | ✅ 完整 | ✅ 完整 | 部分 | 部分 | N/A | ✅ |
| **trace_* 命名空间** | ✅ | ✅ (Parity 兼容) | ❌ | ❌ | N/A | ✅ |
| **GraphQL API** | ✅ EIP-1767 | ❌ | ❌ | ❌ | ✅ 自研 | ❌ |
| **Otterscan API** | ❌ | ✅ | ❌ | ❌ | N/A | ❌ |
| **Engine API (完整)** | ✅ v1-v4 | ✅ v1-v4 | N/A | N/A | N/A | ⚠️ v4 |
| **Admin API** | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ P3-2~6 |
| **Bloom Bits 索引** | ✅ | ✅ | ❌ | ❌ | N/A | ❌ |
| **Subscribe (WS)** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Filter API** | ✅ 完整 | ✅ 完整 | 部分 | ✅ | N/A | ✅ |

### 关键差距

**Bloom Bits 索引**：geth 和 reth 都实现了 bloom bits 索引用于加速 `eth_getLogs` 查询。N42 使用 header bloom 过滤但缺乏 bloom bits 的位级索引，大范围历史日志查询性能较差。

**GraphQL API**：EIP-1767 标准，提供更灵活的数据查询能力。geth 原生支持，但非关键缺失。

**Otterscan API**：reth 特有的区块浏览器优化 API，支持高效的地址交易历史查询、内部交易追踪等。

---

## 七、交易池

| 功能 | geth | reth | Sei | Monad | Aptos | **N42** |
|------|------|------|-----|-------|-------|---------|
| **标准 TxPool** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Blob TxPool** | ✅ 独立池 | ✅ | ❌ | ✅ | N/A | ✅ |
| **持久化** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ P0-5 |
| **RBF (Replace-By-Fee)** | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ |
| **Private TxPool** | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ |
| **动态大小调整** | ✅ | ✅ | ❌ | ✅ | ✅ | ❌ |
| **EIP-7702 TX 类型** | ✅ | ✅ | ❌ | 🔧 | N/A | ✅ |
| **Local Priority** | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ |

### 关键差距

**动态大小调整**：根据系统内存压力自动调整交易池容量，防止内存溢出同时最大化池利用率。

**Private TxPool**：Sei 特有的反 MEV/抢跑机制，交易在被包含进区块前不公开。

---

## 八、开发者工具与 CLI

| 工具 | geth | reth | Sei | Monad | Aptos | **N42** |
|------|------|------|-----|-------|-------|---------|
| **Chain Import/Export** | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ P3-2 |
| **State Dump** | ✅ | ❌ | ❌ | ❌ | ❌ | ✅ P3-4 |
| **DB Inspector** | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ P3-3 |
| **JS Console** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **EVM CLI Tool** | ✅ `evm` | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Clef (签名器)** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **abigen** | ✅ | ❌ | ❌ | ❌ | N/A (Move) | ✅ |
| **Chain Rollback** | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ |
| **Genesis Init** | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ |
| **Keystore 管理** | ✅ | ❌ | ❌ | ❌ | ✅ | ✅ |
| **devp2p CLI** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |

---

## 九、安全与稳定性

| 功能 | geth | reth | Sei | Monad | Aptos | **N42** |
|------|------|------|-----|-------|-------|---------|
| **DoS 防护** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **RPC 速率限制** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Gas Cap 保护** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Graceful Shutdown** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Health Check** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Panic Recovery** | ✅ 全面 | ✅ Rust 安全 | ✅ | ✅ | ✅ Move 安全 | ⚠️ 部分 |
| **Fuzzing 测试** | ✅ 大量 | ✅ | ✅ | ❌ | ✅ | ❌ |
| **内存安全** | ⚠️ Go GC | ✅ Rust 所有权 | ⚠️ Go GC | 自研 | ✅ Move 线性类型 | ⚠️ Go GC |
| **PQ 密码学** | ❌ 2026 路线图 | ❌ | ❌ | ❌ | ❌ | ✅ PQ-STARK |

### 关键差距

**Fuzzing 测试**：geth 有大量 fuzz 测试（尤其是 EVM、RLP、ABI 解码器），reth 也集成了 cargo-fuzz。N42 缺乏系统性的模糊测试。

**N42 优势**：PQ-STARK 后量子密码学是重大领先优势。以太坊基金会 2026 路线图将 PQ 列为 "Harden the L1" 核心优先事项，N42 已提前部署。

---

## 十、MEV 与经济模型

| 功能 | geth | reth | Sei | Monad | Aptos | **N42** |
|------|------|------|-----|-------|-------|---------|
| **MEV-Boost 集成** | ✅ | ✅ | ❌ | ❌ | N/A | ❌ |
| **Flashbots Bundle API** | ✅ 插件 | ✅ 插件 | ❌ | ❌ | N/A | ✅ P2-13 |
| **Priority Ordering** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ P2-13 |
| **Bundle Pool** | ✅ | ✅ | ❌ | ❌ | N/A | ✅ P2-13 |
| **PBS (Builder Separation)** | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ |
| **Inclusion List** | 🔧 研究 | 🔧 | ❌ | ❌ | ❌ | ❌ |
| **Block Value 优化** | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ |
| **EIP-1559 动态费率** | ✅ | ✅ | ✅ 变体 | ✅ | ❌ | ✅ |

---

## 十一、可观测性与运维

| 功能 | geth | reth | Sei | Monad | Aptos | **N42** |
|------|------|------|-----|-------|-------|---------|
| **Prometheus Metrics** | ✅ 200+ | ✅ 300+ | ✅ | ✅ | ✅ | ✅ P1-10 |
| **OpenTelemetry** | ❌ | ✅ | ✅ | ❌ | ✅ | ❌ |
| **Grafana Dashboard** | ✅ 官方模板 | ✅ 官方模板 | ✅ | ❌ | ✅ | ❌ |
| **结构化事件日志** | ✅ | ✅ | ✅ | ✅ | ✅ | 部分 |
| **Live Tracing** | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ |
| **pprof 支持** | ✅ | ✅ (tokio-console) | ✅ | ❌ | ❌ | ✅ 6060 端口 |
| **诊断 API** | ✅ | ✅ ExEx | ❌ | ❌ | ✅ | ✅ P3-6 |

### 关键差距

**Grafana Dashboard 模板**：geth 和 reth 都提供开箱即用的 Grafana 面板配置。N42 有 Prometheus 指标但缺乏预配置面板，运维人员需要自行搭建。

**OpenTelemetry**：分布式追踪标准，reth、Sei、Aptos 均已集成。对于多节点部署的问题诊断至关重要。

---

## 十二、L2/Rollup 与扩展框架

| 功能 | geth | reth | Sei | Monad | Aptos | **N42** |
|------|------|------|-----|-------|-------|---------|
| **ExEx (执行扩展)** | 🔧 PR#30611 | ✅ 核心特性 | ❌ | ❌ | ❌ | ❌ |
| **OP Stack 支持** | ✅ op-geth | ✅ op-reth | ❌ | ❌ | ❌ | ❌ |
| **Sequencer 模式** | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ |
| **Fraud Proof** | 部分 | 部分 | ✅ | ❌ | ❌ | ❌ |
| **SDK/库化使用** | ✅ Go 包 | ✅ Rust crate | ✅ Cosmos SDK | ❌ | ✅ Move SDK | ⚠️ 部分 |
| **插件系统** | 🔧 ExEx | ✅ ExEx | ✅ ABCI | ❌ | ❌ | ❌ |
| **Cosmos IBC** | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ |

### 关键差距

**ExEx (Execution Extensions)**：reth 的核心创新之一 — 后执行钩子（post-execution hooks），支持实时构建 rollup、indexer、MEV bot 等，代码量减少 10x+。特性包括：异步 future 支持、reorg 感知流、替代 VM 集成。geth PR#30611 (by karalabe) 正在移植此概念（因 Go 无动态加载，需编译进二进制）。

**OP Stack 生态转向**：**Optimism 将于 2026 年 5 月 31 日停止支持 op-geth**，全面转向 op-reth。新功能（如 Karst 硬分叉）仅在 op-reth 开发。Superchain（34 条 OP Chain）正在迁移到 Reth 架构。这标志着 Rust 实现在 L2 生态中的主导地位。

**SDK/库化使用**：reth 设计之初就考虑了作为 Rust 库使用 — 每个组件都是独立 crate，开发者可单独引入 P2P 网络栈、直接操作数据库、拆解节点为所需组件。N42 的模块化程度有限，`internal/` 包不可外部导入。

---

## 十三、前沿路线图对齐

### 13.1 以太坊 2025-2026 升级路线

| 升级 | 时间 | 关键 EIP | N42 状态 |
|------|------|----------|----------|
| **Pectra** | 2025.5.7 | 7702(AA), 2537(BLS), 6110(deposits), 7623(calldata cost) | ✅ 大部分已实现 |
| **Fusaka** | 2025.12.3 | PeerDAS(7594), Verkle Tree, 7825(tx gas limit 16.78M), Gas↑150M | ❌ 缺失 PeerDAS/Verkle |
| BPO1/BPO2 | 2025.12.9 / 2026.1.7 | Blob 参数调整（target 3→6, max 6→9） | ❌ |
| **Glamsterdam** | 2026 H1 | EOF(7692) 完整版, 更快出块(6s), MEV 改革 | ✅ EOF 已提前实现 |
| **Hegotá** | 2026 H2 | State expiry, PQ 密码学, 进一步扩容 | ✅ PQ 已有，State expiry ❌ |

### 13.2 高性能链趋势

| 趋势 | Monad | Sei v3/Giga | Aptos | Grevm 2.1 | **N42** |
|------|-------|-------------|-------|-----------|---------|
| **TPS 目标** | 10,000 | 200,000 (Giga 5 gigagas/s) | 250,000 (Raptr) | 100,000+ (目标) | ❌ 未测 |
| **亚秒级 Finality** | ✅ ~800ms | ✅ ~400ms 即时 | ✅ <800ms (Raptr) | N/A | ❌ 8s period |
| **延迟/异步执行** | ✅ 核心 | ✅ Giga 采用 | ✅ | ❌ | ❌ |
| **自定义数据库** | ✅ MonadDB (io_uring) | ✅ SeiDB (SS+SC) | ✅ AptosDB (JMT) | ❌ | ❌ MDBX |
| **多提议者** | ❌ | ✅ Giga Autobahn | ❌ | N/A | ❌ |
| **Move VM** | ❌ | ❌ | ✅ | ❌ | ❌ |

---

## 十四、性能工程

| 优化维度 | geth | reth | Sei | Monad | Aptos | **N42** |
|----------|------|------|-----|-------|-------|---------|
| **并行执行** | ❌ | 🔧 prewarming | ✅ | ✅ | ✅ | ✅ Block-STM |
| **状态预取** | ✅ | ✅ parallel prewarming | ✅ | ✅ async | ✅ | ✅ |
| **内存池化** | ✅ sync.Pool | ✅ arena alloc | ❌ | ✅ | ✅ | ✅ pool.go |
| **零拷贝序列化** | ❌ | ✅ rkyv 实验 | ❌ | ✅ | ✅ | ❌ |
| **NUMA 感知** | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ |
| **IO_uring** | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ |
| **Sparse Trie 缓存** | ❌ | ✅ 核心 | ❌ | N/A | ❌ | ❌ |
| **批量 DB 写入** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **ShardedCache** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ LayeredDB |

### 关键差距

**零拷贝序列化**：避免序列化/反序列化时的内存复制开销。reth 实验性使用 `rkyv`，Monad 和 Aptos 在内部数据结构中广泛使用。

**IO_uring / Async I/O**：Monad 使用 Linux 的 io_uring 接口实现高性能异步磁盘 I/O，是其 10,000 TPS 目标的关键支撑。

**N42 优势**：`lib/kv/layered/ShardedCache` 提供了分片缓存加速，与 reth 的 parallel prewarming 理念一致。

---

## 十五、跨链与互操作性

| 功能 | geth | reth | Sei | Monad | Aptos | **N42** |
|------|------|------|-----|-------|-------|---------|
| **IBC 跨链** | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ |
| **桥接标准** | ❌ | ❌ | ✅ | ❌ | ✅ LayerZero | ❌ |
| **跨链消息传递** | ❌ | ❌ | ✅ | ❌ | ✅ | ❌ |
| **EIP-3668 (CCIP-Read)** | ✅ | ✅ | ❌ | ❌ | N/A | ❌ |
| **Chain Abstraction** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |

---

## 十六、综合评分与优先级建议

### 16.1 功能覆盖率评分（满分 100）

| 维度 | 权重 | geth | reth | Sei | Monad | Aptos | **N42** |
|------|------|------|------|-----|-------|-------|---------|
| 状态管理 | 15% | 95 | 98 | 80 | 90 | 85 | 60 |
| 同步机制 | 10% | 90 | 95 | 75 | 70 | 80 | 65 |
| 执行层/EVM | 20% | 85 | 88 | 90 | 95 | 90* | 80 |
| P2P 网络 | 10% | 95 | 90 | 80 | 80 | 75 | 70 |
| 共识 | 10% | 90 | 90 | 85 | 95 | 90 | 75 |
| RPC API | 10% | 95 | 95 | 60 | 70 | 60 | 80 |
| 交易池 | 5% | 90 | 90 | 85 | 85 | 70 | 85 |
| 工具链 | 5% | 95 | 70 | 50 | 30 | 60 | 75 |
| 安全性 | 5% | 90 | 95 | 85 | 80 | 90 | 80 |
| 可观测性 | 5% | 90 | 95 | 85 | 60 | 85 | 55 |
| 扩展性 | 5% | 80 | 95 | 85 | 40 | 70 | 30 |
| **加权总分** | 100% | **91** | **93** | **80** | **81** | **81** | **71** |

> *Aptos 使用 Move VM，非直接可比

### 16.2 按紧迫度分层的缺失功能

#### P0 — 生产环境必须补齐

| # | 缺失功能 | 影响 | 参考实现 | 预估工作量 |
|---|----------|------|----------|-----------|
| 1 | **Snapshot 加速层** | 状态读取性能差 10-50x | geth snapshot, reth flat state | 2-3 周 |
| 2 | **Bloom Bits 索引** | eth_getLogs 大范围查询极慢 | geth bloombits | 1 周 |
| 3 | **Panic Recovery 全覆盖** | 单个 panic 可导致节点崩溃 | geth/reth 全路径 recover | 3 天 |
| 4 | **Fuzzing 测试基础设施** | 缺乏对抗性测试 | go-fuzz, geth fuzz tests | 1 周 |

#### P1 — 竞争力关键

| # | 缺失功能 | 影响 | 参考实现 | 预估工作量 |
|---|----------|------|----------|-----------|
| 5 | **TX DAG 分析** | 并行执行效率不如 Grevm/Sei | Grevm hint-DAG, Sei dependency | 2-3 周 |
| 6 | **Checkpoint Sync** | 新节点启动慢 | geth/reth checkpoint | 1-2 周 |
| 7 | **Grafana Dashboard 模板** | 运维无开箱即用监控 | reth dashboards | 3 天 |
| 8 | **OpenTelemetry 集成** | 多节点问题诊断困难 | reth tracing crate | 1 周 |
| 9 | **动态 TxPool 大小** | 内存压力下可能 OOM | geth dynamic pool | 3 天 |
| 10 | **Blob Sidecar P2P** | 4844 生态不完整 | geth/reth blob gossip | 1-2 周 |

#### P2 — 差异化竞争

| # | 缺失功能 | 影响 | 参考实现 | 预估工作量 |
|---|----------|------|----------|-----------|
| 11 | **ExEx 执行扩展框架** | 无法支持索引器/分析插件 | reth ExEx | 3-4 周 |
| 12 | **Verkle Tree** | 无法生成以太坊兼容证明 | geth verkle branch | 4-6 周 |
| 13 | **Deferred Execution** | 吞吐量天花板低于 Monad/Aptos | Monad pipeline | 4-6 周 |
| 14 | **PeerDAS** | 不支持数据可用性采样 | geth Fusaka | 3-4 周 |
| 15 | **零拷贝序列化** | 序列化开销较大 | reth rkyv, Aptos BCS | 1-2 周 |

#### P3 — 前瞻布局

| # | 缺失功能 | 影响 | 参考实现 | 预估工作量 |
|---|----------|------|----------|-----------|
| 16 | **State Expiry** | 状态膨胀长期无解 | 以太坊 Hegotá | 研究阶段 |
| 17 | **History Expiry (eth/69)** | 无法裁剪历史数据 | geth v1.16 | 2 周 |
| 18 | **Async I/O (io_uring)** | I/O 密集场景性能受限 | Monad | 4-6 周 |
| 19 | **JIT/AOT EVM** | 热合约执行慢 | reth revmc | 研究阶段 |
| 20 | **Portal Network** | 无轻客户端去中心化支持 | geth portal | 研究阶段 |

### 16.3 N42 独有优势（需保持/强化）

| 优势 | 说明 | 竞争对手状态 |
|------|------|-------------|
| **PQ-STARK 后量子签名** | 已集成到 APoS 共识 | 以太坊 2026 才开始研究 |
| **Block-STM 并行 EVM** | Wave executor 完整实现 | geth 无，reth 仅 prewarming |
| **EOF 提前实现** | EIP-3540/3670/4200/4750/5450 完整 | geth/reth 计划 Glamsterdam |
| **Pectra EIP 完整支持** | 7702/2537/6110/7069/7742 | 与 geth/reth 同步 |
| **LayeredDB 分层存储** | State DB + History DB 分离 | 类似 reth 架构理念 |
| **MDBX 高性能存储** | memory-mapped B+tree | 与 reth 相同选择 |

---

## 附录 A：各链架构速览

### Geth (go-ethereum) v1.16+
- **语言**：Go
- **数据库**：Pebble（v1.14+ 默认，取代 LevelDB）+ Freezer 冷存储
- **状态**：MPT → PBSS (v1.13+ 自动在线裁剪) → Verkle (Fusaka 部分引入)
- **同步**：Snap Sync 默认（动态快照 7min 遍历所有账户），LES 已废弃
- **P2P**：DevP2P, eth/68 + eth/69 (history expiry), snap protocol
- **亮点**：最成熟的以太坊客户端，v1.16.7 Fusaka 主网（Verkle + PeerDAS + Gas↑150M），ExEx PR 进行中，Live Tracing，GraphQL API
- **路线图**：Glamsterdam (2026H1, EOF), Hegotá (2026H2), 每年两次硬分叉新节奏

### Reth v1.11+
- **语言**：Rust
- **数据库**：MDBX + Static Files（v1.11 新增 2 表，+30GB 改善 reorg 性能）
- **状态**：Flat State + Sparse Trie 缓存（跨 payload 复用，state root 延迟↓25%）
- **同步**：Staged Sync（Headers→Bodies→Execution→Hashing→Merkle→History→Pruning）
- **性能**：1G gas/s 吞吐，newPayload 均值 32.4ms，revmc JIT 热合约 6.9x 加速
- **亮点**：ExEx 成熟框架、模块化库设计（每个 crate 独立）、Grevm/PEVM 兼容、**Optimism 官方选择**（2026.5 停止 op-geth）
- **生态**：Gravity Reth (ERC20 ~41k TPS), PEVM (RISE Chain), BSC Reth

### Sei v2/v3/Giga
- **语言**：Go (Cosmos SDK)
- **数据库**：SeiDB — State Store (Log-structured KV) + State Commitment (mmap IAVL, ~100ns 访问)
- **状态**：IAVL Tree → SeiDB SS+SC 分离，WAL + 周期性快照，崩溃快速恢复
- **共识**：Twin-Turbo CometBFT → **Autobahn (Giga)** 多提议者模型
- **并行**：乐观并行化 — worker goroutine + CacheMultiStore + 冲突检测/串行重执行
- **性能**：v2: 100 megagas/s，**Giga (2026): 5 gigagas/s, 200k+ TPS, 50x 吞吐提升**
- **亮点**：EVM + CosmWasm 双引擎、IBC 跨链、即时 Finality、Giga 延迟执行 + 多提议者

### Monad (2025.11 主网上线)
- **语言**：C++ / Rust
- **数据库**：MonadDB — 持久化 Patricia Trie + `io_uring` + 块设备直连（绕过文件系统）
- **状态**：多版本状态数据，内联压缩，单写者 + 多读者
- **共识**：MonadBFT (HotStuff 变体), 400ms 区块, ~800ms finality
- **并行**：乐观并行 + 多 VM 实例 + 延迟执行 + 完整流水线（共识|执行|I/O 三阶段重叠）
- **性能**：10,000 TPS，字节码级 100% EVM 兼容
- **亮点**：完整流水线架构、异步 I/O（io_uring）、延迟执行解耦共识与执行、MONAD_NINE 升级 (2026 初)

### Grevm 2.1 (Gravity/Galxe)
- **语言**：Rust (基于 revm)
- **类型**：EVM 执行库（非完整节点），可嵌入 reth 等节点
- **核心三模块**：Dependency Manager (DAG) + Execution Scheduler + Parallel State Storage
- **2.1 新增**：Lock-Free DAG（调度开销↓60%）、Task Groups（强依赖交易归组）、Parallel State Store（异步打包重叠 30-60ms）
- **性能**：Uniswap 11.25 gigagas/s，30% hot-ratio 2.96 gigagas/s (5.5x↑)，高冲突 95% less CPU vs Block-STM
- **部署**：Gravity 主网 2026 中目标 100k+ TPS，Gravity Reth (reth fork) 集成

### Aptos
- **语言**：Rust
- **数据库**：AptosDB (RocksDB) + Jellyfish Merkle Tree (稀疏 Merkle 变体，利于分片)
- **共识演进**：AptosBFT → Jolteon (Quorum Store) → **Raptr** (Prefix Consensus, 网络跳数 6→4)
- **VM**：Move VM — 线性类型系统，资源导向（资产不可复制/丢失），字节码验证器
- **并行**：Block-STM 原创（低冲突 32 线程 16x 加速，高冲突 8x），Block-STM V2 开发中
- **Gas 模型**：三维分离 — Execution Gas + I/O Gas (浮动) + Storage Gas (固定 APT 绝对值)
- **性能**：Baby Raptr (2025.6 主网) 延迟↓20%，**Raptr 目标 250k TPS, <800ms 延迟**
- **亮点**：Block-STM 原创者、Aave 首个非 EVM 部署 (2025.8)、Velociraptr 规划中

---

## 附录 B：文档整合说明

本文档整合并替代了原 `开发日志.md` 中的 "功能缺失分析" 章节（十三节对比表），新增以下维度：

1. **对比范围扩展**：新增 Monad、Grevm 2.1、Aptos 三个高性能链对比
2. **以太坊路线图对齐**：增加 Fusaka/Glamsterdam/Hegotá 路线图跟踪
3. **性能工程维度**：新增零拷贝序列化、io_uring、NUMA 感知等底层优化对比
4. **扩展框架维度**：新增 ExEx、SDK 化、插件系统对比
5. **综合评分体系**：提供量化评估和分层优先级建议
6. **前沿技术跟踪**：State Expiry、PeerDAS、JIT EVM 等前瞻性布局

相关文档：
- `docs/PERFORMANCE_OPTIMIZATION_PLAN.md` — 性能优化实施细节
- `docs/POST_QUANTUM_UPGRADE_PLAN.md` — PQ 密码学升级路线
- `docs/ETH_EL_TEST_PLAN.md` — 以太坊执行层测试计划
- `docs/SECURITY_AUDIT_REPORT.md` — 安全审计报告
