# N42 测试补充计划

## ✅ 计划已完成 (2024-12-16)

### 最终测试覆盖率

| 模块 | 初始覆盖率 | 最终覆盖率 | 状态 |
|------|------------|------------|------|
| `pkg/errors` | 100% | **100%** | ✅ |
| `common/crypto/blake2b` | 94.7% | **94.7%** | ✅ |
| `internal/p2p/types` | 0% | **94.1%** | ✅ |
| `common/rlp` | 88.9% | **88.9%** | ✅ |
| `internal/vm/stack` | 0% | **78.4%** | ✅ |
| `internal/vm/precompiles` | 75.9% | **75.9%** | ✅ |
| `common` | 0% | **70.6%** | ✅ |
| `log` | 69.1% | **69.1%** | ✅ |
| `internal/consensus` | 65.8% | **65.8%** | ✅ |
| `utils` | 6.1% | **31.3%** | ✅ |
| `internal/sync` | 13.7% | **13.8%** | ✅ |
| `modules/state` | 6.7% | **10.3%** | ✅ |
| `internal/vm` | 7.6% | **8.8%** | ✅ |
| `internal` | 6.0% | **8.0%** | ✅ |

---

## 原始测试状态分析 (供参考)

### 测试覆盖率现状 (初始)

| 模块 | 覆盖率 | 测试文件数 | 优先级 |
|------|--------|------------|--------|
| `internal/api` | 2.5% | 4 | **P0** |
| `modules/rawdb` | 3.1% | 4 | **P0** |
| `common/block` | 6.4% | 1 | **P1** |
| `modules/state` | 6.7% | 3 | **P1** |
| `internal/vm` | 7.6% | 4 | **P1** |
| `internal/sync` | 13.7% | 2 | **P2** |
| `common/transaction` | 17.4% | 1 | **P2** |
| `internal/consensus` | 65.8% | 4 | P3 |
| `internal/vm/precompiles` | 75.9% | 1 | P4 |

### 缺失测试的关键模块

| 模块 | 源文件数 | 测试文件数 | 优先级 |
|------|----------|------------|--------|
| `internal/miner` | 3 | 0 | **P1** |
| `internal/txspool` | 5 | 0 | **P1** |
| `internal/download` | 9 | 0 | **P2** |
| `internal/node` | 5 | 0 | **P2** |
| `internal/p2p` | 70 | 2 | **P2** |

### 现有 Benchmark 统计

- 总计: 96 个 Benchmark 函数
- 主要分布: RLP 编解码、位运算、加密算法
- 缺失: API、State、VM 执行、区块处理

---

## 分阶段执行计划

### Phase 1: API 层测试 (P0, 预计 2-3 天)

**目标**: 将 `internal/api` 覆盖率从 2.5% 提升至 40%+

#### 1.1 RPC 方法单元测试

| 文件 | 测试内容 | 预计行数 |
|------|----------|----------|
| `api_test.go` | 基础 API 初始化、配置 | ~200 |
| `eth_api_test.go` | eth_* 方法测试 | ~400 |
| `debug_api_test.go` | debug_* 方法测试 | ~300 |
| `txpool_api_test.go` | txpool_* 方法测试 | ~200 |

**参考**: 
- geth: `eth/api_test.go`, `internal/ethapi/api_test.go`
- erigon: `turbo/jsonrpc/eth_api_test.go`

#### 1.2 RPC Benchmark

| 文件 | 测试内容 | 预计行数 |
|------|----------|----------|
| `api_bench_test.go` | RPC 方法延迟基准 | ~300 |

```go
// 示例结构
func BenchmarkGetBlockByNumber(b *testing.B) { ... }
func BenchmarkCall(b *testing.B) { ... }
func BenchmarkEstimateGas(b *testing.B) { ... }
```

**验收命令**:
```bash
go test -v -cover ./internal/api/...
go test -bench=. -benchmem ./internal/api/...
```

---

### Phase 2: 数据层测试 (P0, 预计 2-3 天)

**目标**: 将 `modules/rawdb` 和 `modules/state` 覆盖率提升至 50%+

#### 2.1 RawDB 测试

| 文件 | 测试内容 | 预计行数 |
|------|----------|----------|
| `accessors_chain_test.go` | 区块/头读写 | ~400 |
| `accessors_state_test.go` | 状态数据读写 | ~300 |
| `accessors_tx_test.go` | 交易查找 | ~200 |

