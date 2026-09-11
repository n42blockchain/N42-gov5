# N42 账户签名体系架构

日期：2026-09-09。本文把四层签名体系的设计意图**对齐到仓库里实际存在的代码**，标出哪些已经落地、
哪些只有半截、哪些还没有，以及要让这套体系成立必须先定的一个决策。

文中标注：**[已落地]** = 代码在仓库里并接入了主路径；**[半成品]** = 组件在但未接入账户/交易层；
**[缺失]** = 还没有；**[待定]** = 需要决策。

---

## 1. 设计意图：一个账户，四条签名路线

```
                         N42 Account
                              │
        ┌──────────────┬──────┴───────┬──────────────┐
        ▼              ▼              ▼              ▼
   EVM Wallet       Passkey      Native Fast     PQ Wallet
   secp256k1         P-256      Ed25519/Schnorr    Falcon
        │              │              │              │
    兼容生态        极简体验        高频/批量      长期安全
        └──────────────┴──────┬───────┴──────────────┘
                              ▼
                        AA / DID / UID
                              │
                              ▼
                     AI Agent（受托层）
              会话密钥 · 花费策略 · paymaster
```

四层是**用户自己**的凭证；AI Agent 不是第五种算法，而是架在 AA 之上的**受托层**——
它用用户授予的、受限的会话密钥代表用户行动，权限边界（金额、合约、方法、时效）
由 AA 账户合约强制执行。详见 §6。

| 层 | 算法 | 面向 | 取舍 |
|---|---|---|---|
| Legacy / Compatibility | secp256k1 ECDSA | Ethereum 钱包、MetaMask、EVM、跨链 | 生态最大，签名 65 B，验签需 ecrecover |
| Passkey | P-256 (secp256r1) | iPhone Face ID、Android、WebAuthn、11X 用户、无助记词 | 硬件安全芯片托管私钥，用户零助记词负担 |
| Native Fast | Ed25519 或 Schnorr | N42 原生账户、高频交易、手机钱包、批量验证 | 验签快、可批量验证；Schnorr 另有聚合/多签优势 |
| Post-Quantum | Falcon / FN-DSA（未来可换更优） | 高价值长期账户、DAO treasury、冷钱包 | 抗量子；签名 666 B（Falcon-512），是前三者的 10 倍 |

四层不是四条链，也不是四种账户类型——**是同一个账户地址空间下的四种授权凭证**，
统一收敛到 AA（账户抽象）/ DID / UID 之上。

---

## 2. 仓库现状：逐层对账

### 2.1 secp256k1 —— [已落地]

- 实现：`crypto/`（CGO libsecp256k1，`signature_cgo.go`；纯 Go 回退 `signature_nocgo.go`）
- 地址派生：`crypto.PubkeyToAddress` = `Keccak256(pubkey)[12:]`
- 交易类型：`LegacyTxType(0x00)` / `AccessListTxType(0x01)` / `DynamicFeeTxType(0x02)` / `BlobTx(0x03)` /
  `SetCodeTxType(0x04, EIP-7702)`（`common/transaction/`）
- 发送者恢复：`common/transaction/transaction_signing.go`（`Signer` 接口 + 进程级 sender 缓存）

这一层是当前的默认路径，无缺口。

### 2.2 Passkey / P-256 —— [半成品]

- **有**：`internal/vm/contracts_p256.go`，EIP-7212/7951 的 `P256VERIFY` 预编译，地址 `0x100`，
  gas 6900，输入 160 B（hash‖r‖s‖pubX‖pubY），随 Osaka 分叉激活（Pectra 尚未暴露，有测试断言）。
- **没有**：P-256 的原生交易类型，也没有账户层的 passkey 签名器。

**含义**：今天 passkey 只能走**合约路线**——账户是一个智能合约（或经 EIP-7702 委托的 EOA），
它的 `validate` 里 `staticcall 0x100` 验 WebAuthn 断言。这条路是通的，且是行业主流做法
（passkey 的签名对象是 WebAuthn 的 `clientDataJSON`/`authenticatorData`，本来就不是裸 tx hash，
必须有一层合约来解包），因此**不建议**再做原生 P-256 交易类型。

