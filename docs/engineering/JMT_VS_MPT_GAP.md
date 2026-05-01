# JMT vs MPT Gap

> 更新日期：2026-04-16
> 作用：澄清“当前通过 Hive/EEST”和“成为完整 canonical Ethereum EL”之间，MPT/state proof 侧真正还差什么。

## 一句话结论

当前仓库已经证明：**没有完整的 canonical Ethereum state MPT backend，也可以通过当前 Hive/EEST broad shard 矩阵**。

但如果目标是“像 geth、erigon 一样作为完整 ETH EL”，那缺口绝不只是一个 `eth_getProof` RPC，而是至少包括：

- canonical `stateRoot`
- canonical account/storage MPT
- latest/historical proof 的 Ethereum 语义
- 面向 sync/snap/range proof 的 proof serving

## 已验证的事实

### 1. Hive/EEST broad shards 已经全绿

`README.md` 已记录最新 broad consume-engine shard 结果：

- Paris+Shanghai `3573`
- Cancun `17783`
- Prague `20878`
- Osaka `21583`

见 [README.md](../../README.md)。

这说明当前 Hive/EEST 关注的核心是：

- 执行语义
- Engine API 行为
- fork 规则
- block/payload 有效性

而不是“主状态后端必须是 canonical Ethereum MPT”。

### 2. 仓内已经有完整的 JMT 承诺与证明能力

- JMT root computer 在 [modules/state/commitment/root_computer.go](../../modules/state/commitment/root_computer.go)
- account/storage proof 在 [modules/state/commitment/jmt_commitment.go](../../modules/state/commitment/jmt_commitment.go)
- proof 结构与校验在 [lib/jmt/proof.go](../../lib/jmt/proof.go)

所以“仓里完全没有 state proof”并不成立；准确说法是：

- 有 **JMT proof**
- 没有 **canonical Ethereum state MPT proof**

### 3. 仓里也不是完全没有 MPT 代码

[internal/api/engine_mpt.go](../../internal/api/engine_mpt.go) 已实现 Ethereum stack trie，用于 derivable list hash。

所以真正缺的不是“任何 trie 代码”，而是：

- canonical Ethereum account trie backend
- canonical Ethereum storage trie backend
- 面向 proof/sync 的完整 state-MPT serving 路径

## 需要避免的误判

“继续使用 JMT，不实现完整 MPT，只差一个 RPC proof”这个说法不准确。

更准确的说法是：

- 对 `Hive/EEST + 私链/dev 链 + Engine API`：完整 MPT 不是当前前置条件。
- 对“像 geth、erigon 一样的完整 ETH EL”：缺口不止 `eth_getProof`，核心还在 `stateRoot` 和 proof 语义本身。

## 当前 state root 实际是什么

### 1. 默认回退路径不是 canonical Ethereum MPT root

[modules/state/intra_block_state.go](../../modules/state/intra_block_state.go) 的 `IntermediateRoot()` 行为是：

- 如果注入了 `rootComputer`，走可插拔 root computer
- 否则回退到 `GenerateRootHash()`

而 `GenerateRootHash()` 的实现是：

- 遍历 dirty state objects
- 对部分账户字段做 RLP
- 再做一次增量 `Keccak`

这不是 canonical Ethereum account/storage MPT root。

### 2. 可插拔 root computer 当前是 JMT，不是 Ethereum MPT

[modules/state/commitment/root_computer.go](../../modules/state/commitment/root_computer.go) 里的 `JMTRootComputer` 会把 dirty account/storage 写入 JMT，并返回新的 JMT root。

因此当前两条已实现路径分别是：

- legacy dirty-object `Keccak` root
- JMT root

它们都不是 Ethereum canonical `MPT + Keccak256 stateRoot`。

### 3. JMT block processing 也没有在默认 mainnet 路径完全收口

相关代码在：

- [internal/node/node.go](../../internal/node/node.go)
- [internal/blockchain.go](../../internal/blockchain.go)

当前代码事实是：

- 节点启动时会初始化 JMT root computer
- 区块处理阶段只在 fresh-chain 语义下才安全注入该 root computer
- 注释明确写着 mainnet 仍需要 state migration
- 实际自动启用目前只对 `cfg.NodeCfg.Chain == "private"` 打开

所以“全仓已经统一成 JMT 主路径”也不准确。

## 当前 `eth_getProof` 实际提供什么

### 1. RPC 入口已经存在，但语义不是 canonical EIP-1186

