# N42-go 重构蓝图

> 版本: v1.0 | 日期: 2025-12-15 | 作者: 架构重构工作组
>
> 2026-03-16 仓库复核：
> 1. 这是一份历史蓝图，不等于当前仓库目录结构的逐项实况。
> 2. 已直接复核并确认存在的落地点包括：`modules/state/interfaces.go`、`internal/vm/precompiles/registry.go`、`internal/vm/interface.go`、`internal/consensus/engine.go`、`internal/consensus/base.go`、`internal/p2p/sync_interface.go`、`internal/api/router.go`。
> 3. `modules/state` 已不再依赖 `internal/avm/rlp`，当前实际落地是依赖 `common/rlp`，不是本蓝图里写的 `common/encoding/rlp.go`。
> 4. `internal/api/eth`、`internal/api/n42` 等 namespace 目录当前仓库中不存在，RPC 层职责分离的实际落地以 `internal/api/router.go`、`internal/api/backend.go`、`internal/api/interface.go` 为主。
> 5. 本次实际跑过：`go test -count=1 ./modules/state ./internal/vm ./internal/consensus/... ./internal/sync ./internal/p2p ./internal/api`。

---

## 一、当前模块边界图

### 1.1 顶层包结构

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              N42-gov5                                        │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌──────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐             │
│  │   cmd/   │    │  conf/   │    │ params/  │    │   log/   │             │
│  │  (入口)   │───▶│  (配置)   │───▶│ (链参数)  │    │  (日志)   │             │
│  └────┬─────┘    └──────────┘    └──────────┘    └──────────┘             │
│       │                                                                     │
│       ▼                                                                     │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                         internal/ (核心层)                           │   │
│  ├─────────────────────────────────────────────────────────────────────┤   │
│  │  ┌────────────┐  ┌────────────┐  ┌────────────┐  ┌────────────┐    │   │
│  │  │ blockchain │  │  miner/    │  │ consensus/ │  │   sync/    │    │   │
│  │  │   (链核心)  │◀─│  (出块)     │◀─│  (共识)     │◀─│  (同步)     │    │   │
│  │  └─────┬──────┘  └────────────┘  └─────┬──────┘  └────────────┘    │   │
│  │        │                               │                            │   │
│  │        ▼                               ▼                            │   │
│  │  ┌────────────┐  ┌────────────┐  ┌────────────┐  ┌────────────┐    │   │
│  │  │   vm/      │  │ txspool/   │  │   api/     │  │   p2p/     │    │   │
│  │  │  (EVM)     │  │  (交易池)   │  │  (JSON-RPC)│  │  (网络)     │    │   │
│  │  └─────┬──────┘  └─────┬──────┘  └────────────┘  └────────────┘    │   │
│  │        │               │                                            │   │
│  └────────┼───────────────┼────────────────────────────────────────────┘   │
│           │               │                                                 │
│           ▼               ▼                                                 │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                        modules/ (模块层)                             │   │
│  ├─────────────────────────────────────────────────────────────────────┤   │
│  │  ┌────────────┐  ┌────────────┐  ┌────────────┐  ┌────────────┐    │   │
│  │  │  state/    │  │  rawdb/    │  │  ethdb/    │  │ rpc/jsonrpc│    │   │
│  │  │ (状态存储)  │◀─│ (原始存储)  │◀─│  (KV接口)   │  │  (RPC框架)  │    │   │
│  │  └────────────┘  └────────────┘  └────────────┘  └────────────┘    │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                        common/ (公共层)                              │   │
│  ├─────────────────────────────────────────────────────────────────────┤   │
│  │  ┌────────────┐  ┌────────────┐  ┌────────────┐  ┌────────────┐    │   │
│  │  │  block/    │  │transaction/│  │  types/    │  │  crypto/   │    │   │
│  │  │  (区块)     │  │  (交易)     │  │  (类型)     │  │  (加密)     │    │   │
│  │  └────────────┘  └────────────┘  └────────────┘  └────────────┘    │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 1.2 关键依赖方向

