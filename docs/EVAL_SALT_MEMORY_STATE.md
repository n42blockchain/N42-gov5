# 深度评估：内存状态字典树 / SALT (In-Memory State Trie)

> MegaETH SALT 架构在 N42 中的实施评估
> 评估日期：2026-03-24

---

## 1. 功能概述

**SALT（Small Authentication Large Trie）**：将完整状态认证结构驻留 RAM，用 Pedersen 承诺替代 Merkle hash，每次状态更新仅需 4 次椭圆曲线乘法而非完整树遍历。MegaETH mainnet 达到 35K TPS / 10ms 出块。

---

## 2. 关键发现：N42 在当前规模下已是"准内存数据库"

### 2.1 N42 状态规模

| 指标 | N42 mainnet | Ethereum mainnet |
|------|------------|-----------------|
| 区块数 | ~10M | ~19M |
| 热状态估计 | **2-20 GB** | ~250 GB |
| 账户数 | ~100K-1M | ~300M |
| 存储槽 | ~1M-10M | ~1.5B |

### 2.2 N42 已有 5 层缓存栈

| 层 | 范围 | 容量 | 位置 |
|----|------|------|------|
| 1. IntraBlockState.stateObjects | 单块 | ~1000 账户/块 | `intra_block_state.go` |
| 2. stateObject.originStorage | 单账户单块 | ~50-500 槽/对象 | `state_object.go` |
| 3. ShardedCache (LayeredDB) | 跨块持久 | **128M 条目, 2 GB** | `lib/kv/layered/cache.go` |
| 4. JMT nodeCache | 树实例 | **128K 解码节点** | `lib/jmt/tree.go` |
| 5. OS 页缓存 (MDBX mmap) | 系统级 | **可用 RAM** | OS 内核 |

### 2.3 实际 I/O 特征

对于 32 GB RAM 机器 + 10 GB 热状态：

| 指标 | 值 |
|------|-----|
| MDBX mmap 模式 | NoReadahead（按需加载） |
| 热状态 vs RAM | 10 GB vs 32 GB → **完全驻留** |
| 预期缓存命中率 | **>99%**（暖机后） |
| ShardedCache 命中率 | 70-90% |
| JMT nodeCache 命中率 | 80-95%（顶层节点） |
| 每块实际磁盘读 I/O | **接近零**（暖机后） |

### 2.4 真正的瓶颈

```
瓶颈排序（当前 N42）：
  1. CPU：JMT Blake3 哈希（状态根计算）  ← 最大
  2. CPU：EVM 执行                         ← 第二大
  3. CPU：BLS 签名验证                     ← 已优化 14.5x
  4. I/O：MDBX 写入（持久化）              ← 可通过流水线掩盖
  5. I/O：MDBX 读取（冷状态）              ← 几乎为零
```

**SALT 主要解决 #5（读 I/O）——但这已经不是 N42 的瓶颈。**

---

## 3. 价值评估

### 3.1 SALT 在不同规模下的价值

| 状态规模 | 热状态 vs RAM | SALT 价值 |
|---------|-------------|----------|
| N42 当前（2-20 GB） | 完全驻留 | **极低** — mmap 已等效内存 |
| 中等规模（50-100 GB） | 部分驻留 | **中** — 缓存命中率下降 |
| 以太坊规模（250+ GB） | 大量缺失 | **极高** — 消除数千次随机 I/O |

### 3.2 SALT vs N42 现有缓存的对比

| 方面 | SALT (MegaETH) | N42 当前 |
|------|---------------|---------|
| 认证结构内存占用 | ~1 GB (3B keys) | ~16 MB (JMT 128K cache) |
| 状态根更新 | 4 ECMul/key | JMT 树遍历 + Blake3 |
| 磁盘读需求 | 仅值数据 | mmap 自动缓存 |
| 崩溃恢复 | 需重建（可能数小时） | MDBX ACID 即时恢复 |
| GC 压力 | 需 off-heap 管理 | Go heap 可管理 |
| 共识兼容 | **需替换承诺方案 = 硬分叉** | 原生兼容 |

### 3.3 成本 vs 收益

| 成本 | 详情 |
|------|------|
| 开发周期 | 6-10 周（最低限度），全方案 3-6 月 |
| 共识变更 | **硬分叉**（承诺方案从 Blake3 Merkle 改为 Pedersen IPA） |
| 证明格式 | 从 Merkle proof 改为 IPA proof |
| 同步协议 | 需适配新证明格式 |
| 崩溃恢复 | 需从零建设（替代 MDBX ACID） |
| Go GC | 大型内存 trie → GC 停顿风险，需 off-heap 方案 |

| 收益 | 详情 |
|------|------|
| 读 I/O | 当前已接近零 → **改善极小** |
| 状态根计算 | ECMul 可能比 Blake3 快 → **待实测** |
| 写 I/O | 不变（值数据仍需写磁盘） |

---

## 4. 替代方案（更高 ROI）

### 4.1 简单调优（立即可做，零代码）