### 2.3 Native Fast / Ed25519 —— [缺失]（交易层）

- 仓库里 Ed25519 只出现在两处，都**不是**交易签名：
  - `internal/distributed/messaging/group/session.go`：MLS 群组 commit 的签名
  - `internal/distributed/messaging/identity/did.go`：DID 文档的 verification method
- 没有 Ed25519/Schnorr 的交易类型、签名器、地址派生。

这是四层里唯一在交易层完全空白的一层。

### 2.4 Post-Quantum —— [已落地]（可用，SQIsign 除外）

- 算法库：`crypto/falcon`（Falcon-512）、`crypto/dilithium`（mode2/mode3）、
  另有 `crypto/kyber`、`crypto/csidh`、`crypto/kem`、`crypto/pke`（KEM/加密方向，非签名）
- 预编译：`internal/vm/pq_contracts.go`，`0x14` Falcon-512、`0x15` Dilithium2、`0x16` Dilithium3、
  `0x17` SQIsign（**占位**）。gas：3500 / 4000 / 5000。
  **激活受 `ChainConfig.PQPrecompilesTime` 单独控制，刻意不进标准分叉表**——保持 N42 扩展与
  以太坊分叉集隔离。
- 交易类型：`PostQuantumTxType = 0x05`，`common/transaction/pq_transaction.go`：

  ```go
  SigAlgo     uint8  // 0=Falcon-512, 1=SQIsign, 2=Dilithium2, 3=Dilithium3
  PubKeyMode  uint8  // 0=完整公钥, 1=公钥哈希（引用）
  PubKeyData  []byte
  PQSignature []byte
  ```
- 签名器：`common/transaction/pq_signer.go`，`PostQuantumSigner.Sender()` 按 `SigAlgo` 分派验签，
  地址派生同样是 `Keccak256(pubKey)[12:]`。
- 尺寸取舍（代码注释里的实测口径）：Falcon-512 约 666 B、SQIsign 约 177 B、Dilithium2 约 2420 B。

### 2.5 汇聚层：AA / DID —— [已落地]

- **AA**：`internal/bundler/`（ERC-4337）—— `UserOperation`、`Validator`、mempool、bundle、metrics；
  另有 `AgentSessionValidator` 接口用于 AI Agent 会话密钥。
- **EIP-7702 委托**：`common/transaction/setcode_tx.go` + `internal/vm/eips_pectra.go`
  （`0xef0100` 委托前缀），EOA 可以临时获得合约的验签逻辑。
- **DID**：`internal/distributed/messaging/identity/`，`did:n42:<address>`，
  目前 `CreateDID` 只从 secp256k1 钱包私钥派生，verification method 支持挂多把公钥。

---

## 3. 必须先定的一个决策：账户如何声明自己的签名策略 —— [待定]

**问题**：现在**所有**方案的地址派生都是 `Keccak256(pubkey)[12:]`，包括 PQ。
于是链上无从得知"这个地址应该用哪种签名验证"，只能靠交易自报（PQ 交易把公钥带在 `PubKeyData` 里）。
后果：

1. **无法表达"本账户只接受 Falcon 签名"**。而这恰恰是 PQ 高价值账户的核心诉求——
   如果同一地址同时接受 secp256k1，PQ 的意义就被稀释了（虽然找到碰撞需要 2^160 工作量、
   现实不可行，但"策略无法表达"本身是设计缺陷，不是安全缺陷）。
2. **无法表达轮换/多签**："日常用 passkey，超过 X 金额需要 Falcon 联签"这类策略没有落点。
3. **索引器/钱包无法在不看交易的情况下知道账户类型**。

**三条候选路线**：