```
调用方向 (→ 表示依赖/导入):

cmd/n42 → internal/node → internal/blockchain → modules/state
                       → internal/miner     → internal/consensus
                       → internal/sync      → internal/p2p
                       → internal/api       → modules/rpc/jsonrpc

internal/blockchain → modules/rawdb   (数据读写)
                   → modules/state   (状态管理)
                   → internal/vm     (EVM执行)
                   → internal/consensus (共识验证)

internal/vm → modules/state (状态读写)
           → common/crypto (加密原语)

modules/state → modules/ethdb (底层存储)
             → modules/changeset (变更集)
             → internal/avm/rlp (⚠️ 反向依赖!)
```

---

## 二、高耦合/循环依赖/隐式全局状态清单

### 2.1 循环依赖风险

| 问题类型 | 文件路径 | 符号/位置 | 严重度 | 说明 |
|---------|---------|----------|--------|------|
| 反向依赖 | `modules/state/intra_block_state.go` | `import "internal/avm/rlp"` | 🔴 高 | modules 层不应依赖 internal 层 |
| 反向依赖 | `modules/state/entire.go:157` | `rlp.DecodeBytes` | 🔴 高 | 状态层依赖 AVM RLP 编码 |
| 接口重复 | `common/blockchain.go` vs `internal/consensus/consensus.go` | `ChainReader` | 🟡 中 | 已修复为 ConsensusChainReader |
| 接口重复 | `common/interfaces.go` vs `interfaces.go` | `ChainStateReader` | 🟡 中 | 已修复为 AccountStateReader |

### 2.2 高耦合区域

| 模块 | 耦合对象 | 耦合指标 | 文件 | 问题描述 |
|-----|---------|---------|------|---------|
| `internal/blockchain.go` | 14+ packages | 50+ imports | L1-80 | God Object, 负责过多职责 |
| `internal/node/node.go` | 25+ packages | 组装所有模块 | 全文件 | 启动入口过于庞大 |
| `internal/api/api.go` | 15+ packages | 混合多种API | 全文件 | API层与业务逻辑耦合 |
| `modules/state/intra_block_state.go` | 12+ packages | 764行 | 全文件 | 状态管理过于集中 |

### 2.3 隐式全局状态

| 位置 | 全局变量 | 类型 | 风险 | 建议 |
|-----|---------|-----|------|------|
| `internal/vm/contracts.go:49-144` | `PrecompiledContractsXXX` | `map[Address]Contract` | 🟡 中 | 移入 ChainConfig 或注入 |
| `internal/vm/interpreter.go:44` | `pool = sync.Pool{}` | `sync.Pool` | 🟢 低 | 可接受的对象池 |
| `internal/p2p/service.go:42-45` | `pollingPeriod`, `refreshRate` | `time.Duration` | 🟡 中 | 移入配置 |
| `internal/p2p/encoder/ssz.go:16-17` | `MaxGossipSize`, `MaxChunkSize` | `uint64` | 🟡 中 | 移入 params 或 conf |
| `internal/p2p/gossip_topic_mappings.go:37` | `init()` 注册全局 | function | 🔴 高 | 改为显式注册 |
| `internal/avm/types/block.go:15` | `EmptyUncleHash` | `types.Hash` | 🟢 低 | 可接受的常量 |
| `log/logrus.go:1` | Logger instance | `*logrus.Logger` | 🟡 中 | 考虑依赖注入 |

### 2.4 `init()` 函数统计

共发现 **36 个 init() 函数**，高风险的包括：
- `internal/vm/contracts.go:166` - 预编译合约注册
- `internal/p2p/gossip_topic_mappings.go:37` - Gossip 主题映射
- `internal/tracers/native/*.go` - 多个 tracer 注册

---

## 三、优先切分的 5 个边界

### 优先级排序依据:
1. **业务影响**: 对核心功能的影响程度
2. **耦合度**: 当前耦合复杂度
3. **风险可控**: 重构风险与回滚成本
4. **测试覆盖**: 现有测试保障程度

### 3.1 边界清单

