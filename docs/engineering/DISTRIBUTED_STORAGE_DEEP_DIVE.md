# N42 分布式存储深度研究与实施计划

> 日期：2026-03-20
> 基于行业调研：EthStorage、Filecoin FVM、Arweave、Xandeum、Walrus、Celestia、EIP-4844

---

## 一、行业存储方案技术对比

### 1.1 成本对比（每字节）

| 方案 | 成本 | 持久性 | 检索速度 | 验证方式 |
|------|------|--------|----------|----------|
| EVM SSTORE | ~690 gas/字节（最贵） | 永久可变 | 即时 | 共识验证 |
| Calldata | ~10-40 gas/字节 | 永久不可变 | 需重放 | 共识验证 |
| Events/Logs | ~8 gas/字节 | 永久不可变 | 仅链下读取 | Bloom 过滤 |
| EIP-4844 Blob | ~1 wei/字节 | 临时 (~18 天) | 仅链下 | KZG 承诺 |
| EthStorage | Blob 价格 + 存储费 | 永久 | 亚秒级 | zk-SNARK |
| Arweave | ~$10.80/GB 一次性 | 200 年+ | 秒级 | SPoRA 共识 |
| Filecoin | 市场定价 | 合约期限 | 分钟级(冷)/亚秒(热) | PoRep+PoSt |
| Walrus | Sui gas | 可配置 | 亚秒级 | 纠删编码 |
| IPFS | 免费(需 pin) | 依赖 pin | 秒级 | 内容寻址 |

### 1.2 核心机制

**EthStorage**：
- 存储提供者每 12 秒执行 1,048,576 次磁盘采样
- Groth16 zk-SNARK 验证编码正确性（防副本共享）
- 合约层级：`DecentralizedKV → StorageContract → EthStorageContract`
- web3:// 协议（ERC-4804/6860）支持完全链上前端

**Arweave**：
- 一次付费覆盖 200 年存储（保守假设年降 0.5%）
- SPoRA 共识：每次挖矿需读取随机历史数据块 + Merkle 证明
- VDF 限速防止快速 I/O 攻击

**Filecoin FVM**：
- `MarketAPI.sol` 代理 Storage Market Actor（地址 f05）调用
- CCDB 跨链桥：OnRamp 合约(源链) → 中继 Agent → Filecoin SP → Prover 合约 → Oracle 合约(源链)
- Saturn CDN：60ms 中位首字节时间，日处理 4 亿检索请求

**Walrus**：
- Red Stuff 2D 纠删编码：数据编码为矩阵，生成主/辅切片
- 4.5x 冗余系数（远优于传统全副本方式）
- 支持异步网络中的存储挑战

---

## 二、N42 实施方案评估

### 2.1 四种实施路径

| 路径 | 方案 | 工作量 | 风险 | 收益 |
|------|------|--------|------|------|
| **A** | 内容寻址存储预编译 | M (1-2 周) | 低 | 链上原生存储原语 |
| **B** | EthStorage 模式（Blob + zk 证明） | L (4-6 周) | 中 | PB 级去中心化存储 |
| **C** | IPFS 桥接增强（已有基础） | S (3-5 天) | 低 | 即刻可用的外部存储 |
| **D** | 存储证明共识集成 | XL (8+ 周) | 高 | 最深度集成 |

### 2.2 推荐实施顺序

**Phase 1：内容寻址存储预编译（最高优先级）**

在 EVM 层面增加原生的内容寻址存储能力，无需外部依赖。

架构：
```
合约调用 CSTORE(data) → 计算 hash = keccak256(data)
                       → 存入 MDBX ContentStore 表
                       → 返回 hash 给合约
                       → Gas: 基于数据大小定价

合约调用 CLOAD(hash)  → 从 ContentStore 表读取
                       → 返回 data 给合约
                       → Gas: 读取定价
```

关键实现：
- `internal/vm/contracts_content_store.go` — 预编译合约（地址 0x0300）
- `modules/table.go` — 新增 `ContentStore` MDBX 表
- Gas 定价：写入 200 gas/字节，读取 50 gas/字节，大小上限 24KB
- 与 SSTORE2 模式兼容但更高效（原生而非 CREATE 技巧）

**Phase 2：web3:// 协议网关**

支持 ERC-4804/ERC-6860 的 web3:// URL 解析，实现完全链上前端。

架构：
```
浏览器请求 web3://app.n42/index.html
  → web3 网关解析 URL
  → 转换为 EVM CALL（调用合约的 resolveMode/fallback）
  → 合约返回 HTML/CSS/JS
  → 网关返回 HTTP 响应
```

关键实现：
- `internal/api/web3_gateway.go` — HTTP 服务器，解析 web3:// URL
- 调用现有 `eth_call` 基础设施
- 配置项：端口、CORS、缓存

**Phase 3：IPFS 桥接增强**