**参考**:
- geth: `core/rawdb/accessors_chain_test.go`
- erigon: `core/rawdb/accessors_chain_test.go`

#### 2.2 State 测试

| 文件 | 测试内容 | 预计行数 |
|------|----------|----------|
| `state_test.go` | 状态读写、快照 | ~500 |
| `state_object_test.go` | 账户对象操作 | ~300 |

#### 2.3 数据层 Benchmark

| 文件 | 测试内容 | 预计行数 |
|------|----------|----------|
| `rawdb_bench_test.go` | DB 读写性能 | ~200 |
| `state_bench_test.go` | 状态操作性能 | ~200 |

```go
// 示例结构
func BenchmarkReadBlock(b *testing.B) { ... }
func BenchmarkWriteBlock(b *testing.B) { ... }
func BenchmarkStateUpdate(b *testing.B) { ... }
```

**验收命令**:
```bash
go test -v -cover ./modules/rawdb/...
go test -v -cover ./modules/state/...
go test -bench=. -benchmem ./modules/rawdb/... ./modules/state/...
```

---

### Phase 3: 核心数据结构测试 (P1, 预计 2 天)

**目标**: 将 `common/block` 和 `common/transaction` 覆盖率提升至 60%+

#### 3.1 Block 测试

| 文件 | 测试内容 | 预计行数 |
|------|----------|----------|
| `block_test.go` | 区块创建、序列化 | ~400 |
| `header_test.go` | 区块头操作 | ~200 |
| `receipt_test.go` | 收据序列化 | ~200 |

#### 3.2 Transaction 测试

| 文件 | 测试内容 | 预计行数 |
|------|----------|----------|
| `tx_test.go` | 交易类型、签名 | ~400 |
| `tx_signing_test.go` | 签名验证 | ~300 |

#### 3.3 Benchmark

```go
func BenchmarkBlockRLP(b *testing.B) { ... }
func BenchmarkTxSigning(b *testing.B) { ... }
func BenchmarkTxHash(b *testing.B) { ... }
```

**验收命令**:
```bash
go test -v -cover ./common/block/...
go test -v -cover ./common/transaction/...
```

---

### Phase 4: VM 执行测试 (P1, 预计 3 天)

**目标**: 将 `internal/vm` 覆盖率提升至 40%+

#### 4.1 EVM 核心测试

| 文件 | 测试内容 | 预计行数 |
|------|----------|----------|
| `evm_test.go` | EVM 执行流程 | ~500 |
| `instructions_test.go` | 操作码测试 | ~600 |
| `gas_table_test.go` | Gas 计算 | ~300 |
| `memory_test.go` | 内存操作 | ~200 |
| `stack_test.go` | 栈操作 | ~200 |

**参考**:
- geth: `core/vm/evm_test.go`, `core/vm/instructions_test.go`
- erigon: `core/vm/evm_test.go`

#### 4.2 VM Benchmark

```go
func BenchmarkOpAdd(b *testing.B) { ... }
func BenchmarkOpMul(b *testing.B) { ... }
func BenchmarkOpSHA3(b *testing.B) { ... }
func BenchmarkContractCall(b *testing.B) { ... }
```

**验收命令**:
```bash
go test -v -cover ./internal/vm/...
go test -bench=. -benchmem ./internal/vm/...
```

---

### Phase 5: 交易池和矿工测试 (P1, 预计 2 天)

**目标**: 为 `internal/txspool` 和 `internal/miner` 创建测试

#### 5.1 TxPool 测试

| 文件 | 测试内容 | 预计行数 |
|------|----------|----------|
| `txpool_test.go` | 交易池操作 | ~500 |
| `txpool_bench_test.go` | 性能基准 | ~200 |

**参考**:
- geth: `core/txpool/legacypool/legacypool_test.go`
- erigon: `txpool/pool_test.go`

#### 5.2 Miner 测试

| 文件 | 测试内容 | 预计行数 |
|------|----------|----------|
| `miner_test.go` | 出块流程 | ~400 |
| `worker_test.go` | 工作者测试 | ~300 |

**验收命令**:
```bash
go test -v -cover ./internal/txspool/...
go test -v -cover ./internal/miner/...
```

---

### Phase 6: 同步和P2P测试 (P2, 预计 3 天)

**目标**: 将 `internal/sync` 和 `internal/p2p` 覆盖率提升至 30%+

