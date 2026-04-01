# Erigon 变长编码全面迁移计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 统一全项目 StateAccount 编码为 Erigon 风格变长格式，替换当前的 Protobuf (avg 65B) 和固定 107B 两种格式。

**Architecture:** 在 `common/account/state_account.go` 新增 `EncodeForStorageV2` / `DecodeForStorageV2`（Erigon 变长编码），然后逐层替换所有 37 个编码位置。旧 Protobuf 编码保留为 `EncodeForStorageLegacy` 用于兼容迁移。

**Tech Stack:** Go, MDBX, JMT, LtHash, Protobuf (被替换)

---

## Erigon 变长编码格式

```
[fieldBits:1B][nonce:0-8B][balance:0-32B][incarnation:0-8B][codeHash:0-32B]

fieldBits:
  bit 0: HasNonce      (1=后续有 nonce 字段)
  bit 1: HasBalance    (1=后续有 balance 字段)
  bit 2: HasIncarnation(1=后续有 incarnation 字段)
  bit 3: HasCodeHash   (1=后续有 codeHash 字段)

数值字段使用最小字节编码:
  nonce=0       → 不写 (bit 0=0)
  nonce=5       → 写 1B: 0x05
  nonce=256     → 写 2B: 0x01 0x00
  nonce=2^64-1  → 写 8B

balance 同理 (uint256, 最多 32B, trim 前导零)
incarnation 同理 (最多 8B)
codeHash 固定 32B (有则写, 无则不写)

空账户 (nonce=0, balance=0, no code): 1 byte (fieldBits=0x00)
简单 EOA (nonce=5, balance=1ETH):     ~12 bytes
合约 (有 code+incarnation):           ~50 bytes
最大:                                  ~74 bytes
```

**对比:**

| 账户类型 | Protobuf (当前) | 固定 107B (JMT) | **Erigon 变长** |
|---------|----------------|----------------|----------------|
| 空 (nonce=0, bal=0) | ~10B | 107B | **1B** |
| 简单 EOA | ~20B | 107B | **~10B** |
| 有 balance 的 EOA | ~35B | 107B | **~20B** |
| 合约 | ~65B | 107B | **~50B** |
| **平均** | **65B** | **107B** | **~12B** |

---

## 影响范围 (37 个位置)

### 核心编码函数 (7 个 — Task 1)

| # | 文件 | 函数 | 当前格式 |
|---|------|------|---------|
| 1 | `common/account/state_account.go:62` | `EncodingLengthForStorage()` | Protobuf |
| 2 | `common/account/state_account.go:67` | `EncodeForStorage()` | Protobuf |
| 3 | `common/account/state_account.go:92` | `DecodeForStorage()` | Protobuf |
| 4 | `common/account/state_account.go:133` | `Marshal()` | Protobuf |
| 5 | `common/account/state_account.go:137` | `Unmarshal()` | Protobuf |
| 6 | `common/account/state_account.go:146` | `ToProtoMessage()` | Protobuf |
| 7 | `common/account/state_account.go:157` | `FromProtoMessage()` | Protobuf |

### JMT 承诺层 (5 个 — Task 2)

| # | 文件 | 函数 | 当前格式 |
|---|------|------|---------|
| 8 | `modules/state/commitment/account_encoding.go:37` | `EncodeAccountValue()` | 固定 107B |
| 9 | `modules/state/commitment/account_encoding.go:69` | `DecodeAccountValue()` | 固定 107B |
| 10 | `modules/state/commitment/jmt_commitment.go:65` | JMT leaf 写入 | 固定 107B |
| 11 | `modules/state/commitment/lthash_commitment.go:46-58` | LtHash 更新 | 固定 107B |
| 12 | `modules/state/commitment/root_computer.go:52` | BatchUpdate | 固定 107B |

### 状态读写器 (10 个 — Task 3)

| # | 文件 | 路径 |
|---|------|------|
| 13 | `modules/state/plain_state_writer.go:54` | PlainState 写 |
| 14 | `modules/state/plain_state_reader.go:42` | PlainState 读 |
| 15 | `modules/state/cached_state_writer.go:43` | Cache 写 |
| 16 | `modules/state/cached_state_reader.go:43` | Cache 读 |
| 17 | `modules/state/cached_state_reader.go:68` | Cache 填充 |
| 18 | `modules/state/plain_readonly.go:101,166` | 只读读取 |
| 19 | `modules/state/reader.go:77` | History 读 |
| 20 | `modules/ethdb/olddb/reader.go:59` | Legacy 读 |
| 21 | `modules/state/entire.go:185` | Entire 快照读 |
| 22 | `modules/rawdb/accessors_account.go:26` | RawDB 读 |

