# N42 Cross-Chain Bridge Architecture Design

> Status: Approved
> Date: 2026-03-25
> Scope: 三阶段跨链桥总体架构（Hyperlane + ZK Bridge + 多链扩展）

---

## 1. 决策摘要

| 问题 | 决定 |
|------|------|
| 设计范围 | 全部三阶段总体架构 |
| 目标链 | 全部（ETH L1 + L2 + Solana + BNB + Cosmos） |
| 跨链能力 | Token + 任意消息 + DA 一步到位 |
| 多签桥 | **跳过**，不做 |
| 起步方案 | 先 Hyperlane → 再 ZK Bridge |
| ZK 证明范围 (N42→ETH) | Header + State 默认，按需 Full Execution |
| ETH→N42 验证 | 先 Hyperlane ISM → 后升级 ZK 轻客户端 |
| IBC v2 | **不移植**，如需 IBC 互操作通过 Union ZK 桥接层 |

---

## 2. 整体架构

```
┌─────────────────────────────────────────────────────────┐
│                    N42 Cross-Chain Stack                 │
│                                                         │
│  ┌─────────────────────────────────────────────────┐    │
│  │            Application Layer                     │    │
│  │  Token Bridge │ Arbitrary Msg │ DA Publisher     │    │
│  └──────────┬──────────────┬──────────────┬────────┘    │
│             │              │              │              │
│  ┌──────────▼──────────────▼──────────────▼────────┐    │
│  │          Cross-Chain Router (unified API)        │    │
│  │  send(destChain, payload) → receipt              │    │
│  └──────────┬──────────────────────────┬───────────┘    │
│             │                          │                 │
│  ┌──────────▼───────┐    ┌─────────────▼───────────┐    │
│  │   Hyperlane       │    │   ZK Verification       │    │
│  │   Mailbox + ISM   │    │   Module (Phase 2)      │    │
│  │   (消息路由+多链)  │    │   (N42↔ETH 信任根)      │    │
│  └──────────┬───────┘    └─────────────┬───────────┘    │
│             │                          │                 │
│  ┌──────────▼──────────────────────────▼───────────┐    │
│  │              Relayer / Prover Network            │    │
│  │  Hyperlane Relayer │ ZK Prover (SP1) │ Watcher  │    │
│  └─────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────┘
```

### 核心分层

| 层 | 职责 | 实现 |
|---|------|------|
| **Application** | Token Bridge / 消息 / DA | Solidity 合约 + Go 服务 |
| **Router** | 统一跨链 API，屏蔽底层差异 | Go module `internal/bridge/router.go` |
| **Verification** | 消息验证 | Phase 1: Hyperlane ISM / Phase 2: ZK Proof |
| **Transport** | 链间消息传递 | Hyperlane Relayer + ZK Prover |

### 关键设计原则

- **Router 抽象**：应用层不直接调用 Hyperlane 或 ZK Bridge，通过统一 Router 接口。Phase 2 替换验证层时，应用层零修改。
- **ISM 热替换**：Hyperlane 的 Interchain Security Module 是可插拔的。Phase 2 只需将 MultisigISM 替换为 ZKISM，Mailbox 合约和应用层不变。
- **分层证明**：ZK 证明按安全需求分 3 级（Header → State → Full Execution），平衡成本和安全。

---

## 3. Phase 1 — Hyperlane 消息层（Week 1-2）

### 3.1 N42 侧合约

```
contracts/bridge/
  ├── Mailbox.sol          — Hyperlane 标准 Mailbox（dispatch/process）
  ├── N42ISM.sol           — 自定义 ISM：N42 验证者集合验证入站消息
  ├── WarpRoute.sol        — Token Router（ERC-20 lock/unlock + mint/burn）
  ├── CrossChainRouter.sol — 统一入口，面向应用层
  └── DAPublisher.sol      — DA 承诺发布（block proof hash → ETH）
```

### 3.2 Ethereum / L2 侧合约

```
contracts/bridge/
  ├── Mailbox.sol          — Hyperlane 标准（已有部署，复用）
  ├── HyperlaneISM.sol     — 默认多签 ISM（Phase 1），后替换为 ZKISM
  ├── WarpRoute.sol        — 对称 Token Router
  └── N42LightClient.sol   — Phase 2 占位：ZK Verifier 合约
```

### 3.3 Go 服务层

