# Binary Blake3 Tree (Path-Addressed + Inline) 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现 Binary Blake3 Merkle Tree 替代 JMT 16叉，使用 path-addressed 存储 + 叶子内联优化，从备份数据重放验证真实存储量，对比 JMT。

**Architecture:** 新建 `lib/bmt/` (Binary Merkle Tree) 包，与 `lib/jmt/` 并存。复用 `commitment/` 接口（RootComputer），通过 replay-v2 `--tree=bmt` 开关选择。最终对比两种树在相同数据上的存储量、proof 大小、性能。

**Tech Stack:** Go, Blake3, MDBX (path-addressed), Erigon V2 account encoding

---

## 设计

### 节点格式

```
Path-addressed key: [version:8B][path:变长 1-32B] = 9-40B avg ~14B
  version = block height (支持历史 proof)
  path = bit-path from root, 每 8 bits 压缩为 1 byte

节点 value 格式 (通过长度区分类型):
  len == 32:  Internal 节点 — value = hash(left || right)
  len < 32:   Inline leaf  — value = V2 encoded account/storage (avg 12B)
  len > 32:   Extended leaf — value = V2 encoded data (罕见)
  len == 33:  Leaf with 32B value — value = data(32B) + type_tag(1B=0x01)

不需要额外 type byte! 长度即类型
```

### 存储布局

```
MDBX 表:
  BMTNode      — path-addressed 节点 (key=version+path, value=hash/inline)
  BMTRoot      — 最新 root + version (崩溃恢复)
  BMTVersionRoots — height → root hash (历史 proof 索引)

seg 文件 (冷存储):
  bmt-archive/*.seg — 历史节点归档 (字典压缩)
  bmt-archive/*.idx — RecSplit O(1) 索引

分级:
  MDBX: 最近 1K 块的节点 (热)
  seg:  历史节点 (冷)
```

### 预估存储 (N42 12M blocks, 22M 变更)

```
Binary path-addressed + inline:
  内部节点: key(14B avg) + value(32B) + MDBX(15B) = 61B
  叶子节点: key(14B avg) + value(12B avg) + MDBX(12B) = 38B (inline, 无独立条目)

  但 inline 叶子嵌入父节点 → 父节点 value 变为:
    left=hash(32B), right=inline(12B) → value=44B
    或 left=inline, right=hash → 同上
    平均: ~38B (有些父有两个 hash child, 有些有 inline)

  去重后节点数: ~200M (叶子不单独存 → 比 JMT 少)
  预估总存储: 200M × ~55B avg = 10.3 GiB
  vs JMT 实测: 98.6 GiB → 预计 9.6x 小
```

---

## File Structure

| File | Responsibility |
|------|---------------|
| **Create:** `lib/bmt/tree.go` | Binary Merkle Tree 核心: Put/Get/Delete/Root |
| **Create:** `lib/bmt/node.go` | 节点编码/解码 (path-key, inline detection) |
| **Create:** `lib/bmt/proof.go` | Merkle proof 生成/验证 |
| **Create:** `lib/bmt/store.go` | NodeStore 接口 (path-addressed) |
| **Create:** `lib/bmt/store/mdbx_store.go` | MDBX path-addressed 实现 |
| **Create:** `lib/bmt/tree_test.go` | 核心测试 |
| **Create:** `modules/state/commitment/bmt_commitment.go` | BMT → RootComputer 桥接 |
| **Modify:** `internal/replay/engine_v2.go` | 添加 `--tree=bmt` 开关 |
| **Modify:** `internal/replay/config_v2.go` | 添加 TreeType 配置 |
| **Modify:** `cmd/n42/replay_v2_cmd.go` | 添加 CLI flag |
| **Modify:** `modules/table.go` | 注册 BMTNode/BMTRoot/BMTVersionRoots 表 |

---

### Task 1: 核心 Binary Merkle Tree

**Files:**
- Create: `lib/bmt/tree.go`
- Create: `lib/bmt/node.go`
- Create: `lib/bmt/store.go`

- [ ] **Step 1: 定义接口和类型**

