# Ethereum MPT 树替代方案全景分析

基于 N42 已有代码实现和行业前沿研究的深度技术分析。

---

## 一、为什么要替换 MPT

Ethereum 的 Merkle Patricia Trie (MPT) 存在三个结构性缺陷：

| 缺陷 | 影响 | 量化 |
|------|------|------|
| **16 叉树深度大** | 证明路径长（~8 层），每层 512 bytes 分支节点 | 单次 proof ≈ 4KB |
| **Keccak256 不 ZK 友好** | ZK 电路中 Keccak 约需 15 万 R1CS 约束 | 是 Poseidon 的 ~100x |
| **随机 I/O 密集** | 每次状态访问需 ~8 次磁盘随机读 | SSD IOPS 成为瓶颈 |

N42 的现状：当前主网使用 `GenerateRootHash()` — 一个**增量 Keccak 哈希**（仅哈希 dirty 账户），不是真正的 MPT。这是一个过渡方案，已完成的 JMT + LtHash 架构是目标替代。

---

## 二、N42 已实现的三层状态承诺架构

```
┌─────────────────────────────────────────────────────┐
│                   Block Execution                    │
│  IntraBlockState → dirty accounts + dirty storage   │
└───────────┬──────────────────┬──────────────────┬───┘
            │                  │                  │
     ┌──────▼──────┐   ┌──────▼──────┐   ┌──────▼──────┐
     │  Flat State  │   │    JMT      │   │   LtHash    │
     │  (MDBX O(1)) │   │ (Blake3 16叉)│   │ (2048B XOR) │
     │  执行读写     │   │ Merkle 证明  │   │ O(k) 验证   │
     └──────────────┘   └──────────────┘   └──────────────┘
```

### 2.1 Jellyfish Merkle Tree (JMT)

**位置**: `lib/jmt/` — 完整实现，含 GC、缓存、归档、证明系统

**核心设计**:

```
节点类型:
├── InternalNode: bitmap 索引的 16 叉稀疏分支（仅存非空子节点）
├── ExtensionNode: 路径压缩（消除单子节点链）
└── LeafNode: Blake3(key) → [keyHash:32][valueLen:4][value:N]

Key 映射:
├── AccountKey = Blake3(address)           // 32 bytes → 64 nibbles
└── StorageKey = Blake3(address || slot)   // 52 bytes → 64 nibbles

账户编码: 固定 107 bytes
[flags:1][nonce:8][balance:32][incarnation:2][codeHash:32][root:32]
```

**性能指标** (Apple M1 Max):

| 操作 | 延迟 | 吞吐 |
|------|------|------|
| Put | 7.4 μs | 135K/s |
| Get | 1.0 μs | 1M/s |
| BatchUpdate 1K keys | 3.5 ms | 286K keys/s |
| Proof generation | ~10 μs | 100K/s |

**为什么选 JMT 而不是标准 MPT**:

| 维度 | MPT (geth) | JMT (N42) |
|------|-----------|-----------|
| 叉数 | 16（密集存储所有 16 slot） | 16（bitmap 稀疏，仅存非空） |
| 路径压缩 | Extension + Branch 双节点 | ExtensionNode 统一压缩 |
| 哈希 | Keccak256 | Blake3（3x 更快，SIMD 友好） |
| GC | trie pruning（复杂） | 引用计数级联删除（简洁） |
| 缓存 | hashdb/pathdb | LRU 131K nodes + dirty buffer |
| 证明大小 | ~4KB | ~2KB（bitmap 压缩省分支） |

### 2.2 LtHash — 格密码状态摘要

**位置**: `lib/lthash/` — 受 Solana SIMD-215 启发

**核心原理**:
```
Digest = 2048 bytes (256 × uint64)

同态性: LtHash(A ∪ B) = LtHash(A) ⊕ LtHash(B)

Add(x):    digest ⊕= Blake3_XOF(tag || keyHash || value, 2048)
Remove(x): digest ⊕= Blake3_XOF(tag || keyHash || value, 2048)  // XOR 自逆
Update(old, new): 一次遍历完成 Remove(old) + Add(new)

Summary: Blake3(digest[:]) → 32 bytes (存入 header.LtHashRoot)
```

**复杂度优势**:

| 操作 | Merkle Tree | LtHash |
|------|-------------|--------|
| 单账户更新 | O(log N) 节点重哈希 | O(1) XOR |
| 块级验证 | O(k × log N) | O(k) |
| 全量重算 | O(N × log N) | O(N) |
| Proof | ✅ 支持 | ❌ 不支持（需配合 JMT） |

