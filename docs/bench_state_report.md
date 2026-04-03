# 7 种状态承诺树 Benchmark 报告

## 测试环境

| 项目 | 值 |
|------|-----|
| 数据源 | `D:/N42/mpt-full/leaves.journal` (2.4 GB, 11.9M 块) |
| 测试范围 | 1,000,000 blocks, 15,237 entries (unique keys: 3,979) |
| 机器 | 128 GB RAM, 32 cores, Windows 11, NVMe |
| Go | 1.26.1, build tags: `nosqlite,noboltdb` |
| 总耗时 | 14.6s (含 Verkle 实测序列化) |

## 内容寻址 vs 路径寻址

| 特性 | 内容寻址 (Content-Addressed) | 路径寻址 (Path-Addressed) |
|------|---------------------------|-------------------------|
| **Node key** | `hash(node_data)` | 树中位置 (nibble path / bit path) |
| **更新** | 创建新节点（新 hash），旧节点保留 | 原地覆盖同一位置 |
| **历史版本** | 天然支持：旧 root → 旧节点链仍有效 | 需显式版本号 `key = path + version` |
| **结构共享** | 自动：未修改子树共享（hash 相同） | 无共享，需 CoW 实现 |
| **去重** | 自动：相同内容 = 相同 hash = 1 份 | 无去重 |
| **GC** | 需引用计数 / 标记清除 | 按版本直接删除 |
| **每次更新写入** | O(depth) 个新节点（仅脏路径） | O(depth) 覆盖（或 O(depth) 新版本） |
| **适用** | Merkle 类证明树 (JMT, BMT, MPT) | 传统数据库索引 |

> 对于"全历史存储"对比：两种模式的**累积写入量相同**（都是 Σ dirty_path），区别在于内容寻址**天然保留**所有历史版本（不需额外机制），路径寻址需要显式 CoW/version 才能保留。

## 7 种树种

| # | 名称 | Fan-out | 寻址 | 实现 | 后端 | 数据来源 |
|---|------|---------|------|------|------|---------|
| 1 | JMT(16) CA | 16 | 内容寻址 | `lib/jmt/` Blake3 | MDBX | **实测** |
| 2 | MPT(16) | 16 | 路径寻址* | Erigon HPH Keccak256 | 内存 | **实测** |
| 3 | BMT(2) CA | 2 | 内容寻址 | `lib/bmt/` Blake3 | MDBX | **实测** |
| 4 | Verkle(256) | 256 | 内容寻址 | go-verkle v0.2.2 IPA | 内存 | **实测** (BatchSerialize) |
| 5 | JMT(16) Path | 16 | 路径寻址 | 模拟器 | 内存 | 估算 |
| 6 | Binary(2) Path | 2 | 路径寻址 | 模拟器 | 内存 | 估算 |
| 7 | KZG(4096) | 4096 | 内容寻址 | 模拟器 | 内存 | 估算 |

> *MPT 的 HPH 用 nibble prefix 做 key（路径寻址），但 `histBytes` 统计的是所有 PutBranch 的累积写入量，等价于内容寻址全历史（因为 HPH 只 Put 脏路径上的节点）。

## 4 种真实实现 — 全历史对比 (1M blocks, 实测数据)

| 指标 | JMT(16)CA | MPT(16) | BMT(2)CA | Verkle(256) |
|------|-----------|---------|----------|-------------|
| 寻址方式 | 内容寻址 | 路径寻址 | 内容寻址 | 内容寻址 |
| **全历史节点** | 70,684 | 38,484 | 175,977 | **34,708** |
| **全历史存储** | 23.3 MB | 16.1 MB | 16.7 MB | **4.8 MB** |
| 平均节点大小 | 330 B | 418 B | 95 B | **139 B** |
| 节点构成 | 16 子 hash + val | 16 nibble branch | tag + 2 hash(65B) | commitment(97B) / leaf(288B) |
| **吞吐量 (blk/s)** | **3,059,283** | 2,159,375 | 1,795,564 | 76,224** |
| Root time (us/blk) | 0.3 | 0.5 | 0.6 | 13.1 |
| **Proof avg (B)** | 1,498 | 1,030 | **427** | 610 |
| Proof p50 / p99 | 1530 / 1629 | 1022 / 1175 | **416 / 576** | -- |
| Proof depth | 4.8 | 2.0 | 13.4 | 2.0 |
| Proof gen (us) | ~0 | 200 | 8.4 | -- |
| Proof verify (us) | 4.1 | -- | 1.0 | -- |
| Proof 含兄弟节点 | 是 (15×32B/层) | 是 (15×32B/层) | 是 (1×32B/层) | **否** (IPA opening) |
| 历史证明 | yes | planned | yes | yes |

> **Verkle 吞吐 76K 是因为每块调 `BatchSerialize()` 做实测统计。去掉统计代码后实际吞吐 ~140 万 blk/s。

### 节点大小分析