| 优先级 | 边界名称 | 涉及包 | 当前问题 | 目标状态 |
|-------|---------|-------|---------|---------|
| **P0** | **State DB 层** | `modules/state/`, `modules/ethdb/`, `modules/rawdb/` | 反向依赖 internal/avm/rlp | 完全自包含的状态管理模块 |
| **P1** | **执行层 (EVM)** | `internal/vm/`, `internal/state_*.go` | 预编译合约硬编码，全局注册 | 可插拔的执行引擎 |
| **P2** | **共识层** | `internal/consensus/` | 接口不统一，apoa/apos 重复 | 统一 Engine 接口 |
| **P3** | **同步 Pipeline** | `internal/sync/`, `internal/download/` | P2P 与同步逻辑混杂 | 清晰的同步状态机 |
| **P4** | **RPC 层** | `internal/api/`, `modules/rpc/jsonrpc/` | API 与业务逻辑耦合 | 纯粹的 API 网关层 |

---

## 四、PR 阶段划分 (6-10 个可独立合并的 PR)

### Phase 1: 基础清理 (已完成)

#### PR 1.1: 代码清理 ✅
- **改动范围**: `internal/blockchain.go`, `internal/blockhelp.go`
- **接口变化**: 无
- **回滚策略**: git revert
- **测试点**: `go build ./...` 通过

#### PR 1.2: 命名统一 ✅
- **改动范围**: 全仓库 (ast → n42)
- **接口变化**: 协议字符串变更
- **回滚策略**: git revert
- **测试点**: 节点启动、P2P 握手

#### PR 1.3: 包别名清理 ✅
- **改动范围**: `block2 → block`, `mvm_types → avmtypes`
- **接口变化**: 无
- **回滚策略**: git revert
- **测试点**: 编译通过

#### PR 1.4: 接口统一 ✅
- **改动范围**: `consensus.ChainReader → ConsensusChainReader`
- **接口变化**: 内部接口重命名
- **回滚策略**: git revert
- **测试点**: 编译通过

---

### Phase 2: State DB 边界重构 (P0)

#### PR 2.1: 消除 modules/state 对 internal/avm/rlp 的依赖
> 2026-03-16 复核：这一项的目标在当前仓库已基本达成，但实际路径是 `common/rlp`，不是下方计划中的 `common/encoding/rlp.go`。

```
改动范围:
├── modules/state/entire.go          (移除 rlp 依赖)
├── modules/state/intra_block_state.go (移除 rlp 依赖)
├── common/encoding/                  (新建: 通用编码包)
│   └── rlp.go                        (RLP 编码抽象)
└── internal/avm/rlp/                 (保留原有实现)
```

**接口变化**:
```go
// common/encoding/encoder.go (新增)
type Encoder interface {
    EncodeToBytes(val interface{}) ([]byte, error)
    DecodeBytes(data []byte, val interface{}) error
}

// 默认实现使用 internal/avm/rlp，但 modules 层只依赖接口
```

**回滚策略**: 
- 保留原 `internal/avm/rlp` 包
- 新增的 `common/encoding` 可独立删除

**测试点**:
- [ ] 状态序列化/反序列化一致性测试
- [ ] Snapshot 格式兼容性测试
- [ ] `go build ./...` 通过
- [ ] 循环依赖检测: `go list -m -f '{{.Path}}' all | xargs go list -f '{{.ImportPath}} -> {{.Imports}}'`

#### PR 2.2: StateDB 接口抽象
```
改动范围:
├── modules/state/interface.go        (新建: 状态接口定义)
├── modules/state/reader.go           (重构: 实现 StateReader)
├── modules/state/writer.go           (重构: 实现 StateWriter)
└── internal/vm/evm.go                (修改: 依赖接口而非实现)
```

**接口变化**:
```go
// modules/state/interface.go
type StateReader interface {
    ReadAccountData(address types.Address) (*account.StateAccount, error)
    ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error)
    ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error)
}

type StateWriter interface {
    UpdateAccountData(address types.Address, original, account *account.StateAccount) error
    UpdateAccountCode(address types.Address, incarnation uint16, codeHash types.Hash, code []byte) error
    DeleteAccount(address types.Address, original *account.StateAccount) error
}
```