**设计决策**: LtHash 不能替代 Merkle proof（无法生成 inclusion proof），因此 N42 采用 **JMT + LtHash 双轨**：JMT 提供 proof，LtHash 提供 O(k) 快速块验证。

### 2.3 可插拔架构

```go
// modules/state/interfaces.go
type RootComputer interface {
    ComputeRoot(accounts, storage) (Hash, error)
}

type LtHashRootComputer interface {
    RootComputer
    ComputeRootWithOriginals(accounts, originals, storage, origStorage)
        (jmtRoot, ltHashRoot Hash, err error)
}

// 激活路径 (internal/node/node.go):
JMTCommitment → JMTRootComputer → LtHashAwareRootComputer → ibs.SetRootComputer()
```

**激活控制**:
- `conf.JMTCommitment`: 启用 JMT 状态承诺
- `params.LtHashTime`: LtHash fork 时间戳（链级激活）
- 当前主网: 使用 legacy `GenerateRootHash()`（增量 Keccak）
- 新链/私链: 可从 genesis 启用 JMT + LtHash

---

## 三、行业方案对比

### 3.1 主流方案矩阵

| 方案 | 哈希函数 | 树结构 | ZK 友好 | 量子安全 | Proof 大小 | 采用者 |
|------|---------|--------|---------|---------|-----------|--------|
| **MPT** | Keccak256 | 16-ary Patricia | ❌ | ⚠️ 128-bit | ~4KB | Ethereum L1, geth |
| **Verkle** | Pedersen/IPA | 256-ary | ⚠️ 部分 | ❌ ECDLP 可破 | ~150B | ~~Ethereum 2.0~~ 已搁置 |
| **JMT Blake3** | Blake3 | 16-ary sparse | ⚠️ 中等 | ✅ 128-bit | ~2KB | **N42**, Aptos |
| **Binary Blake3** | Blake3 | 2-ary | ⚠️ 中等 | ✅ 128-bit | ~1KB (32层) | EIP-7864 提案 |
| **Poseidon SMT** | Poseidon | 2-ary sparse | ✅ 极佳 | ✅ | ~1KB | zkSync, Polygon zkEVM |
| **LtHash** | Blake3 XOF | 无树结构 | N/A | ✅ | N/A (无 proof) | **N42**, Solana |

### 3.2 Verkle 树的战略放弃

N42 的文档明确记录了放弃 Verkle 的技术决策（来自 `docs/GAP_ANALYSIS.md`）：

> Verkle Tree 依赖 Pedersen 承诺（Bandersnatch 椭圆曲线），不具备量子抗性（Shor 算法可在多项式时间内破解 ECDLP）。2025年1月 EIP-7864 提出用 STARKed 二叉哈希树（Blake3/Poseidon）替代 Verkle，Vitalik 明确表态支持。

**时间线验证**:
- 2025-01: EIP-7864 提出 Binary Hash Tree 替代 Verkle
- 2026-01: Ethereum Foundation 成立 Post-Quantum Team + $1M 研究奖金
- N42 的 JMT + Blake3 选择与 Ethereum 自身的转向**完全一致**

### 3.3 各方案 ZK 电路成本

```
ZK 约束数 (per hash):
├── Keccak256:    ~150,000 R1CS  (MPT 用)
├── Pedersen:     ~2,000 R1CS    (Verkle 用，但不量子安全)
├── Blake3:       ~10,000 R1CS   (JMT 用)
├── Poseidon:     ~300 R1CS      (zkEVM 用，最 ZK 友好)
└── Blake2s:      ~8,000 R1CS    (折中方案)

N42 的 ZKML 验证 (zkprover/zkml.go):
使用 SP1 STARK 后端 → Blake3 的 ~10K 约束在 STARK 体系内可接受
```

---

## 四、N42 与竞品的状态承诺对比

### 4.1 Ethereum geth

```
geth:  MPT (Keccak256) → pathdb/hashdb → LevelDB/Pebble
N42:   JMT (Blake3) + LtHash → MDBX flat state

优势: N42 读性能 O(1) vs geth O(log N)
      N42 proof 2KB vs geth 4KB
      N42 块验证 O(k) LtHash vs geth O(k×log N) trie update
劣势: N42 需要 JMT + flat state 双写（磁盘空间 ~1.5x）
```