```go
// lib/bmt/node.go
package bmt

import "github.com/n42blockchain/N42/common/types"

const HashSize = 32

type Hash = types.Hash

// NodeValue 通过长度区分类型:
//   32B = internal hash; <32B = inline leaf; 33B = leaf with 32B data + tag
type NodeValue []byte

func (v NodeValue) IsHash() bool   { return len(v) == HashSize }
func (v NodeValue) IsInline() bool { return len(v) != HashSize }

// PathKey encodes a bit-path from root to this node.
// Each byte holds 8 bits of the path. Length in bits stored separately.
type PathKey struct {
    Bytes  []byte // packed bits (MSB first)
    BitLen int    // actual number of bits used
}

// EncodeMDBXKey creates the MDBX key: version(8B) + packed path bytes
func (p PathKey) EncodeMDBXKey(version uint64) []byte {
    key := make([]byte, 8+len(p.Bytes))
    binary.BigEndian.PutUint64(key[:8], version)
    copy(key[8:], p.Bytes)
    return key
}
```

```go
// lib/bmt/store.go
package bmt

type NodeStore interface {
    Get(version uint64, path PathKey) (NodeValue, error)
    Put(version uint64, path PathKey, value NodeValue) error
    Delete(version uint64, path PathKey) error
}
```

```go
// lib/bmt/tree.go
package bmt

type Tree struct {
    root    Hash
    version uint64
    store   NodeStore
    dirty   map[string]NodeValue // path-string → value
}

func New(store NodeStore) *Tree
func NewFromRoot(store NodeStore, root Hash, version uint64) *Tree
func (t *Tree) Root() Hash
func (t *Tree) Version() uint64
func (t *Tree) Get(keyHash Hash) ([]byte, error)     // 从 key hash 查找 leaf
func (t *Tree) Put(keyHash Hash, value []byte) error  // 插入/更新 leaf
func (t *Tree) Delete(keyHash Hash) error              // 删除 leaf
func (t *Tree) FlushTo(store NodeStore) error          // 持久化 dirty 节点
```

- [ ] **Step 2: 实现 Put/Get (核心算法)**

```go
func (t *Tree) Put(keyHash Hash, value []byte) error {
    bits := hashToBits(keyHash) // 256 bits

    // 决定存储方式: inline (value < 32B) 或 hash pointer
    var leafValue NodeValue
    if len(value) < HashSize {
        leafValue = value // inline
    } else if len(value) == HashSize {
        leafValue = append(value, 0x01) // 33B, 加 tag 区分
    } else {
        leafValue = value // extended leaf
    }

    // 从 root 向下遍历, 找到插入位置
    // 更新路径上所有 internal 节点的 hash
    return t.insertAt(bits, 0, leafValue)
}

func (t *Tree) insertAt(bits []byte, depth int, value NodeValue) error {
    path := bitsToPath(bits, depth)

    // 读当前节点
    current, err := t.getNode(path)
    if err != nil {
        // 空位置, 直接写入
        t.setDirty(path, value)
        return t.updateAncestors(bits, depth)
    }

    if current.IsInline() {
        // 当前是 inline leaf, 需要分裂
        // 找到旧 leaf 的 key, 在下一层分叉
        return t.splitAndInsert(bits, depth, current, value)
    }

    // 当前是 hash pointer, 继续向下
    bit := getBit(bits, depth)
    if bit == 0 {
        return t.insertAt(bits, depth+1, value) // go left
    }
    return t.insertAt(bits, depth+1, value) // go right
}

func (t *Tree) updateAncestors(bits []byte, fromDepth int) error {
    // 从 fromDepth 向上到 root, 重新计算每层的 hash
    for d := fromDepth; d >= 0; d-- {
        path := bitsToPath(bits, d)
        leftChild, _ := t.getNode(appendBit(path, 0))
        rightChild, _ := t.getNode(appendBit(path, 1))

        newHash := blake3Hash(leftChild, rightChild)
        t.setDirty(path, NodeValue(newHash[:]))
    }
    t.root = t.computeRootHash()
    return nil
}
```

- [ ] **Step 3: 实现 proof 生成/验证**

```go
// lib/bmt/proof.go
type Proof struct {
    Key      Hash
    Siblings []Hash // 从叶子到 root 的 sibling hash, 每层一个
    Value    []byte // 叶子值 (inline 或 hash)
    Depth    int
}

func (t *Tree) GetProof(keyHash Hash) (*Proof, error) {
    bits := hashToBits(keyHash)
    proof := &Proof{Key: keyHash}

    for depth := 0; depth < 256; depth++ {
        path := bitsToPath(bits, depth)
        siblingPath := flipLastBit(path)

        sibling, _ := t.getNode(siblingPath)
        if sibling == nil {
            proof.Siblings = append(proof.Siblings, Hash{}) // empty
        } else if sibling.IsHash() {
            var h Hash
            copy(h[:], sibling)
            proof.Siblings = append(proof.Siblings, h)
        } else {
            // sibling is inline → hash it for proof
            h := blake3.Sum256(sibling)
            proof.Siblings = append(proof.Siblings, h)
        }

        // 到达叶子?
        node, _ := t.getNode(appendBit(path, getBit(bits, depth)))
        if node != nil && node.IsInline() {
            proof.Value = node
            proof.Depth = depth + 1
            break
        }
    }
    return proof, nil
}

func VerifyProof(root Hash, proof *Proof) bool {
    current := blake3.Sum256(proof.Value)
    bits := hashToBits(proof.Key)

    for i := proof.Depth - 1; i >= 0; i-- {
        if getBit(bits, i) == 0 {
            current = blake3Hash(current[:], proof.Siblings[i][:])
        } else {
            current = blake3Hash(proof.Siblings[i][:], current[:])
        }
    }
    return current == root
}
```