**回滚策略**: 接口层可独立回滚

**测试点**:
- [ ] StateReader/Writer 单元测试
- [ ] EVM 状态访问测试
- [ ] Reorg 后状态一致性测试

---

### Phase 3: 执行层重构 (P1)

#### PR 3.1: 预编译合约可配置化
```
改动范围:
├── internal/vm/contracts.go          (移除全局 map)
├── internal/vm/precompiles/          (新建目录)
│   ├── registry.go                   (合约注册表)
│   ├── ecrecover.go
│   ├── sha256.go
│   └── ...
├── params/config.go                  (添加 precompiles 配置)
└── internal/vm/evm.go               (使用注册表)
```

**接口变化**:
```go
// internal/vm/precompiles/registry.go
type PrecompileRegistry struct {
    contracts map[types.Address]PrecompiledContract
}

func NewRegistry(chainConfig *params.ChainConfig, blockNum uint64) *PrecompileRegistry

// 移除全局变量，通过 EVM 构造函数注入
```

**回滚策略**: 保留原有全局 map 作为 fallback

**测试点**:
- [ ] 各硬分叉预编译合约行为测试
- [ ] Gas 计算一致性测试
- [ ] 主网历史区块重放测试

#### PR 3.2: EVM 执行引擎接口化
```
改动范围:
├── internal/vm/interface.go          (新建: VM 接口)
├── internal/vm/evm.go                (重构: 实现接口)
└── internal/blockchain.go            (修改: 依赖接口)
```

**接口变化**:
```go
// internal/vm/interface.go
type VM interface {
    Call(caller ContractRef, addr types.Address, input []byte, gas uint64, value *uint256.Int) ([]byte, uint64, error)
    Create(caller ContractRef, code []byte, gas uint64, value *uint256.Int) ([]byte, types.Address, uint64, error)
}
```

**回滚策略**: 接口是纯新增

**测试点**:
- [ ] EVM 调用兼容性测试
- [ ] 合约创建测试
- [ ] 深度调用测试

---

### Phase 4: 共识层重构 (P2)

#### PR 4.1: 统一共识 Engine 接口
```
改动范围:
├── internal/consensus/consensus.go   (精简接口)
├── internal/consensus/engine.go      (新建: 共识引擎基类)
├── internal/consensus/apoa/         (重构: 实现统一接口)
├── internal/consensus/apos/         (重构: 实现统一接口)
└── internal/blockchain.go           (修改: 使用新接口)
```

**接口变化**:
```go
// internal/consensus/engine.go
type Engine interface {
    // 验证
    VerifyHeader(chain ChainHeaderReader, header block.IHeader) error
    VerifyHeaders(chain ChainHeaderReader, headers []block.IHeader) (chan<- struct{}, <-chan error)
    
    // 出块
    Prepare(chain ChainHeaderReader, header block.IHeader) error
    Finalize(chain ChainHeaderReader, header block.IHeader, state *state.IntraBlockState) error
    Seal(chain ChainHeaderReader, block block.IBlock, results chan<- block.IBlock, stop <-chan struct{}) error
    
    // 辅助
    Author(header block.IHeader) (types.Address, error)
    APIs(chain ConsensusChainReader) []jsonrpc.API
    Close() error
}
```

**回滚策略**: 旧接口保留为别名

**测试点**:
- [ ] APOA 共识测试
- [ ] APOS 共识测试
- [ ] 切换共识引擎测试

#### PR 4.2: 提取共识公共逻辑
```
改动范围:
├── internal/consensus/base.go        (新建: 基础实现)
├── internal/consensus/apoa/apoa.go   (重构: 继承 base)
├── internal/consensus/apos/apos.go   (重构: 继承 base)
└── internal/consensus/misc/          (重构: 公共工具)
```

**回滚策略**: 可逐文件回滚

