# codex/dev2603 分支代码评审

## 概述

对 `origin/codex/dev2603` 分支进行了全面评审（1133 文件变更，+9,108 / -98,779 行）。

## 正面评价

### Engine API 兼容性（优秀）
- Engine API v1/v4 完善，payload validation 增强
- osaka/pectra/fusaka EIP 实现覆盖全面
- eest 测试覆盖到位，29 个新测试函数，90 个断言

### 标准 TX 编解码（优秀）
- `ethereum_rlp.go` 301 行标准 ETH RLP 编解码
- 签名兼容性增强（多 fork 签名支持）

### Hive 测试框架（好）
- `genesis_hive.go` 支持 Hive 测试基础设施

---

## 严重问题

### 🔴 P0: Header.Hash() 改为 json.Marshal — 致命 bug

**文件：** `common/block/header.go`

```go
// 旧代码（正确）:
if IsLegacyHeader(h) {
    buf, err = h.legacyHashBytes()  // 结构化 RLP-like 编码
} else {
    buf, err = h.v2HashBytes()      // 结构化编码
}

// 新代码（错误）:
buf, err := json.Marshal(h)  // JSON 序列化
```

**问题：**
1. JSON 字段顺序在 Go 中是字母序，不是声明序 — 不同 Go 版本可能不同
2. JSON 编码与 RLP 编码的输出完全不同 — 同一个 header 产生不同 hash
3. 所有历史 block hash 全部失效 — 破坏链兼容性
4. 性能差：JSON marshal 比结构化编码慢 5-10x

**建议：** 恢复结构化编码。已提供 21 字段 ETH Pectra 兼容方案（见 `feat/header-eth-compat` 分支）。

---

### 🔴 P0: 编译失败

```
cmd/n42/eracmd.go:17:2: no required module provides package
    github.com/n42blockchain/N42/internal/sync/torrentsync
```

分支无法编译通过。

---

### 🟡 P1: 删除 N42 扩展模块过于激进

删除了 374 个文件，包括所有核心扩展功能：

| 模块 | 删除文件数 | 状态 |
|------|-----------|------|
| AI Agent（钱包/训练/推理/治理） | 20+ | ❌ 全删 |
| 分布式（消息/计算/存储） | 50+ | ❌ 全删 |
| ZK 跨链桥 | 10+ | ❌ 全删 |
| Replay 引擎 | 10+ | ❌ 全删 |
| BMT/JMT 状态树 | 20+ | ❌ 全删 |
| MCP Server | 5+ | ❌ 全删 |
| MEV 优化器 | 5+ | ❌ 全删 |

**建议：** 用编译标签或配置开关控制模块加载，不要从代码库删除：
```go
//go:build n42_extended

package ai
```

---

### 🟡 P1: engine_mpt.go 去掉了错误返回

```go
// 旧（正确）:
func (t *ethereumStackTrie) insert(...) error { ... }

// 新（隐藏错误）:
func (t *ethereumStackTrie) insert(...) { ... }  // void
```

去掉错误返回会隐藏 trie 插入失败。

---

## 建议合并策略

### 不要整体合并 codex 分支

原因：
1. 编译不通过
2. Header hash 致命 bug
3. 模块删除不可接受

### 逐文件移植有价值的改动

| 文件 | 行数 | 优先级 |
|------|------|--------|
| `internal/api/engine_api_v4.go` | 250 | P0 |
| `internal/state_transition.go` | 137 | P0 |
| `internal/vm/eips_pectra.go` | 88 | P0 |
| `common/transaction/ethereum_rlp.go` | 301 (new) | P0 |
| `internal/api/engine_payload_validation.go` | 41 | P1 |
| `internal/vm/eips_osaka.go` | 小 | P1 |
| `internal/vm/eips_fusaka.go` | 39 | P1 |
| `conf/genesis_hive.go` | 50 | P2 |

### 统一 Header 标准

已提供 `feat/header-eth-compat` 分支：
- 21 字段 ETH Pectra 兼容
- 新增 UncleHash, WithdrawalsHash, ParentBeaconRoot, RequestsHash
- BlobGasUsed/ExcessBlobGas 改为 *uint64（nil = pre-fork）
- LtHashRoot + 树 root + BLS 签名 → Extra 字段
- 保持结构化 hash 编码（不用 json.Marshal）

**请基于此分支统一 Header，不要用 json.Marshal 方案。**

---

## 双模式架构建议

```
N42 节点：
  --mode eth    → 纯 ETH EL（标准兼容，无扩展模块）
  --mode n42    → 全功能（含 AI/bridge/distributed/replay）
  默认: n42
```

两个模式共存同一代码库，通过编译标签或运行时配置切换。

---

*评审时间：2026-03-30*
*分支：origin/codex/dev2603 @ 44d7950*