| 方案 | 做法 | 优点 | 代价 |
|---|---|---|---|
| A. 地址编码方案 | 派生时加域分隔：`addr = Keccak256(schemeID ‖ pubkey)[12:]` | 地址自带类型，验证端零状态 | 破坏与 Ethereum 地址空间的一致性；跨链/浏览器/钱包全要改 |
| B. 账户状态字段 | 在 `StateAccount` 加 `authPolicy`（或指向策略的 hash） | 策略可升级、可多签、可轮换 | 改共识状态结构，要动状态根、所有编解码器和迁移 |
| **C. AA 合约账户（推荐）** | 账户是合约（或 7702 委托的 EOA），策略写在 `validateUserOp` 里 | **零共识改动**；策略任意复杂；四层可组合；已有 4337 + 7702 + 0x100 + 0x14–0x17 全套原语 | 每笔多一次合约调用的 gas；EOA 要先做一次 7702 委托 |

**建议选 C**，理由：仓库里 C 需要的四块积木**已经全部就位**（bundler、7702 委托、P256VERIFY、PQ 预编译），
剩下的工作是写账户合约和 SDK，不碰共识；而 A 和 B 都要动地址空间或状态结构，代价与收益不成比例。

在 C 之下，四层的落点变成：

```
用户资产 → AA 账户合约（N42Account）
              │
              ├─ owner 槽位 1: secp256k1  → ecrecover（EVM 原生）
              ├─ owner 槽位 2: P-256      → staticcall 0x100（WebAuthn 解包后）
              ├─ owner 槽位 3: Ed25519    → 需新增预编译（见 §4）
              └─ owner 槽位 4: Falcon     → staticcall 0x14
                                 │
                        策略：金额阈值 / 时间锁 / 多签组合
```

`PostQuantumTxType(0x05)` 则保留为**不经 AA 的直通路径**：给那些不想付合约调用 gas、
且愿意接受 666 B 签名的纯 PQ 账户（冷钱包、treasury 的最终提款路径）。

---

## 4. 缺口清单（按优先级）

| # | 缺口 | 影响 | 建议 |
|---|---|---|---|
| 1 | **签名策略无处表达**（§3） | 四层体系无法真正组合 | 定 C 方案，写 `N42Account` AA 合约参考实现 + SDK |
| 2 | **Ed25519/Schnorr 完全缺失** | "Native Fast"层不存在 | 加 `0x18` Ed25519 验签预编译（与 PQ 一样受 `PQPrecompilesTime` 之外的独立时间戳控制），先服务 AA 路线；是否再加原生交易类型，看实测收益 |
| 3 | **SQIsign 是占位实现** | `0x17` 与 `SigAlgo=1` 声明了但不可用 | 要么补齐实现，要么从 `SigAlgo` 枚举和预编译表里移除，避免留下"看起来能用"的死路 |
| 4 | **DID 只绑定 secp256k1** | passkey / PQ 账户无法作为 DID 主体 | `CreateDID` 泛化到"任意 verification method 集合"，与 §3 的策略共用同一份公钥列表 |
| 5 | **PQ 公钥引用模式（`PubKeyMode=1`）的注册表未定义** | 省不掉 666 B 里的公钥部分 | 定义公钥注册合约或状态表，让重复交易只带 32 B 哈希 |
| 6 | **无跨层轮换/恢复流程** | 用户换手机 / 私钥泄露无解 | 在 AA 合约里定义 guardian / 时间锁恢复，属于 §3 的一部分 |
| 7 | **会话密钥不验签**（§6.2-1） | 知道 keyID 即可冒用 agent 额度 | `ValidateSessionKey` 增加 `verify(sk.PublicKey, hash, sig)`；合约侧同样强制 |
| 8 | **bundler 的 value 恒为 nil**（§6.2-2） | 花费上限在该路径上空转 | 从 `CallData` 解析真实 value 后传入 |
| 9 | **agent 地址非链上地址**（§6.2-3） | 链下账本与链上账户脱节 | 随 §3 的 C 方案统一：agent 是账户合约里的 owner 槽位 |

---

## 5. 尺寸与成本对照（用于选层）