### 快照层 (6 个 — Task 4)

| # | 文件 | 路径 |
|---|------|------|
| 23 | `modules/state/snapshot/journal.go:71` | DiffLayer 序列化 |
| 24 | `modules/state/snapshot/journal.go:192` | DiffLayer 反序列化 |
| 25 | `modules/state/snapshot/disk_layer.go:85` | 磁盘快照读 |
| 26 | `modules/state/snapshot/tree.go:209,249` | 缓存合并 |
| 27 | `modules/rawdb/accessors_snapshot.go:24` | 快照 RawDB 写 |
| 28 | `modules/rawdb/accessors_snapshot.go:28` | 快照 RawDB 读 |

### 其他系统 (9 个 — Task 5)

| # | 文件 | 路径 |
|---|------|------|
| 29 | `internal/sync/snapsync/manager.go:684` | SnapSync 网络 |
| 30 | `internal/parallel/state_reader.go:138,144` | 并行执行 |
| 31 | `internal/txspool/read_state.go:42,65,87,117` | TxPool 验证 |
| 32 | `lib/state/domain_committed.go:115` | Domain 承诺 |
| 33 | `lib/commitment/hex_patricia_hashed_types.go:180` | Legacy HP |
| 34 | `cmd/n42/migratecmd.go:103,110` | JMT 迁移 |
| 35 | `modules/state/witness/state_reader.go:76` | Witness 读 |
| 36-37 | 多个测试文件 | 测试 |

---

## 实施任务

### Task 1: 核心编码函数 (最底层, 其他全部依赖)

**Files:**
- Modify: `common/account/state_account.go`
- Create: `common/account/state_account_test.go` (新增变长编码测试)

- [ ] **Step 1: 新增 Erigon 变长编码函数**

在 `state_account.go` 中新增（不改旧函数，先共存）:

```go
// EncodeForStorageV2 encodes a StateAccount using Erigon-style variable-length
// format. Fields with default values are omitted, prefixed by a fieldBits byte.
func (a *StateAccount) EncodeForStorageV2(buf []byte) int {
    var fieldBits byte
    pos := 1 // skip fieldBits byte

    if a.Nonce > 0 {
        fieldBits |= 1
        n := putUvarint(buf[pos:], a.Nonce)
        pos += n
    }
    if !a.Balance.IsZero() {
        fieldBits |= 2
        balBytes := a.Balance.Bytes32()
        // trim leading zeros
        start := 0
        for start < 31 && balBytes[start] == 0 { start++ }
        buf[pos] = byte(32 - start)
        pos++
        copy(buf[pos:], balBytes[start:])
        pos += 32 - start
    }
    if a.Incarnation > 0 {
        fieldBits |= 4
        n := putUvarint(buf[pos:], uint64(a.Incarnation))
        pos += n
    }
    if a.CodeHash != emptyCodeHash && a.CodeHash != (types.Hash{}) {
        fieldBits |= 8
        copy(buf[pos:], a.CodeHash[:])
        pos += 32
    }
    buf[0] = fieldBits
    return pos
}

// EncodingLengthForStorageV2 returns the byte length of the V2 encoding.
func (a *StateAccount) EncodingLengthForStorageV2() int {
    n := 1 // fieldBits
    if a.Nonce > 0 { n += uvarintSize(a.Nonce) }
    if !a.Balance.IsZero() {
        balBytes := a.Balance.Bytes32()
        start := 0
        for start < 31 && balBytes[start] == 0 { start++ }
        n += 1 + (32 - start) // length byte + trimmed balance
    }
    if a.Incarnation > 0 { n += uvarintSize(uint64(a.Incarnation)) }
    if a.CodeHash != emptyCodeHash && a.CodeHash != (types.Hash{}) { n += 32 }
    return n
}

// DecodeForStorageV2 decodes a V2-encoded StateAccount.
func (a *StateAccount) DecodeForStorageV2(enc []byte) error {
    a.Reset()
    if len(enc) == 0 { return nil }
    fieldBits := enc[0]
    pos := 1

    if fieldBits & 1 != 0 {
        v, n := getUvarint(enc[pos:])
        a.Nonce = v
        pos += n
    }
    if fieldBits & 2 != 0 {
        balLen := int(enc[pos])
        pos++
        var balBytes [32]byte
        copy(balBytes[32-balLen:], enc[pos:pos+balLen])
        a.Balance.SetBytes32(balBytes[:])
        pos += balLen
    }
    if fieldBits & 4 != 0 {
        v, n := getUvarint(enc[pos:])
        a.Incarnation = uint16(v)
        pos += n
    }
    if fieldBits & 8 != 0 {
        copy(a.CodeHash[:], enc[pos:pos+32])
        pos += 32
    }
    a.Initialised = true
    return nil
}

func putUvarint(buf []byte, v uint64) int {
    i := 0
    for v >= 0x80 { buf[i] = byte(v) | 0x80; v >>= 7; i++ }
    buf[i] = byte(v); return i + 1
}

func getUvarint(buf []byte) (uint64, int) {
    var v uint64; var s uint;
    for i, b := range buf {
        if b < 0x80 { return v | uint64(b)<<s, i + 1 }
        v |= uint64(b&0x7f) << s; s += 7
    }
    return v, len(buf)
}

func uvarintSize(v uint64) int {
    n := 1; for v >= 0x80 { v >>= 7; n++ }; return n
}
```

