# 存储分层设计 — 参考 Erigon v3 的 N42 数据架构

## 一、设计原则

```
数据按访问频率和变更模式分为三层存储引擎：

┌──────────────────────────────────────────────────────────┐
│  L1 HOT — MDBX (内存映射 B+tree)                         │
│  延迟: <1μs   适用: 随机读写、事务性更新、DupSort          │
│  数据: 当前状态、最近 blocks、共识状态、JMT 活跃节点       │
│  介质: NVMe SSD                  容量: ~50-100 GB         │
├──────────────────────────────────────────────────────────┤
│  L2 WARM — 文件系统 (seg + RecSplit 索引)                 │
│  延迟: ~10μs  适用: 顺序读、O(1)哈希索引、不可变段文件     │
│  数据: Domain 快照、History、InvertedIndex、JMT 归档       │
│  介质: NVMe/SATA SSD             容量: ~300-500 GB        │
├──────────────────────────────────────────────────────────┤
│  L3 COLD — 文件系统 (EraE + Freezer + Zstd 压缩)         │
│  延迟: ~1ms   适用: 顺序批量读、归档、网络分发              │
│  数据: 古老块数据、完整日志索引、EraE 归档、BT 种子        │
│  介质: HDD / 对象存储             容量: ~800 GB - 数 TB    │
└──────────────────────────────────────────────────────────┘
```

---

## 二、数据分类矩阵

### 2.1 L1 HOT — MDBX（必须 MDBX）

这些数据需要 **事务性 ACID**、**随机读写**、**DupSort** 支持。MDBX 的内存映射 B+tree 是唯一合适的选择。

| 表名 | Key | Value | 读模式 | 写模式 | 大小 |
|------|-----|-------|--------|--------|------|
| **Account** | address(20B) | StateAccount(protobuf) | 随机 ~10K/s | 随机更新/块 | ~200B/条 |
| **Storage** | addr(20)+inc(2)+key(32) | value(32B) | 随机 ~50K/s | 随机更新/块 | DupSort |
| **Code** | code_hash(32B) | bytecode | 随机 | 少写 | 变长 ~24KB |
| **PlainContractCode** | addr(20)+inc(2) | code_hash(32) | 随机 | 少写 | 32B |
| **JMTNode** | blake3(32B) | 节点序列化 | 随机 ~1K/s | 批量/块 | ~40B |
| **JMTRoot** | fixed keys | root/version | 单条 | 每块 | 32B |
| **JMTVersionRoots** | height(8B) | root(32B) | cursor seek | 每块 | 40B |
| **LtHashDigest** | "digest" | 2048B | 单条 | 每块 | 2KB |
| **Headers** (近 90K 块) | num(8)+hash(32) | RLP header | 随机 | 追加 | ~500B |
| **BlockBody** (近 90K 块) | num(8)+hash(32) | RLP body | 随机 | 追加 | 变长 |
| **HeaderCanonical** | num(8B) | hash(32B) | 顺序 | 追加 | 32B |
| **HotStuffState** | "state" | 共识状态 | 单条 | 频繁 | ~1KB |
| **TxPool** | 各种 | 待处理交易 | 随机 | 频繁 | 变长 |
| **SnapSyncProgress** | 固定 key | 恢复数据 | 单条 | 崩溃恢复 | 小 |

**为什么不能用文件/RocksDB**:
- Account/Storage 需要 MDBX 的 DupSort 和原子事务
- JMTNode 在 proof 生成时需要亚微秒随机读
- 共识状态需要事务一致性

### 2.2 L2 WARM — 文件系统 seg/idx（适合文件）

这些数据是 **不可变的**（一旦写入不再修改）、**顺序访问** 或 **O(1) 索引** 访问。Erigon v3 的 Domain/History 文件架构最优。

| 数据类型 | 文件格式 | 索引方式 | 读模式 | 大小预估 |
|---------|---------|---------|--------|---------|
| **Account Domain 快照** | .seg (字典压缩) | .idx (RecSplit) | O(1) 哈希 | ~5GB/域 |
| **Storage Domain 快照** | .seg | .idx | O(1) 哈希 | ~30GB/域 |
| **Code Domain 快照** | .seg | .idx | O(1) 哈希 | ~10GB/域 |
| **AccountChangeSet** | .seg (per step) | .idx | 顺序 per-block | ~50GB 总 |
| **StorageChangeSet** | .seg (per step) | .idx | 顺序 per-block | ~200GB 总 |
| **AccountHistory** | .seg + .vi | RecSplit + bitmap | 范围查询 | ~20GB |
| **StorageHistory** | .seg + .vi | RecSplit + bitmap | 范围查询 | ~50GB |
| **JMT Archive** (归档节点) | .seg | RecSplit | O(1) 哈希 | ~50GB |
| **BlobSidecars** | .seg | RecSplit | O(1) | 18 个月保留 |
| **SnapshotAccount/Storage** | MDBX 或 .seg | B-tree/RecSplit | O(1) | ~30GB |