```
internal/bridge/
  ├── router.go       — CrossChainRouter 接口 + 实现
  │     type Router interface {
  │       Send(destChain uint32, recipient Address, payload []byte) (MessageID, error)
  │       EstimateFee(destChain uint32, payload []byte) (*uint256.Int, error)
  │       Status(messageID MessageID) (MessageStatus, error)
  │     }
  │
  ├── hyperlane.go    — Hyperlane 后端（调用 Mailbox 合约）
  ├── token.go        — Token Bridge 逻辑（lock/mint, burn/unlock）
  ├── relayer.go      — Relayer 服务（监听事件、提交证明）
  ├── config.go       — 跨链配置（目标链、ISM 地址、费用参数）
  └── metrics.go      — 跨链指标（消息数、延迟、成功率）
```

### 3.4 消息流

**N42 → Ethereum（出站）：**

1. 用户/合约调用 `CrossChainRouter.send()`
2. Router 调用 `Mailbox.dispatch(destDomain, recipient, body)`
3. Mailbox emit `Dispatch` 事件
4. Relayer 监听事件，构造 metadata
5. Relayer 在 ETH 侧调用 `Mailbox.process(metadata, message)`
6. ETH ISM 验证 metadata（Phase 1: N42 验证者签名）
7. 消息投递到目标合约

**Ethereum → N42（入站）：**

1. ETH 用户调用 ETH `Mailbox.dispatch()`
2. Relayer 监听 ETH `Dispatch` 事件
3. Relayer 在 N42 侧调用 `Mailbox.process()`
4. N42ISM 验证（Phase 1: Hyperlane 默认验证者签名）
5. 消息投递到 N42 目标合约

### 3.5 Phase 1 交付物

| 交付物 | 说明 | 估时 |
|--------|------|------|
| N42 Mailbox + ISM 合约 | 部署到 N42 EVM | 2 天 |
| ETH WarpRoute 合约 | Token 锁定/铸造 | 1 天 |
| Go Router + Relayer | 节点内服务 | 2 天 |
| Token Bridge E2E | N42 ↔ ETH token 转移 | 1 天 |
| 任意消息 E2E | 跨链合约调用 | 1 天 |
| 监控 + 告警 | Prometheus 指标 | 0.5 天 |

---

## 4. Phase 2 — ZK 验证层（Week 3-7）

### 4.1 ZK 证明体系（N42 → Ethereum）

```
┌─────────────────────────────────────────────┐
│           ZK Proof Hierarchy                │
│                                             │
│  Tier 1: Header Chain Proof     [默认]      │
│  证明: N42 区块头序列有效                    │
│  内容: validator sigs + header chain         │
│  证明时间: ~10s  验证 gas: ~300K             │
│                                             │
│  Tier 2: State Inclusion Proof  [默认]      │
│  证明: 特定账户/存储在 state 中              │
│  内容: JMT proof inside ZK circuit           │
│  证明时间: ~15s  验证 gas: ~350K             │
│                                             │
│  Tier 3: Full Execution Proof   [按需]      │
│  证明: 整个区块执行正确                      │
│  内容: SP1 block execution (已有)            │
│  证明时间: ~2min 验证 gas: ~400K             │
└─────────────────────────────────────────────┘
```

### 4.2 新增 Go 模块

```
internal/bridge/
  ├── zkprover_bridge.go   — 扩展 SP1 prover 生成跨链证明
  │     type BridgeProver interface {
  │       ProveHeaderChain(headers []Header) (Proof, error)
  │       ProveStateInclusion(stateRoot Hash, key []byte, proof jmt.Proof) (Proof, error)
  │       ProveBlockExecution(block Block) (Proof, error)
  │     }
  │
  ├── zk_backend.go        — ZK 验证后端（实现 Router 的 Verification 接口）
  │
  ├── light_client.go      — ETH sync committee 跟踪器
  │     type EthLightClient interface {
  │       VerifySyncCommittee(update SyncUpdate) error
  │       LatestFinalizedHeader() Header
  │       VerifyStorageProof(header Header, proof MPTProof) ([]byte, error)
  │     }
  │
  └── zk_light_client.go   — ZK 化 ETH 轻客户端（替换 Hyperlane ISM）
```

### 4.3 Ethereum 侧合约

```
contracts/bridge/
  ├── N42Verifier.sol      — SP1 proof verifier（Groth16/PLONK）
  │     verifyHeaderChain(proof, stateRoot) → bool
  │     verifyStateInclusion(proof, key, value) → bool
  │     verifyExecution(proof, blockHash) → bool
  │
  ├── ZKISM.sol            — Hyperlane ISM 替换为 ZK 验证
  │     // Mailbox 无需修改——只换 ISM 实现
  │     verify(metadata, message) → bool {
  │       return n42Verifier.verifyHeaderChain(metadata.proof, metadata.stateRoot);
  │     }
  │
  └── N42LightClient.sol   — 存储已验证的 N42 header chain
        mapping(uint256 => bytes32) verifiedStateRoots;
```