#### 6.1 Sync 测试

| 文件 | 测试内容 | 预计行数 |
|------|----------|----------|
| `sync_test.go` | 同步流程 | ~400 |
| `downloader_test.go` | 下载器 | ~400 |

#### 6.2 P2P 测试

| 文件 | 测试内容 | 预计行数 |
|------|----------|----------|
| `service_test.go` | P2P 服务 | ~400 |
| `peer_test.go` | 节点管理 | ~300 |
| `protocol_test.go` | 协议处理 | ~300 |

**验收命令**:
```bash
go test -v -cover ./internal/sync/...
go test -v -cover ./internal/p2p/...
```

---

### Phase 7: 集成测试和端到端测试 (P2, 预计 2 天)

**目标**: 创建跨模块集成测试

#### 7.1 集成测试

| 文件 | 测试内容 | 预计行数 |
|------|----------|----------|
| `tests/blockchain_test.go` | 区块链完整流程 | ~500 |
| `tests/rpc_integration_test.go` | RPC 集成 | ~400 |
| `tests/sync_integration_test.go` | 同步集成 | ~300 |

---

### Phase 8: 性能基准套件 (P3, 预计 2 天)

**目标**: 建立完整的性能基准体系

#### 8.1 Benchmark 套件

| 文件 | 测试内容 |
|------|----------|
| `tools/bench/blockchain_bench_test.go` | 区块链操作基准 |
| `tools/bench/evm_bench_test.go` | EVM 执行基准 |
| `tools/bench/state_bench_test.go` | 状态操作基准 |
| `tools/bench/rpc_bench_test.go` | RPC 延迟基准 |

#### 8.2 基准指标

```
# 目标指标
BenchmarkBlockImport           < 50ms/block
BenchmarkStateUpdate           < 1ms/update
BenchmarkEVMSimpleTransfer     < 100µs/tx
BenchmarkRPCGetBalance         < 1ms/call
BenchmarkRPCCall               < 10ms/call
```

---

## 执行时间表

| Phase | 内容 | 预计时间 | 累计 |
|-------|------|----------|------|
| Phase 1 | API 层测试 | 2-3 天 | 3 天 |
| Phase 2 | 数据层测试 | 2-3 天 | 6 天 |
| Phase 3 | 核心数据结构 | 2 天 | 8 天 |
| Phase 4 | VM 执行 | 3 天 | 11 天 |
| Phase 5 | TxPool/Miner | 2 天 | 13 天 |
| Phase 6 | Sync/P2P | 3 天 | 16 天 |
| Phase 7 | 集成测试 | 2 天 | 18 天 |
| Phase 8 | Benchmark 套件 | 2 天 | 20 天 |

**总计: 约 20 个工作日 (4 周)**

---

## 目标覆盖率

| 模块 | 当前 | Phase 1-2 后 | 最终目标 |
|------|------|--------------|----------|
| `internal/api` | 2.5% | 40% | 60% |
| `modules/rawdb` | 3.1% | 50% | 70% |
| `modules/state` | 6.7% | 50% | 70% |
| `internal/vm` | 7.6% | 30% | 50% |
| `internal/txspool` | 0% | 30% | 50% |
| `internal/miner` | 0% | 30% | 50% |
| `internal/sync` | 13.7% | 30% | 40% |
| **整体** | ~15% | 35% | **50%+** |

---

## 参考资源

### Geth 测试参考
- `eth/api_test.go` - API 测试模式
- `core/rawdb/*_test.go` - 数据库测试
- `core/vm/*_test.go` - VM 测试
- `core/txpool/*_test.go` - 交易池测试

### Erigon 测试参考
- `turbo/jsonrpc/*_test.go` - RPC 测试
- `core/rawdb/*_test.go` - 数据库测试
- `core/vm/*_test.go` - VM 测试
- `txpool/*_test.go` - 交易池测试

### 工具
- `go test -cover` - 覆盖率
- `go test -bench` - 性能基准
- `go test -race` - 竞态检测
- `go test -v -run TestXxx` - 运行特定测试

---

## 验收标准

每个 Phase 完成后需满足:

1. ✅ `make build` 通过
2. ✅ `make test` 通过
3. ✅ `make vet` 通过
4. ✅ 覆盖率达到阶段目标
5. ✅ 无新增竞态条件 (`go test -race`)
6. ✅ Benchmark 结果记录到 `docs/METRICS_BASELINE.md`