**文件生命周期（参考 Erigon v3）**:
```
HOT (MDBX) ──聚合步骤──→ COLD 文件 ──合并──→ FROZEN 文件
                         (<32 steps)        (=32 steps, 不可变)
```

**为什么适合文件而非 MDBX**:
- 不可变 = 无事务开销，直接 mmap 读取
- 字典压缩后体积减少 3-5x
- RecSplit O(1) 索引替代 B-tree，查找更快
- 可独立备份/移动到不同存储介质

### 2.3 L3 COLD — 文件系统 + 压缩（HDD 可用）

| 数据类型 | 文件格式 | 压缩 | 访问频率 | 大小预估 |
|---------|---------|------|---------|---------|
| **EraE 归档** (旧块+receipt) | .era | Zstd Level 3 | <1/min | ~30GB (压缩后) |
| **Freezer** (>90K 块前的数据) | 固定格式 | 无 | <10/s | ~200GB |
| **LogAddressIndex** | bitmap 分片 | RoaringBitmap | eth_getLogs | ~20GB |
| **LogTopicIndex** | bitmap 分片 | RoaringBitmap | eth_getLogs | ~30GB |
| **CallTraceSet/Index** | bitmap 分片 | RoaringBitmap | debug_trace | ~50GB |
| **DepositContract 数据** | MDBX 冷表 | 无 | <1/hour | ~100MB |
| **BT 种子/Manifest** | .torrent/.json | 无 | <1/day | ~10MB |

---

## 三、RocksDB 适用性评估

### 为什么 Erigon 不用 RocksDB

Erigon 选择 MDBX 而非 RocksDB 的核心原因：

| 维度 | MDBX | RocksDB |
|------|------|---------|
| 读延迟 | ~0.1μs (mmap 直读) | ~1-10μs (block cache) |
| 写放大 | 1x (CoW B+tree) | 10-30x (LSM compaction) |
| 空间放大 | 1x | 1.1-2x (SST 层) |
| 并发读 | 无锁 (MVCC snapshot) | 需 block cache 竞争 |
| DupSort | ✅ 原生支持 | ❌ 需要模拟 |
| 事务大小 | 仅受磁盘限制 | 内存限制 |

### N42 中 RocksDB 可能适用的场景

| 场景 | 适用性 | 原因 |
|------|--------|------|
| **Log 索引** (eth_getLogs) | ⚠️ 可选 | 写密集（追加 bitmap 分片），RocksDB 的 LSM merge 适合 append-heavy |
| **TxLookup** (tx hash → block) | ⚠️ 可选 | 写一次读多次，RocksDB 的 prefix bloom 可加速 |
| **CallTrace 索引** | ⚠️ 可选 | 纯追加，可异步构建 |
| **Account/Storage/JMT** | ❌ 不适合 | 随机读写热路径，MDBX 的 mmap 更优 |

**结论**: 当前阶段不引入 RocksDB。MDBX + 文件系统分层已覆盖所有场景。RocksDB 可作为 P2 优化项专门用于 Log/Trace 索引。

---

## 四、分级缓存设计

```
┌─────────────────────────────────────────────────────────────┐
│  L0 CPU Cache — Go map + sync.Pool                          │
│  DiffLayer (128层) + ShardedCache (16分片) + JMT LRU (131K) │
│  命中率: >95% (近期状态)    延迟: <100ns                     │
├─────────────────────────────────────────────────────────────┤
│  L1 MDBX Page Cache — OS mmap 页缓存                        │
│  Account/Storage/JMTNode 表的内存映射                        │
│  命中率: >80% (工作集)     延迟: <1μs                        │
├─────────────────────────────────────────────────────────────┤
│  L2 Domain File mmap — seg/idx 文件内存映射                  │
│  Domain 快照 + RecSplit 索引                                 │
│  命中率: ~50-70%           延迟: ~10μs                       │
├─────────────────────────────────────────────────────────────┤
│  L3 Cold Read — 磁盘 I/O                                    │
│  Freezer/EraE/History (HDD 顺序读)                          │
│  命中率: N/A               延迟: ~1-10ms                     │
└─────────────────────────────────────────────────────────────┘

查找路径 (Account 读取):
1. DiffLayer[blockN].accounts[addr]     → hit? return
2. DiffLayer[blockN-1]...               → parent 遍历
3. ShardedCache.Get(addr)               → 16 分片 map
4. SnapshotAccount table (MDBX)         → B-tree 查找
5. Account table (MDBX)                 → 最终回退
6. AccountDomain .seg + .idx            → RecSplit O(1)
```

### 缓存配置

| 缓存层 | 容量 | 淘汰策略 | 配置项 |
|--------|------|---------|--------|
| DiffLayer | 128 层 | FIFO (最旧层淘汰) | `SnapshotAccelCfg.MaxDiffLayers` |
| ShardedCache | 50 万账户 | LRU per shard | `SnapshotAccelCfg.WarmupAccounts` |
| JMT Node Cache | 131K 节点 | LRU (container/list) | `jmt.DefaultNodeCacheSize` |
| MDBX Page Cache | OS 管理 | OS LRU | MDBX mapsize |
| Code Cache | ~1000 合约 | LRU | 内部 CodeCache |