### 4.4 ISM 热替换路径

```
Phase 1:  Mailbox ──► MultisigISM (N42 验证者签名)
Phase 2:  Mailbox ──► ZKISM (SP1 proof)     ← ISM.setImplementation()
          Mailbox 不变，应用层不变，Router 不变
```

**反向桥升级（ETH → N42）：**

```
Phase 1:  N42 Mailbox ──► HyperlaneISM (默认验证者)
Phase 2a: N42 Mailbox ──► EthSyncCommitteeISM (BLS 签名)
Phase 2b: N42 Mailbox ──► ZKEthISM (SP1 证明 sync committee)
```

### 4.5 Phase 2 交付物

| 交付物 | 说明 | 估时 |
|--------|------|------|
| SP1 Header Chain 电路 | 证明 N42 validator 签名链 | 3 天 |
| SP1 JMT Inclusion 电路 | 在 ZK 内验证 JMT proof | 2 天 |
| N42Verifier.sol | ETH 侧 Groth16 验证合约 | 2 天 |
| ZKISM.sol | Hyperlane ISM 替换为 ZK | 1 天 |
| ETH Sync Committee 轻客户端 | Go 实现，跟踪 ETH 终局 | 3 天 |
| ZK ETH 轻客户端 (SP1) | 证明 sync committee BLS 签名 | 3 天 |
| 按需 Full Execution 证明 | 高价值提款触发 | 2 天 |
| 集成测试 + 审计准备 | E2E + fuzzing | 3 天 |

---

## 5. Phase 3 — 多链扩展 + DA 发布（Week 8-10）

### 5.1 多链路由架构

```
                         ┌──────────────┐
                         │  N42 Router  │
                         └──────┬───────┘
                                │
                 ┌──────────────┼──────────────┐
                 │              │              │
          ┌──────▼──────┐ ┌────▼────┐  ┌──────▼──────┐
          │ ZK Verified │ │Hyperlane│  │ Hyperlane   │
          │ (N42↔ETH)   │ │ (L2s)  │  │ (非EVM链)   │
          └──────┬──────┘ └────┬────┘  └──────┬──────┘
                 │              │              │
          ┌──────▼──┐  ┌───────▼──────────────▼───────┐
          │Ethereum │  │Arbitrum│Base│OP│Solana│BNB│...│
          │  L1     │  └──────────────────────────────┘
          └─────────┘
```

**路由策略：**

| 目标链 | 验证方式 | 原因 |
|--------|---------|------|
| Ethereum L1 | ZK Proof (ZKISM) | 高价值资产，最强安全 |
| Ethereum L2 | Hyperlane ISM | L2 继承 L1 安全性 |
| Solana/BNB/Cosmos | Hyperlane ISM | 已有覆盖，直接复用 |

Router 根据 `destChain` 自动选路：

```go
func (r *router) Send(destChain uint32, recipient Address, payload []byte) (MessageID, error) {
    switch r.verificationTier(destChain) {
    case TierZK:
        return r.zkBackend.Send(destChain, recipient, payload)
    case TierHyperlane:
        return r.hyperlane.Send(destChain, recipient, payload)
    }
}
```

### 5.2 DA 发布（N42 → Ethereum 结算层）

```
N42 Block Production
        │
        ▼
  DA Publisher Service (internal/bridge/da_publisher.go)
  │
  │  每 100 区块 (~13 分钟):
  │  1. 取最新 stateRoot
  │  2. SP1 生成 header chain proof
  │  3. 提交到 ETH: proof + stateRoot + blockRange
  │
        ▼
  N42Settlement.sol (Ethereum L1)
  │  verifyAndStore(proof, startBlock, endBlock, stateRoot)
  │  mapping(uint256 => bytes32) verifiedStateRoots;
```

| 参数 | 值 | 说明 |
|------|---|------|
| 发布频率 | 每 100 区块 (~13 分钟) | 平衡 gas 成本和延迟 |
| 证明类型 | Header Chain (Tier 1) | 足够证明 state root 有效 |
| 高价值回退 | Full Execution (Tier 3) | 可配置触发 |
| ETH gas 预算 | ~300K gas/次 | ~$3-10 按当前 gas 价格 |