| 方案 | 签名大小 | 公钥大小 | 验证成本（gas 口径） | 现状 |
|---|---|---|---|---|
| secp256k1 | 65 B | 33/65 B | ecrecover 3000 | 已落地 |
| P-256 | 64 B | 64 B | 6900（`0x100`） | 预编译已落地 |
| Ed25519 | 64 B | 32 B | 待定（预估 2000–3000） | 缺失 |
| Falcon-512 | ~666 B | 897 B | 3500（`0x14`） | 已落地 |
| Dilithium2 | ~2420 B | 1312 B | 4000（`0x15`） | 已落地 |
| Dilithium3 | ~3300 B | 1952 B | 5000（`0x16`） | 已落地 |
| SQIsign-I | ~177 B | 64 B | 声明未实现 | 占位 |

**观察**：PQ 的瓶颈不是验签 gas（3500 与 ecrecover 3000 同量级），而是**签名 + 公钥的字节数**——
Falcon 一笔要多带 1.5 KB 左右，直接吃 calldata 费用和区块空间。所以 §4 第 5 条（公钥引用模式）
不是优化项，而是 PQ 层能否日常使用的前提。

---

## 6. AI Agent：架在 AA 之上的受托层

AI Agent 需要**代表用户花钱**（调合约、付 gas、买算力/推理），但绝不能拿到用户的主私钥。
这正是 AA 的用武之地：Agent 持有的是一把**会话密钥**，权限在账户合约里被显式限制。
所以 Agent 不是四层之外的第五种签名算法——它是**同一套算法的受限委托**。

### 6.1 已有的积木 —— [已落地]（但只在链下）

`internal/ai/wallet/`：

| 组件 | 文件 | 内容 |
|---|---|---|
| `Account` | `account.go` | `Owner` / `Address` / `AgentDID` / 最多 16 把 `SessionKey` |
| `SessionKey` | `account.go` | `PublicKey`、`Expiry`、`SpendLimit`/`SpentAmount`、`Revoked`；权限 = 合约白名单 + 4 字节方法选择器 + `MaxGasPerTx` |
| `SpendingPolicy` | `policy.go` | `RatePolicy`（滑窗限频）、`CapPolicy`（单笔 + 每日）、`AllowlistPolicy`、`CompositePolicy`（AND/OR） |
| `PaymasterService` | `paymaster.go` | 按 owner 押金池代付 gas |
| 接入点 | `internal/bundler/validator.go` | `AgentSessionValidator` 接口；`ValidateAgentSession` 从 `UserOperation.Signature` 前 32 字节取 keyID |

`internal/ai/coord/`：agent 注册表（能力 + 质押 + 信誉）、任务协商（request → bid → accept → complete/dispute，接受时托管）。
`internal/ai/attestation/`：推理结果签名存证与多跳链验证，`SafetyLevel` 分级。

### 6.2 三个必须先解决的问题 —— [待修]

把这层当成**授权机制**之前，下面三点必须解决。它们现在不构成线上风险
（`AICfg.Wallet.Enabled` 默认 false），但决定了这层能不能"算数"。

1. **会话密钥的签名从未被验证**。`Account.ValidateSessionKey()`（`account.go:145`）检查
   keyID 是否存在、是否吊销/过期、花费上限、合约与方法白名单、gas 上限——
   **没有任何一步验证调用者持有该会话密钥的私钥**。`SessionKey.PublicKey` 存下来了，
   但从未参与验签。而 keyID 取自 `UserOperation.Signature` 的前 32 字节，是**公开可见**的。
   结论：知道 keyID 的人就能冒用该会话密钥的额度。**这是本层最重要的缺口。**

2. **bundler 传给策略检查的 `value` 恒为 `nil`**。`validator.go:140` 调用
   `ValidateSessionKey(keyID, target, methodSig, nil, gas)`，而 `checkSpendLimit` 在
   `value == nil` 时直接返回 `nil`（`account.go:280`）——**花费上限这一维在 bundler 路径上
   实际空转**。真实 value 在 `CallData` 里，需要解析后传入。

