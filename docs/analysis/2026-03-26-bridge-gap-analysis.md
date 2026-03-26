# N42 跨链桥：遗漏遗留问题全面盘点

> 日期：2026-03-26
> 范围：对照设计文档 + 代码实现，逐项排查所有 gap
> 方法：20 个实现文件 + 3 个 Solidity 合约 + 3 个设计/评估文档逐行审计

---

## 总体完成度

```
Phase 1 (Hyperlane 消息层):       ████████░░░░░░░  ~30%
Phase 2 (ZK 验证层):              ██████████████░  ~80%
Phase 3 (多链 + DA):              ██████████░░░░░  ~60%
──────────────────────────────────────────────────
总体:                              ██████████░░░░░  ~65%
```

---

## 1. P0 阻塞项（必须修复才能运行）

### 1.1 node.go 接线缺失

**问题**：`internal/node/node.go` 中桥服务创建时传入多个 `nil` 参数。

| 参数 | 当前值 | 应传入 | 影响 |
|------|--------|--------|------|
| `ValidatorSet` | `nil` | 从 HotStuff 引擎获取 | BLS 签名验证被跳过，本地验证形同虚设 |
| `ethLightClient` | `nil` | 创建并传入 | ETH→N42 反向桥完全不工作 |
| `stateProver` | `nil` | JMT tree 闭包 | 无法生成状态包含证明 |

**修复工作量**：0.5-1 天

### 1.2 Hyperlane Mailbox 合约未部署

**问题**：`HyperlaneMailboxBinding` 需要链上 Mailbox 合约地址，但合约未在 N42 链上部署。

- `hyperlane_dispatcher.go` 的 `Dispatch()` 调用 `eth_sendTransaction` 到 Mailbox 地址
- 无 Mailbox = Dispatcher 所有调用失败
- 设计文档计划 4 个合约（Mailbox, N42ISM, WarpRoute, CrossChainRouter），均未实现

**修复工作量**：2-3 天（使用 Hyperlane CLI 无许可部署）

### 1.3 ETH MPT RLP 解码不完整

**问题**：`eth_light_client.go:506-534` 的 `verifyMPTPath()` 有两个 TODO：
- `// TODO: full RLP decoding for branch/extension/leaf node types`（L526）
- `_ = keyHash // TODO: verify key path through nibbles matches keyHash`（L532）

当前仅验证哈希链，不解析 RLP 节点类型，不验证 key 路径。真实 ETH MPT 证明会通过验证但可能接受伪造证明。

**修复工作量**：2-3 天

---

## 2. P1 重要项（生产环境必须）

### 2.1 SP1 Prover 网络连通性

**问题**：`ProveHeaderRangeWithSP1()` 在 SP1 不可用时 fallback 到 `ProofData=nil`。ETHSubmitter 现在正确拒绝空 proof，意味着整个发布链路在无 SP1 时停止工作。

**需要**：
- SP1 prover 端点配置和测试
- SP1 verifier 合约部署到 ETH（Sepolia 测试网先行）
- SP1 verification key (`headerChainVKey`, `stateProofVKey`) 生成和配置

**工作量**：3-5 天

### 2.2 WarpRoute 合约（Token 桥）

**问题**：设计文档 Phase 1 计划包含 `WarpRoute.sol`（ERC-20 锁定/铸造），但未实现。当前桥只能传消息，不能传 Token。

- `N42Bridge.sol` 有 `deposit()`/`withdraw()` 但仅支持 ETH 原生代币
- 无 ERC-20 支持
- 无 Hyperlane Warp Route（标准 Token 桥接协议）

**工作量**：2-3 天

### 2.3 ETH 签名中间件

**问题**：`ETHSubmitter` 使用 `eth_sendTransaction`（需要 RPC 端点解锁账户），生产环境不安全。

**需要**：
- 签名交易（`eth_sendRawTransaction`）
- 密钥管理（HSM、custody service 或本地 keystore）
- Nonce 管理

**工作量**：1-2 天

### 2.4 ETH 轻客户端多 Fork 支持

**问题**：`eth_light_client.go:424` 硬编码 `ForkVersionAltair`（0x01000000）。

```go
_ = slot // TODO: derive fork version from slot/epoch for multi-fork support
```

ETH 已经过 Altair→Bellatrix→Capella→Deneb→Electra。使用错误的 fork version 会导致 BLS domain 计算错误，合法的 sync committee 签名验证失败。

**工作量**：1-2 天（fork 版本查找表 + epoch→version 映射）

### 2.5 SP1 Verifier 合约部署

**问题**：N42Verifier.sol 的 `sp1Verifier` 地址需要指向 Succinct 的链上验证器合约。合约已有 `sp1Verifier.code.length > 0` 检查，但实际部署和配置未完成。

**工作量**：1 天（Succinct 提供标准部署流程）

---

## 3. P2 非紧急项

### 3.1 AI Agent 跨链消息适配

**问题**：设计文档 Section 5.3 计划了 `agent_bridge.go` + `CrossChainAgentMessage` 结构。代码中未实现。