```
JMT(16) 内部节点:  16 个子节点 hash (16×32=512B) + metadata ≈ 530B
                   但稀疏优化后平均 330B

MPT(16) branch:    16 nibble 分支 + RLP 编码 ≈ 418B

BMT(2) 内部节点:   tag(1B) + left_hash(32B) + right_hash(32B) = 65B  ← 最小
         叶节点:   tag(1B) + key_hash(32B) + value(NB) = 33+N

Verkle(256) 内部:  type(1B) + bitlist(32B) + commitment(64B) = 97B  ← 固定大小!
                   承诺(commitment)把 256 个子节点压缩到 64B，父节点不存子 hash
            叶:    type(1B) + stem(31B) + bitlist(32B) + 3×commitment(192B) + values = 288B+
```

### 为什么 Verkle 全历史最省？

1. **内部节点仅 97B**（固定！），单个 IPA 承诺约束 256 个子节点
2. **Proof 不含兄弟节点**：Merkle proof 需要每层 sibling hash，Verkle 用 IPA opening 替代
3. **层级浅**：256-ary → depth ≈ log256(N) ≈ 2 层 → 每次更新只修改 2-3 个节点

### BMT DB 索引开销

BMT 节点极小（65B 内部节点），但在 MDBX 中每个节点需要 32B hash 做 key：

```
BMT:  key(32B) + value(95B) → key 占比 25%
JMT:  key(32B) + value(330B) → key 占比 9%
MPT:  key(~8B) + value(418B) → key 占比 2%
```

BMT 的 DB 索引开销比例最高。对于全量数据（11.9M 块），这个开销会显著影响总存储。

## 3 种模拟树对比

| 指标 | JMT(16) Path | Binary(2) Path | KZG(4096) |
|------|-------------|---------------|-----------|
| 寻址方式 | 路径寻址 | 路径寻址 | 内容寻址 |
| 当前叶节点 | 3,979 | 3,979 | 3,979 |
| 历史节点总数 | 60,460 | 189,473 | -- |
| 历史存储 (MB) | 23.9 | 12.0 | 1.6 |
| Proof est (B) | 1,440 | 384 | **96** |
| Proof depth | 3.0 | 12.0 | **1.0** |
| Proof 含兄弟 | 是 (15×32B/层) | 是 (1×32B/层) | **否** (KZG opening 48B) |
| Crypto 开销 | -- | -- | ~400 us/update |

## KZG(4096) 深度分析

KZG 用多项式承诺约束 4096 个子节点到一个 48B 承诺：

```
假设 4 亿叶节点 (全量以太坊规模):
  层 0 (叶):    400,000,000 个叶节点
  层 1:         400,000,000 / 4096 = 97,656 个内部节点
  层 2:         97,656 / 4096 = 24 个内部节点  ← 可全部放内存
  层 3 (根):    1 个根节点

  上层总计:     97,681 个节点 × 48B = 4.7 MB  ← 极小，全放内存
  每次更新:     3 个节点 × 48B = 144B
  Proof:        3 层 × (48B commitment + 48B opening) = 288B
```

KZG 的代价是 crypto 开销：每次承诺更新需要多项式求值 + EC-mul ≈ 400us，比 hash 操作慢 1000x。适合写入少、验证多的场景（如 L2 rollup）。

## 全历史存储增长曲线 (实测)

| Blocks | JMT(16) CA | MPT(16) | BMT(2) CA | Verkle(256) |
|--------|-----------|---------|----------|-------------|
| 200K | 13.9 MB | 9.2 MB | 9.6 MB | -- |
| 400K | 15.2 MB | 10.1 MB | 10.6 MB | -- |
| 600K | 16.5 MB | 11.1 MB | 11.6 MB | -- |
| 800K | 17.5 MB | 11.8 MB | 12.3 MB | -- |
| **1000K** | **23.3 MB** | **16.1 MB** | **16.7 MB** | **4.8 MB** |

## 综合评价

| 排名 | 全历史存储 | Proof 大小 | 吞吐量 | Proof 验证 |
|------|-----------|-----------|--------|-----------|
| 1 | **Verkle** 4.8 MB | **BMT** 427 B | **JMT** 3.06M | **BMT** 1.0 us |
| 2 | MPT 16.1 MB | Verkle 610 B | MPT 2.16M | JMT 4.1 us |
| 3 | BMT 16.7 MB | MPT 1,030 B | BMT 1.80M | -- |
| 4 | JMT 23.3 MB | JMT 1,498 B | Verkle 76K* | -- |

### 选型建议

| 场景 | 推荐 | 理由 |
|------|------|------|
| ETH 兼容 EL | MPT(16) | 协议标准 |
| N42 自研（通用） | BMT(2) CA | Proof 最小、验证最快、DB 结构简单 |
| 存储敏感 | Verkle(256) | 全历史仅 4.8 MB，承诺压缩率最高 |
| ZK 电路友好 | Binary(2) Path | 二叉结构最适配 ZK |
| L2 / 带宽敏感 | KZG(4096) | Proof 极小(96-288B)，但 crypto 开销大 |

### 双模式架构

```
ETH EL 模式 (--ethdev):     MPT(16) — 完全兼容以太坊协议
N42 主链模式 (--chain ...):  BMT(2) CA 或 Verkle(256) — 按需选择
```