---

## 五、目录结构

```
{datadir}/
├── chaindata/
│   └── mdbx.dat              ← L1 HOT: MDBX (Account, Storage, JMT, Headers...)
├── snapshots/
│   ├── domain/
│   │   ├── accounts.0-1000.seg    ← L2 WARM: Account domain
│   │   ├── accounts.0-1000.idx
│   │   ├── storage.0-1000.seg     ← L2 WARM: Storage domain
│   │   └── code.0-1000.seg        ← L2 WARM: Code domain
│   ├── history/
│   │   ├── accounts.0-1000.seg    ← L2 WARM: Account history
│   │   └── storage.0-1000.seg
│   ├── idx/
│   │   ├── logaddrs.0-1000.seg    ← L3 COLD: Log address index
│   │   └── logtopics.0-1000.seg
│   └── accessor/
│       └── *.bt, *.idx            ← L2 WARM: B-tree/RecSplit accessors
├── era/
│   ├── era-00000000-00008192.era  ← L3 COLD: EraE Zstd 压缩归档
│   ├── era-00008192-00016384.era
│   └── manifest.json
├── jmt-archive/
│   ├── jmtnodes.0-1000.seg       ← L2 WARM: JMT 历史节点归档
│   └── jmtnodes.0-1000.idx
├── freezer/
│   ├── headers.0000              ← L3 COLD: 古老块头
│   ├── bodies.0000
│   └── receipts.0000
└── tmp/                           ← L1 HOT: 临时文件
```

---

## 六、数据可用性 (DA) 与分片计划

### 6.1 PeerDAS (EIP-7594) — 已有基础

| 组件 | 状态 | 位置 |
|------|------|------|
| DataColumns 表 | ✅ 已实现 | `modules/table.go:DataColumns` |
| PeerDAS Service | ✅ 已实现 | `internal/peerdas/` |
| BlobSidecars 存储 | ✅ 已实现 | `modules/table.go:BlobSidecars` |

### 6.2 数据分片 — 滞后实现计划

| Phase | 内容 | 优先级 | 依赖 |
|-------|------|--------|------|
| **DA-1** | EraE 段文件按 shard 分片存储 | P2 | replay-v2 完成 |
| **DA-2** | JMT 节点按子树分片（前 4 nibble = 16 分片） | P2 | JMT 全量历史 |
| **DA-3** | Domain 快照按地址范围分片 | P3 | Aggregator 集成 |
| **DA-4** | DAS 采样验证（KZG/IPA） | P3 | Blob 交易普及 |
| **DA-5** | 跨节点 DA 证明交换 | P4 | P2P 协议扩展 |

**JMT 分片策略**:
```
JMT 16 叉树天然支持按 nibble 分片：
├── shard 0: 所有 keyHash 以 0x0 开头的节点
├── shard 1: 所有 keyHash 以 0x1 开头的节点
├── ...
└── shard f: 所有 keyHash 以 0xf 开头的节点

每个 shard 独立存储、独立归档、独立同步
轻节点只需下载相关 shard 即可验证 proof
```

---

## 七、实施路线图

### Phase 1: 基础分层 (已完成)

- [x] MDBX 热数据（Account/Storage/JMT）
- [x] EraE 冷归档（Zstd 压缩）
- [x] Freezer 古老块存储
- [x] StorageTier 配置（Hot/Warm/Cold 路径）
- [x] JMT 归档压缩（seg 字典）

### Phase 2: Domain 快照文件化 (P1)

- [ ] Account Domain → .seg + .idx 文件（从 MDBX ChangeSet 导出）
- [ ] Storage Domain → .seg + .idx 文件
- [ ] Code Domain → .seg + .idx 文件
- [ ] History 段文件化（AccountHistory/StorageHistory → .seg）
- [ ] Aggregator 后台合并（step 级别聚合）
- [ ] RecSplit 索引构建（替代 B-tree 查找）

### Phase 3: 分级缓存优化 (P1)

- [ ] DiffLayer 持久化（SnapshotJournal 崩溃恢复）
- [ ] ShardedCache 预热策略优化
- [ ] JMT Node Cache 自适应大小（按内存压力调整）
- [ ] Domain 文件 mmap 预加载

### Phase 4: DA 分片 (P2)

- [ ] EraE 段按 shard 分发
- [ ] JMT 节点按子树分片归档
- [ ] PeerDAS 列采样集成
- [ ] 轻节点 shard 选择性同步

### Phase 5: 可选 RocksDB (P3)

- [ ] 评估 Log/Trace 索引迁移到 RocksDB 的收益
- [ ] Prefix bloom + compaction 优化 eth_getLogs
- [ ] 可插拔存储引擎接口