### 4.2 Erigon

```
Erigon: PlainState (flat) + HexPatriciaHashed (增量 commitment)
N42:    PlainState (flat) + JMT (全量 commitment) + LtHash (增量 digest)

共同: 都使用 flat state 表做执行层读写
差异: Erigon 的 HexPatricia 是行列网格结构（128×16），N42 继承了但已废弃
      N42 的 JMT 是完整的 Merkle 树，支持证明生成
      N42 额外有 LtHash 做 O(k) 验证（Erigon 无此层）
```

### 4.3 Reth

```
Reth:   MPT (Keccak256) + parallel trie hashing
N42:    JMT (Blake3) + LtHash parallel

Reth 的并行 trie hashing 在多核下有优势
N42 的 Blake3 本身 SIMD 加速 + JMT BatchUpdate 排序后顺序写入
```

### 4.4 Aptos (同为 JMT 用户)

```
Aptos:  JMT (SHA3-256) + sparse Merkle 证明 + 版本化 (versioned JMT)
N42:    JMT (Blake3) + LtHash + 引用计数 GC

共同: 都源自 Diem/Libra 的 JMT 论文
差异: Aptos 的 JMT 保留所有版本（versioned），占用更多存储
      N42 的 JMT 使用引用计数 GC 在线裁剪旧节点
      N42 额外有 LtHash 层做 O(k) 验证
      N42 用 Blake3（比 SHA3 快 3-5x）
```

---

## 五、代码位置参考

### 已完成组件

| 组件 | 位置 |
|------|------|
| JMT 核心（树/节点/缓存/GC/归档） | `lib/jmt/` |
| JMT Merkle 证明 | `lib/jmt/proof.go` |
| JMT MDBX 后端 | `lib/jmt/store/mdbx_store.go` |
| JMT 归档压缩 | `lib/jmt/archive/` |
| LtHash 摘要 | `lib/lthash/` |
| 可插拔 RootComputer 架构 | `modules/state/commitment/` |
| 账户编码 (107 bytes) | `modules/state/commitment/account_encoding.go` |
| Key 哈希 (Blake3) | `modules/state/commitment/key_hasher.go` |
| Witness 生成/验证 | `modules/state/witness/` |
| Snapshot 加速层 | `conf/snapshot_accel_config.go` |
| LtHash fork 激活门控 | `params/config.go:LtHashTime` |
| Legacy HexPatricia (废弃) | `lib/commitment/hex_patricia_hashed.go` |
| Legacy BinaryPatricia (废弃) | `lib/commitment/bin_patricia_hashed.go` |
| Pedersen Hash (未启用) | `lib/pedersen_hash/` |

---

## 六、Gap 与路线图

### 待完成

| Gap | 说明 | 优先级 |
|-----|------|--------|
| **主网状态迁移** | 从 legacy `GenerateRootHash` 迁移到 JMT+LtHash 需全量重算 state root | P0 |
| **State root 修复** | 当前主网同步跳过了 state root 验证（EVM 修复导致 divergence） | P0 |
| **ZK-friendly hash 可选** | 当前 Blake3 约束 ~10K，未来可切换 Poseidon (~300) 优化 ZK proof | P2 |
| **版本化 JMT** | 当前 JMT 无版本号，不支持历史状态查询（需 changeset 回溯） | P2 |

### 迁移策略

```
Phase 1 (当前): 完成首遍数据同步（skip state root）
Phase 2: 重放全量状态，同时写入 JMT + LtHash
Phase 3: 从特定高度激活 LtHash 验证（LtHashTime fork）
Phase 4: 新出块使用 JMT root 作为 header.Root
Phase 5: 废弃 GenerateRootHash，全面切换
```

---

## 七、结论

N42 的 **JMT + LtHash + Flat State 三层架构** 在技术上优于以下方案：

1. **优于 MPT**: Blake3 更快、proof 更小、bitmap 稀疏存储更紧凑
2. **优于 Verkle**: 量子安全（Blake3 128-bit vs Pedersen ECDLP 可破）
3. **优于纯 flat**: JMT 保留 Merkle proof 能力，支持 stateless 验证和跨链桥
4. **独特的 LtHash 层**: O(k) 块验证是独有优势，其他方案均需 O(k × log N)

当前最大 blocker 是**主网状态迁移**——需要从 legacy hash 全面切换到 JMT。正在进行的首遍数据同步是这个迁移的前置步骤。