### 5.3 AI Agent 跨链协调

```
internal/bridge/
  └── agent_bridge.go    — AI Agent 跨链扩展
        type CrossChainAgentMessage struct {
            SourceChain  uint32
            AgentDID     string        // did:n42:<address>
            MessageType  AgentMsgType  // Discovery / Bid / Accept / Complete
            Payload      []byte
        }
```

通过 `Router.Send()` 发出，目标链的 Agent Registry 接收处理。复用 Hyperlane 任意消息能力。

### 5.4 Phase 3 交付物

| 交付物 | 估时 |
|--------|------|
| Router 多链路由逻辑 + chain registry | 2 天 |
| DA Publisher 服务 + N42Settlement.sol | 3 天 |
| Hyperlane L2 部署脚本（Arbitrum/Base/OP） | 2 天 |
| AI Agent 跨链消息适配 | 2 天 |
| 多链 E2E 测试 | 3 天 |
| 文档 + 运维手册 | 1 天 |

---

## 6. 完整时间线

```
Week 1-2:   Phase 1 — Hyperlane 部署 + Token Bridge + 任意消息
Week 3-7:   Phase 2 — ZK 证明体系 + ISM 替换 + ETH 轻客户端
Week 8-10:  Phase 3 — 多链路由 + DA 发布 + AI Agent 跨链
```

---

## 7. 安全模型演进

```
Phase 1:  N42 验证者多签 (Hyperlane ISM)
            ↓  ISM 热替换，无需迁移合约
Phase 2:  ZK 数学证明 (SP1 + JMT)
            ↓  Hyperlane 保留多链路由
Phase 3:  ETH L1 ZK 信任根 + Hyperlane 多链扩展
```

| 阶段 | 安全级别 | 信任假设 |
|------|---------|---------|
| Phase 1 | 中 | N42 验证者诚实多数 |
| Phase 2 (N42→ETH) | 最高 | 仅密码学假设（ZK proof） |
| Phase 2 (ETH→N42) | 最高 | ETH sync committee + ZK |
| Phase 3 (其他链) | 中 | Hyperlane 验证者集合 |

---

## 8. 文件结构总览

```
internal/bridge/
  ├── router.go            — 统一跨链 Router 接口
  ├── hyperlane.go         — Hyperlane 后端
  ├── zk_backend.go        — ZK 验证后端
  ├── zkprover_bridge.go   — SP1 跨链证明扩展
  ├── light_client.go      — ETH sync committee 轻客户端
  ├── zk_light_client.go   — ZK 化 ETH 轻客户端
  ├── token.go             — Token Bridge 逻辑
  ├── relayer.go           — Relayer 服务
  ├── da_publisher.go      — DA 发布服务
  ├── agent_bridge.go      — AI Agent 跨链适配
  ├── config.go            — 跨链配置
  └── metrics.go           — Prometheus 指标

contracts/bridge/
  ├── Mailbox.sol          — Hyperlane Mailbox
  ├── N42ISM.sol           — N42 验证者 ISM
  ├── ZKISM.sol            — ZK Proof ISM（Phase 2 替换）
  ├── WarpRoute.sol        — Token Router
  ├── CrossChainRouter.sol — 统一入口
  ├── N42Verifier.sol      — SP1 proof verifier
  ├── N42LightClient.sol   — 已验证 state roots
  ├── N42Settlement.sol    — DA 结算合约
  └── DAPublisher.sol      — DA 承诺发布
```

---

## 9. 不做的事

| 排除项 | 原因 |
|--------|------|
| ibc-go 移植 | Cosmos SDK 依赖陷阱（8-14 人月 + 永久维护） |
| 多签锁定桥 | 安全最低，桥黑客 #1 模式 |
| 自建消息路由协议 | Hyperlane 已覆盖 150+ 链 |
| 全链 ZK 验证 | 对非 ETH 链 ROI 不足，Hyperlane ISM 足够 |

---

## 10. 风险和缓解

| 风险 | 影响 | 缓解 |
|------|------|------|
| ZK 证明时间过长 | 跨链延迟增加 | Tier 分层 + 并行证明 + proof 缓存 |
| Hyperlane 协议升级 | 需跟踪更新 | Router 抽象隔离影响 |
| ETH gas 成本飙升 | DA 发布成本增加 | 批量证明 + L2 回退 |
| 桥合约漏洞 | 用户资金损失 | 审计 + 限额 + 暂停开关 |
| SP1 prover 可用性 | 无法生成证明 | 回退到 Hyperlane ISM |
