# N42 性能指标基线 (Metrics Baseline)

本文档记录 N42 区块链节点的性能指标基线，用于监控性能回归和优化效果评估。

---

## 1. RPC 延迟指标

### 1.1 核心读取方法

| 方法 | P50 目标 | P95 目标 | 说明 |
|------|----------|----------|------|
| `eth_chainId` | < 1ms | < 5ms | 静态值，最快 |
| `eth_blockNumber` | < 5ms | < 20ms | 链头查询 |
| `eth_getBlockByNumber` | < 50ms | < 200ms | 区块读取 |
| `eth_getBlockByHash` | < 50ms | < 200ms | 区块读取 |
| `eth_getTransactionByHash` | < 30ms | < 100ms | 交易查找 |
| `eth_getTransactionReceipt` | < 30ms | < 100ms | 收据查找 |
| `eth_getBalance` | < 20ms | < 80ms | 状态读取 |
| `eth_getCode` | < 30ms | < 100ms | 代码读取 |
| `eth_getStorageAt` | < 30ms | < 100ms | 存储读取 |

### 1.2 计算密集方法

| 方法 | P50 目标 | P95 目标 | 说明 |
|------|----------|----------|------|
| `eth_call` | < 100ms | < 500ms | EVM 执行 |
| `eth_estimateGas` | < 100ms | < 500ms | Gas 估算 |
| `eth_getLogs` | < 100ms | < 1s | 日志查询（受范围影响） |
| `debug_traceTransaction` | < 1s | < 5s | 交易追踪 |

### 1.3 写入方法

| 方法 | P50 目标 | P95 目标 | 说明 |
|------|----------|----------|------|
| `eth_sendRawTransaction` | < 50ms | < 200ms | 交易提交 |

---

## 2. 同步性能指标

### 2.1 Initial Sync (初始同步)

| 指标 | 目标值 | 说明 |
|------|--------|------|
| 区块导入速度 | > 100 blocks/s | 空块情况 |
| 区块导入速度 | > 20 blocks/s | 有交易情况 |
| 状态同步速度 | > 1000 accounts/s | 账户下载 |

### 2.2 Catch-up Sync (追赶同步)

| 指标 | 目标值 | 说明 |
|------|--------|------|
| 单区块导入 | < 500ms | 正常区块 |
| 状态转换 | < 100ms | 单区块状态变更 |

---

## 3. Reorg 性能指标

| 深度 | 目标时间 | 说明 |
|------|----------|------|
| Depth 1 | < 100ms | 最常见情况 |
| Depth 2-5 | < 500ms | 一般 reorg |
| Depth 6-10 | < 2s | 较大 reorg（需告警） |
| Depth > 10 | < 5s | 异常（需调查） |

### Reorg 监控指标

```
# 通过 reorg 审计日志监控
grep "Reorg completed" /path/to/logs/*.log

# 关键字段:
# - depth: reorg 深度
# - duration: 处理时间
# - old_blocks: 回滚的区块数
# - new_blocks: 新增的区块数
```

---

## 4. 资源使用基线

### 4.1 内存使用

| 状态 | Heap 目标 | 说明 |
|------|-----------|------|
| 空闲 | < 500MB | 无活动请求 |
| 正常负载 | < 2GB | 一般 RPC 请求 |
| 高负载 | < 4GB | 大量并发请求 |
| 同步中 | < 8GB | 初始同步阶段 |

### 4.2 磁盘使用

| 指标 | 目标值 | 说明 |
|------|--------|------|
| 数据增长率 | < 50GB/month | 主网平均 |
| 日志增长率 | < 1GB/day | 正常日志级别 |

### 4.3 CPU 使用

| 状态 | 目标值 | 说明 |
|------|--------|------|
| 空闲 | < 5% | 无活动 |
| 正常 | < 30% | 一般负载 |
| 同步 | < 80% | 初始同步 |

---