- [ ] **Step 2: 写测试**

```go
func TestEncodeDecodeV2(t *testing.T) {
    tests := []struct{
        name string
        acc  StateAccount
    }{
        {"empty", StateAccount{}},
        {"nonce only", StateAccount{Nonce: 5, Initialised: true}},
        {"balance only", StateAccount{Balance: *uint256.NewInt(1e18), Initialised: true}},
        {"full EOA", StateAccount{Nonce: 100, Balance: *uint256.NewInt(5e18), Initialised: true}},
        {"contract", StateAccount{Nonce: 1, Incarnation: 1, CodeHash: types.HexToHash("0xabc..."), Initialised: true}},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            buf := make([]byte, 128)
            n := tt.acc.EncodeForStorageV2(buf)
            if n != tt.acc.EncodingLengthForStorageV2() {
                t.Fatalf("length mismatch: encoded %d, calculated %d", n, tt.acc.EncodingLengthForStorageV2())
            }
            var dec StateAccount
            dec.DecodeForStorageV2(buf[:n])
            if dec.Nonce != tt.acc.Nonce || dec.Balance.Cmp(&tt.acc.Balance) != 0 ||
               dec.Incarnation != tt.acc.Incarnation || dec.CodeHash != tt.acc.CodeHash {
                t.Fatalf("roundtrip mismatch")
            }
        })
    }
}
```

- [ ] **Step 3: 验证测试通过**

Run: `go test ./common/account/ -run TestEncodeDecodeV2 -v`

- [ ] **Step 4: 替换主入口 — EncodeForStorage/DecodeForStorage 指向 V2**

```go
func (a *StateAccount) EncodeForStorage(buffer []byte) {
    n := a.EncodeForStorageV2(buffer)
    _ = n // buffer is pre-sized by caller via EncodingLengthForStorage
}

func (a *StateAccount) EncodingLengthForStorage() uint {
    return uint(a.EncodingLengthForStorageV2())
}

func (a *StateAccount) DecodeForStorage(enc []byte) error {
    return a.DecodeForStorageV2(enc)
}
```

保留 `Marshal()/Unmarshal()` 仍走 Protobuf（网络兼容），后续 Task 5 迁移。

- [ ] **Step 5: 运行全部 account 测试**

Run: `go test ./common/account/ -v`

- [ ] **Step 6: Commit**

```bash
git commit -m "feat(account): Erigon-style variable-length encoding (V2)"
```

---

### Task 2: JMT 承诺层迁移

**Files:**
- Modify: `modules/state/commitment/account_encoding.go`

- [ ] **Step 1: 替换 EncodeAccountValue 为变长**

```go
func EncodeAccountValue(a *account.StateAccount) []byte {
    buf := make([]byte, a.EncodingLengthForStorageV2())
    a.EncodeForStorageV2(buf)
    return buf
}

func DecodeAccountValue(data []byte, a *account.StateAccount) error {
    return a.DecodeForStorageV2(data)
}
```