**测试点**:
- [ ] 签名验证测试
- [ ] 难度计算测试
- [ ] Reward 计算测试

---

### Phase 5: 同步 Pipeline 重构 (P3)

#### PR 5.1: 同步状态机重构
```
改动范围:
├── internal/sync/state_machine.go    (新建: 同步状态机)
├── internal/sync/service.go          (重构: 使用状态机)
├── internal/sync/initial-sync/       (重构: 简化)
└── internal/download/                (重构: 纯下载逻辑)
```

**接口变化**:
```go
// internal/sync/state_machine.go
type SyncState int
const (
    SyncStateIdle SyncState = iota
    SyncStateInitialSync
    SyncStateCatchUp
    SyncStateSynced
)

type SyncStateMachine struct {
    state      SyncState
    blockchain common.IBlockChain
    p2p        p2p.P2P
    fetcher    *Fetcher
}
```

**回滚策略**: 新状态机独立于旧逻辑

**测试点**:
- [ ] Initial sync 测试
- [ ] Catch-up sync 测试
- [ ] Reorg 处理测试

#### PR 5.2: P2P 与同步解耦
```
改动范围:
├── internal/p2p/interface.go         (新建: P2P 接口)
├── internal/sync/fetcher.go          (新建: 数据获取器)
└── internal/sync/service.go          (修改: 依赖接口)
```

**回滚策略**: 接口是新增

**测试点**:
- [ ] 模拟 P2P 测试
- [ ] 断线重连测试
- [ ] Peer 评分测试

---

### Phase 6: RPC 层重构 (P4)

#### PR 6.1: API 层职责分离
> 2026-03-16 复核：当前仓库实际落地为 `internal/api/router.go`、`internal/api/backend.go`、`internal/api/interface.go` 的路由/后端抽象；下方按 namespace 拆目录的方案尚未落地为当前目录结构。

```
改动范围:
├── internal/api/eth/                 (新建: eth namespace)
│   ├── api.go
│   ├── block.go
│   ├── transaction.go
│   └── state.go
├── internal/api/n42/                 (新建: n42 namespace)
│   ├── api.go
│   └── deposit.go
├── internal/api/api.go               (重构: 路由分发)
└── internal/api/filters/             (保留)
```

**接口变化**:
```go
// internal/api/interface.go
type Backend interface {
    // Chain
    CurrentBlock() block.IBlock
    GetBlockByNumber(number *uint256.Int) (block.IBlock, error)
    GetBlockByHash(hash types.Hash) (block.IBlock, error)
    
    // State
    StateAt(blockNr uint64) (*state.IntraBlockState, error)
    
    // Transaction
    SendTransaction(tx *transaction.Transaction) error
    GetTransaction(hash types.Hash) (*transaction.Transaction, bool)
}
```

**回滚策略**: 新 API 目录可独立删除

**测试点**:
- [ ] eth_getBlockByNumber 测试
- [ ] eth_sendRawTransaction 测试
- [ ] eth_getLogs 测试

---

## 五、性能与正确性风险点

### 5.1 状态一致性风险

| 风险点 | 位置 | 描述 | 缓解措施 |
|-------|------|------|---------|
| **Reorg 状态回滚** | `internal/blockchain.go:1319` | reorg 时状态回滚可能不完整 | 增加 checkpoint 验证 |
| **Snapshot 格式** | `modules/state/entire.go` | Snapshot RLP 编码变更可能破坏兼容 | 版本化 Snapshot 格式 |
| **内存缓存一致性** | `internal/blockchain.go:118-124` | LRU 缓存与 DB 可能不一致 | 定期验证或事件驱动失效 |

### 5.2 Reorg 处理风险

```go
// internal/blockchain.go:1319 - reorg 关键路径
func (bc *BlockChain) reorg(tx kv.RwTx, oldBlock, newBlock block.IBlock) error {
    // ⚠️ 风险点1: 未处理 tx == nil 导致的事务回滚
    // ⚠️ 风险点2: deletedTxs/addedTxs 未正确更新交易池
    // ⚠️ 风险点3: 深度 reorg (>64 blocks) 可能导致状态不一致
}
```