```
// 设计文档计划：
type CrossChainAgentMessage struct {
    SourceChain  uint32
    AgentDID     string        // did:n42:<address>
    MessageType  AgentMsgType  // Discovery / Bid / Accept / Complete
    Payload      []byte
}
```

通过 `Router.Send()` 发出，复用 Hyperlane 任意消息能力。

**工作量**：2-3 天

### 3.2 L2 部署脚本

**问题**：设计文档计划 Hyperlane 部署到 Arbitrum/Base/OP，但无部署脚本。

**工作量**：2-3 天（Hyperlane CLI 一键部署 + 配置）

### 3.3 hashSyncCommittee SSZ 不标准

**问题**：`eth_light_client.go:471-496` 对 48 字节 BLS pubkey 直接 `sha256(pk)`。SSZ 标准要求先 pad 到 64 字节（48+16 零填充），分成两个 32 字节 chunk 后再哈希。

与真实 beacon API 数据不兼容。

**工作量**：0.5 天

---

## 4. 代码中的 TODO/Placeholder 清单

| 文件 | 行 | 内容 | 严重性 |
|------|:--:|------|:------:|
| `eth_light_client.go` | 424 | `_ = slot // TODO: derive fork version` | P1 |
| `eth_light_client.go` | 526 | `// TODO: full RLP decoding for branch/extension/leaf` | P0 |
| `eth_light_client.go` | 532 | `_ = keyHash // TODO: verify key path through nibbles` | P0 |
| `eth_submitter.go` | 34 | `// For production with signed transactions, configure a signing middleware` | P1 |

---

## 5. 接口 vs 实现配对状态

| 接口 | 定义位置 | 实现 | 状态 |
|------|---------|------|:----:|
| `Router` | `router.go:23` | `ZKRouter` (`zk_router.go`) | ✅ |
| `ProofSubmitter` | `publisher.go:19` | `ETHSubmitter` (`eth_submitter.go`) | ✅ |
| `HyperlaneDispatcher` | `zk_router.go:52` | `HyperlaneMailboxBinding` (`hyperlane_dispatcher.go`) | ✅* |
| `SyncCommitteeBLSVerifier` | `eth_light_client.go:139` | `BLSVerifierImpl` (`bls_verifier.go`) | ✅ |
| `StateProverFunc` | `zk_router.go:92` | 闭包（node.go 中应创建） | ❌ nil |

\* Dispatcher 实现存在但 Mailbox 合约未部署，实际无法运行。

---

## 6. Solidity 合约状态

| 合约 | 文件 | 行数 | 状态 | 关键问题 |
|------|------|:----:|:----:|---------|
| N42Verifier | `N42Verifier.sol` | 186 | ✅ 完成 | `_verifyJMTProof` 用 SP1 验证，需 SP1 verifier 合约 |
| N42Bridge | `N42Bridge.sol` | 167 | ✅ 完成 | 仅 ETH 原生代币，无 ERC-20 |
| ZKISM | `ZKISM.sol` | 152 | ✅ 完成 | 依赖 N42Verifier 可用 |
| Mailbox | - | - | ❌ 缺失 | Hyperlane 标准合约，需部署 |
| N42ISM | - | - | ❌ 缺失 | N42 验证者集合 ISM |
| WarpRoute | - | - | ❌ 缺失 | Token 锁定/铸造桥 |

---

## 7. 优先级排序的行动计划

```
紧急 (P0) — 桥能跑起来:
  1. node.go ValidatorSet 接线                    0.5 天
  2. node.go ethLightClient + stateProver 接线    1 天
  3. Hyperlane Mailbox 部署                       2 天
  4. ETH MPT RLP 完整解码                         3 天
                                          小计：~7 天

重要 (P1) — 生产环境:
  5. SP1 prover 端点配置+测试                     3 天
  6. SP1 verifier 合约部署                        1 天
  7. WarpRoute 合约                               3 天
  8. ETH 签名中间件                                2 天
  9. 多 fork 版本支持                              1 天
                                          小计：~10 天

收尾 (P2):
  10. AI Agent 跨链消息                            3 天
  11. L2 部署脚本                                  2 天
  12. hashSyncCommittee SSZ 修正                   0.5 天
                                          小计：~5.5 天

总剩余: ~22-23 天
```

---

## 8. 结论

跨链桥**架构完整、代码质量高**（经过 5 轮深度审计+简化），但有 4 个 P0 阻塞项阻止实际运行：

1. **node.go nil 接线** — 最简单也最紧急，0.5 天修复
2. **Hyperlane Mailbox** — 多链消息的基础，2 天部署
3. **MPT RLP 解码** — 反向桥安全的关键，3 天
4. **SP1 端到端** — ZK 证明链的最后一环，3-5 天

修复这 4 项后，N42 将拥有**完整运行的 ZK 原生跨链桥**：
- N42→ETH：ZK 数学证明（SP1 + JMT）
- ETH→N42：Sync Committee BLS + MPT 存储证明
- N42→150+ 链：Hyperlane 消息路由
- 全部通过 `bridge_*` JSON-RPC API 暴露

---

*本报告基于 20 个 Go 文件、3 个 Solidity 合约、3 个设计/评估文档的逐行审计。*