| 优化 | 效果 | 工作量 |
|------|------|--------|
| MDBX 启用 huge pages | TLB miss 减少 → mmap 读加速 20-30% | 配置 |
| 增大 ShardedCache 到 8 GB | 缓存命中率 → 95%+ | 1 行改动 |
| 增大 JMT nodeCache 到 512K | 状态根计算更快 | 1 行改动 |
| MDBX dirtySpace 调大 | 减少写溢出频率 | 1 行改动 |

### 4.2 io_uring 异步写入（2-4 周，高 ROI）

解决 #4 瓶颈（写 I/O），为 DSMR 流水线铺路。

### 4.3 JMT 树结构持久化（reth sparse-trie 思路，2-3 周）

跨块保留 JMT 树结构，避免每块重建。reth 实测减少 ~25% 延迟。

### 4.4 off-heap JMT 节点缓存（2 周）

用 mmap 分配固定区域存 JMT 节点，避免 Go GC 扫描。

---

## 5. 可能带来的问题和负面影响

### 5.1 内存管理 — 评级：高

- 状态增长超过 RAM → 性能断崖式下降（不像 mmap 的渐进退化）
- N42 预期 5 年状态增长：如果达到 100+ GB，SALT 的 1 GB 认证层仍可管理
- 但实际值数据仍需磁盘存储，不是纯内存方案

### 5.2 崩溃恢复 — 评级：极高

- MDBX 提供开箱即用的 ACID 崩溃恢复
- SALT 的内存承诺层在崩溃时丢失
- 重建需遍历全量状态 → 10M 键可能需要 **数分钟到数小时**
- 或维护 WAL（增加写放大 + 复杂度）

### 5.3 Go GC 压力 — 评级：高

- 大型内存 trie = 百万级指针 → GC 扫描停顿 50-100ms
- MegaETH 用 Rust 实现，无 GC 问题
- Go 中需要 off-heap 方案（mmap/cgo），增加复杂度

### 5.4 共识不兼容 — 评级：极高

- SALT 用 Pedersen 承诺替代 Merkle hash
- **所有验证者必须同时升级** = 硬分叉
- 证明格式变化影响轻客户端、同步协议、跨链桥

### 5.5 技术债务 — 评级：高

- 替换整个状态承诺层影响：block_validator, state_processor, consensus, sync, API
- 两套承诺方案并存（Merkle 用于旧块验证）增加维护负担
- IPA 证明库在 Go 中不成熟（MegaETH 用 Rust）

---

## 6. 综合评估

### 评分卡

| 维度 | 分数 (1-5) | 说明 |
|------|-----------|------|
| 价值/收益 | ★★ | 当前规模下 I/O 已非瓶颈 |
| 必要性 | ★ | 5 层缓存栈已接近零 I/O |
| 可行性 | ★★ | 需硬分叉 + 新承诺方案 + GC 管理 |
| 风险 | ★ | 崩溃恢复 + 共识不兼容 + GC |
| ROI | ★ | 6+ 月开发换极小 I/O 改善 |

### 结论

**在 N42 当前规模下，SALT 是不必要的。** N42 的 5 层缓存栈 + MDBX mmap 在 32 GB RAM 机器上已实现近零 I/O 延迟。SALT 解决的问题（大规模随机状态读取）**在 N42 上不存在**。

**建议：**

1. **从路线图 P1 降为 P3（战略储备）**
2. **立即做**：4 项简单调优（huge pages, cache 扩容, JMT cache 扩容, dirtySpace），零代码风险
3. **中期**：io_uring + DSMR Tier A（解决真正的瓶颈：写 I/O + 串行持久化）
4. **远期**：当状态增长到 100+ GB 时重新评估。此时 Monad 风格的 io_uring 比 SALT 更适合 Go 技术栈

### 更新路线图建议

| 原 P1 | 建议 |
|--------|------|
| SALT 内存状态树 | → **P3 战略储备**（状态 >100 GB 时重评） |
| 替代优化 | → **P0 立即做**：cache 扩容 + huge pages（1 天） |
| | → **P1**：io_uring + DSMR Tier A（真正瓶颈） |

---

## 参考文献

1. [MegaETH SALT: How SALT Breaks the Bottleneck](https://www.megaeth.com/blog-news/endgame-how-salt-breaks-the-bottleneck-thats-been-strangling-blockchains)
2. [megaeth-labs/salt GitHub](https://github.com/megaeth-labs/salt)
3. [Stanford Blockchain Review — MegaETH](https://review.stanfordblockchain.xyz/p/66-megaeth-building-a-real-time-blockchain)
4. [MonadDB Documentation](https://docs.monad.xyz/monad-arch/execution/monaddb)
5. [reth Sparse Trie as Cache](https://github.com/paradigmxyz/reth)
6. [Ress: Stateless Reth Nodes (Paradigm)](https://www.paradigm.xyz/2025/03/stateless-reth-nodes)
7. [Ethereum State Size Management (Vitalik)](https://hackmd.io/@vbuterin/state_size_management)
8. N42 源码分析: `lib/kv/mdbx/`, `lib/kv/layered/`, `lib/jmt/`, `modules/state/`
