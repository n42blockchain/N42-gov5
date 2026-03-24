# 深度评估：三项网络优化替代方案

> 替代 XDP 内核旁路的实际可行优化
> 评估日期：2026-03-24

---

## 总览对比

| 维度 | Tile Pipeline (无锁 IPC) | QUIC + GossipSub 调参 | 批量交易 Gossip |
|------|------------------------|---------------------|----------------|
| **实际延迟收益** | ~0（DeepPipeline 已存在） | 0-150ms（连接建立） | **100x**（10s→100ms） |
| **开发必要性** | ★★ 低 | ★★★ 中（QUIC 是个 bug） | ★★★★★ 高 |
| **工作量** | 2-3 周 | **1-3 天** | 1-2 周 |
| **风险** | 高（重构核心路径） | 低（传输层，TCP 回退） | 中（新 gossip topic） |
| **冲突** | 和 DeepPipeline 冲突 | 无 | 无 |
| **推荐** | ❌ 不做 | ✅ **立即做** | ✅ **高优先** |

---

## 方案 1：接通 Tile Pipeline（无锁 IPC）

### 核心发现：与 DeepPipeline 完全冗余

N42 已有两套流水线基础设施：

| 组件 | 实现 | 连接状态 |
|------|------|---------|
| `internal/tile/` — SPSC Ring Buffer | Lamport 无锁队列 + CPU affinity | ❌ 未接入 |
| `internal/deferred/deep_pipeline.go` — Go Channel | 5 级 channel 流水线 + 背压 | ⚠️ 已接入但用 no-op 适配器 |

两者解决**同一个问题**（区块处理阶段间流水线化），但 DeepPipeline 已接入 `node.go`。

### SPSC vs Go Channel 性能差异

| 指标 | SPSC Ring | Go Channel | 差异 |
|------|----------|------------|------|
| 单次 push/pop | ~10-20ns | ~50-100ns | 3-5x |
| **区块处理开销** | 5×20ns = **100ns** | 5×100ns = **500ns** | 400ns |
| EVM 执行时间 | — | — | 10-100**ms** |

**400ns 差异 vs 10ms 执行时间 = 0.004%。完全不可感知。**

### CPU Affinity 和 Block-STM 冲突

Tile 的 `SetCPUAffinity()` 将每个 tile 固定到一个 CPU 核。但 Block-STM 的 `ProcessParallel()` 启动 `runtime.NumCPU()` 个 goroutine。如果 Execute tile 固定到核 3，Block-STM 的所有 worker 也会在核 3 上竞争——**完全破坏并行性**。

### 评估

| 维度 | 评分 | 说明 |
|------|------|------|
| 价值 | ★ | SPSC 节省 400ns/block，不可感知 |
| 必要性 | ★ | DeepPipeline 已存在，是更好的实现 |
| 可行性 | ★★★ | 代码已建成，接线工作量确定 |
| 风险 | ★★ | CPU affinity vs Block-STM 冲突 |
| ROI | ★ | 2-3 周换 0.004% 改善 |

### 结论

**不推荐实施。** DeepPipeline 已提供相同的流水线能力。正确做法是接通 DeepPipeline 的真实执行适配器（参见 EVAL_DSMR_PIPELINE.md Tier A），而不是引入另一套竞争性基础设施。

---

## 方案 2：启用 QUIC + GossipSub 调参

### 核心发现：QUIC 监听是一个 Bug

当前代码状态：
```go
// options.go:65-66 — QUIC transport 已注册
libp2p.Transport(tcp.NewTCPTransport),
libp2p.Transport(libp2pquic.NewTransport),

// 但监听地址只有 TCP:
listen, _ := MultiAddressBuilder(listenIP, uint(cfg.TCPPort))  // /ip4/.../tcp/...
// 没有 QUIC 监听: /ip4/.../udp/.../quic-v1
```

节点可以**拨出** QUIC 连接，但**无法接受**入站 QUIC 连接。

### 修复：约 5 行代码

```go
quicListen, _ := ma.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/%d/quic-v1", listenIP, cfg.UDPPort))
options = append(options, libp2p.ListenAddrs(listen, quicListen))
```

### GossipSub 调参建议

| 参数 | 当前 | 建议 | 风险 | 收益 |
|------|------|------|------|------|
| `PeerOutboundQueueSize` | 600 | **1024** | 低 | 突发容忍 +70% |
| `ValidateQueueSize` | 600 | **1024** | 低 | 消息处理容量 +70% |
| `HeartbeatInterval` | 700ms（硬编码） | **可配置**，默认 500ms | 低 | 网格修复速度 +30% |
| `HistoryLength` | 6 | 8 | 低 | IHAVE 恢复窗口更大 |
| `D` (mesh degree) | 8 | 保持 | — | 已经合理 |

### QUIC 的风险

| 风险 | 严重度 | 缓解 |
|------|--------|------|
| UDP 防火墙封锁 | 高（企业/云环境） | TCP 保持为主传输，QUIC 为机会升级 |
| NAT 穿越 | 中 | libp2p AutoNAT + Relay 已有 |
| Go QUIC 库成熟度 | 低 | quic-go 已久经验证 |
| 调试困难 | 中 | QUIC 流量全加密，pcap 不可读 |

### 评估