## 5. P2P 网络指标

| 指标 | 目标值 | 说明 |
|------|--------|------|
| 连接节点数 | 25-50 | 稳定状态 |
| 区块传播延迟 | < 500ms | 收到新区块 |
| 交易传播延迟 | < 200ms | 收到新交易 |
| 请求成功率 | > 95% | 区块/状态请求 |

---

## 6. 基准测试方法

### 6.1 RPC 基准测试

```bash
# 使用 tools/bench
cd tools/bench

# 运行冒烟测试
./run_smoke.sh http://localhost:8545

# 运行详细基准
go run ./cmd/rpc -url http://localhost:8545 -n 100

# 输出示例:
# Method              | Calls |  P50  |  P95  |  Max  | Errors
# --------------------|-------|-------|-------|-------|--------
# eth_blockNumber     |   100 |   2ms |   8ms |  15ms |      0
# eth_getBlockByNumber|   100 |  35ms | 120ms | 250ms |      0
```

### 6.2 指标收集

```bash
# 收集系统指标
go run ./cmd/metrics -datadir /path/to/n42/data -pprof http://localhost:6060

# 输出 JSON 格式便于对比
go run ./cmd/metrics -output metrics_$(date +%Y%m%d).json
```

### 6.3 性能对比

```bash
# 部署前收集基线
./collect_baseline.sh > baseline_before.txt

# 部署后收集
./collect_baseline.sh > baseline_after.txt

# 对比
diff baseline_before.txt baseline_after.txt

# 检查是否有 > 20% 的回归
```

---

## 7. 告警阈值

### 7.1 RPC 延迟告警

| 级别 | 条件 | 动作 |
|------|------|------|
| Warning | P95 > 2x 基线 | 记录日志 |
| Critical | P95 > 5x 基线 | 发送告警 |
| Emergency | P95 > 10x 基线 | 立即调查 |

### 7.2 Reorg 告警

| 级别 | 条件 | 动作 |
|------|------|------|
| Info | depth <= 2 | 正常记录 |
| Warning | depth 3-5 | 详细记录 |
| Critical | depth > 5 | 发送告警 |
| Emergency | depth > 10 | 立即调查 |

### 7.3 资源告警

| 资源 | Warning | Critical |
|------|---------|----------|
| Memory | > 6GB | > 10GB |
| Disk | > 80% | > 90% |
| CPU | > 70% 持续 5min | > 90% 持续 1min |

---

## 8. 版本基线记录

### v0.1.x (当前)

```
测试环境: 4 CPU, 16GB RAM, SSD
测试日期: 2024-12-15

RPC 延迟 (ms):
  eth_blockNumber:       P50=2,   P95=8
  eth_getBlockByNumber:  P50=35,  P95=120
  eth_call:              P50=80,  P95=350

Reorg 性能 (ms):
  Depth 1:  avg=45,  max=95
  Depth 5:  avg=180, max=420

资源使用:
  Heap (idle):   380MB
  Heap (active): 1.2GB
  Disk growth:   ~30GB/month
```

---

## 9. 性能优化检查清单

部署前检查:

- [ ] RPC 基准测试通过
- [ ] 冒烟测试通过
- [ ] 无内存泄漏（heap profile 对比）
- [ ] 无 goroutine 泄漏
- [ ] Reorg 测试通过
- [ ] 资源使用在基线范围内

---

## 10. 相关工具

| 工具 | 位置 | 用途 |
|------|------|------|
| run_smoke.sh | tools/bench/ | 冒烟测试 |
| bench_rpc | tools/bench/cmd/rpc/ | RPC 基准 |
| bench_metrics | tools/bench/cmd/metrics/ | 指标收集 |
| pprof | 内置 | 性能分析 |

---

## 更新记录

| 日期 | 版本 | 变更 |
|------|------|------|
| 2024-12-15 | v1.0 | 初始基线文档 |