- [ ] **Step 4: 测试**

```go
func TestBMTPutGet(t *testing.T) {
    tree := New(NewMemStore())
    key := blake3.Sum256([]byte("account1"))
    value := []byte{0x05} // nonce=5, V2 encoded, 1 byte

    tree.Put(key, value)
    got, err := tree.Get(key)
    assert(bytes.Equal(got, value))
}

func TestBMTProofRoundtrip(t *testing.T) { ... }
func TestBMTInlineVsHash(t *testing.T) { ... }
func TestBMT1000Inserts(t *testing.T) { ... }
```

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(bmt): Binary Merkle Tree core — Put/Get/Delete/Proof"
```

---

### Task 2: MDBX Path-Addressed Store

**Files:**
- Create: `lib/bmt/store/mdbx_store.go`
- Modify: `modules/table.go`

- [ ] **Step 1: 注册 BMT 表**

在 `modules/table.go` 添加:
```go
BMTNode         = "BMTNode"         // version(8B)+path(var) → hash/inline value
BMTRoot         = "BMTRoot"         // "root" → root hash, "version" → height
BMTVersionRoots = "BMTVersionRoots" // height(8B) → root hash(32B)
```

- [ ] **Step 2: 实现 MDBX store**

```go
// lib/bmt/store/mdbx_store.go
type MDBXStore struct {
    tx    kv.RwTx
    table string
}

func (s *MDBXStore) Get(version uint64, path bmt.PathKey) (bmt.NodeValue, error) {
    key := path.EncodeMDBXKey(version)
    data, err := s.tx.GetOne(s.table, key)
    if err != nil || data == nil {
        return nil, bmt.ErrNotFound
    }
    cp := make([]byte, len(data))
    copy(cp, data)
    return cp, nil
}

func (s *MDBXStore) Put(version uint64, path bmt.PathKey, value bmt.NodeValue) error {
    key := path.EncodeMDBXKey(version)
    return s.tx.Put(s.table, key, value)
}
```

- [ ] **Step 3: 测试 MDBX roundtrip**
- [ ] **Step 4: Commit**

```bash
git commit -m "feat(bmt): MDBX path-addressed store + table registration"
```

---

### Task 3: BMT Commitment 桥接

**Files:**
- Create: `modules/state/commitment/bmt_commitment.go`

- [ ] **Step 1: 实现 RootComputer 接口**

```go
type BMTCommitment struct {
    tree *bmt.Tree
}

func (c *BMTCommitment) UpdateAccount(addr types.Address, acct *account.StateAccount) error {
    keyHash := AccountKeyHash(addr) // 复用 JMT 的 key hash
    if isAccountEmpty(acct) {
        return c.tree.Delete(keyHash)
    }
    value := acct.MarshalV2() // V2 编码, avg 12B → inline!
    return c.tree.Put(keyHash, value)
}

func (c *BMTCommitment) UpdateStorage(addr types.Address, slot types.Hash, val *uint256.Int) error {
    keyHash := StorageKeyHash(addr, slot)
    if val == nil || val.IsZero() {
        return c.tree.Delete(keyHash)
    }
    // trim leading zeros for inline benefit
    b := val.Bytes32()
    start := 0
    for start < 31 && b[start] == 0 { start++ }
    return c.tree.Put(keyHash, b[start:]) // avg ~8B → inline!
}
```

- [ ] **Step 2: 实现 BMTRootComputer (RootComputer 接口)**

```go
type BMTRootComputer struct {
    commitment *BMTCommitment
}