3. **agent 地址不是链上地址**。`NewAccount` 用 `Keccak256(ownerKey)[12:]` 作 owner 地址、
   `Keccak256(ownerKey ‖ agentDID)[12:]` 作 agent 地址，其中 `ownerKey` 是**私钥字节**而非公钥。
   这与以太坊的 `Keccak256(pubkey)[12:]` 不是同一个东西，所以这些地址与链上账户没有对应关系。
   当前这套是**链下账本**，不是链上账户。

**合起来看**：今天的 AI Agent 层是一个**链下策略引擎**——能表达"该 agent 每天最多花 1 ETH、
只能调这三个合约"，但既不能**证明**请求来自那个 agent，也没把限制写到链上任何地方。

### 6.3 目标形态：策略落进账户合约

按 §3 推荐的 C 方案，agent 是账户合约里的一类 owner 槽位，与用户自己的四层凭证并列：

```solidity
// N42Account.validateUserOp 的判定顺序（示意）
if (sig.isOwnerSig())            → 用户本人：四层凭证之一，无限额
else if (sig.isSessionKeySig())  → agent：先 verify(sessionPubKey, hash, sig)   ← 现在缺的就是这步
                                   再查 expiry / revoked / 合约+方法白名单
                                   再查 spendLimit（value 必须真实传入）
                                   再按 SafetyLevel 决定是否需要额外联签
else                             → reject
```

要点：

- **会话密钥的算法可以是四层里的任何一种**：高频 agent 用 Ed25519（验签快、签名 64 B，
  依赖 §4 缺口 2 的预编译）；跨生态 agent 用 secp256k1（`ecrecover` 最便宜）；
  托管 treasury 的 agent 用 Falcon 联签。
- **限额与白名单必须在合约里强制**；链下的 `SpendingPolicy` 降级为 bundler 侧的提前拒绝
  （省 gas、快速失败），不再是唯一防线。
- **吊销要立即生效**：合约里维护 `revokedKeys` 位图，用户一笔交易即可吊销，
  不依赖链下服务还活着。
- **与 `attestation` 打通**：`SafetyLevel = Critical` 的 agent 操作（自动驾驶、机器人控制类）
  必须附带已验证的推理存证，否则账户合约拒绝——这把 `internal/ai/attestation/` 从
  "事后审计"升级成"事前授权条件"。

### 6.4 与 DID / UID 的关系

`Account.AgentDID` 已经把 agent 绑到 `did:n42:<address>`。目标是一份 DID 文档同时承载：
用户的四层凭证（作为 `verificationMethod`）+ 被授权 agent 的会话公钥（作为
`capabilityDelegation`——W3C DID 里正是为委托设计的关系类型）。这样"谁能代表这个身份做什么"
只有一个事实来源，钱包、bundler、账户合约读同一份文档。

现状差距：`CreateDID` 只从 secp256k1 钱包私钥派生（§4 缺口 4），还不支持多凭证与委托关系。

## 7. 与已有子系统的关系

- **消息层身份**（`internal/distributed/messaging/`）：X25519（加密）+ Ed25519（群组签名）+ DID，
  已经在用非 secp256k1 的密钥，可以与账户层共用 DID 文档里的公钥集合。
- **PQ 隔离原则**（见 `CLAUDE.md`）：PQ 预编译不进标准分叉表，只由 `PQPrecompilesTime` 激活。
  新增 Ed25519 预编译时应沿用同样的隔离方式，不要塞进以太坊分叉集。

---

## 8. 下一步

1. 就 §3 的 A/B/C 拍板（建议 C）。
2. 写 `N42Account` AA 合约参考实现：四个 owner 槽位 + 金额阈值策略 + guardian 恢复。
3. 补 Ed25519 验签预编译（缺口 2），先服务 AA 路线。
4. 处置 SQIsign（缺口 3）：补齐或移除，不要留占位。
5. PQ 公钥注册表（缺口 5），让 PQ 交易的常态开销从 1.5 KB 降到 32 B 量级。
6. **先修会话密钥验签（缺口 7）与 bundler 的 value（缺口 8）**——这两条与 §3 的决策无关，
   现在就能做，且是 AI Agent 层能否被当作授权机制的前提。
