# JMT 冷热分层设计

## 问题

JMT 全量历史占 MDBX 的 88%（81 GiB / 91 GiB），随链增长持续膨胀。

## 方案

MDBX 仅保留最近 1K 块的 JMT 节点，旧节点驱逐到 seg 压缩文件。

```
MDBX (热): 最近 1K 块 JMT 节点 ~1-2 GiB
文件 (冷): 历史 JMT 节点 seg+idx  ~20 GiB (Zstd 压缩)
JMTVersionRoots: 永久保留在 MDBX  ~数 MB (每条 40B)
```

## 数据基础

| 指标 | 值 |
|------|-----|
| 总 JMT 节点 | 124M |
| 平均节点/块 | ~12K |
| 平均字节/节点 | ~40B (原始), ~85B (MDBX) |
| 1K 块节点数 | ~12M |
| 1K 块 MDBX 占用 | ~1.0 GiB |
| 压缩比 (seg 字典) | 3-5x |
| 历史节点压缩后 | ~20 GiB |

## 参考: 行业标准

| 实现 | trie 保留 | 策略 |
|------|----------|------|
| geth PathDB | 128 diff layers | 超出后 flatten 到 disk |
| reth | 128 diff layers | 同上 |
| N42 SnapshotAccel | 128 diff layers | MaxDiffLayers=128 |
| **N42 JMT (本方案)** | **1000 blocks** | 超出后归档到 seg 文件 |

保留 1K 块 (而非 128) 的原因:
- JMT 节点比 MPT 节点小 10x (~40B vs ~500B)
- 1K 块 × 12K 节点 × 85B = 1 GiB，内存友好
- 更大的热窗口减少文件回退频率

## 驱逐机制

```
条件: MDBX 中 JMT 节点数 > RetentionBlocks × 2 (默认 2K 块的量)
触发: 后台 goroutine，驱逐最旧的 RetentionBlocks 个块的节点

步骤:
1. 确定驱逐范围: [最旧有效块, HEAD - RetentionBlocks]
2. 遍历 JMTVersionRoots 找到范围内的所有 root
3. 从每个 root 向下遍历，收集该范围独占的节点 hash
   (被更新的块共享的节点不驱逐)
4. 用 seg.Compressor 写入 jmt-archive/ seg 文件
5. 用 RecSplit 构建 .idx 索引
6. 从 MDBX JMTNode 表批量删除已归档节点
7. 记录驱逐进度: JMTRoot 表 "evict_height" key
```

## 查询路径

```go
// archiveAwareStore 实现 jmt.NodeStore 接口
type archiveAwareStore struct {
    mdbx    jmt.NodeStore           // MDBX JMTNode 表 (热)
    archive *jmt_archive.Reader     // seg+idx 文件 (冷)
}

func (s *archiveAwareStore) Get(hash jmt.Hash) ([]byte, error) {
    // 先查热存储
    data, err := s.mdbx.Get(hash)
    if err == nil {
        return data, nil
    }
    // 回退到冷归档 (O(1) RecSplit 查找 + 解压)
    return s.archive.GetNode(hash)
}
```

## 正常出块流程

```
每 8 秒 (出块):
  1. EVM 执行 → dirty state
  2. JMT BatchUpdate → dirty nodes 写入 MDBX (同事务)
  3. WriteBlockWithState → 原子提交

后台 (每 N 分钟检查):
  if mdbxNodeCount > 2 * retentionBlocks * avgNodesPerBlock:
      evictOldestSegment()  // 驱逐最旧 1K 块到文件
```

## 配置

```go
type JMTRetentionConfig struct {
    RetentionBlocks uint64  // MDBX 保留的块数 (默认 1000)
    EvictThreshold  float64 // 触发驱逐的倍数 (默认 2.0)
    ArchiveDir      string  // seg 文件目录 (默认 {datadir}/jmt-archive/)
    SegmentSize     uint64  // 每个 seg 文件的块数 (默认 1000)
}
```

## 预期效果

| 指标 | 当前 | 优化后 |
|------|------|--------|
| MDBX 大小 | ~91 GiB | **~11 GiB** |
| JMT 在 MDBX | 81 GiB | **~1 GiB** |
| 历史节点 (文件) | 0 | **~20 GiB** (压缩) |
| 总磁盘 | 91 GiB | **~31 GiB** (-66%) |
| 最近块 proof | <1μs | <1μs (无变化) |
| 历史 proof | <1μs | ~10-50μs (可接受) |

## 实施阶段

### Phase B-1: migrate-jmt 直接输出到文件

修改 `migrate-jmt` 命令:
- MDBX 只写最近 1K 块的节点
- 历史节点直接写 seg 文件 (不经过 MDBX)
- 构建 RecSplit 索引

### Phase B-2: 运行时驱逐

修改 `writeBlockWithState`:
- 正常写入 JMT 到 MDBX (不变)
- 后台检查节点数，超阈值触发驱逐
- 驱逐到 seg 文件

### Phase B-3: archiveAwareStore

修改 JMT 查询路径:
- `PooledDBStore` 包装为 `archiveAwareStore`
- MDBX miss → seg 文件回退
- 对 GetProof RPC 透明