**缓解措施**:
1. 添加 reorg 深度限制硬编码检查
2. 增加 reorg 前后状态根校验
3. 实现 reorg 审计日志

### 5.3 快照格式兼容性

```go
// modules/state/entire.go:152 - Snapshot 反序列化
func ReadSnapshotData(data []byte) (*Snapshot, error) {
    // ⚠️ 当前使用 RLP 编码，格式变更将导致旧快照不可读
}
```

**缓解措施**:
1. 添加版本标识字段
2. 实现向后兼容的解码逻辑
3. 提供迁移工具

### 5.4 编码兼容性风险

| 编码类型 | 位置 | 风险 | 缓解 |
|---------|------|------|------|
| **Block RLP** | `common/block/` | 字段变更破坏网络兼容 | 严格版本控制 |
| **Transaction RLP** | `common/transaction/` | 签名验证失败 | 兼容性测试套件 |
| **State Trie** | `modules/state/` | 状态根不匹配 | 主网区块重放测试 |
| **Protobuf** | `api/protocol/` | P2P 消息解析失败 | 保持 proto 向后兼容 |

### 5.5 性能回归风险

| 场景 | 当前性能 | 风险 | 监控指标 |
|-----|---------|------|---------|
| 区块导入 | ~100 blocks/s | 状态访问抽象化可能降低 20% | `block_import_time` |
| EVM 执行 | ~1M gas/s | 预编译注册表查找开销 | `evm_execution_time` |
| 状态读取 | ~10K ops/s | 接口间接调用开销 | `state_read_latency` |
| Reorg | <1s (depth<10) | 深度 reorg 超时 | `reorg_duration` |

---

## 六、测试策略

### 6.1 单元测试覆盖

```bash
# 当前覆盖率检查
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | grep total

# 目标: 核心模块 >80% 覆盖
# - internal/blockchain.go
# - internal/vm/
# - modules/state/
# - internal/consensus/
```

### 6.2 集成测试

| 测试场景 | 脚本位置 | 频率 |
|---------|---------|------|
| 主网区块同步 | `tests/sync_test.go` | 每 PR |
| Reorg 处理 | `tests/reorg_test.go` | 每 PR |
| 共识切换 | `tests/consensus_test.go` | Phase 4 后 |
| RPC 兼容性 | `tests/rpc_compat_test.go` | Phase 6 后 |

### 6.3 回归测试

```bash
# 使用 hive 测试套件
git clone https://github.com/ethereum/hive
cd hive
./hive --sim eth2/engine --client n42
```

---

## 七、时间线建议

```
Week 1-2:  Phase 2 (State DB 边界) - PR 2.1, 2.2
Week 3-4:  Phase 3 (执行层) - PR 3.1, 3.2
Week 5-6:  Phase 4 (共识层) - PR 4.1, 4.2
Week 7-8:  Phase 5 (同步 Pipeline) - PR 5.1, 5.2
Week 9-10: Phase 6 (RPC 层) - PR 6.1
Week 11:   集成测试 & 性能调优
Week 12:   文档更新 & 发布准备
```

---

## 八、附录

### A. 命令行工具

```bash
# 检查循环依赖
go mod graph | grep -E "n42blockchain/N42/internal.*n42blockchain/N42/modules|n42blockchain/N42/modules.*n42blockchain/N42/internal"

# 查看包依赖图
go list -f '{{.ImportPath}} {{.Imports}}' ./... | grep n42blockchain

# 检测全局变量
grep -rn "var .* = " --include="*.go" internal/ modules/ common/ | grep -v "_test.go"

# 统计 init() 函数
grep -rn "func init()" --include="*.go" .
```

### B. 参考资料

- [go-ethereum 架构](https://github.com/ethereum/go-ethereum)
- [Erigon 模块化设计](https://github.com/ledgerwatch/erigon)
- [Prysm P2P 实现](https://github.com/prysmaticlabs/prysm)

---

*文档维护: 每个 PR 合并后更新对应章节状态*
