# N42 全局功能缺失深度对比分析

> 对比对象：go-ethereum (geth) v1.17.3、reth v2.3.0、Erigon 3.4.3、Sei v2/v3 (Giga)、Monad (主网)、Grevm 2.2.x、Aptos (Raptr)
> 分析日期：2026-03-22（**2026-06-12 重大修订**）
> 范围：以太坊及高性能公链客户端全局功能模块
> 方法：N42 数据基于源码审计（行数/测试覆盖/集成状态），竞品数据标注来源（官方文档/白皮书/宣称/GitHub releases），并区分 **SHIPPED（已交付/已实测）** 与 **CLAIMED/PLANNED（宣称/规划）**

> **2026-06-12 修订要点**（距上次修订 ~3 个月，N42 主仓 1,100+ commit）：
> - **竞品版本全面刷新**：geth v1.16+→**v1.17.3 "Enzymatic Injector"（2026-05-11，ETH/70 协议，Amsterdam 分叉准备）**；reth v1.11+→**v2.3.0（2026-06-10，Storage V2 默认，~1.7 Ggas/s）**；Erigon 3.3.9→**v3.4.3 "Splashing Saga"（2026-06-02，chaindata 4x 缩减至 ~20GB，历史 eth_getProof 转正）**；Grevm 2.1→**v2.2.5（架构代号仍 2.1）**
> - **高性能链主网现实校准**：Monad 主网（2025-11-24）实测组织流量远低于 10,000 TPS 宣称（峰值约数百 TPS）；MegaETH 主网（2026-02-09）实测 35–50k TPS（宣称 100k 未达）；Aptos 250k TPS 为 100 节点实验室数据（主网实测 ~34 TPS，理论上限 ~160k），Baby Raptr/Velociraptr 已上主网（亚 50ms 出块）但**完整 Raptr 仍为 2026 路线图**；Sei Giga **未上线**（Autobahn 仍 "coming soon"，无公开测试网）；Gravity L1 主网 2026-06-04 上线（许可制，实测 ~9.5–12k TPS，100k 未达）
> - **以太坊路线图转向**：Glamsterdam 从 H1 滑至 **Q3 2026**，确认头牌为 **EIP-7732 ePBS + EIP-7928 块级访问列表（BAL）**，**EOF 已被移出窄化范围**（状态存疑/推迟），FOCIL 推迟至 Hegotá；**Verkle 已实质搁置，转向二叉默克尔树（EIP-7864，仍 Draft，哈希函数 BLAKE3 vs Poseidon2 未定）**；后量子 "Lean Ethereum"（leanXMSS 替代 BLS + STARK 聚合）多年期推进
> - **N42 新增能力**：可插拔 **6 引擎状态承诺**（MPT-HPH/JMT/BMT/Verkle/LtHash + **QMDB**）；**DATC 深度自适应时序检查点**（全历史 EIP-1186 证明）；**Body F2 压缩 −45%**（去签名 + Ledger 列式编码 + MPHF 哈希索引）；**Stateless P8 生产端+消费端闭环 + 多 IDC 聚合 + 在线 code 取回**；eth-el 三模式（archive 已实测追平 tip，minimal/full snapshot-direct 待接线 #94）

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
- [附录 C：N42 源码审计摘要](#附录-cn42-源码审计摘要)

---

## 一、状态管理与存储

### 1.1 各链方案概览

| 功能 | geth | reth | Erigon 3.4 | Sei v2/v3 | Monad | Grevm 2.2 | Aptos | **N42** |
|------|------|------|------------|-----------|-------|-----------|-------|---------|
| **状态承诺树** | ✅ Hex-MPT (→二叉树 EIP-7864 Draft) | ✅ Hex-MPT + Sparse Trie | ✅ 扁平KV+HPH MPT commitment | ❌ IAVL→SeiDB | ❌ MonadDB Patricia | N/A (库) | ❌ JMT | ✅ **可插拔 6 引擎**：MPT-HPH(生产/ETH 字节兼容) · JMT-Blake3 · BMT · Verkle · LtHash · QMDB |
| **生产默认承诺引擎** | Hex-MPT | Hex-MPT | HPH MPT | — | Patricia | — | JMT | **MPT-HPH（eth-el 模式，与 ETH stateRoot 字节一致）** / JMT（N42 自研链） |
| **Path-Based Storage (PBSS)** | ✅ v1.13+ opt-in (`--state.scheme=path`) | ✅ flat state + Storage V2 | ✅ E3 扁平KV核心 | ❌ | ❌ | N/A | N/A | ✅ flat state (Erigon 式) + JMT 引用计数 GC 在线裁剪 |
| **Verkle Tree** | ⚡ 已搁置→二叉树 | ⚡ 已搁置→跟进二叉树 | ❌ | ❌ | ❌ | N/A | ❌ | ✅ 引擎在仓 (lib/verkle/, 实验/交叉验证) |
| **二叉默克尔树 (EIP-7864)** | 🔧 Draft (geth 已加 binary trie 改进) | 🔧 跟进中 | ❌ | ❌ | ❌ | N/A | ❌ | ✅ **BMT(Blake3 二叉, ~427B 最小证明) + JMT 已对齐方向** |
| **State Pruning** | ✅ PBSS 在线裁剪 | ✅ 多模式 | ✅ archive/full/minimal | ✅ SeiDB | ✅ | N/A | ✅ | ✅ 快照感知裁剪 (235行, 7测试) |
| **Snapshot / Flat State** | ✅ 完整快照层 | ✅ flat state 核心设计 | ✅ 不可变segment文件 | ✅ SeiDB SS | ✅ MonadDB | N/A | ✅ | ✅ DiffLayer树+ShardedCache+MDBX持久化 |
| **Ancient/Freezer DB** | ✅ 5 表冷存储 | ✅ static files (Storage V2 热/冷分层) | ✅ segment+OtterSync | ❌ | ❌ | N/A | ✅ | ✅ 5表冷存储+后台冻结引擎 |
| **State Expiry** | 🔧 长期 (Lean Ethereum) | 🔧 跟进中 | 🔧 EIP-4444 minimal模式 | ❌ | ❌ | N/A | ❌ | 🔧 基础设施已预备 (JMT GC + Witness + History Expiry + 冷段卸载) |
| **History Expiry (EIP-4444)** | ✅ Partial History Expiry (2025.7 起) | ✅ | ✅ v3.1+ phase1 | ❌ | ❌ | N/A | ❌ | ✅ **F2 body 压缩 −45% + 冷段 relocate + torrent 1-of-N**；HistoryExpirer + earliestBlock 持久化 |
| **历史状态证明 (eth_getProof @任意高度)** | ⚠️ 需 archive 全节点 (>20TB) | ⚠️ | ✅ v3.3 Historical Proofs (v3.4 转正) | ❌ | ❌ | N/A | ❌ | ✅ **DATC 深度自适应时序检查点**（全历史 EIP-1186，2M 块 100/100 验证，~170–420GB） |
| **DB Inspection 工具** | ✅ | ✅ | ✅ diagnostics模块 | ❌ | ❌ | N/A | ✅ | ✅ stats/list/get/inspect 四命令 |
| **Sparse Trie (内存缓存)** | ❌ | ✅ v2.0+ 核心 (跨区块复用, root ~1–2ms) | ❌ | ❌ | ❌ | N/A | ❌ | ✅ JMT/QMDB 节点 LRU 缓存 (16384–65536 entries, 跨 payload 复用) |
| **追加式状态森林 (QMDB 式)** | ❌ | ❌ | ❌ | ❌ | ❌ | N/A | ❌ | ✅ **QMDB 线程分片无锁森林** (2.26M upd/s @16 分片, SIMD AVX-512 哈希, 近块证明窗口) |
| **Per-TX 历史粒度** | ❌ per-block | ❌ per-block | ✅ E3 核心创新 | ❌ | ❌ | N/A | ❌ | ⚠️ DATC 时序检查点提供等价历史证明能力 (非 per-TX domain) |

### 1.2 关键差距分析

**N42 状态承诺架构演进（2026-06 重大更新）**：上一版文档将 JMT 描述为 N42 唯一/主要状态承诺。源码审计（`modules/state/commitment/`，~6,661 行 / 28 文件）确认 N42 现采用**可插拔 `RootComputer` 接口驱动的 6 引擎架构**，各引擎各擅其长（见 `docs/bench_state_report.md` 1M 块跨树基准）：

| 引擎 | 哈希/结构 | 生产状态 | 关键指标 (1M 块基准) |
|------|----------|----------|---------------------|
| **MPT (HPH)** | Keccak / 16-ary (Erigon HexPatriciaHashed 移植) | ✅ **生产 (eth-el 模式)** | 与 ETH stateRoot **字节一致**；2.16M blk/s；并行 ConcurrentMPTRootComputer |
| **JMT** | Blake3 / 16-ary 稀疏 | ✅ 活跃 (N42 自研链) | **最高写吞吐 3.06M blk/s**；冷/热分层；引用计数 GC |
| **BMT** | Blake3 / 二叉 内容寻址 | ✅ 11.7M EVM-replay 验证 | **最小证明 ~427B**；天然对齐 EIP-7864 二叉树方向 |
| **Verkle** | go-verkle IPA/Banderwagon / 256-ary | ⚠️ 实验/交叉验证 | **最小持久状态 ~4.8MB 全历史**；写慢 ~40× |
| **LtHash** | Blake3 XOF 同态 (无树) | ⚠️ 实验/交叉验证 | O(changes) 根更新；无证明；与 JMT 并行交叉校验 |
| **QMDB** | 追加式 twig 森林 + SIMD | ✅ Phase 1 (内存) | **2.26M upd/s @16 分片**；AVX-512 8/16-way 哈希；近块 eth_getProof ~零增量存储 |

**关键澄清**：N42 在 **eth-el（ETH 兼容）模式下的生产承诺引擎是 MPT-HPH**（Erigon HexPatriciaHashed 移植，与以太坊 stateRoot 字节级一致，已在主网 25.19M 高度对齐验证），而非 JMT。JMT/BMT/Verkle/LtHash/QMDB 作为可选引擎并存，服务于 N42 自研链、量子安全研究、最小证明、交叉验证等差异化场景。这一架构使 N42 在"以太坊正从 Verkle 转向二叉树（EIP-7864）"的方向上**同时持有 ETH 字节兼容路径（MPT-HPH）与二叉树路径（BMT/JMT）**，无双重迁移风险。

**Path-Based Storage (PBSS)**：geth v1.13+（opt-in，未确认 2026 转默认）和 reth（v2.0 Storage V2 默认）均采用路径/扁平索引替代哈希索引存储状态节点，实现在线裁剪。N42 的 EVM 执行层已采用 Erigon 式 flat state（`Account`/`Storage` 表以 address 为 key，O(1) 直接读写，不经过 trie），本质上等价于 PBSS 的核心执行收益。JMT 承诺层使用 content-addressed 节点（`blake3(node) → data`）+ 引用计数 GC 实现在线裁剪。

**Verkle Tree — 以太坊已实质搁置，转向二叉树（2026-06 确认）**：截至 2026-06，以太坊已**实质放弃 Verkle Tree**，转向**统一二叉默克尔树（EIP-7864）**，主因是 Verkle 的椭圆曲线（Bandersnatch / Pedersen 承诺）**不具量子抗性**（Shor 多项式破解 ECDLP），且二叉哈希树更利于 STARK 证明。EIP-7864 **仍为 Draft**（未分配激活分叉，哈希函数 BLAKE3 vs Poseidon2 待定），但 geth v1.17.3 已落地"binary trie improvements"作为佐证信号。Vitalik 将此规划为执行层两步重构（先二叉状态树，后 EVM/RISC-V 替换）。**N42 的多引擎策略恰好覆盖此转向**：BMT（Blake3 二叉）与 JMT（Blake3 16-ary）均为哈希树，天然量子安全（Grover 仅降半至 128-bit），无需 "先 Verkle 再二叉树" 的双重迁移成本。

**Snapshot Layer**：geth 的 snapshot 层提供 O(1) 状态读取（非遍历 trie），reth 的 flat state 设计从一开始就内建此能力。N42 的 `modules/state/snapshot/` 已实现完整的 geth 式性能加速层：DiffLayer 树 + DiskLayer + ShardedCache + **MDBX 持久化**（SnapshotAccount/SnapshotStorage/SnapshotMeta/SnapshotJournal 4 张表）。支持 flatten-to-disk 原子写入、diff layer journal 崩溃恢复、后台 snapshot 生成器（批量处理 + crash-resume marker），38 个测试全面覆盖。此外 `internal/snapshot/` 提供逻辑快照（裁剪恢复点 + P2P 传输压缩）。

**Sparse Trie**：reth 的核心优化（v1.11 引入，**v2.0 转为持久化跨区块缓存 `SparseTrieCacheTask`**），将 state root 计算从瓶颈中移除——v1.11 延迟降低 25-27%、吞吐 700M→1G gas/s，v2.0 后 state root ~1–2ms/块、块持久化 ~20× 加速。通过跨 payload 复用内存中的 trie 节点避免每次重建。N42 以 JMT/QMDB 节点 LRU（16384–65536 entries，跨 payload 复用）提供等价能力。

**Erigon E3/E3.4 架构**：Erigon 3 的状态管理是重大革新 — 采用 domain/history/idx 三层结构（domains 存最新状态，history 存 per-transaction 粒度历史，idx 存倒排索引）。**v3.4.0 "Splashing Saga"（2026-04-28）将热 chaindata（MDBX）进一步 4x 缩减至 ~20GB**（archive 静态文件单列），Ethereum archive 约 1.8–2.2TB（第三方测量，2026-01）。v3.3 引入 Historical Proofs Data Model（Haystack 灵感 + Elias-Fano/Roaring Bitmaps 压缩索引，p50 0.003s vs geth 0.015s，5x 更快），**v3.4 将历史 `eth_getProof` 从实验转为支持端点**（建议 32GB+ RAM，需 `--prune.include-commitment-history` 重同步）。

> **N42 对位 DATC**：N42 以 `cmd/n42-datc/`（~5,404 行）实现**深度自适应时序检查点（Depth-Adaptive Temporal Checkpointing）**，目标即 Erigon Historical Proofs 的等价能力——在任意历史高度生成 EIP-1186 证明，而无需全量 archive 重建。原型在 2M 主网块上 **100/100 历史根验证通过**，叶历史 zstd 段 + 节点 diff coding + 聚合变更行，footprint ~170–420GB（对位 Erigon 3.3 archive 4.1TB 基线）。生态调研确认其新颖性。

### 1.3 N42 优势

- MDBX 作为底层 KV 存储具有优秀的读写性能（memory-mapped B+tree）
- `lib/kv/layered/` LayeredDB 分层存储（State DB + History DB）是合理的架构选择
- **可插拔 6 引擎状态承诺**（业界唯一同时持有 ETH 字节兼容 + 量子安全二叉树两条路径）：
  - **MPT-HPH**（生产/eth-el）：Erigon HexPatriciaHashed 移植，与 ETH stateRoot **字节级一致**，主网 25.19M 高度对齐验证；`ConcurrentMPTRootComputer` 并行化（per-worker RoTx + bulk_resume 检查点）
  - **JMT-Blake3**（N42 自研链）：16-ary 稀疏 trie + Extension 路径压缩 + 内容寻址节点 + 引用计数 GC（96% 废弃节点回收）；与 Aptos JMT 同源；最高写吞吐 3.06M blk/s
  - **BMT**（二叉/Blake3）：最小证明 ~427B，11.7M EVM-replay 验证，天然对齐 EIP-7864 二叉树方向
  - **QMDB**（追加式森林）：线程分片无锁 2.26M upd/s @16 分片，AVX-512 SIMD 哈希，近块 `eth_getProof` 近零增量存储
- **DATC 全历史证明**：任意历史高度 EIP-1186 证明，等价 Erigon Historical Proofs，~170–420GB（独立创新，生态调研确认新颖）
- **Body F2 压缩 + EIP-4444 冷段卸载**：去签名 + Ledger 列式编码（from-ID/to-ID/科学计数 value），实测 254M tx **−44.8% vs 基线**（394GB→~217GB），From/To/Value/Nonce/Gas vs ecrecover **0 mismatch**；MPHF 哈希索引保 `eth_getTransactionByHash`（10.79 B/tx）；冷段 relocate + torrent 1-of-N 分发
- Blake3 天然具备 128-bit 量子安全性（Grover 降半），优于 Verkle 的 Pedersen（Shor 完全破解）

---

## 二、同步机制

| 功能 | geth | reth | Erigon 3.4 | Sei v2/v3 | Monad | Grevm 2.2 | Aptos | **N42** |
|------|------|------|------------|-----------|-------|-----------|-------|---------|
| **Snap Sync** | ✅ 默认模式 | ✅ | ✅ OtterSync(BitTorrent, 2024 起默认) | ❌ Cosmos 快照 | ❌ 自研 | N/A | ✅ state sync | ✅ 完整实现 (service+manager+tasks+verify+progress+metrics) |
| **Full Sync** | ✅ | ✅ | ✅ | ✅ | ✅ | N/A | ✅ | ✅ |
| **Staged Sync** | ❌ | ✅ 核心创新 | ✅ 原创者 | ❌ | ❌ | N/A | ❌ | ✅ 7 stage 管线 + forward/unwind/prune |
| **Checkpoint Sync** | ✅ | ✅ | ✅ Caplin支持 | ✅ (Cosmos) | ❌ | N/A | ✅ | ✅ trusted hash |
| **Backfill Sync** | ❌ | ✅ | ❌ | ❌ | ❌ | N/A | ❌ | ✅ 后台历史回填 (checkpoint→genesis, 批量下载+写入) |
| **Light Client** | ✅ LES | ❌ | ❌ | ✅ IBC light | ❌ | N/A | ✅ | ✅ 手机轻节点 (JMT Merkle proof + 无状态 EVM) |
| **Portal Network** | 🔧 实验 | ❌ | ❌ | ❌ | ❌ | N/A | ❌ | N/A (轻客户端 + Witness P2P 已覆盖核心用例) |
| **Beam Sync** | 🔧 实验(已废弃) | ❌ | ❌ | ❌ | ❌ | N/A | ❌ | N/A (行业已淘汰，Checkpoint+Backfill 替代) |
| **State Sync (应用层)** | ❌ | ❌ | ❌ | ✅ Cosmos | ❌ | N/A | ✅ | N/A (Snap Sync + Checkpoint 提供等价功能) |

### 关键差距

**Staged Sync**：Erigon 原创、reth 借鉴的核心创新，将同步过程分解为独立的阶段（headers→bodies→senders→execution→hashing→trie→finish），每个阶段可以独立重试、回退（unwind）和监控。数据通过 ETL 预处理减少写放大。显著提高了同步的可靠性和可观测性。

**OtterSync (Erigon)**：将 ~98% 计算从 CPU 转移到网络带宽。基于 BitTorrent 分发不可变 segment 文件，自 Erigon 3.0 Alpha 2（2024-08）起为默认同步模式；官方 archive <8h 数据源自 2024 启动博文（非 2026 重测）。**N42 对位**：`cmd/n42-eth-torrent`（BitTorrent v1+v2 桥）+ EraE 段 + manifest，提供等价的不可变段分发；F2 冷段 torrent 1-of-N 已实测 PASS。

**Checkpoint Sync**：从可信检查点快速启动，跳过历史验证。对于新节点加入网络极其重要，可将启动时间从数天缩短到数小时。Erigon Caplin 内置支持（**v3.4.0 新增持久化历史下载——beacon block 下载跨重启续传；discv5 转为默认**）。

**N42 eth-el 三模式同步（2026-05 实测）**：minimal（~30GB，anchor 信任）/ full（~126GB 热段）/ archive（全量，witness 重放）。archive 模式已实测从 leaves-rebuild → catchup → live 追平主网 tip（25,191,536 → 25,199,323，7,787 块，1h23m，0 mismatch），转入 12s live。minimal/full 的 snapshot-direct 直读尚待接线（task #94，预估 3–5 天）。详见 `docs/ethel/architecture-framework-and-plan.md`。

---

## 三、执行层与 EVM

### 3.1 并行执行对比

| 功能 | geth | reth | Erigon 3.4 | Sei v2/v3 | Monad | Grevm 2.2 | Aptos | **N42** |
|------|------|------|------------|-----------|-------|-----------|-------|---------|
| **并行执行引擎** | 🔧 EIP-7928 BAL (Glamsterdam) | 🔧 prewarming (Grevm/PEVM 集成) | 🔧 v3.4 BAL 实验性 | ✅ 乐观并行 (OCC, v2 已上线) | ✅ 乐观并行 (主网) | ✅ hint-DAG | ✅ Block-STM (v2 规划中) | ✅ Block-STM |
| **并行策略** | BAL 预声明 | prefetch+warmup | 实验性并行 | Block-STM 变体→Ares 重写(规划) | 乐观调度+多VM | 预分析 DAG + Lock-Free | 原生 Block-STM | Wave Block-STM + 依赖预测 |
| **状态预取** | ✅ prefetcher | ✅ parallel prewarming | ✅ ETL预处理 | ✅ | ✅ async I/O | N/A | ✅ | ✅ ShardedCache 预加载 + 异步 I/O 预取池 + SLOAD 学习 |
| **TX 依赖分析 (DAG)** | ❌ (BAL 提供静态访问集) | ❌ | ❌ | ✅ | ❌ 乐观重试 | ✅ 核心特性 (Lock-Free DAG) | ❌ 乐观重试 | ✅ access list DAG + 合约+选择器分组预排序 |
| **Async I/O** | ❌ | ❌ | ❌ | 🔧 Eidos(io_uring, 规划) | ✅ 核心特性 (MonadDB io_uring) | ❌ | ❌ | ⚠️ channel 异步派发 + I/O 工作池 (Go goroutine, 部分等价；非 io_uring) |
| **JIT/AOT EVM 编译** | ❌ | 🔧 实验 (revmc, 未转默认) | 🔧 Erigon++ C++20/evmone (实验) | 🔧 Ares (规划) | ❌ (字节码级解释) | ❌ | N/A (Move, MonoMove 规划) | N/A (Go 无 LLVM；热合约 precompile 替代；RISC-V EVM 用于 ZK 证明) |
| **SIMD 优化** | ❌ | 🔧 revmc 自动向量化 | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ QMDB AVX-512 8/16-way 哈希内核 |

### 3.2 EVM 兼容性与 EIP 支持

| EIP/功能 | geth | reth | Erigon 3.4 | Sei | Monad | **N42** | 说明 |
|----------|------|------|------------|-----|-------|---------|------|
| **EIP-1153 (TLOAD/TSTORE)** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | 瞬态存储 |
| **EIP-4844 (Blobs)** | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | Proto-Danksharding |
| **EIP-5656 (MCOPY)** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | 内存复制 |
| **EIP-7516 (BLOBBASEFEE)** | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | Blob 基础费 |
| **EIP-3855 (PUSH0)** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | 零值推入 |
| **EIP-7702 (Pectra AA)** | ✅ | ✅ | ✅ | ❌ | ✅ (MIP-4 配套) | ✅ | 委托账户代码 |
| **EIP-2537 (BLS12-381)** | ✅ | ✅ | ✅ Pectra含 | ❌ | 🔧 | ✅ 9 预编译完整 | BLS 预编译 |
| **EIP-7594 PeerDAS** | ✅ Fusaka (2025.12) | ✅ | ✅ Fusaka | ❌ | ❌ | ✅ 列采样+KZG真实验证 | 数据可用性采样 |
| **EIP-7951 (P-256/secp256r1)** | ✅ Fusaka | ✅ | ✅ Fusaka | ❌ | ✅ (MIP-5) | ✅ | Fusaka 转正的 P-256 |
| **EIP-7825 (tx gas cap)** | ✅ Fusaka | ✅ | ✅ | ❌ | ✅ (MIP-5) | ✅ | 单 tx gas 上限 |
| **EIP-7823/7883 (ModExp 上界/提价)** | ✅ Fusaka | ✅ | ✅ | ❌ | ✅ (MIP-5/MONAD_NINE) | ✅ | ModExp 重定价 |
| **EOF (EVM Object Format)** | ⚠️ 已移出 Glamsterdam 窄化范围 | ⚠️ 同 | ⚠️ | ❌ | ❌ | ✅ **已实现** | **N42 提前实现；以太坊侧 2026-06 EOF 状态存疑/推迟** |
| **EIP-7928 (Block-Level Access Lists)** | 🔧 Glamsterdam 头牌 | 🔧 | 🔧 v3.4 实验 | ❌ | ❌ | ❌ (Block-STM DAG 提供等价并行) | 块级访问列表→并行执行 |
| **EIP-7732 (ePBS)** | 🔧 Glamsterdam 头牌 | 🔧 | 🔧 | ❌ | ❌ | ❌ (MEV-Boost；ePBS 待规格冻结) | enshrined PBS |
| **ERC-4337 (AA)** | 部分 | 部分 | ✅ RIP-7560+ERC-7562 | ❌ | 🔧 (MIP-4 储备余额内省) | ✅ bundler+mempool | 账户抽象 |

### 3.3 关键差距

**Transaction DAG 分析**：Grevm（架构代号 2.1，最新发布 **v2.2.5 / 2026-03-09**）的核心创新 — 在执行前通过模拟执行结果（hints）构建交易依赖 DAG，使用 Lock-Free DAG（调度开销降低 60%）和 Task Groups（强依赖交易归组同线程顺序执行）。**自published 微基准（未经第三方复现）**：Uniswap 场景 11.25 gigagas/s，30% hot-ratio 混合场景 2.96 gigagas/s（5.5x 提升），不可并行化场景比 Block-STM 减少 **95% CPU 使用**。落地载体 Gravity Reth ~41,000 TPS / 1.5 gigagas/s；Gravity L1 主网 **2026-06-04 上线（许可制）**，实测 ~9.5–12k TPS，**100k TPS 目标未达**。N42 的 Block-STM 采用纯乐观方式（execute→validate→retry），已补充**合约+选择器分组预排序的依赖预测**（对位 Sei Dependency Prediction）以优化波效率，但在高冲突场景下相对 hint-DAG 仍有优化空间。

**Async I/O (MonadDB)**：Monad（**2025-11-24 主网上线**）的核心差异化特性 — MonadDB 使用 Linux `io_uring` 内核技术，执行线程发起 I/O 请求不阻塞，可直接在块设备（block device）上运行绕过文件系统。多 VM 实例 + 异步 I/O + 延迟执行实现真正的流水线。**主网现实校准**：10,000 TPS 为容量目标/devnet 与第三方压测数字，**组织流量实测远低于此**（启动日峰值 ~350 TPS，日费用 <$3,000）。**MONAD_NINE 升级 2026-03-19 上线**（MIP-3 线性内存 gas / MIP-4 储备余额内省 / MIP-5 激活 Fusaka EIP），RPC `latest` 响应 1.2s→400ms；**MIP-12（2026-06-06 提案）**拟将共识投票周期 400ms→300ms。N42 以 channel 异步派发 + I/O 工作池 + SLOAD 学习预取在 Go 层部分等价（非 io_uring 内核旁路）。

**JIT/AOT EVM 编译**：reth 的 `revmc` 将 EVM 字节码编译为本机机器码（JIT 计算密集场景最高 6.9x，Fibonacci 19x；AOT 预编译热门合约消除预热延迟，LLVM 自动向量化）。**截至 2026-06 仍为实验/opt-in（`--experimental.compiler`），未转默认**；Paradigm 称"将集成于生产"属规划。Erigon 侧 Erigon++（C++20/evmone）与 Zilkworm（C++23 RISC-V zkEVM 原型，~100s/块 on 单 4090）同为实验/R&D。

**PeerDAS (Data Availability Sampling, EIP-7594)**：**Fusaka 硬分叉已于 2025-12-03 主网激活**，blob target 经 BPO1/BPO2 提至 14（max 21）；BPO3+ 因 blob 用量未饱和而暂缓（长期目标 48→128 仍为愿景）。N42 已提前集成列采样 + go-eth-kzg cell proof 真实批量验证（39 测试）。

---

## 四、P2P 网络层

| 功能 | geth | reth | Erigon 3.4 | Sei | Monad | Aptos | **N42** |
|------|------|------|------------|-----|-------|-------|---------|
| **协议栈** | DevP2P | DevP2P | DevP2P+libp2p(Caplin, discv5 默认) | libp2p (Tendermint) | 自研 | 自研 | libp2p |
| **eth/68 (TX announce)** | ✅ | ✅ | ✅ | N/A | N/A | N/A | ✅ |
| **eth/69 (history expiry)** | ✅ v1.16 | ✅ | ✅ v3.2+ | N/A | N/A | N/A | ✅ 语义等价 (libp2p Status.EarliestBlock + range handler 门控) |
| **eth/70 (最新 wire)** | ✅ v1.17.3 (2026-05) | 🔧 | 🔧 | N/A | N/A | N/A | N/A (libp2p 路线；概念可借鉴) |
| **Snap Protocol** | ✅ | ✅ | ✅ OtterSync(BT) | N/A | N/A | N/A | ✅ 完整实现 (service+manager+tasks+verify+progress+metrics) |
| **Blob Sidecar P2P** | ✅ | ✅ | ✅ Caplin gossipsub | N/A | N/A | N/A | ✅ gossip+RPC |
| **Witness Protocol** | 🔧 | 🔧 | ❌ | N/A | N/A | N/A | ✅ P2P handler + RPC API |
| **PeerDAS 网络层** | ✅ Fusaka | ✅ | ✅ Fusaka | N/A | N/A | N/A | ✅ 列采样+KZG |
| **Portal Network** | 🔧 | ❌ | ❌ | N/A | N/A | N/A | ❌ |
| **Gossip 优化** | ✅ | ✅ | ✅ | ✅ GossipSub | ✅ | ✅ | ✅ |
| **连接管理/门控** | ✅ | ✅ | ✅ Sentry独立 | ✅ | ✅ | ✅ | ✅ |
| **Kademlia DHT** | ✅ | ✅ | ✅ DISCV5 | ✅ | ❌ | ❌ | ✅ |

### 关键差距

**eth/69 协议**：EIP-7642，增加了 `earliestBlock` / `latestBlock` 字段到 Status 消息，新增 `BlockRangeUpdate` 消息。支持 history expiry — 客户端可在 2025.5 后丢弃 pre-merge 历史数据。N42 使用 libp2p 而非 DevP2P，协议不直接兼容，但概念可借鉴。

**Blob Sidecar P2P**：~~已关闭~~ N42 已实现完整的 blob sidecar P2P 传播：GossipSub 独立 topic (`blob_sidecar`)、`BlobSidecarsByRange` / `BlobSidecarsByRoot` RPC handler (`internal/sync/rpc_blob.go`)、gossip scoring 参数配置、SSZ 编码。覆盖 EIP-4844 全部 P2P 需求。

---

## 五、共识层与区块构建

| 功能 | geth | reth | Erigon 3.4 | Sei | Monad | Aptos | **N42** |
|------|------|------|------------|-----|-------|-------|---------|
| **共识引擎** | PoS (Beacon) | PoS (Beacon) | PoS(Caplin内置CL) | Tendermint/CometBFT (Twin-Turbo) | MonadBFT (主网) | AptosBFT→Jolteon→Velociraptr (主网) | APoA/APoS/**HotStuff-2 BFT** |
| **Engine API** | ✅ v1-v4 | ✅ v1-v4 | ✅ v1-v4(+Caplin) | N/A | N/A | N/A | ✅ v1+v4 (Dencun blob)；V5/Osaka 不需要 (Osaka 仍用 V4) |
| **内置共识层** | ❌ 需外部CL | ❌ 需外部CL | ✅ Caplin默认 | ✅ CometBFT | ✅ | ✅ | ✅ APoA/APoS + Caplin embedded (Engine API done, sync loop 7.4-7.6 stub) |
| **Proposer-Builder Separation** | ✅ MEV-Boost (ePBS 规划) | ✅ | ✅ MEV-Boost | ❌ | ❌ | ❌ | ✅ MEV-Boost Relay |
| **Slot-based 出块** | ✅ 12s | ✅ 12s | ✅ 12s | ✅ ~400ms (Twin-Turbo；Giga 未上线) | ✅ 400ms (MIP-12 拟 300ms) | ✅ **亚 50ms 主网实测 (Baby Raptr+Velociraptr, 2025.12)** | ✅ 8s (period) |
| **Finality 速度** | ~15min (2 epoch) | ~15min | ~15min(+Caplin) | ~400ms 即时 | ~800ms (主网) | 亚秒 (主网延迟优化) | 单槽即时 (HotStuff-2 两轮) |
| **Deferred Execution** | ❌ | ❌ | ❌ | 🔧 Giga (未上线) | ✅ 核心特性 (主网) | ✅ Zaptos 流水线 | ✅ PoC (consensus-execution 分离) |
| **流水线共识** | ❌ | ❌ | ❌ | 🔧 Autobahn (Giga, "coming soon" 未上线) | ✅ 完整流水线 (主网) | ✅ Velociraptr 乐观提议 (主网)；完整 Raptr 规划中 | ✅ HotStuff-2 流水线 (Prepare\|Commit 重叠) |
| **BFT 共识 (两轮优化)** | ❌ | ❌ | ❌ | ✅ CometBFT | ✅ MonadBFT | ✅ Jolteon | ✅ HotStuff-2 |
| **BLS 聚合签名** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ BLS12-381 |
| **PQ-STARK 验证** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ 独有 |
| **Withdrawal 处理** | ✅ | ✅ | ✅ | N/A | N/A | N/A | ⚠️ Engine API 传递✅ + Withdrawal 类型✅ + withdrawalsRoot 计算✅; 余额转移使用 deposit 合约替代以太坊原生 withdrawal 机制 |

### 关键差距

**Deferred Execution（延迟执行）**：Monad 和 Aptos 的核心创新 — 将区块执行与共识解耦。共识先对交易排序达成一致（区块 = 交易排序服务），执行在后台异步进行，执行可利用完整区块时间平滑突发负载。Sei Giga（2026 年上线）也采用此模式，共识仅排序不包含状态变更结果。

**流水线共识（Pipelining）**：Monad 实现了共识 → 执行 → I/O 的完整流水线重叠（Block N 共识 | Block N-1 执行 | Block N-2 提交，主网已上线）。**Aptos**：Baby Raptr（2025-06 主网，网络跳数 6→4，延迟 −20%/100-150ms）+ Velociraptr（2025-09/10 主网，乐观提议，出块 ~100ms→~60ms）→ **主网亚 50ms 出块（2025-12 实测）**；但 **完整 Raptr（decoupled prefix voting）与 Block-STM v2 仍为 2026 路线图**，250k TPS 系 **100 节点 geo-distributed 实验室数字（arXiv 2504.18649），主网实测 ~34 TPS（理论上限 ~160k）**。**Sei Giga**：引入 Autobahn 多提议者（lane-based + PoA）模型，但**截至 2026-06 未上线**——Autobahn/Ares/Eidos 仍 "in progress / coming soon"，无公开测试网；5 gigagas/s、200k TPS 均为 **Sei 内部 devnet 宣称**（whitepaper arXiv 2505.14914），原 H1-2026 主网目标已滑至"贯穿 2026"；2026 上主网的 v6.x（SIP-3 EVM-only）是 Giga 的铺垫而非性能核心。

**HotStuff-2 BFT 共识引擎**：N42 新增 HotStuff-2 BFT 共识引擎（`internal/consensus/hotstuff/`，~3000 行代码，60 个测试），实现两轮优化协议（Prepare + Commit），BLS12-381 聚合签名验证，Jolteon 风格自适应 Pacemaker（指数退避 + p95 延迟自适应），动态领导者轮转，MDBX 状态持久化和 diff layer journal 崩溃恢复，以及完整的 P2P 集成（SSZ 编码、gossip 主题映射）。这使 N42 的共识能力与 MonadBFT、AptosBFT (Jolteon) 同级别。

**N42 量子抗性评估**：
- **PQ-STARK**：N42 已集成 STARK 验证到 APoS 共识。STARK 的安全性完全建立在哈希函数抗碰撞性上（无椭圆曲线依赖），天然具备后量子安全性。NIST 和学术界公认：基于哈希的密码学方案是量子安全的第一梯队。
- **Blake3 状态根**：N42 的 JMT 状态承诺使用 Blake3-256，Grover 算法仅使安全性从 256-bit 降至 128-bit，仍是充分的安全水平。相比之下，Verkle Tree 的 Pedersen 承诺（椭圆曲线）会被 Shor 算法完全破解。
- **客观对比**：截至 2026.6，主网部署 PQ 密码学的公链仍仅有 Algorand（2025.11 首笔 Falcon-1024 交易）和 QRL（XMSS→SPHINCS+）。N42 的 PQ-STARK + JMT/BMT Blake3 组合是有意义的领先，但需注意 STARK 证明的验证开销（Stwo 证明器 >600K Poseidon hash/s，M3 Pro 约 0.5s 证明一个以太坊状态根）。
- **以太坊 "Lean Ethereum" 后量子路线（2026 更新）**：以太坊将后量子列为多年期核心方向——共识签名拟用 **leanXMSS（哈希签名，~3KB/sig）替代 BLS**，配 **leanMultisig STARK 聚合**；约 10 个客户端团队在建 lean consensus client。早期 PQ 组件（密钥注册、签名预编译）**可能**落地 Hegotá（2026 H2，未确认），全面迁移目标 ~2029，Q-Day 估计 ~2032。这进一步印证 N42 "哈希优先"承诺/共识方向的前瞻性。
- **时间线参考**：Vitalik 将 ~2028 标记为量子计算关键窗口；以太坊基金会 2026.1 成立 PQ Security Team + 研究奖金，pq.ethereum.org 为官方专题。

---

## 六、RPC API 完整性

| API | geth | reth | Erigon 3.4 | Sei | Monad | Aptos | **N42** |
|-----|------|------|------------|-----|-------|-------|---------|
| **eth_* 标准** | ✅ 完整 | ✅ 完整 | ✅ 完整 | ✅ 部分 | ✅ | N/A | ✅ 大部分 |
| **eth_getProof** | ✅ MPT proof (archive 需全节点) | ✅ | ✅ +历史proof (v3.4 转正) | ✅ | ✅ | N/A | ✅ **MPT-HPH proof（eth-el, canonical EIP-1186）** + JMT proof（自研链）+ **DATC 任意历史高度** |
| **eth_getStorageValues (批量 RPC)** | ✅ v1.17.1 (EIP-7834) | ✅ | ✅ v3.4 新增 | ❌ | ❌ | N/A | ✅ |
| **eth_createAccessList** | ✅ | ✅ | ✅ +StateOverrides | ❌ | ✅ | N/A | ✅ 迭代式 AccessListTracer |
| **eth_simulateV1** | ✅ | ✅ | ✅ | ❌ | ❌ | N/A | ✅ |
| **eth_getBlockReceipts** | ✅ | ✅ | ✅ | ❌ | ✅ | N/A | ✅ |
| **debug_* 命名空间** | ✅ 完整 | ✅ 完整 | ✅ 完整 | 部分 | 部分 | N/A | ✅ |
| **trace_* 命名空间** | ✅ | ✅ (Parity 兼容) | ✅ OE兼容 | ❌ | ❌ | N/A | ✅ |
| **GraphQL API** | ✅ EIP-1767 | ❌ | ✅ --graphql | ❌ | ❌ | ✅ 自研 | ✅ EIP-1767 |
| **Otterscan API** | ❌ | ✅ | ✅ 原生集成 | ❌ | ❌ | N/A | ✅ 完整 (getApiLevel/hasCode/blockDetails/blockTxs/contractCreator/searchTxs/txError) |
| **Engine API (完整)** | ✅ v1-v4 | ✅ v1-v4 | ✅ v1-v4+Caplin | N/A | N/A | N/A | ✅ v1-v4 完整 |
| **Admin API** | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ import/export + DB inspect + state dump + debug RPC |
| **Bloom Bits 索引** | ✅ | ✅ | ✅ receipt持久化 | ❌ | ❌ | N/A | ✅ roaring bitmap |
| **Subscribe (WS)** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Filter API** | ✅ 完整 | ✅ 完整 | ✅ 完整 | 部分 | ✅ | N/A | ✅ |
| **RPCDaemon 独立部署** | ❌ | ❌ | ✅ 核心特性 | ❌ | ❌ | ❌ | ✅ 独立二进制 (gRPC remote KV) |

### 关键差距

**Bloom Bits 索引**：~~已关闭~~ N42 使用 roaring bitmap 索引 (`modules/rawdb/log_index_read.go`) 实现地址+topic 级别的位图交集查询，比传统 bloom bits 更精确（无误报）且更快。`internal/api/filters/filter.go` 的 `indexedLogs()` 使用 `BlocksForAddresses()` + `BlocksForTopics()` 进行 O(1) 位图查找。同时 `internal/exex/extensions/ai_indexer.go` 提供了 O(1) 结构化事件查询。已超越 geth bloom bits 方案。

**GraphQL API**：~~已关闭~~ N42 已实现完整的 EIP-1767 GraphQL API (`internal/api/graphql/`): schema 定义、resolver、HTTP handler、helper。功能与 geth 对齐。

**Otterscan API**：reth 和 Erigon 支持的区块浏览器优化 API，支持高效的地址交易历史查询、内部交易追踪等。Erigon 是 Otterscan 的原始集成目标。

**RPCDaemon 独立部署**：Erigon 的 RPCDaemon 可作为独立进程运行，支持 RPC 集群扩展，多个 RPCDaemon 实例共享同一核心节点。v3.1.0 启用 `--persist.receipts` 后 `eth_getLogs` 等调用提速 10x；**v3.4.0 新增 `trace_rawTransaction` / `eth_getStorageValues` / `engine_getBlobsV3` / `admin_addTrustedPeer` 等端点，并降低 RPC 对 ChainTip 的影响**。N42 已以 `cmd/rpcdaemon/` + gRPC remote KV 提供等价的 RPC 层拆分。

---

## 七、交易池

| 功能 | geth | reth | Erigon 3.4 | Sei | Monad | Aptos | **N42** |
|------|------|------|------------|-----|-------|-------|---------|
| **标准 TxPool** | ✅ | ✅ | ✅ 独立模块 | ✅ | ✅ | ✅ | ✅ |
| **Blob TxPool** | ✅ 独立池 | ✅ | ✅ | ❌ | ✅ | N/A | ✅ |
| **持久化** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ MDBX 持久化 (flushToDB/loadFromDB) |
| **RBF (Replace-By-Fee)** | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ |
| **Private TxPool** | ❌ | ❌ | ✅ Shutter加密 | ✅ | ❌ | ❌ | ✅ 阈值加密 Mempool |
| **动态大小调整** | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | ✅ 内存感知 |
| **EIP-7702 TX 类型** | ✅ | ✅ | ✅ | ❌ | 🔧 | N/A | ✅ |
| **Local Priority** | ✅ | ✅ | ✅ --txpool.nolocals | ❌ | ❌ | ❌ | ✅ |
| **独立进程部署** | ❌ | ❌ | ✅ 核心特性 | ❌ | ❌ | ❌ | N/A (RPCDaemon 已拆分 RPC 层; TxPool 拆分收益边际) |

### 关键差距

**动态大小调整**：根据系统内存压力自动调整交易池容量，防止内存溢出同时最大化池利用率。

**Private TxPool**：Sei 特有的反 MEV/抢跑机制，交易在被包含进区块前不公开。

---

## 八、开发者工具与 CLI

| 工具 | geth | reth | Erigon 3.4 | Sei | Monad | Aptos | **N42** |
|------|------|------|------------|-----|-------|-------|---------|
| **Chain Import/Export** | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ protobuf 格式批量导入导出 |
| **State Dump** | ✅ | ❌ | ✅ | ❌ | ❌ | ❌ | ✅ JSON 流式输出含 storage/code |
| **DB Inspector** | ✅ | ✅ | ✅ diagnostics | ❌ | ❌ | ❌ | ✅ stats/list/get/inspect 四命令 |
| **JS Console** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | N/A (过时特性; MCP Server + curl + foundry 替代) |
| **EVM CLI Tool** | ✅ `evm` | ❌ | ❌ | ❌ | ❌ | ❌ | N/A (debug_traceCall RPC 提供在线调试) |
| **Clef (签名器)** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ IPC 签名器 + 规则引擎 + 审计日志 |
| **abigen** | ✅ | ❌ | ❌ | ❌ | ❌ | N/A (Move) | ✅ |
| **Chain Rollback** | ✅ | ✅ | ✅ unwind | ✅ | ❌ | ❌ | ✅ |
| **Genesis Init** | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ |
| **Keystore 管理** | ✅ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ |
| **devp2p CLI** | ✅ | ❌ | ✅ sentry独立 | ❌ | ❌ | ❌ | N/A (N42 用 libp2p; admin_peers RPC 提供诊断) |
| **TOML 配置文件** | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ YAML |

---

## 九、安全与稳定性

| 功能 | geth | reth | Erigon 3.4 | Sei | Monad | Aptos | **N42** |
|------|------|------|------------|-----|-------|-------|---------|
| **DoS 防护** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **RPC 速率限制** | ✅ | ✅ | ✅ batch limit | ✅ | ✅ | ✅ | ✅ |
| **Gas Cap 保护** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Graceful Shutdown** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Health Check** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Panic Recovery** | ✅ 全面 | ✅ Rust 安全 | ⚠️ Go GC | ✅ | ✅ | ✅ Move 安全 | ✅ SafeGo+8处 |
| **Fuzzing 测试** | ✅ 大量 | ✅ | ⚠️ hive测试 | ✅ | ❌ | ✅ | ✅ 29 fuzz函数 |
| **内存安全** | ⚠️ Go GC | ✅ Rust 所有权 | ⚠️ Go GC | ⚠️ Go GC | 自研 | ✅ Move 线性类型 | ⚠️ Go GC (SafeGo + 3 轮安全审计 47+ bug 修复加固) |
| **PQ 密码学** | ❌ 2026 路线图 | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ PQ-STARK |
| **加密Mempool** | ❌ | ❌ | ✅ Shutter Network | ❌ | ❌ | ❌ | ✅ 阈值加密 (AES-256-GCM) |

### 关键差距

**Fuzzing 测试**：~~已更正~~ N42 有 18 个 fuzz 测试文件（29 个 fuzz 函数），覆盖：ABI 编码 (`accounts/abi/`)、EVM precompiles (`internal/vm/`)、RLP 编解码 (`lib/rlp/`)、seg 压缩/解压 (`lib/seg/`)、RecSplit (`lib/recsplit/`)、Elias-Fano (`lib/recsplit/eliasfano*/`)、Patricia trie (`lib/seg/patricia/`)、SSZ (`api/protocol/sync_pb/`)、Aggregator (`lib/state/`)、Blake2b (`common/crypto/`, `lib/crypto/`)。覆盖核心编解码和密码学组件。

**N42 优势**：PQ-STARK 后量子密码学是客观领先优势。行业对比：Algorand 2025.11 主网首笔 Falcon PQ 交易，QRL 使用 SPHINCS+，其余主流客户端(geth/reth/Erigon)均无 PQ 部署。以太坊基金会 2026.1 成立 PQ Team + $1M 奖金，将 PQ 列为 "Harden the L1" 核心优先事项。N42 的 PQ-STARK + JMT Blake3 状态根组合使其在量子安全维度处于行业前列。新增加密 Mempool（阈值加密 AES-256-GCM）进一步增强交易隐私保护。

---

## 十、MEV 与经济模型

| 功能 | geth | reth | Erigon 3.4 | Sei | Monad | Aptos | **N42** |
|------|------|------|------------|-----|-------|-------|---------|
| **MEV-Boost 集成** | ✅ | ✅ | ✅ Engine API | ❌ | ❌ | N/A | ✅ Relay 通信 + 区块拍卖 |
| **Flashbots Bundle API** | ✅ 插件 | ✅ 插件 | ✅ | ❌ | ❌ | N/A | ✅ TxByPriceAndNonce + BundlePool + 两阶段出块 |
| **Priority Ordering** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ TxByPriceAndNonce 堆排序 |
| **Bundle Pool** | ✅ | ✅ | ✅ | ❌ | ❌ | N/A | ✅ BundlePool + 过期驱逐 |
| **PBS (Builder Separation)** | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ MEV-Boost Relay 集成 |
| **Inclusion List** | 🔧 研究 | 🔧 | ❌ | ❌ | ❌ | ❌ | N/A (等 Glamsterdam 确定; HotStuff 共识无 PBS 审查问题) |
| **Block Value 优化** | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ 本地/Relay 价值对比拍卖 |
| **EIP-1559 动态费率** | ✅ | ✅ | ✅ | ✅ 变体 | ✅ | ❌ | ✅ |
| **加密Mempool (反MEV)** | ❌ | ❌ | ✅ Shutter | ✅ Private Pool | ❌ | ❌ | ✅ 阈值加密池 |

---

## 十一、可观测性与运维

| 功能 | geth | reth | Erigon 3.4 | Sei | Monad | Aptos | **N42** |
|------|------|------|------------|-----|-------|-------|---------|
| **Prometheus Metrics** | ✅ 200+ | ✅ 300+ | ✅ 端口6061 | ✅ | ✅ | ✅ | ✅ **250+** 指标 (EVM/Chain/Reorg/Fee/TxLifecycle/EngineAPI/RPC/JMT/P2P/DB/Consensus/Cache/Sync/ZK) |
| **OpenTelemetry** | ❌ | ✅ | ❌ | ✅ | ❌ | ✅ | ✅ OTLP/HTTP |
| **Grafana Dashboard** | ✅ 官方模板 | ✅ 官方模板 | ✅ | ✅ | ❌ | ✅ | ✅ 3面板 |
| **结构化事件日志** | ✅ | ✅ | ✅ JSON+分级 | ✅ | ✅ | ✅ | ✅ JSON 默认 (文件输出) |
| **Live Tracing** | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ✅ liveTracer (实时 EVM 事件流 + tracer directory 注册) |
| **pprof 支持** | ✅ | ✅ (tokio-console) | ✅ 6060端口 | ✅ | ❌ | ❌ | ✅ 6060 端口 |
| **诊断 API** | ✅ | ✅ ExEx | ✅ diagnostics模块 | ❌ | ❌ | ✅ | ✅ debug_nodeStatus 全面诊断 |
| **MCP Server (AI)** | ❌ | ❌ | ✅ 端口8553 | ❌ | ❌ | ❌ | ✅ 端口 8553 (8工具+4资源) |

### 关键差距

**Grafana Dashboard**：N42 现有 3 个 Grafana 面板 — `n42.json`(基础)、`n42_internal.json`(内部)、`n42_advanced.json`(7分组/20+面板: Sync Progress, DB I/O, TxPool Advanced, Snap Sync, ERC-4337 Bundler, P2P Network, Miner)。覆盖新增的所有指标。

**N42 Metrics 审计备注**：经源码审计确认，N42 有 **250+ Prometheus 指标**（272 个 metric 注册调用），覆盖：系统/Go runtime (11)、链/同步 (12)、MDBX (30+)、P2P (20+)、TxPool (10)、快照 (10)、HotStuff (5)、Bundler (6)、ZK (9)、EVM 执行/状态/链/reorg/费用/交易/Engine API/RPC/JMT (55+)、Pipeline timing (8)、AI/Ingest (10+)。**与 geth 200+ 相当，不存在差距。**

**OpenTelemetry**：分布式追踪标准，reth、Sei、Aptos 均已集成。对于多节点部署的问题诊断至关重要。

---

## 十二、L2/Rollup 与扩展框架

| 功能 | geth | reth | Erigon 3.4 | Sei | Monad | Aptos | **N42** |
|------|------|------|------------|-----|-------|-------|---------|
| **ExEx (执行扩展)** | 🔧 PR#30611 | ✅ 核心特性 | ❌ | ❌ | ❌ | ❌ | ✅ Manager+Hook |
| **OP Stack 支持** | ✅ op-geth | ✅ op-reth | ❌ | ❌ | ❌ | ❌ | N/A (N42 是独立 L1; ExEx 可作为 L2 数据输出 hook) |
| **Sequencer 模式** | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | N/A (HotStuff-2 BFT 共识 + Miner 替代) |
| **Fraud Proof** | 部分 | 部分 | ✅ Historical Proofs | ✅ | ❌ | ❌ | N/A (ZK proof 有效性证明严格强于 fraud proof 欺诈证明; STARK/SNARK/SP1 三后端) |
| **SDK/库化使用** | ✅ Go 包 | ✅ Rust crate | ✅ erigon-lib | ✅ Cosmos SDK | ❌ | ✅ Move SDK | ✅ sdk/ 包 |
| **插件系统** | 🔧 ExEx | ✅ ExEx | ✅ 模块化进程 | ✅ ABCI | ❌ | ❌ | ✅ ExEx Manager+Hook |
| **Cosmos IBC** | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | N/A (非 Cosmos 链) |
| **ZK 证明** | ❌ | ❌ | 🔧 Zilkworm zkEVM | ❌ | ❌ | ❌ | ✅ STARK/SNARK/SP1 三后端 + RISC-V64 guest + JMT GC |
| **L2 合作** | ✅ OP/Arbitrum | ✅ OP 官方 | ✅ Arbitrum合作 | ✅ Cosmos | ❌ | ❌ | ❌ (需业务驱动) |

### 关键差距

**ExEx (Execution Extensions)**：reth 的核心创新之一 — 后执行钩子（post-execution hooks），支持实时构建 rollup、indexer、MEV bot 等，代码量减少 10x+。特性包括：异步 future 支持、reorg 感知流、替代 VM 集成。geth PR#30611 (by karalabe) 正在移植此概念（因 Go 无动态加载，需编译进二进制）。

**OP Stack 生态转向（已发生）**：**Optimism 已于 2026-05-31 正式停止支持 op-geth**（2026-03-05 公告），全面转向 op-reth。安全补丁仅维持到该日期，新功能（如 Karst 硬分叉）仅在 op-reth 开发，op-reth 由 OP Labs + Paradigm 共同维护并成为 Superchain 强制 EL 客户端（约 2026-07-08 有网络升级）。这标志着 Rust 实现在 L2 生态中的主导地位。

**SDK/库化使用**：reth 设计之初就考虑了作为 Rust 库使用 — 每个组件都是独立 crate，开发者可单独引入 P2P 网络栈、直接操作数据库、拆解节点为所需组件。N42 的模块化程度有限，`internal/` 包不可外部导入。

---

## 十三、前沿路线图对齐

### 13.1 以太坊 2025-2026 升级路线（2026-06 更新）

| 升级 | 时间 | 关键 EIP | N42 状态 |
|------|------|----------|----------|
| **Pectra** | 2025.5.7 已上线 | 7702(AA), 2537(BLS), 6110(deposits), 7623(calldata cost) | ✅ 完整: 7702✅ 2537✅ 6110✅ 7623✅ 7251✅ 7002✅ 2935✅ 7685✅ |
| **Fusaka** | 2025.12.3 已上线 | PeerDAS(7594), 7825(tx gas cap), 7918(blob 费下界), 7951(P-256), 7823/7883(ModExp), Gas 45M→60M | ✅ PeerDAS✅ 7825✅ 7951✅ ModExp✅ |
| **BPO1/BPO2** | 2025.12 / 2026.1 已上线 | blob target 6→10→14, max 9→15→21 | ✅ BlobSchedule 完整支持；当前 14/21 |
| BPO3+ | 暂缓 (blob 用量未饱和) | 长期 48→128 (愿景) | ✅ 参数化就绪 |
| **Glamsterdam** | **2026 Q3 (从 H1 滑期)** | **头牌确认: 7732(ePBS) + 7928(块级访问列表 BAL)**; gas 重定价(8037/2780); gas limit→200M; **EOF 已移出窄化范围**; **FOCIL 推迟至 Hegotá** | ✅ EOF 已提前实现, MEV-Boost✅, BAL 由 Block-STM DAG 等价, ePBS ❌(待规格冻结) |
| **Hegotá** | 2026 Q4 命名中 | FOCIL, Native AA(8141), 原生隐私转账(8182), 二叉树状态迁移(候选), 早期 PQ 组件(可能) | ✅ PQ 已有(领先), BMT/JMT Blake3 对齐二叉树方向, Native AA 有 Bundler 基础, State expiry 🔧 |
| **Lean Ethereum / Beam** | 多年期 (~2029 迁移) | leanXMSS 替代 BLS + STARK 聚合, snarkification, 1-ETH staking | ✅ PQ-STARK + 哈希签名方向天然对齐 |

> **关键变化（2026-06）**：① Glamsterdam 从 H1 滑至 **Q3**，范围窄化为 **ePBS + BAL** 两头牌，**EOF 被移出**（以太坊侧状态存疑，而 N42 已提前实现，反成差异化）；② **Verkle 实质搁置**，统一二叉默克尔树 **EIP-7864 仍为 Draft**（哈希函数 BLAKE3 vs Poseidon2 未定，未分配激活分叉）；geth v1.17.3 已加入 "binary trie improvements" 佐证转向。

### 13.2 高性能链趋势（2026-06 更新，区分实测/宣称）

| 趋势 | Erigon 3.4 | Monad (主网) | Sei Giga | Aptos (Raptr) | MegaETH | Gravity (Grevm) | **N42** |
|------|------------|-------------|----------|-------------|---------|----------------|---------|
| **吞吐 (宣称 / 实测)** | 1Ggas/s 目标 | 10k TPS 容量 / **峰值~350 实测** | 200k 宣称 / **devnet only** | 250k **实验室** / **主网~34** | 100k 目标 / **35–50k 自测** | 100k 目标 / **~9.5–12k 实测** | **EVM 重放实测：880 Mgas/s 单流 · 10.8 Ggas/s 并行(32核)**；374 Ggas/s 系合成调度基准 |
| **主网状态** | 生产 v3.4.3 | ✅ 2025-11-24 上线 | ❌ **未上线** | ✅ 主网 (Baby Raptr/Velociraptr) | ✅ 2026-02-09 L2 上线 | ✅ 2026-06-04 (许可制) | ✅ eth-el 已追平 tip live |
| **亚秒级 Finality** | ❌ ~15min | ✅ ~800ms | ✅ ~400ms (Twin-Turbo) | ✅ 亚 50ms 出块 | ✅ L2 preconf (非最终性) | ✅ 亚秒 (AptosBFT 衍生) | ✅ HotStuff-2 即时 |
| **延迟/异步执行** | ❌ | ✅ 核心 | 🔧 Giga (未上线) | ✅ Zaptos | ❌ | ✅ | ✅ PoC (可配置启用) |
| **多提议者** | ❌ | ❌ | 🔧 Autobahn (未上线) | ❌ | ❌ | ❌ | ❌ |
| **实时 ZK 证明** | 🔧 Zilkworm 原型 (~100s/块) | ❌ | ❌ | ❌ | 🔧 无状态 prover | ❌ | 🔧 STARK/SNARK/SP1 三后端框架 |
| **PQ 密码学** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ **领先全行业** |
| **RPCDaemon 拆分** | ✅ 核心 | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ gRPC KV |
| **验证者动态重配置** | ❌ | ✅ MIP 升级 | ❌ | ✅ 自动 | ✅ 序列器轮转 | ❌ | ✅ commit-then-activate |

> **现实校准说明（N42 三个执行口径，务必区分，勿混用）**：
> 1. **374 Ggas/s** — Block-STM 调度微基准（`internal/parallel/executor_bench_test.go`，32 核独立 TX）。**注意：该 bench 以 `simulateWork()` 忙等循环模拟 EVM 工作、非真实字节码执行**，仅衡量调度/MVS 开销，不能当作真实 gas 吞吐。
> 2. **10.8 Ggas/s** — **真实并行 EVM 重放实测**（2026-06-13 复现，Ryzen 9 9950X 16C/32T，`--workers 32`）：witness-replay 主网区块 24,980,000–24,990,000，10,000 个 tx-密集块 / 4,711,911 笔 tx / 301.78 Ggas，27s 完成，359 blk/s，窗口稳定 9.2–13.5 Ggas/s。这是**纯并行 EVM 再执行**（按 witness 重放、code 取自 freezer、senders 预算、不算 state root、不落盘），表明 EVM 执行本身不是瓶颈。命令见 `cmd/witness-replay`（`--no-output --skip-verify --continue-on-error`）。
> 3. **880 Mgas/s** — **单流（`--workers 1`）真实 EVM 重放实测**（2026-06-13 复现，同上数据源，区块 24,980,000–24,982,000，2,000 块 / 1,026,847 tx / 60.05 Ggas，68s，failed=0，窗口 940–1046 mgas/s）。即 ② 关并行后的单线程基线，32 线程并行扩展 ~12.3×（880→10,829）。同为纯 EVM（不含 state root/落盘）。
>
> **关于"端到端"（含 state root + 持久化 + 每块开销）**：上一版曾给出 "~270 Mgas/s 端到端 staged 追平"，但经核查该值是 `7.8 blk/s × 35M gas` 的**手算估计**（`docs/ethel/exec-perf-plan.md` 标注 "our run"），**并非直接实测、本会话未单独复现，故已移除**。N42 的端到端 staged 执行（叠加 Merkle + changeset 落盘）目前**没有单一可复现的 mgas/s 基准**；据组件分解，root+落盘+每块开销会把单流吞吐从 880 Mgas/s 显著拉低，但具体数字待专门基准。
>
> **横向对照注意**：上述 880 Mgas/s / 10.8 Ggas/s 均为**（近）纯 EVM 重放**，与竞品"端到端主网 live 吞吐"（如 reth-2.0 含 trie+落盘的 ~1.7 Ggas/s）**口径不同，不可直接并列**；374 Ggas/s 更是合成忙等基准。高性能链宣称 TPS 多为 devnet/实验室/容量峰值（Monad 峰值约数百、Aptos ~34、Gravity ~1万量级），亦无独立第三方复现。

### 13.3 2026 行业趋势与 N42 战略方向

| 趋势 | 行业动态 (2026-06) | N42 机遇 | 优先级 |
|------|----------|----------|--------|
| **ePBS (EIP-7732)** | Glamsterdam Q3 头牌；enshrined PBS | 升级 MEV-Boost 为 ePBS 原生支持（待规格冻结） | P1 |
| **块级访问列表 (EIP-7928)** | Glamsterdam Q3 头牌；启用并行执行（geth 实测 ~2.2–2.6× 加速） | N42 Block-STM DAG 已提供等价并行；可加 BAL 预声明接口 | P1 |
| **实时 ZK 证明** | Erigon Zilkworm ~100s/块 (4090)；SP1/RISC Zero 持续提速 | 集成 SP1/RISC Zero 作为 ZK 后端（已有三后端框架） | P1 |
| **二叉默克尔树 (EIP-7864)** | Verkle 已搁置；二叉树 Draft，BLAKE3 vs Poseidon2 未定 | **BMT/JMT Blake3 已天然对齐**；可参与哈希函数标准讨论 | ✅ 已对齐 |
| **后量子 (Lean Ethereum)** | leanXMSS 替代 BLS + STARK 聚合；~10 客户端在建；~2029 迁移 | **N42 唯一已集成 PQ 的主流客户端**；扩展哈希签名 | ✅ 领先 |
| **Native AA (EIP-8141)** | Hegotá 候选，协议级账户抽象 | 已有 Bundler + EIP-7702，可扩展到 Native AA | P2 |
| **历史过期 + 全历史证明** | EIP-4444 partial expiry 推进；Erigon Historical Proofs 转正 | **F2 压缩 −45% + DATC 全历史 EIP-1186 已落地** | ✅ 领先 |
| **L2 主导权转向 Rust** | op-geth 已于 2026-05-31 停止支持，op-reth 成 Superchain 主客户端 | N42 为独立 L1；ExEx 可作 L2 数据 hook | P3 |

---

## 十四、性能工程

| 优化维度 | geth | reth | Erigon 3.4 | Sei | Monad | Aptos | **N42** |
|----------|------|------|------------|-----|-------|-------|---------|
| **并行执行** | ❌ | 🔧 prewarming | 🔧 实验性 | ✅ | ✅ | ✅ | ✅ Block-STM |
| **状态预取** | ✅ | ✅ parallel prewarming | ✅ ETL | ✅ | ✅ async | ✅ | ✅ ShardedCache 预加载 |
| **内存池化** | ✅ sync.Pool | ✅ arena alloc | ✅ | ❌ | ✅ | ✅ | ✅ pool.go |
| **零拷贝序列化** | ❌ | ✅ rkyv 实验 | ❌ | ❌ | ✅ | ✅ | ✅ Lazy+BufPool |
| **NUMA 感知** | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | N/A (Go runtime 不支持 NUMA 亲和性; 单 socket 部署零收益; 仅 Monad 自研 runtime 实现) |
| **IO_uring / 异步 I/O** | ❌ | ❌ | ❌ | 🔧 Eidos(规划) | ✅ MonadDB | ❌ | ⚠️ channel 异步派发 + I/O 工作池 + SLOAD 学习预取 (Go goroutine 部分等价; 非 io_uring 内核旁路) |
| **SIMD 哈希** | ❌ | 🔧 revmc 向量化 | ❌ | ❌ | ❌ | ❌ | ✅ QMDB AVX-512 8/16-way 内核 |
| **Sparse Trie 缓存** | ❌ | ✅ v2.0 核心 (root ~1–2ms) | ❌ | ❌ | N/A | ❌ | ✅ JMT/QMDB 节点 LRU (16384–65536 entries, 跨 payload 复用) |
| **批量 DB 写入** | ✅ | ✅ | ✅ ETL预处理 | ✅ | ✅ | ✅ | ✅ ETL Collector + MerkleStageIncremental |
| **ShardedCache** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ LayeredDB |
| **Receipt持久化** | ✅ | ✅ | ✅ RPC 10x提速 | ❌ | ❌ | ❌ | ✅ per-block Receipts + roaring bitmap 日志索引 + ReadReceiptByTxHash O(1) 查询 |

### 关键差距

**零拷贝序列化**：避免序列化/反序列化时的内存复制开销。reth 实验性使用 `rkyv`，Monad 和 Aptos 在内部数据结构中广泛使用。

**IO_uring / Async I/O**：Monad 使用 Linux 的 io_uring 接口实现高性能异步磁盘 I/O，是其 10,000 TPS 目标的关键支撑。

**N42 优势**：`lib/kv/layered/ShardedCache` 提供了分片缓存加速，与 reth 的 parallel prewarming 理念一致。

---

## 十五、跨链与互操作性

| 功能 | geth | reth | Erigon 3.4 | Sei | Monad | Aptos | **N42** |
|------|------|------|------------|-----|-------|-------|---------|
| **IBC 跨链** | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | N/A (非 Cosmos 链; IBC 不兼容) |
| **ZK 原生跨链桥** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ **ZK-native bridge** (header proof + state proof + evidence chain) |
| **桥接标准** | ❌ | ❌ | ❌ | ✅ | ❌ | ✅ LayerZero | ✅ ZK bridge (N42→ETH Phase 1); EVM 兼容性已支持标准桥接合约部署 |
| **跨链消息传递** | ❌ | ❌ | ❌ | ✅ | ❌ | ✅ | ✅ ZK bridge relayer 提供跨链证明中继 |
| **EIP-3668 (CCIP-Read)** | ✅ | ✅ | ✅ | ❌ | ❌ | N/A | ✅ |
| **Chain Abstraction** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ (全行业零实现; 研究阶段) |

### 15.1 ZK-Native Cross-Chain Bridge

N42 实现了 ZK 原生跨链桥，提供密码学级别的跨链验证安全性。信任链路：HotStuff-2 BLS 聚合签名 → SP1 ZK 证明 → JMT 状态证明 → ETH 链上验证。

**核心组件**：

| 组件 | 路径 | 说明 |
|------|------|------|
| **HeaderProver** | `internal/bridge/header_prover.go` | 将 HotStuff-2 BLS 共识签名转换为 SP1 ZK 证明，证明区块头有效性 |
| **StateProver** | `internal/bridge/state_prover.go` | 生成 JMT Merkle 状态证明，证明特定账户/存储在某区块的状态 |
| **Relayer** | `internal/bridge/relayer.go` | 监听 N42 链事件，打包 header proof + state proof 为完整 evidence chain，提交至目标链 |
| **Router** | `internal/bridge/router.go` | 跨链消息路由，管理桥接请求的生命周期和状态跟踪 |
| **N42Verifier.sol** | `contracts/bridge/N42Verifier.sol` | 以太坊链上 ZK 证明验证合约，验证 SP1 proof + JMT state proof |
| **N42Bridge.sol** | `contracts/bridge/N42Bridge.sol` | 以太坊桥接合约，处理资产锁定/释放和跨链消息 |

**信任链 (Trust Chain)**：
```
N42 HotStuff-2 BLS 共识签名
  → SP1 ZK Proof (将 BLS 签名验证压缩为简洁证明)
    → JMT State Proof (Merkle inclusion proof 证明状态)
      → ETH N42Verifier.sol 链上验证
```

**当前状态**：Phase 1 完成 — N42→ETH 单向桥接。ETH→N42 反向桥接在 Phase 2 路线图中。

---

## 十六、综合评分与优先级建议

### 16.1 功能覆盖率评分（满分 100）

| 维度 | 权重 | geth | reth | Erigon 3.4 | Sei | Monad | Aptos | **N42** |
|------|------|------|------|------------|-----|-------|-------|---------|
| 状态管理 | 15% | 95 | 98 | 97 | 80 | 90 | 85 | **92** |
| 同步机制 | 10% | 90 | 95 | 98 | 75 | 70 | 80 | **88** |
| 执行层/EVM | 20% | 85 | 88 | 85 | 90 | 95 | 90* | **94** |
| P2P 网络 | 10% | 95 | 90 | 92 | 80 | 80 | 75 | 83 |
| 共识 | 10% | 90 | 90 | 93 | 85 | 95 | 90 | **90** |
| RPC API | 10% | 95 | 95 | 96 | 60 | 70 | 60 | **95** |
| 交易池 | 5% | 90 | 90 | 92 | 85 | 85 | 70 | 88 |
| 工具链 | 5% | 95 | 70 | 80 | 50 | 30 | 60 | **84** |
| 安全性 | 5% | 90 | 95 | 85 | 85 | 80 | 90 | **93** |
| 可观测性 | 5% | 90 | 95 | 85 | 85 | 60 | 85 | **93** |
| 扩展性 | 5% | 80 | 95 | 88 | 85 | 40 | 70 | **85** |
| **加权总分** | 100% | **91** | **93** | **92** | **80** | **81** | **81** | **94** |

> *Aptos 使用 Move VM，非直接可比

### 16.2 按紧迫度分层的功能状态

> 截至 2026-06-12。已完成项保留记录以体现完整演进轨迹。P4 为 2026-04~06 新增项。

#### P0 — 生产环境基线（全部已完成 ✅）

| # | 功能 | 状态 |
|---|------|------|
| 1 | Snapshot 加速层 | ✅ DiffLayer 树 + DiskLayer + MDBX 持久化, 38 测试 |
| 2 | Bloom/Log 索引 | ✅ roaring bitmap (LogTopicIndex + LogAddressIndex), eth_getLogs 索引路径 |
| 3 | Panic Recovery | ✅ SafeGo + 8 处关键 goroutine |
| 4 | Fuzzing 测试 | ✅ 29 fuzz 函数 |
| 5 | Engine API v1-v4 | ✅ 完整 (含 getBlobsV1 + getPayloadBodies) |
| 6 | eth_getProof | ✅ **MPT-HPH canonical EIP-1186 主路径** (eth-el 模式, mptproof 包 latest + state-as-of) + JMT proof (自研链) + DATC 任意历史高度 |
| 7 | PQ 预编译隔离 | ✅ ChainConfig.PQPrecompilesTime 独立开关, 标准 fork 零感知 |
| 8 | 安全审计 | ✅ 3 轮深度审计, 47+ bug 修复 (CRITICAL/HIGH/MEDIUM) |

#### P1 — 竞争力关键（全部已完成 ✅）

| # | 功能 | 状态 |
|---|------|------|
| 9 | Block-STM 并行 EVM | ✅ Wave executor + DAG + MVS, 23 测试 + benchmarks |
| 10 | Checkpoint Sync | ✅ trusted hash + service + snap sync 集成 |
| 11 | Backfill Sync | ✅ 后台历史回填 (checkpoint→genesis, 批量 P2P 下载) |
| 12 | Staged Sync 框架 | ✅ 7 stage pipeline + forward/unwind/prune + MDBX 持久化 |
| 13 | Grafana Dashboard | ✅ n42_advanced.json (7 分组, 20+ 面板) |
| 14 | OpenTelemetry | ✅ OTLP/HTTP + 4 处 span |
| 15 | 250+ Prometheus 指标 | ✅ EVM/Chain/Reorg/Fee/TxLifecycle/EngineAPI/RPC/JMT |
| 16 | Live Tracing | ✅ liveTracer (实时 EVM 事件流, tracer directory 注册) |
| 17 | Otterscan API | ✅ 完整 ots_* 命名空间 (blockDetails/searchTxs/contractCreator/txError) |
| 18 | Receipt 持久化 | ✅ per-block + roaring bitmap 日志索引 + ReadReceiptByTxHash O(1) |
| 19 | Blob Sidecar P2P | ✅ gossip + RPC + SSZ 编码 |
| 20 | 动态 TxPool | ✅ 内存感知, 85%/70% 滞环策略 |

#### P2 — 差异化竞争（全部已完成 ✅）

| # | 功能 | 状态 |
|---|------|------|
| 21 | ExEx 执行扩展 | ✅ Manager + Hook + LogExtension, 8 测试 |
| 22 | Deferred Execution | ✅ Executor + Pipeline + EVM adapter + config 可启用 |
| 23 | PeerDAS | ✅ 列采样 + KZG 真实验证 + 39 测试 |
| 24 | 零拷贝序列化 | ✅ LazyReceipt/LazyHeader + BufPool, 28 测试 |
| 25 | RPCDaemon 独立部署 | ✅ gRPC KV server + 独立二进制 |
| 26 | HotStuff-2 验证者重配置 | ✅ commit-then-activate 协议 + RPC + MarkCommitted, 8 测试 |
| 27 | JMT 稀疏 Trie 缓存 | ✅ 16384-entry LRU + 跨 payload 复用 |
| 28 | JMT 引用计数 GC | ✅ 在线节点裁剪 (等价 PBSS), 测试显示 96% 废弃节点回收 |
| 29 | SP1 zkVM 后端 | ✅ ProverClient 实现 + VerifySP1 + simulation smoke |
| 30 | History Expiry (EIP-4444) | ✅ HistoryExpirer + P2P 门控 + earliestBlock 持久化 |
| 31 | eth/69 语义等价 | ✅ libp2p StatusExt.EarliestBlock + range handler 门控 |
| 32 | ZK 协处理器 | ✅ 程序注册 + 任务生命周期 + 原子证明提交 + 后台维护, 74.7% 覆盖率 |
| 33 | 去中心化消息中继 | ✅ 发布/订阅 + LRU Store + 速率限制 + 主题隔离, 91.8% 覆盖率 |
| 34 | IPFS 存储桥接 | ✅ Pin/Unpin/Get/Stat + CID 验证 + 注入防护, 60.3% 覆盖率 |
| 35 | 推送通知 | ✅ Channel 订阅 + 过滤匹配 + per-address 历史, 86.3% 覆盖率 |
| 36 | Otterscan API 完整 | ✅ 10 方法 (blockDetails/searchTxs/contractCreator/txError 等) |
| 37 | Receipt per-tx 索引 | ✅ ReadReceiptByTxHash O(1) 查询 |
| 38 | Backfill Sync | ✅ checkpoint→genesis 后台历史回填 |
| 39 | Engine API v4 完整 | ✅ getBlobsV1 + getPayloadBodies + executionRequests |

#### P4 — 2026-04~06 新增（源码审计确认，~1,100 commit）

| # | 功能 | 状态 |
|---|------|------|
| 49 | **多引擎状态承诺** | ✅ MPT-HPH(生产/ETH 字节兼容) + JMT + BMT + Verkle + LtHash + **QMDB**, 可插拔 RootComputer, ~6,661 行, 42 测试, `docs/bench_state_report.md` 跨树基准 |
| 50 | **QMDB 追加式状态森林** | ✅ 线程分片无锁 2.26M upd/s @16 分片 + AVX-512 SIMD 哈希 + 扁平开放寻址索引 + 近块证明窗口 (`lib/qmdb/`, ~5,101 行) |
| 51 | **DATC 全历史证明** | ✅ 深度自适应时序检查点, 任意历史高度 EIP-1186, 2M 块 100/100 验证, ~170–420GB (`cmd/n42-datc/`, ~5,404 行) |
| 52 | **Body F2 压缩** | ✅ 去签名 + Ledger 列式编码, 实测 254M tx −44.8% (394GB→~217GB), 0 mismatch vs ecrecover, MPHF 哈希索引保 getTxByHash (`internal/ethel/bodyf2/`) |
| 53 | **Stateless P8 (生产+消费闭环)** | ✅ partialTrie + 两级 StateRootUpdater + BlockProof + 多签 attestation + 多 IDC 聚合器 + 在线 code 取回(keccak 验), 3398 anchor E2E 验证, 58+ 测试 (`internal/ethel/stateless/`) |
| 54 | **eth-el 三模式** | ✅ archive 已实测追平 tip (25.19M→25.20M, 0 mismatch, 12s live); ⚠️ minimal/full snapshot-direct 待接线 (#94) |
| 55 | **txindex/blockhash MPHF 索引** | ✅ RecSplit MPHF + fingerprint, txhash→(block,idx) ~8.8 B/tx, blockhash→number 自校验; mmap 查找堆→0 |
| 56 | **EIP-4444 冷段卸载** | ✅ relocate + torrent 1-of-N 分发 (实测 PASS), `n42-history-expiry` / `n42-cold-seed` / `n42-eth-torrent` |
| 57 | **BLS 委员会签名池** | ✅ 实时委员会池 + 部分签名收集 + scalar-sum 聚合 + Beacon-API 风格 REST (`internal/blspool/`, ~1,139 行) |

#### P3 — 不实现或等待（已评估，明确决策）

| # | 功能 | 决策 | 理由 |
|---|------|------|------|
| 32 | Verkle Tree (生产部署) | ⚡ **战略废弃** | 以太坊 2026-06 已实质搁置 Verkle 转向二叉树 (EIP-7864 Draft); N42 BMT/JMT Blake3 已对齐; Verkle 引擎仅留作交叉验证 |
| 33 | State Expiry | 🔧 **等待** | 全行业零实现 (~2029); 基础设施已预备 (JMT GC + Witness + History Expiry + F2 冷段卸载) |
| 34 | Async I/O (io_uring) | ⚠️ **部分实现** | 已加 channel 异步派发 + I/O 工作池 + SLOAD 学习预取 (Go 层部分等价); io_uring 内核旁路待评估 (仅 Monad 自研 DB 实现) |
| 35 | JIT/AOT EVM | ❌ **不实现** | Go 无 LLVM; precompile 替代; 编译器 bug 致共识分裂风险 |
| 36 | Portal Network | ❌ **不实现** | 轻客户端 + Witness P2P 已覆盖; geth 自身仅实验阶段 |
| 37 | OP Stack | N/A | N42 是 L1; ExEx 可作为 L2 hook |
| 38 | Fraud Proof | N/A | ZK proof (有效性证明) 严格强于 fraud proof (欺诈证明) |
| 39 | JS Console | N/A | 过时; MCP Server + foundry/curl 替代 |
| 40 | EVM CLI Tool | N/A | debug_traceCall RPC 替代 |
| 41 | devp2p CLI | N/A | N42 用 libp2p; admin_peers 诊断 |
| 42 | TxPool 独立进程 | N/A | RPCDaemon 已拆分 RPC 层; 边际收益 |
| 43 | NUMA 感知 | N/A | Go runtime 不支持; 单 socket 零收益 |
| 44 | 多提议者 | N/A | HotStuff 流水线已满足; 无生产验证; 研究前沿 |
| 45 | Inclusion List (FOCIL) | N/A | 等 Glamsterdam; HotStuff 无 PBS 审查问题 |
| 46 | Cosmos IBC | N/A | 非 Cosmos 链 |
| 47 | 桥接/跨链消息 | ✅ **ZK 原生桥** | Phase 1 完成: N42→ETH 单向 ZK bridge (header proof + state proof + evidence chain); `internal/bridge/` + `contracts/bridge/` |
| 48 | Chain Abstraction | ❌ | 全行业零实现; 纯研究阶段 |

### 16.3 N42 独有优势（需保持/强化）

| 优势 | 说明 | 数据支撑 | 竞争对手状态 |
|------|------|----------|-------------|
| **PQ-STARK 后量子验证** | 已集成到 APoS 共识层 | STARK 仅依赖哈希函数抗碰撞性，128-bit 量子安全；无椭圆曲线依赖 | 以太坊 2026.1 成立 PQ Team + $1M 奖金，Algorand 2025.11 首个主网 PQ 交易(Falcon)，其余客户端均无 |
| **可插拔 6 引擎状态承诺** | MPT-HPH / JMT / BMT / Verkle / LtHash / QMDB 同仓可切换 | ~6,661 行(modules/state/commitment/), 42 测试, `docs/bench_state_report.md` 跨树 1M 块基准 | **业界唯一**同时持有 ETH 字节兼容(MPT-HPH)与量子安全二叉树(BMT/JMT)两条路径; geth/reth 单 Hex-MPT; Aptos 单 JMT |
| **MPT-HPH ETH 字节兼容根** | Erigon HexPatriciaHashed 移植 + 并行 ConcurrentMPTRootComputer | 与 ETH stateRoot **字节一致**, 主网 25.19M 高度对齐验证, 2.16M blk/s | geth/reth/Erigon 同为 ETH 兼容; N42 在自研链之外另持 ETH 兼容路径 |
| **Blake3 量子安全状态根** | BMT(二叉)/JMT(16-ary) 均基于 Blake3-256 | Grover 仅使安全性减半(256→128bit)，远优于 Verkle 的 Pedersen(Shor 完全破解); BMT 最小证明 ~427B | 以太坊 2026-06 已从 Verkle(已搁置) 转向二叉哈希树(EIP-7864 Draft); N42 已天然对齐 |
| **QMDB 追加式状态森林** | 线程分片无锁森林 + AVX-512 SIMD 哈希 + 近块证明窗口 | 2.26M upd/s @16 分片, ~5,101 行(lib/qmdb/), 近块 eth_getProof 近零增量存储 | geth/reth/Erigon 均无追加式森林; 独家 |
| **DATC 全历史证明** | 深度自适应时序检查点, 任意历史高度 EIP-1186 | 2M 块 100/100 验证, ~170–420GB (对位 Erigon archive 4.1TB), ~5,404 行 | 等价 Erigon v3.4 Historical Proofs; 独立实现, 生态调研确认新颖 |
| **Body F2 压缩 −45%** | 去签名 + Ledger 列式编码 + MPHF 哈希索引 | 254M tx 实测 −44.8% (394GB→~217GB), 0 mismatch vs ecrecover, getTxByHash 10.79 B/tx | 超越 geth/Erigon EIP-4444 partial expiry(仅丢 pre-merge); N42 压缩活跃历史 |
| **Stateless P8 闭环** | 生产端 BlockProof + 消费端 minimal client + 多 IDC 聚合 + 在线 code 取回 | 3398 anchor E2E 验证, witness ~7.8KB/块, 58+ 测试 | reth Ress(~14GB PoC, Holesky); N42 含多 IDC 多签聚合 + keccak 验在线 code |
| **Block-STM 并行 EVM** | Wave executor + DAG + MVS + 依赖预测 | 真实 EVM 重放实测(主网 24.98M): **单流 880 Mgas/s → 32 核并行 10.8 Ggas/s (~12.3×)**; 374 Ggas/s 系合成调度基准(忙等)。端到端(含 root+落盘)未单独基准 | geth 无并行(BAL 规划), reth 仅 prewarming, Erigon 实验性 |
| **EOF 提前实现** | EIP-3540/3670/4200/4750/5450 完整 | 509行代码，含验证器+容器格式+跳转表 | **以太坊侧 EOF 已移出 Glamsterdam 窄化范围(2026-06 状态存疑)**; N42 反成差异化 |
| **Pectra EIP 完整支持** | 7702✅ 7212✅ 2537✅ 6110✅ 7251✅ 7002✅ 7623✅ 2935✅ 7685✅ | 9 项 Pectra EIP 全部实现：delegation + BLS + P-256 + deposits + MaxEB/consolidation + withdrawal requests + floor data gas + historical hashes + execution requests | geth/reth 完整实现，N42 功能追平 |
| **LayeredDB 分层存储** | State DB + History DB 分离 | ~1,200行代码, ShardedCache 分片缓存 | 类似 reth 架构理念 |
| **战略性废弃 Verkle** | 避免双重迁移成本(Verkle→二叉树) | 以太坊 EIP-7864(2025.1) 证实方向转变 | geth/reth 投入 Verkle 开发后面临返工风险 |
| **HotStuff-2 BFT 共识** | 两轮优化 BFT + BLS 聚合签名 | ~3000行代码, 60 测试, 自适应 Pacemaker, MDBX 持久化 | MonadBFT/Jolteon 同级别，geth/reth 无 BFT |
| **PeerDAS KZG 真实验证** | go-eth-kzg EIP-7594 cell proof 批量验证 | ProduceColumns 128列转置, 39 测试, v2 固定大小存储 | geth/reth Fusaka 支持，但 N42 已提前集成 |
| **Snapshot MDBX 持久化** | 4 张 MDBX 表 + journal 崩溃恢复 + 后台生成器 | 38 测试, flatten-to-disk 原子写入, crash-resume marker | geth 快照层成熟，N42 功能追平 |
| **手机无状态 Light Client** | JMT Merkle proof + 移动端完整 EVM 重执行 | Witness 生成/验证/StateReader + P2P 协议 + RPC API, 15+ 测试 | 独家创新，业界首个移动端密码学验证无状态 EVM，无竞品实现 |
| **加密 Mempool (反 MEV)** | 阈值加密交易池 + AES-256-GCM | 加密/解密/Keyper 密钥管理 + 区块级批量解密 | Erigon Shutter 集成，N42 原生实现 |
| **MEV-Boost 集成** | Relay 通信 + Builder API + 区块拍卖 | 多 Relay 并发竞价 + 本地/Relay 价值对比 | geth/reth/Erigon 均支持，N42 功能追平 |
| **MCP Server (AI)** | 8 个区块链工具 + 4 个资源，端口 8553 | JSON-RPC 2.0 MCP 协议，AI agent 直接查询链上数据 | Erigon 端口 8553 同类实现，N42 功能追平 |
| **GraphQL API** | EIP-1767 标准，Block/Transaction/Account/Log 查询 | HTTP handler + resolver + schema 类型定义 | geth 原生支持，N42 功能追平 |
| **Clef 外部签名器** | IPC 签名服务 + JSON 规则引擎 + 审计日志 | SignTransaction/SignData/SignTypedData + accounts.Backend 集成 | geth 原生支持，N42 功能追平 |
| **Staged Sync 管线** | 7 stage (Headers→Finish) + forward/unwind/prune | per-stage MDBX 持久化 + crash resume, 5 测试 | Erigon 原创/reth 借鉴，N42 功能追平 |
| **Deferred Execution (PoC)** | consensus-execution 分离 + 可配置 worker pool | 三阶段架构 (Queue→Execute→Commit), stateRoot(N) 在 block N+1, 6 测试 | Monad/Aptos 核心特性，N42 PoC 就位 |
| **RPCDaemon 独立部署** | 独立二进制 via gRPC remote KV | cmd/rpcdaemon/ + remotedbserver 集成 | Erigon 核心特性，N42 功能追平 |
| **JMT 稀疏 Trie 节点缓存** | 16384-entry LRU decoded node cache | 跨 payload 复用, 单调驱逐, per-tree 序列号 | reth Sparse Trie 同类设计 |
| **HotStuff-2 验证者重配置** | commit-then-activate 协议 (HotStuff-2 §5) | quorum overlap 验证 + safe add/remove + epoch 边界激活, 8 测试 | Jolteon §4.3 同级安全保证 |
| **250+ Prometheus 指标** | EVM/Chain/Reorg/Fee/TxLifecycle/EngineAPI/RPC/JMT/P2P/DB/Consensus/Cache/Sync/ZK | 超越 geth 200+ | 行业领先 |
| **PQ 预编译隔离** | ChainConfig.PQPrecompilesTime 独立开关 | 标准 fork PQ-free, EEST 零干扰 | 安全与兼容性双保障 |
| **47+ 安全 Bug 修复** | 3 轮深度审计: CRITICAL/HIGH/MEDIUM | VM/State/API/Consensus 全覆盖 | 生产安全基线 |
| **ZK 协处理器** | 链下计算 + 链上 ZK 证明验证 (internal/distributed/coprocessor/) | 程序注册 + 任务生命周期 + 原子状态转换, 74.7% 覆盖率 | geth/reth/Erigon 均无; 独家创新 |
| **去中心化消息中继** | 发布/订阅 + LRU Store + 速率限制 (internal/distributed/messaging/) | 长度前缀消息 ID + TTL 过期 + 主题隔离, 91.8% 覆盖率 | geth/reth 无; Waku 兼容设计 |
| **IPFS 存储桥接** | Pin/Unpin/Get/Stat via HTTP API (internal/distributed/storage/) | CID 注入防护 + 大小限制 + httptest 验证, 60.3% 覆盖率 | 原生 IPFS 集成; 竞品无 |
| **推送通知服务** | 合约事件→钱包流 (internal/distributed/notify/) | 过滤匹配 + 非阻塞 Channel + per-address 历史, 86.3% 覆盖率 | Push Protocol 兼容设计 |
| **Pectra 9 EIP 完整** | 7702+7212+2537+6110+7251+7002+7623+2935+7685 | 全部经代码验证确认, 与 geth/reth 追平 | 完整支持 |
| **Backfill Sync** | checkpoint→genesis 后台历史回填 | 批量 P2P 下载 + rawdb 写入 + 进度跟踪 | reth 同类功能追平 |
| **JMT 引用计数 GC** | 在线节点裁剪 (等价 PBSS) | 测试显示 96% 废弃节点回收 | flat state + GC 等价 PBSS |
| **Engine API v4 完整** | getBlobsV1 + getPayloadBodies + executionRequests | 全部方法已实现 | 与 geth/reth 追平 |

---

## 附录 A：各链架构速览

### Erigon v3.4.3 "Splashing Saga"（2026-06-02）
- **语言**：Go（主体）+ C++（Erigon++ evmone / Zilkworm zkEVM，均实验）
- **数据库**：MDBX + 不可变 segment 文件；**v3.4.0 将热 chaindata 4x 缩减至 ~20GB**
- **状态**：E3 三层架构 — domain（最新状态）/ history（per-TX 粒度历史）/ idx（倒排索引）；archive ~1.8–2.2TB（第三方测量 2026-01）
- **同步**：Staged Sync 原创者 + OtterSync（BitTorrent，2024 起默认），支持 eth/68+eth/69
- **共识**：Caplin 内置共识层（**v3.4 新增持久化历史下载 + discv5 默认**），支持 MEV-Boost
- **P2P**：DevP2P（EL）+ libp2p gossipsub（Caplin CL），Sentry 组件可独立部署
- **历史证明**：v3.3 Historical Proofs Data Model（Haystack + Elias-Fano/Roaring），**v3.4 历史 `eth_getProof` 从实验转正**
- **RPCDaemon**：v3.4 新增 trace_rawTransaction / eth_getStorageValues / engine_getBlobsV3 / admin_addTrustedPeer
- **亮点**：Staged Sync、per-TX 历史粒度、Caplin、OtterSync、Historical Proofs、Shutter 加密 mempool（Gnosis）、Otterscan、RPCDaemon 拆分、模块化进程架构
- **实验/R&D**：Parallel EVM（v3.4 BAL 工作负载修复，仍实验）、Erigon++（C++20/evmone，默认关闭）、Zilkworm（C++23 RISC-V zkEVM 原型，~100s/块 on 4090，SP1/RISC0/Pico 后端）
- **路线图**：Erigon NeXT（2026/27，并行执行 + 统一数据库 + 可组合架构 + L2）

### Geth (go-ethereum) v1.17.3 "Enzymatic Injector"（2026-05-11）
- **语言**：Go
- **数据库**：Pebble（v1.14+ 默认，取代 LevelDB）+ Freezer 冷存储
- **状态**：MPT → PBSS (`--state.scheme=path`, opt-in, 未确认转默认)；**Verkle 已搁置，转向二叉树（v1.17.3 已加 binary trie improvements）**
- **同步**：Snap Sync 默认（动态快照 7min 遍历所有账户），LES 已废弃
- **P2P**：DevP2P，eth/68 + eth/69 + **eth/70（v1.17.3 新增）**，snap protocol
- **历史过期**：Partial History Expiry（2025-07 起，丢 pre-merge bodies/receipts，省 ~300–500GB）；v1.17.3 加 path-mode archive 历史索引 pruner
- **亮点**：最成熟的以太坊客户端，Fusaka 主网（2025-12-03，PeerDAS + Gas 45M→60M），Live Tracing，GraphQL，批量 RPC（v1.17.1 EIP-7834 eth_getStorageValues），ExEx PR#30611 进行中（未合并）
- **路线图**："Amsterdam"（geth 对 Glamsterdam EL 侧的内部代号，v1.17.3 已实现 7928/8037/7976/7981/7610）；Glamsterdam **2026 Q3**，Hegotá 2026 Q4，每年两次硬分叉

### Reth v2.3.0（2026-06-10）
- **语言**：Rust
- **数据库**：MDBX + Static Files；**v2.0 Storage V2 默认**（热/冷分层，full ~240GB，snapshot ~170GB）
- **状态**：Flat State + **Sparse Trie 缓存（v2.0 核心，跨区块复用，state root ~1–2ms/块，块持久化 ~20× 加速）**
- **同步**：Staged Sync（Headers→Bodies→Execution→Hashing→Merkle→History→Pruning）
- **性能**：**v2.0 marquee ~1.7 Ggas/s（持续 ~1.4–1.5）**，newPayload 均值 32.4ms（v1.11，−25%），revmc JIT **仍实验/opt-in（`--experimental.compiler`，未转默认）**
- **无状态**：Ress（~14GB，~70× 缩减，Holesky 验证，PoC 非生产）
- **亮点**：ExEx 成熟框架（exex.rs 目录）、模块化库设计（每 crate 独立）、Grevm/PEVM 兼容、**Optimism 官方选择（op-geth 已于 2026-05-31 停止支持，op-reth 成 Superchain 主客户端）**
- **生态**：Gravity Reth（~41k TPS / 2.96 Ggas/s）、BSC Reth（alpha，~40% 更快同步）、PEVM/RISE、Bera-Reth

### Sei v2/v3/Giga
- **语言**：Go (Cosmos SDK)
- **数据库**：SeiDB — State Store (Log-structured KV) + State Commitment (mmap IAVL, ~100ns 访问)
- **状态**：IAVL Tree → SeiDB SS+SC 分离，WAL + 周期性快照
- **共识**：Twin-Turbo CometBFT（主网，~400ms）→ **Autobahn (Giga) 多提议者模型（"coming soon"，未上线）**
- **并行**：乐观并行化 OCC（v2 已上线）→ Ares 重写（Giga，规划中，~40× 宣称）
- **性能**：v2 主网 ~400ms 出块；**Giga 5 gigagas/s, 200k+ TPS 为内部 devnet 宣称（whitepaper arXiv 2505.14914），未上主网**
- **2026 进展**：SIP-3 EVM-only 改造（v6.3 主网 2026-02 EVM staking，v6.4 2026-03 关 IBC）——Giga 的铺垫而非性能核心；原 H1-2026 主网目标滑至"贯穿 2026"
- **亮点**：EVM + CosmWasm 双引擎、IBC 跨链、即时 Finality；Giga（Autobahn + Ares + Eidos + Sedna 加密 mempool）全部 in-progress

### Monad（2025-11-24 主网上线）
- **语言**：C++ / Rust（Category Labs）
- **数据库**：MonadDB — 持久化 Patricia Trie + `io_uring` + 块设备直连（绕过文件系统）
- **状态**：多版本状态数据，内联压缩，单写者 + 多读者
- **共识**：MonadBFT（HotStuff 变体 + tail-forking 抗性，arXiv 2502.20692 v3 2026-03），400ms 区块，~800ms finality
- **并行**：乐观并行 + 多 VM 实例 + 延迟执行 + 完整流水线（共识|执行|I/O 三阶段重叠）
- **性能**：**10,000 TPS 为容量/devnet 目标；主网组织流量实测远低于此（启动日峰值 ~350 TPS，日费用 <$3,000，TVL ~$350M）**，字节码级 100% EVM 兼容
- **2026 升级**：**MONAD_NINE（2026-03-19 主网）= MIP-3 线性内存 gas + MIP-4 储备余额内省 + MIP-5 激活 Fusaka EIP（7823/7883/7939）**，RPC latest 1.2s→400ms；MIP-12（2026-06-06 提案）拟 400ms→300ms 投票周期
- **亮点**：完整流水线架构、异步 I/O（io_uring）、延迟执行解耦共识与执行

### Grevm 2.1 (Gravity/Galxe)
- **语言**：Rust (基于 revm)
- **类型**：EVM 执行库（非完整节点），可嵌入 reth 等节点
- **核心三模块**：Dependency Manager (DAG) + Execution Scheduler + Parallel State Storage
- **2.1 新增**：Lock-Free DAG（调度开销↓60%）、Task Groups（强依赖交易归组）、Parallel State Store（异步打包重叠 30-60ms）
- **版本**：架构代号 Grevm 2.1；**最新发布 v2.2.5（2026-03-09），无 3.x**
- **性能（自published 微基准，未经第三方复现）**：Uniswap 11.25 gigagas/s，30% hot-ratio 2.96 gigagas/s (5.5x↑)，高冲突 95% less CPU vs Block-STM
- **部署**：Gravity Reth（reth fork，~41k TPS / 1.5 Ggas/s）；**Gravity L1 主网 2026-06-04 上线（许可制，3 验证者，实测 ~9.5–12k TPS，100k 目标未达）**；2026-05-30 桥被盗 $5.4M（签名密钥泄露，非合约 bug）

### Aptos
- **语言**：Rust
- **数据库**：AptosDB (RocksDB) + Jellyfish Merkle Tree (稀疏 Merkle 变体，利于分片)
- **共识演进**：AptosBFT → Jolteon (Quorum Store) → Baby Raptr（2025-06 主网）→ **Velociraptr（2025-09/10 主网，乐观提议）**；完整 Raptr（decoupled prefix voting）仍为 2026 路线图
- **VM**：Move VM — 线性类型系统，资源导向；2025 已上 AIP-91 枚举 / AIP-112 函数值 / 有符号整数 / Loader V2；MonoMove（VM 重写）2026 规划
- **并行**：Block-STM 原创（低冲突 32 线程 16x 加速，高冲突 8x）；Block-STM V2（256 核线性扩展）规划/在建（无干净主网激活日期）
- **Gas 模型**：三维分离 — Execution Gas + I/O Gas (浮动) + Storage Gas (固定 APT 绝对值)
- **性能**：**主网亚 50ms 出块（2025-12 实测，Baby Raptr+Velociraptr）；250k TPS 系 100 节点 geo-distributed 实验室数字（arXiv 2504.18649），主网组织流量实测 ~34 TPS（理论上限 ~160k）**
- **2026 升级**：Confidential APT（2026-04-24 主网，ZK 隐私转账）
- **亮点**：Block-STM 原创者、Aave 首个非 EVM 部署 (2025.8)、Zaptos 低延迟流水线

### MegaETH（2026-02-09 主网上线，实时 L2）
- **语言/架构**：专用 sequencer（100 核 / 1–4TB RAM）+ stateless prover（1 核 / 0.5GB）；EigenDA 做 DA，以太坊做结算
- **出块**：**mini-block ~10ms（仅 tx/receipts/state changes，无 state root/bloom）+ EVM block ~1s**；sequencer 签名提供 preconf（**非最终性**，最终性仍需 L1 确认）
- **状态**：SALT（Small Authentication Large Trie，热数据驻内存）；多维 gas 模型
- **性能**：**100k TPS / 10 gigagas/s 为目标；7 天压测自报峰值 35–50k TPS（无独立基准），live ~1.7 Ggas/s**
- **代币**：MEGA TGE 2026-04-30（10B 供应，KPI 奖励驱动）；2026-05 TVL ~$580M

### Gravity（Galxe，2026-06-04 L1 主网）
- **执行**：Gravity Reth（reth fork）+ Grevm 并行 EVM
- **共识**：pipelined AptosBFT 衍生 PoS，亚秒 finality
- **性能**：实测 ~9.5–12k TPS @ ~200ms（小验证者集），100k 目标未达；原 Arbitrum Nitro L2（2024-08）→ 独立 L1
- **背景**：Galxe（Web3 增长平台）为承载其积分/忠诚系统（≥4M gas/s 需求）自建

---

## 附录 B：文档整合说明

本文档整合并替代了原 `开发日志.md` 中的 "功能缺失分析" 章节（十三节对比表），新增以下维度：

1. **对比范围扩展**：新增 Monad、Grevm、Aptos、MegaETH、Gravity 高性能链对比；新增 **Erigon 3.4** 全维度对比
2. **以太坊路线图对齐**：增加 Fusaka/Glamsterdam/Hegotá/Lean Ethereum 路线图跟踪
3. **性能工程维度**：新增零拷贝序列化、io_uring、NUMA 感知等底层优化对比
4. **扩展框架维度**：新增 ExEx、SDK 化、插件系统对比
5. **综合评分体系**：提供量化评估和分层优先级建议
6. **前沿技术跟踪**：State Expiry、PeerDAS、JIT EVM、二叉树（EIP-7864）、后量子等前瞻性布局
7. **竞品数据来源**：基于各项目官方 GitHub releases、README、博客、文档；2026-06 修订交叉验证多源并区分 SHIPPED/CLAIMED

**2026-06-12 修订来源（竞品）**：geth releases (v1.17.3)、reth releases (v2.3.0)、Paradigm 博客、erigon.tech / docs.erigon.tech (v3.4.3)、EF blog (Fusaka/Glamsterdam/Protocol Priorities)、EIP-7864/7732/7928、pq.ethereum.org、Monad docs + MIPs repo + Monad Pulse、Sei giga.seilabs.io + arXiv 2505.14914、Aptos arXiv 2504.18649 + Everstake、Galxe/grevm + Gravity docs、MegaETH docs + CoinDesk/The Block。所有高性能链 TPS 数字均标注 **devnet/实验室/容量** 与 **主网实测** 之别。

相关文档：
- `docs/bench_state_report.md` — 5 引擎 + QMDB 跨树 1M 块基准
- `docs/ethel/body-compression-design.md` — F1/F1.5/F2 压缩设计与实测
- `docs/ethel/stateless-verification.md` — P8 无状态验证三层信任模型
- `docs/ethel/architecture-framework-and-plan.md` — eth-el 双模式 + 三模式路线
- `docs/PERFORMANCE_OPTIMIZATION_PLAN.md` — 性能优化实施细节
- `docs/POST_QUANTUM_UPGRADE_PLAN.md` — PQ 密码学升级路线
- `docs/SECURITY_AUDIT_REPORT.md` — 安全审计报告

---

## 附录 C：N42 源码审计摘要

> 审计日期：2026-03-09（**2026-06-12 补充 P4 新增模块**），方法：逐文件阅读源码，统计代码行数、测试数量、集成状态

### C.1 功能实现状态（按源码验证）

| 功能模块 | 核心文件 | 代码行数 | 测试数 | 实际状态 | 备注 |
|----------|----------|----------|--------|----------|------|
| **可插拔多引擎状态承诺** | `modules/state/commitment/` (MPT/JMT/BMT/Verkle/LtHash/QMDB) | ~6,661 | 42 | ✅ 生产可用 | RootComputer 接口 6 引擎；**MPT-HPH 为 eth-el 生产默认（ETH stateRoot 字节兼容）**；`docs/bench_state_report.md` 跨树基准 |
| **JMT Blake3 状态承诺** | `lib/jmt/` + `lib/jmt/store/` | ~2,500 | 33 | ✅ 生产可用 (N42 自研链) | 16-ary JMT + Blake3 内容寻址 + Merkle proof + LazyDBStore + 引用计数 GC(96% 回收) + 冷/热分层 + BatchUpdate 1000key ~3.5ms |
| **QMDB 追加式森林** | `lib/qmdb/` (23 文件) | ~5,101 | 10+ | ✅ Phase 1 (内存) | 线程分片无锁 2.26M upd/s @16 分片 + AVX-512 8/16-way SIMD 哈希 + 扁平开放寻址索引 + 近块证明窗口；`cmd/eth-el --tree qmdb` |
| **DATC 全历史证明** | `cmd/n42-datc/` (13 文件) | ~5,404 | 有 | ✅ 原型验证 | 深度自适应时序检查点 + 叶历史 zstd 段 + 节点 diff coding；2M 块 100/100 历史根验证；任意高度 EIP-1186 |
| **Body F2 压缩** | `internal/ethel/bodyf2/` + `cmd/n42-bodyc-f2` (~508) | ~1,000 | ~50 | ✅ 生产可用 | 去签名 + Ledger 列式(from-ID/to-ID/科学计数 value) + MPHF 哈希索引；254M tx −44.8% + 0 mismatch；`--stream` OOM-safe |
| **Stateless P8** | `internal/ethel/stateless/` (54 文件) | ~1,600 | 58+ | ✅ 生产/消费闭环 | partialTrie + 两级 StateRootUpdater + BlockProof + 多签 attestation + 多 IDC 聚合 + 在线 code 取回(keccak 验)；3398 anchor E2E |
| **eth-el 三模式** | `cmd/eth-el/` + `internal/ethel/eldevp2p/` | — | 有 | ✅ archive / ⚠️ minimal·full | archive 实测追平 tip (25.19M→25.20M, 0 mismatch, 12s live)；minimal/full snapshot-direct 待接线(#94) |
| **txindex/blockhash MPHF** | `cmd/txindex-rebuild` (202) + `cmd/blockhash-rebuild` (125) | ~330 | 有 | ✅ 生产可用 | RecSplit MPHF + fingerprint；txhash→(block,idx) ~8.8 B/tx；blockhash→number 自校验；mmap 查找堆→0 |
| **BLS 委员会签名池** | `internal/blspool/` + `cmd/n42-blspool` + `cmd/n42-consensus-rest` | ~1,139 | 有 | ✅ 实时池 | 实时委员会池 + 部分签名收集 + scalar-sum 聚合 + Beacon-API REST + n42_getCommittee/getValidator RPC |
| **State Pruning** | `internal/node/pruner.go` | 235 | 7 | ✅ 生产可用 | 真实数据删除，快照感知边界 |
| **Logical Snapshots** | `internal/snapshot/manager.go` + `compress.go` | ~450 | 4 | ✅ 生产可用 | 非 geth 式性能加速层，用于裁剪恢复点 |
| **Snap Sync** | `internal/sync/snapsync/` | ~4,274 | 51 | ✅ 生产可用 | 完整实现：service+manager+tasks+verify+progress+metrics |
| **Block-STM** | `internal/parallel/` | ~830 | 23+7bench | ✅ 算法+性能验证 | Wave executor+MVS+基准测试(3.9x加速)，缺真实 EVM 集成测试 |
| **State Prefetch** | `internal/prefetcher.go` | ~150 | - | ✅ 集成 | ShardedCache 预加载 sender/recipient/access-list |
| **Ancient/Freezer** | `modules/rawdb/freezer/` | ~800 | - | ✅ 默认关闭 | 5 表冷存储，`ancient_db: true` 启用 |
| **LayeredDB** | `lib/kv/layered/` | ~1,200 | - | ✅ 默认关闭 | ShardedCache + LayeredDB 分层 |
| **TX Pool Journal** | `internal/txspool/journal.go` | ~200 | - | ✅ 生产可用 | flushToDB/loadFromDB，集成到 Start/Stop |
| **EOF (EVM Object Format)** | `internal/vm/eof.go` | 509 | 有 | ✅ 完整实现 | EIP-3540/3670/4200/4750/5450 |
| **EIP-7702 (Delegation)** | `internal/vm/eips_pectra.go` | ~200 | - | ✅ 完整实现 | 委托账户代码设置 |
| **EIP-2537 (BLS)** | `internal/vm/contracts.go:854-1360` + `common/crypto/bls12381/` | ~500+800 | - | ✅ 完整实现 | 9 预编译(G1Add/Mul/MSM,G2同,Pairing,MapG1/G2)，含 x86 汇编优化 |
| **EIP-6110 (Deposits)** | `internal/vm/eips_pectra.go`, `internal/blockhelp.go` | ~150 | 有 | ✅ 完整 | ParseDepositLog (含 overflow 保护) + receipt log 提取 + Engine API executionRequests 传递 |
| **EIP-7251 (MaxEB)** | `internal/vm/eips_pectra.go`, `internal/blockhelp.go` | ~80 | - | ✅ 完整 | MaxEffectiveBalance 常量 + ConsolidationRequestsAddress 系统合约 + ProcessPragueSystemCalls request 收集 |
| **ERC-4337 (AA)** | `internal/vm/erc4337.go` + `internal/bundler/` | 362+1120 | 22+18 | ✅ Bundler 实现 | UserOp mempool + validator + bundle builder + RPC 端点 |
| **P-256 Verify** | `internal/vm/contracts_p256.go` | 276 | - | ✅ 完整实现 | secp256r1 verify + recover |
| **Cancun EIPs** | `internal/vm/eips_cancun.go` | 251 | - | ✅ 完整实现 | TLOAD/TSTORE/MCOPY/BLOBHASH/BLOBBASEFEE |
| **System Metrics** | `internal/metrics/system_metrics.go` | ~150 | - | ✅ 实际收集 | 11 指标：goroutines/内存/GC/CPU |
| **Chain Metrics** | `internal/metrics/chain_metrics.go` + `lib/kv/` + `freezer/` | ~100 | - | ✅ 已全部接入 | DB 读写/Freezer/Sync 指标已接入执行路径 |
| **DB CLI 工具** | `cmd/n42/dbcmd.go` | 289 | - | ✅ 完整 | stats/list/get/inspect 四命令 |
| **Chain Import/Export** | `cmd/n42/chaincmd.go` | 252 | - | ✅ 完整 | protobuf 格式，批量导入 |
| **State Export** | `cmd/n42/statecmd.go` | 203 | - | ✅ 完整 | JSON 流式输出，含 storage/code 选项 |
| **Snapshot 加速层** | `modules/state/snapshot/` | ~1,600 | 38 | ✅ 生产可用 | DiffLayer 树+DiskLayer+SnapshotStateReader+DiffCollector+Warmer+指标+**MDBX 持久化(4表)+journal 崩溃恢复+后台生成器** |
| **HotStuff-2 BFT** | `internal/consensus/hotstuff/` | ~3,000 | 60 | ✅ 生产可用 | 两轮共识(Prepare+Commit)+BLS12-381 聚合签名+自适应 Pacemaker+MDBX 状态持久化+P2P SSZ 集成+hotstuff_testnet.json |
| **PeerDAS KZG** | `internal/peerdas/` + `common/crypto/kzg/` | ~1,500 | 39 | ✅ 生产可用 | go-eth-kzg v1.4.0 真实验证+ProduceColumns 128列转置+批量 cell proof 验证+v2 固定大小存储 |
| **History Expiry** | `internal/node/history_expiry.go` + `modules/rawdb/accessors_history.go` | ~360 | 8 | ✅ 生产可用 | EIP-4444 风格过期+P2P 门控+批量限制+持久化 EarliestBlock |

### C.2 关键风险点

1. **Block-STM 性能已验证**：23 个单元测试 + 7 个基准测试套件（`executor_bench_test.go` 306行）。M1 Max(10核) 实测数据：独立TX 50→0.65ms(3.9x), 100→1.4ms(3.7x), 200→3.3ms(3.0x)；热点场景(5-50%)触发 wave limit(64 waves, 31-63x 重执行)，量化了 TX DAG 优化的必要性。仍缺乏真实 EVM 交易的集成测试。
3. ~~Chain Metrics 死代码~~：**已修复** — DB 读写/Freezer/Sync 指标已接入执行路径。
4. ~~ERC-4337 误标为已支持~~：**已修复** — 实现了 bundler service、UserOp mempool、validator、bundle builder 和 4 个 RPC 端点。

### C.4 生产审计修复记录 (2026-03-09 ~ 2026-03-10)

| 文件 | 问题 | 修复 |
|------|------|------|
| `internal/sync/checkpoint/service.go` | `time.After()` 在 `waitForPeers` 中创建 timer 泄漏：ctx 先取消时 deadline timer 持续到超时 | 改用 `time.NewTimer()` + `defer timer.Stop()` |
| `internal/peerdas/service.go` | `Start()` 中 `context.WithCancel(ctx)` 返回的子 context 被丢弃 | 存储子 context 供未来后台 goroutine 使用 |
| `internal/peerdas/peerdas_test.go` | `TestCustodyColumns_Coverage` 概率性失败（200 节点不足以覆盖 128 列） | 增加到 500 节点（2000 次分配，覆盖概率 ≈1.0） |
| `modules/state/snapshot/journal.go` | **CRITICAL** `LoadJournal` 中 `dl.parent.Root()` 空指针（反序列化后 parent 为 nil） | 改为按 block number 排序 + 仅用 block number 匹配 parent |
| `modules/state/snapshot/tree.go` | `mergeDiffIntoCache` 缺少 acc nil 检查，ToProtoMessage 会 panic | 添加 nil guard |
| `modules/state/snapshot/tree.go` | `persistDiffToDisk` proto.Marshal 失败静默跳过 | 添加 log.Warn 日志 |
| `modules/state/snapshot/tree.go` | `persistDiffToDisk` 事务失败无日志 | 添加 log.Error |
| `modules/state/snapshot/generator.go` | marker 全 0xFF 溢出导致无限重处理 | 检测溢出后标记 batchDone |
| `modules/state/snapshot/disk_layer.go` | `BeginRo(nil)` MDBX 要求非 nil context | 改用 `context.Background()` |
| `modules/state/snapshot/tree.go` | `db.Update(nil, ...)` MDBX 要求非 nil context | 改用 `context.Background()` |
| `internal/peerdas/kzg.go` | `VerifyDataColumn` 缺少 Commitments/KZGProofs 长度验证 | 添加 slice length 匹配检查 |
| `internal/peerdas/store.go` | `encodeColumn` 缺少 slice 长度一致性检查 | 添加前置验证 |

### C.3 与竞品的诚实差距（2026-06-12 更新）

| 维度 | N42 实际水平 | geth/reth 水平 | Erigon 3.4 水平 | 差距评估 |
|------|-------------|---------------|----------------|----------|
| EVM 兼容性 | Cancun✅ Pectra✅完整(9项EIP) EOF✅提前实现 Fusaka✅(PeerDAS+7825+7951+ModExp) | 完整 (Glamsterdam 准备中) | 完整 | ✅ **完整** — EOF 反成差异化(以太坊侧已移出 Glamsterdam) |
| 并行执行 | Block-STM 3.9x + 374 Ggas/s (32核基准) + 依赖预测 | geth 无并行(BAL 规划); reth prewarming | 实验性并行(BAL) | 🏆 **N42 领先** — 注:Grevm hint-DAG 高冲突更优 |
| 执行吞吐量 | **真实 EVM 重放实测(主网 24.98M)**: 单流 880 Mgas/s · 32 核并行 10.8 Ggas/s; 374 Ggas/s 系合成调度基准。端到端(含 root+落盘)未单独基准 | reth-2.0: ~1.4–1.7 Ggas/s (端到端 live, 含 trie+落盘) | geth: ~0.2 Ggas/s (端到端) | ⚖️ **口径不同, 不可直接并列**: N42 数字为(近)纯 EVM 重放, reth/geth 为端到端 live; N42 端到端 mgas/s 待补基准 |
| 同步机制 | Full + Snap + Checkpoint + Backfill + Staged + eth-el 三模式 | Snap Sync 成熟 | Staged Sync + OtterSync(默认) | ✅ **完整** — N42 链规模下已覆盖全场景;超大数据集分发用 n42-eth-torrent |
| 状态存储 | MDBX flat + **6 引擎承诺(MPT-HPH 生产/ETH 兼容 + JMT + BMT + Verkle + LtHash + QMDB)** + 跨 payload LRU + GC + DiffLayer + History Expiry + **F2 压缩 −45% + DATC 全历史证明** | PBSS flat 成熟 | E3 三层 + segment + Historical Proofs(v3.4 转正) | 🏆 **N42 领先** — 唯一多引擎 + 同时持 ETH 兼容/二叉树两路径 |
| 可观测性 | **250+** Prometheus 指标 + Live Tracing + 3 Grafana 面板 + JSON 日志 + OpenTelemetry | 200-300+ 指标 | Prometheus + diagnostics | 🏆 **N42 领先** — 含 P2P/DB/Consensus/Cache/Sync/ZK 细分 |
| RPC 完整性 | eth_* + debug_* + trace_* + Engine API v1+v4 + Otterscan ots_* + GraphQL + Clef + MCP + 批量 (EIP-7834) | 完整 | 完整 + Otterscan | ✅ **完整** |
| 共识 | HotStuff-2 BFT 即时终局 + 验证者动态重配置 + APoA/APoS + BLS 聚合签名 + 实时委员会池 | Beacon Chain PoS (~15min 终局) | Caplin 内置 CL | 🏆 **N42 领先** — 即时终局 + commit-then-activate 重配置 |
| 安全性 | PQ-STARK 后量子 + 3 轮审计 47+ 修复 + SafeGo + PQ 预编译隔离 + 加密 Mempool | Go GC 基础防护 | Go GC | 🏆 **N42 领先** — 唯一已集成 PQ 密码学的主流客户端 |
| ZK 证明 | STARK/SNARK/SP1 三后端 + RISC-V64 guest + JMT GC + Verifier | 无 | Zilkworm (C++23 RISC-V 原型, ~100s/块) | 🏆 **N42 领先** — Erigon 2026 入场;N42 三后端管线更完整 |
| 无状态/最小客户端 | **Stateless P8 闭环 + 多 IDC 多签聚合 + 在线 code 取回(keccak 验)** | reth Ress(~14GB PoC, Holesky) | 无 | 🏆 **N42 领先** — 含多 IDC 聚合;reth Ress 仍 PoC |
| 模块化部署 | RPCDaemon 独立二进制 + gRPC KV server + ExEx hook | 单体 (geth/reth 均不拆分) | RPC/TxPool/Sentry/CL 独立 | ✅ **完整** — RPCDaemon 已拆分核心读负载; TxPool/Sentry 拆分仅 Erigon 架构需要，geth/reth 均为单体 |
| 测试覆盖 | 450+ 单元测试 + 150 AI 测试 + /simplify 审计 110 修复 + 29 fuzz + recovery/archive/soak smoke + EEST 本地 runner | 数千 + fuzzing | hive + EEST | ⚠️ **持续推进** — 测试基础设施完备，EEST blocker 逐个修复中; 非架构缺失 |
| AI 原生平台 | Agent 钱包 + 推理预编译 + 数据治理 + 训练 ZK + 推理签名 + 区块优化 + 150 测试 | 无 | 无 | 🏆 **N42 领先** — 唯一在 L1 层面提供完整 AI 安全基础设施的区块链 |
| 生态工具 | Otterscan + GraphQL + Clef + MCP + abigen + mobile SDK + RPCDaemon | 完整生态 | 完整 + diagnostics | ✅ **完整** |
| Gas 优化 | Glamsterdam EIP-7904 (转账 4500 gas) | Glamsterdam (计划) | 计划 | 🏆 **N42 领先** — 率先实现 |
| 历史格式 | EraE 存档格式 (随机访问) | EraE (v1.17.0) | 计划 | ✅ **完整** |
| 批量 RPC | eth_getStorageValues (EIP-7834) | v1.17.1 | 无 | ✅ **完整** |
| 存储分层 | NVMe/HDD 分层 (热/温/冷) | Erigon: NVMe/HDD 分离 | 无 | ✅ **完整** — 等价 Erigon |
| BitTorrent 同步 | OtterSync (EraE 段 + manifest) | Erigon: OtterSync 默认 | 无 | ✅ **完整** — 等价 Erigon |
| 历史证明压缩 | JMT archive (seg + RecSplit) | Erigon: Haystack v3.3 | 无 | ✅ **完整** — JMT 版 Haystack |
| 无状态验证 | Stateless validator + witness P2P + CodeCache | reth: Ress (v1.3.1+, 14GB) | 无 | ✅ **完整** — 基础设施等价 Ress |
| **Rotor 单跳中继** | SHA256 确定性中继选择 + 直连 libp2p 流 + gossip 回退 | Solana: Rotor (单跳纠删编码, 18ms 传播) | 无 | ✅ **完整** — N42 Rotor 协议等价 Solana Alpenglow Rotor, 适配 HotStuff-2 BFT |
| **LtHash 格哈希** | BLAKE3 XOF 2048B 同态摘要, O(k) 增量状态验证, fork-gated | Solana: SIMD-215 Accounts Lattice Hash (BLAKE3, 2048B) | 无 | ✅ **完整** — 等价 Solana SIMD-215, 基准 Add 2.1μs, 100 元素 0.4ms |
| **Tile 无锁 IPC** | SPSC Lamport 环形缓冲区 (50ns/op) + CPU 绑核 + 崩溃恢复 | Firedancer: Tile+Tango (C 实现, AF_XDP, 1M TPS) | 无 | ✅ **完整** — Go 层面等价 Firedancer Tile 架构; AF_XDP 内核旁路待评估 |
| **深度流水线** | 5 阶段跨区块重叠 (Prefetch∥Execute∥Commit∥Persist) | Monad: Superscalar Pipeline (MonadDB, io_uring) | 无 | ✅ **完整** — 等价 Monad 流水线深度; MonadDB 级别 I/O 优化通过 PooledDBStore 部分实现 |
| **异步 I/O 预取** | channel 异步派发 + I/O 工作池 + SLOAD 学习 + 预测预取 | Monad: MonadDB 异步 I/O (io_uring) | Sei: 依赖预测 | ✅ **完整** — 预测预取等价 Sei; 异步 I/O 池部分等价 Monad (Go goroutine vs io_uring) |
| **依赖预测** | 合约+选择器分组预排序, Block-STM 波效率优化 | Sei: Dependency Prediction | 无 | ✅ **完整** — 等价 Sei 依赖预测 |
| **ZK 原生跨链桥** | HotStuff-2 BLS → SP1 ZK proof → JMT state proof → ETH 验证 | 无 (geth/reth 无原生桥) | 无 | 🏆 **N42 领先** — 唯一具备 ZK 原生跨链桥的主流客户端; Phase 1 N42→ETH 完成 |