在已有 `internal/distributed/storage/` 基础上增强：
- 内容寻址预编译与 IPFS CID 互操作
- 自动 pin 管理（合约事件驱动）
- 存储证明（简化版：定期验证 pin 状态）

---

## 三、详细实施计划

### Phase 1：内容寻址存储预编译

**3.1 新增 MDBX 表**

在 `modules/table.go` 中添加：
```go
ContentStore = "ContentStore"  // keccak256(data) → data
```

**3.2 预编译合约**

新建 `internal/vm/contracts_content_store.go`：

接口：
- `store(data []byte) → hash [32]byte`（写入）
- `load(hash [32]byte) → data []byte`（读取）
- `exists(hash [32]byte) → bool`（检查）
- `size(hash [32]byte) → uint256`（查询大小）

选择器：
- `0x00` = store
- `0x01` = load
- `0x02` = exists
- `0x03` = size

Gas 定价：
```
store: 200 * len(data) + 20000 (基础)
load:  50 * len(data) + 5000 (基础)
exists: 2600 (冷读取)
size:   2600 (冷读取)
```

限制：
- 单次最大 24,576 字节（与合约代码限制一致）
- 数据不可变（内容寻址天然保证）
- 数据永久存储在 MDBX ContentStore 表

**3.3 状态管理**

预编译使用 `StatefulPrecompiledContract` 模式（参考 Avalanche）：
- `Run` 函数接收 `StateDB` 访问器
- 通过自定义表而非 SSTORE 存储数据
- 独立于账户状态，不影响 state root

**3.4 测试**

- 单元测试：store/load/exists/size 各方法
- Gas 计算测试
- 大小限制测试（超限拒绝）
- 并发访问测试
- 与 SSTORE2 模式的 gas 对比基准测试

### Phase 2：web3:// 协议网关

**3.5 URL 解析**

实现 ERC-6860 URL → EVM Call 转换：
```
web3://[contract]:[chainId]/[path]
  → 解析 contract 地址（支持 ENS）
  → 确定 resolveMode（auto/manual/resourceRequest）
  → 构造 EVM CALL 消息
  → 返回 HTTP 响应（Content-Type 从合约返回值推断）
```

**3.6 HTTP 网关服务**

新建 `internal/api/web3_gateway.go`：
- HTTP 服务器监听独立端口（默认 8080）
- 路由：`/{contractAddr}/{path...}`
- 调用 `eth_call` 获取合约响应
- MIME 类型推断（.html, .css, .js, .png 等）
- ETag 缓存（基于区块号）

**3.7 配置**

```go
type Web3GatewayCfg struct {
    Enabled bool
    Port    int    // default: 8080
    Host    string // default: "127.0.0.1"
}
```

### Phase 3：IPFS 桥接增强

**3.8 CID 互操作**

- 内容寻址预编译存储的 hash 可转换为 CIDv1
- `keccak256 hash → CIDv1(codec=raw, hash=keccak256)`
- 实现双向查找：IPFS CID ↔ 链上 ContentStore hash

**3.9 自动 Pin 管理**

- 监听 `ContentStored(hash, size)` 事件
- 自动将新存储的内容 pin 到本地 IPFS 节点
- 提供 IPFS 网关 URL 作为备选检索路径

---

## 四、与竞品对比

| 能力 | geth | reth | Erigon | Filecoin | Arweave | **N42 (实施后)** |
|------|------|------|--------|----------|---------|-----------------|
| 链上存储(SSTORE) | ✅ | ✅ | ✅ | N/A | N/A | ✅ |
| 内容寻址存储 | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ **预编译原生** |
| web3:// 网关 | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ **首创** |
| IPFS 桥接 | ❌ | ❌ | ❌ | ✅ | ❌ | ✅ |
| Blob 支持 | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ (EIP-4844) |
| 存储证明验证 | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ (ZK verifier) |

---

## 五、资料来源

- EthStorage 合约架构：github.com/ethstorage/storage-contracts-v1
- EthStorage es-node（Go 实现）：github.com/ethstorage/es-node
- ERC-6860 web3:// 协议规范：eips.ethereum.org/EIPS/eip-6860
- Filecoin Solidity API：github.com/filecoin-project/filecoin-solidity
- Filecoin CCDB 跨链桥：docs.filecoin.io/smart-contracts/programmatic-storage/ccdb
- Arweave 存储捐赠模型：permaweb-journal.arweave.net
- Arweave SPoRA 规范：docs.arweave.org/developers
- Walrus Red Stuff 编码：arxiv.org/abs/2505.05370
- Celestia DA 文档：docs.celestia.org
- EIP-4844 规范：eips.ethereum.org/EIPS/eip-4844
- EIP-8032 大小定价：eips.ethereum.org/EIPS/eip-8032
- Avalanche 有状态预编译：medium.com/avalancheavax
- SSTORE2 库：github.com/0xsequence/sstore2