func (r *BMTRootComputer) ComputeRoot(
    accounts map[types.Address]*account.StateAccount,
    storage map[types.Address]map[types.Hash]*uint256.Int,
) (types.Hash, error) {
    for addr, acct := range accounts {
        r.commitment.UpdateAccount(addr, acct)
    }
    for addr, slots := range storage {
        for slot, val := range slots {
            r.commitment.UpdateStorage(addr, slot, val)
        }
    }
    return types.Hash(r.commitment.tree.Root()), nil
}
```

- [ ] **Step 3: 测试**
- [ ] **Step 4: Commit**

```bash
git commit -m "feat(bmt): BMTCommitment + BMTRootComputer bridging"
```

---

### Task 4: Replay-V2 集成 `--tree=bmt`

**Files:**
- Modify: `internal/replay/config_v2.go` — 添加 `TreeType string`
- Modify: `internal/replay/engine_v2.go` — 根据 TreeType 选择 JMT 或 BMT
- Modify: `cmd/n42/replay_v2_cmd.go` — 添加 `--tree` flag

- [ ] **Step 1: ConfigV2 添加 TreeType**

```go
type ConfigV2 struct {
    // ... existing fields ...
    TreeType string // "jmt" (default) or "bmt"
}
```

- [ ] **Step 2: engine_v2.go 中根据 TreeType 初始化不同的树**

```go
if e.cfg.TreeType == "bmt" {
    bmtStore := bmtstore.NewMDBXStore(dstTx, "BMTNode")
    bmtTree := bmt.New(bmtStore)
    bmtCommit := commitment.NewBMTCommitment(bmtTree)
    rootComputer = commitment.NewBMTRootComputer(bmtCommit)
} else {
    // existing JMT path
}
```

- [ ] **Step 3: CLI flag**

```go
&cli.StringFlag{Name: "tree", Usage: "Tree type: jmt or bmt", Value: "jmt"},
```

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(replay): --tree=bmt flag for Binary Merkle Tree replay"
```

---

### Task 5: 从备份重放 + 数据对比

- [ ] **Step 1: BMT 重放**

```bash
rm -rf d:/mainnetnew && mkdir -p d:/mainnetnew
n42 replay-v2 --source d:/mainnet --target d:/mainnetnew \
    --tree bmt --batch 100000 --chain mainnet_v2
```

- [ ] **Step 2: db stats 对比**

```bash
# BMT 数据
n42 db stats --datadir d:/mainnetnew

# JMT 数据 (备份)
n42 db stats --datadir d:/mainnetnew_backup
```

- [ ] **Step 3: 记录对比**

```
预期:
  JMT:  98.6 GiB (150M 节点, avg 697B in MDBX)
  BMT:  ~10 GiB  (200M 节点, avg ~55B in MDBX)
  比率:  ~10x 小
```

- [ ] **Step 4: Proof 大小对比**

```bash
# 随机抽取 1000 个 key, 生成 proof, 比较大小
n42 bmt-verify --datadir d:/mainnetnew --sample 1000
```

- [ ] **Step 5: Commit 结果**

```bash
git commit -m "bench(bmt): real data comparison — BMT vs JMT storage and proof"
```

---

### Task 6: 冷热分层 (jmt-compact 的 BMT 版)

- [ ] **Step 1: BMT compact**

复用 `jmt-compact` 的 mark-sweep 思路:
- Walk HEAD root 收集热节点
- 归档冷节点到 seg 文件
- 保留 BMTVersionRoots 索引

- [ ] **Step 2: 执行 compact**

```bash
n42 bmt-compact --source d:/mainnetnew --target d:/mainnetnew_compact
```

- [ ] **Step 3: 对比**

```
预期:
  JMT 冷热:  14 GiB MDBX + 57 GiB seg = 71 GiB
  BMT 冷热:  ~1.5 GiB MDBX + ~8 GiB seg = ~10 GiB
  比率:       ~7x 小
```

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(bmt): cold/hot tiering with seg archive"
```

---

## 预期结果对比

| 指标 | JMT 16叉 (实测) | BMT 2叉 (预估) | 比率 |
|------|----------------|----------------|------|
| 全量历史 MDBX | 98.6 GiB | ~10 GiB | **9.9x ↓** |
| HEAD 树 MDBX | 2.53 GiB | ~300 MiB | **8.4x ↓** |
| 冷热后 MDBX | 1.23 GiB | ~150 MiB | **8.2x ↓** |
| 冷热后 seg | 57 GiB | ~8 GiB | **7.1x ↓** |
| 冷热后总计 | 71 GiB | ~10 GiB | **7.1x ↓** |
| 单 proof | 2,832B | 736B | **3.8x ↓** |
| 单次更新 CPU | ~120 ns | ~184 ns | 1.5x ↑ |
| 每块磁盘写 | 1.98 MB | ~0.5 MB | **4.0x ↓** |