删除 `const accountEncodingSize = 107`。

- [ ] **Step 2: 运行 commitment 测试**

Run: `go test ./modules/state/commitment/ -v`

- [ ] **Step 3: Commit**

```bash
git commit -m "feat(jmt): use Erigon variable-length encoding for JMT leaves"
```

---

### Task 3: 状态读写器迁移

所有 reader/writer 已经通过 `EncodeForStorage/DecodeForStorage` 间接调用。Task 1 已经将这些指向 V2。

- [ ] **Step 1: 运行全部 state 测试**

Run: `go test ./modules/state/... -v -count=1`

- [ ] **Step 2: 修复任何失败的测试**

- [ ] **Step 3: Commit**

```bash
git commit -m "test(state): verify all readers/writers work with V2 encoding"
```

---

### Task 4: 快照层迁移

快照层使用 `ToProtoMessage() + proto.Marshal()`，需要改为 V2。

- [ ] **Step 1: 修改 snapshot/journal.go 序列化**

将 `pb := acc.ToProtoMessage()` + `proto.Marshal(pb)` 替换为：
```go
buf := make([]byte, acc.EncodingLengthForStorageV2())
acc.EncodeForStorageV2(buf)
```

- [ ] **Step 2: 修改 snapshot/journal.go 反序列化**

将 `proto.Unmarshal(accData, pb)` + `acc.FromProtoMessage(pb)` 替换为：
```go
acc.DecodeForStorageV2(accData)
```

- [ ] **Step 3: 修改 snapshot/tree.go 和 disk_layer.go**

同样替换 protobuf 调用。

- [ ] **Step 4: 运行快照测试**

Run: `go test ./modules/state/snapshot/ -v -count=1`

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(snapshot): migrate to V2 encoding"
```

---

### Task 5: 其他系统迁移

- [ ] **Step 1: SnapSync (network)** — `internal/sync/snapsync/manager.go` — 替换 protobuf 解码
- [ ] **Step 2: Parallel reader** — `internal/parallel/state_reader.go` — 替换 proto.Marshal
- [ ] **Step 3: TxPool** — `internal/txspool/read_state.go` — 已通过 DecodeForStorage 间接迁移
- [ ] **Step 4: Domain committed** — `lib/state/domain_committed.go` — 已通过 DecodeForStorage 间接迁移
- [ ] **Step 5: Witness reader** — `modules/state/witness/state_reader.go` — 改用 DecodeForStorageV2
- [ ] **Step 6: Migrate-JMT** — `cmd/n42/migratecmd.go` — 替换 decodeProtoAccount
- [ ] **Step 7: 运行全部测试**

Run: `go test ./... 2>&1 | grep -E "FAIL|ok" | tail -30`

- [ ] **Step 8: Commit**

```bash
git commit -m "feat: complete V2 encoding migration across all subsystems"
```

---

### Task 6: 清理与验证

- [ ] **Step 1: 移除 Protobuf 编码路径** (可选, 如果网络协议不再需要)

将 `ToProtoMessage/FromProtoMessage/Marshal/Unmarshal` 标记为 deprecated。

- [ ] **Step 2: 运行 replay-v2 验证**

```bash
n42 replay-v2 --source d:/mainnet --target /tmp/v2test --to 10000 --jmt=false
```

- [ ] **Step 3: db stats 对比**

```bash
n42 db stats --datadir /tmp/v2test    # V2 编码
n42 db stats --datadir d:/mainnetnew  # 旧编码
```

预期 Account 表: 0.31 GiB → ~0.06 GiB (5x 缩小)

- [ ] **Step 4: Commit + push**

```bash
git commit -m "refactor: deprecate protobuf account encoding, V2 is default"
git push origin main
```

---

## 预期收益

| 存储位置 | 当前 | V2 后 | 节省 |
|---------|------|-------|------|
| Account 表 (5.2M 条) | 0.31 GiB (avg 65B) | **0.06 GiB (avg 12B)** | -81% |
| JMT Leaf (22M 条) | 2.35 GiB (107B each) | **0.33 GiB (15B avg)** | -86% |
| SnapshotAccount | ~0.31 GiB | ~0.06 GiB | -81% |
| DiffLayer journal | 变长 | 更小 | ~-60% |
| **总计** | **~3.0 GiB** | **~0.5 GiB** | **-83%** |