[internal/api/blockscout.go](../../internal/api/blockscout.go) 的 `GetProof()` 当前行为是：

- JMT 可用时，返回 JMT proof path 中的序列化节点
- JMT 不可用时，回退成一个 hash-based placeholder

同时，`GetBlockscoutCompatibility()` 也把 `StateProofs` 标成了 `true // Partial support`。

这说明当前实现更接近：

- “有 proof RPC”
- “有 JMT proof 能力”
- “但不是 canonical Ethereum MPT proof”

### 2. `StorageHash` 仍不是 Ethereum storage trie root

同一个 `GetProof()` 实现里，`StorageHash` 的计算方式是：

- 把本次查询的 `key + value` 顺序拼接
- 再做一次 `Keccak256`

这不是 Ethereum 账户对象里的 canonical storage root。

所以即使 RPC 名字叫 `eth_getProof`，当前返回结果也还不能视为完整的 EIP-1186 兼容语义。

## 历史 proof 的准确边界

### 1. “历史 proof 只支持 latest”已经不是最新代码事实

这点需要和旧判断区分开。

当前仓内已经有一条**历史 JMT proof** 路径：

- [lib/jmt/store/mdbx_store.go](../../lib/jmt/store/mdbx_store.go) 提供 `JMTVersionRoots` 与 `ReadJMTVersionRootAt()`
- [modules/state/commitment/jmt_commitment.go](../../modules/state/commitment/jmt_commitment.go) 提供 `SnapshotAt()` / `GetAccountProofAt()` / `GetStorageProofAt()`
- [internal/api/blockscout.go](../../internal/api/blockscout.go) 会通过 `resolveJMTRoot()` 查历史高度 root，并尝试基于该 root 生成 proof

因此更准确的说法是：

- 当前已有 **historical JMT proof lookup**
- 但这仍然不是 **historical canonical Ethereum MPT proof**

### 2. 历史 proof 的可用性仍有边界

`GetProof()` 的注释已经写明：历史 proof 依赖 old nodes 没有被 GC 掉。

另外，仓里虽然有 [lib/jmt/archive/](../../lib/jmt/archive/) 的 archive reader/writer，但从当前调用点看，我没有在 RPC 路径里看到把 archive reader 接入 `eth_getProof` 的实现。

所以今天能确认的结论应当是：

- 仓内有历史 JMT root 索引
- 仓内有 JMT archive 库能力
- 但“RPC 已经稳定支持 archive-backed historical proof serving”这件事，代码接线还不能下这个结论

## 如果目标是完整 ETH EL，仍然缺什么

### 1. Canonical Ethereum `stateRoot`

至少还缺：

- canonical account trie root
- canonical storage trie root
- bit-for-bit 匹配 Ethereum 的 header `stateRoot`

### 2. Canonical EIP-1186 proof 语义

至少还缺：

- canonical MPT account proof nodes
- canonical MPT storage proof nodes
- 正确的 `storageHash` 语义
- latest + historical block 的标准 proof 语义

### 3. 面向 sync/snap 的 proof serving

如果目标是 geth/erigon 级别的 ETH EL，仅有 RPC proof 还不够，还需要：

- trie node 级别 proof serving
- range proof / boundary proof
- state sync / snap sync 依赖的 canonical proof 语义

按当前代码事实看，仓里还没有一条“canonical Ethereum state MPT backend -> proof serving -> sync serving”的完整主路径。

## 分层结论

### 如果目标只是

- 持续通过 Hive/EEST
- 保持现有 N42 链能力
- 提供执行语义兼容、但不承诺 canonical Ethereum state proof 的 `eth-el` 运行人格

那么：

- **JMT 可以继续用**
- **完整 MPT 不是当前阻塞项**

### 如果目标是

- 真正像 geth、erigon 一样成为完整 ETH EL
- 提供 canonical Ethereum `stateRoot`
- 提供完整 `eth_getProof`
- 支撑 ETH sync / snap / proof-serving

那么：

- **完整 MPT backend 仍然是硬缺口**
- **不能把缺口简化成“只差一个 proof RPC”**

## 一句话总结

当前代码已经证明：**没有完整 MPT，也能通过当前 Hive/EEST broad shards；但如果长期坚持只用 JMT、不建设 canonical Ethereum MPT，那么缺的至少是 canonical `stateRoot`、canonical `eth_getProof`、historical proof 的 Ethereum 语义，以及面向 sync/snap 的 proof serving，不只是一个 RPC。**