| 维度 | 评分 | 说明 |
|------|------|------|
| 价值 | ★★★ | 修复 QUIC bug + 突发容忍 + 连接建立加速 |
| 必要性 | ★★★★ | QUIC 不监听是 bug，队列太小是隐患 |
| 可行性 | ★★★★★ | 1-3 天，改动极小 |
| 风险 | ★★★★★ | TCP 回退保障兼容性 |
| ROI | ★★★★★ | 1-3 天换显著改善 |

### 结论

**强烈推荐立即实施。** QUIC 监听修复是一个零风险的 bug fix。GossipSub 调参是安全的配置优化。

---

## 方案 3：批量交易 Gossip

### 核心发现：N42 的交易传播是严重架构短板

当前交易传播机制（`txspool/txs_fetcher.go`）：

```
发现阶段：每 10 分钟交换一次 bloom filter（BloomFetcherMaxTime）
发送阶段：每 10 秒发送 ≤100 条缺失交易（BloomSendTransactionTime）
```

**结果：新交易的传播延迟高达 10 秒（最差 10 分钟）。**

而 `internal/sync/subscriber.go` 中交易订阅是**注释掉的**：
```go
//todo txs?
//s.subscribe(
//	p2p.TransactionTopicFormat,
//	digest,
//)
```

### 当前 vs 目标

| 指标 | 当前（Bloom Pull） | GossipSub Batch（目标） | 以太坊 |
|------|------------------|----------------------|--------|
| 传播延迟 | **10 秒**（最差 10 分钟） | **100ms** | ~500ms |
| 模式 | 拉取（被动） | 推送（主动） | 推送 |
| 批量 | 100 tx/10s | 50 tx/100ms | 按 hash 推送 |
| 改善 | — | **100x** | — |

### 批量策略

推荐**双触发**（定时 + 阈值）：
- 新交易入 buffer
- 达到 50 条 **或** 100ms 定时器触发 → flush 为单条 GossipSub 消息
- 主题：`/n42/{digest}/transaction_batch`

### 对链性能的实际影响

| 影响 | 说明 |
|------|------|
| **区块更满** | 出块者在 8 秒窗口内看到更多交易 → 更高 gas 利用率 |
| **用户体验** | "pending" 确认从 10s+ 降到 100ms |
| **MEV 公平性** | 所有节点几乎同时看到交易 → 减少信息不对称 |
| **AI 优化器** | `AIBlockOptimizer` 有更大交易池做评分 → 更优排序 |

### 带宽影响

| 参数 | 值 |
|------|-----|
| TPS | 100 |
| 平均 tx 大小 | 200 bytes |
| GossipSub mesh degree | 8 |
| 额外带宽 | 100 × 200 × 8 = **160 KB/s** |

160 KB/s 在 100 Mbps+ 带宽下完全可忽略。

### 评估

| 维度 | 评分 | 说明 |
|------|------|------|
| 价值 | ★★★★★ | 交易传播 100x 提速，直接影响用户体验和出块质量 |
| 必要性 | ★★★★★ | 10 秒传播延迟是明确的架构短板 |
| 可行性 | ★★★★ | 1-2 周，topic 已定义，基础设施就绪 |
| 风险 | ★★★ | 带宽增加可控，需处理无效 tx 验证 |
| ROI | ★★★★★ | 1-2 周换 100x 交易传播改善 |

### 工作量

| 文件 | 改动 |
|------|------|
| `internal/sync/subscriber.go` | 取消注释 tx 订阅，加 validator |
| `internal/sync/subscriber_transactions.go` | 新建：batch tx subscriber + handler |
| `internal/sync/tx_batcher.go` | 新建：accumulator（50tx 或 100ms flush） |
| `internal/p2p/topics.go` | 已有 `TransactionTopicFormat` |
| `internal/p2p/gossip_scoring_params.go` | 新增 `transactionTopicParams()` |
| `conf/p2p_config.go` | 新增 `TxGossipBatchSize`, `TxGossipFlushMs` |
| `internal/txspool/txs_fetcher.go` | 可保留为 fallback |

### 结论

**强烈推荐作为 P0 优先实施。** 10 秒交易传播延迟是 N42 当前最大的网络层短板。GossipSub 推送 + 100ms 批量可实现 100x 改善，直接提升用户体验和出块质量。

---

## 推荐实施顺序

| 优先级 | 方案 | 工作量 | 收益 |
|--------|------|--------|------|
| **P0** | QUIC 监听修复 + GossipSub 调参 | **1-3 天** | Bug fix + 突发容忍 |
| **P0** | 批量交易 GossipSub | **1-2 周** | 交易传播 100x |
| **不做** | Tile Pipeline 接通 | — | DeepPipeline 已存在 |

### 更新路线图建议

原路线图第 5 项 "内核旁路网络 XDP" 应替换为：

```
P0: QUIC 监听修复 + GossipSub 调参 (1-3 天)
P0: GossipSub 批量交易传播 (1-2 周)
P1: DeepPipeline 真实适配器接通 (4-6 周, 见 DSMR Tier A)
```

---

## 参考文献

1. N42 源码分析: `internal/p2p/`, `internal/tile/`, `internal/sync/`, `internal/txspool/`
2. [libp2p GossipSub v1.1 Spec](https://github.com/libp2p/specs/blob/master/pubsub/gossipsub/gossipsub-v1.1.md)
3. [Ethereum devp2p Transaction Gossip (EIP-4938)](https://eips.ethereum.org/EIPS/eip-4938)
4. [quic-go Library](https://github.com/quic-go/quic-go)
5. EVAL_DSMR_PIPELINE.md — DeepPipeline 已存在的分析
