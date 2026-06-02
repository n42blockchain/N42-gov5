# Body 极致压缩设计（full 节点折衷方案）

目标：在 **full 节点**上，去掉历史 body 中"已验证、不可变"的高熵冗余，只保留**账本必要信息**（谁转给谁、合约调用参数），把 post-merge bodies 压到极限。

本设计基于 `cmd/bodyc-entropy` 在真实 post-merge 数据上的实测（blocks 20,000,000 起，640万–1630万 tx），不是估算。

---

## 1. 实测熵分布（每 tx 原始字节，zstd 之前）

| 字段 | B/tx | 原始占比 | 压缩性质 |
|---|---:|---:|---|
| **calldata** | 317.6 | **70.6%** | 65.7% 是零字节（ABI 字前导零）；74.5% 是 32B 对齐参数 |
| **sig R+S+V** | 65.0 | 14.4% | **随机数，几乎不可压** |
| accessList | 26.6 | 5.9% | 20B 地址 + 32B slot |
| to | 20.0 | 4.4% | 20B 地址（dict 列已做 segment 内字典） |
| gasFeeCap | 6.0 | 1.3% | trimmed u256 |
| value | 4.5 | 1.0% | trimmed u256 |
| gasTipCap | 4.2 | 0.9% | trimmed u256 |
| gas | 3.0 | 0.7% | varint |
| nonce | 1.9 | 0.4% | varint |
| type | 1.0 | 0.2% | 1B |
| **合计** | **449.8** | 100% | |

## 1b. 实测 post-zstd 每列占用（真正的盘上目标图）

`cmd/bodyc-entropy --zstd` 把每个字段单独成列做 zstd，得到**压缩后**的真实占用（blocks 20,000,000 起，8192 块，122万 tx）：

| 字段 | zstd 后 B/tx | 盘上占比 | 性质 |
|---|---:|---:|---|
| **sig R+S+V** | **65.00** | **43.2%** | 完全不可压（随机），盘上头号 |
| calldata | 63.25 | 42.0% | 原始 317→63，**zstd 已 5× 压扁，到极致** |
| to | 6.25 | 4.1% | 76% 单例，per-segment zstd 压不动 |
| accessList | 5.55 | 3.7% | 地址+slot |
| value | 2.86 | 1.9% | |
| gasFeeCap | 2.60 | 1.7% | |
| gas | 1.96 | 1.3% | |
| nonce | 1.84 | 1.2% | |
| gasTipCap | 1.28 | 0.8% | |
| **DISK-SUM** | **150.6** | 100% | |

> 盘上 **150.6 B/tx**（不是早先估的 278）。反推 394.2 GB ≈ **26 亿** post-merge tx。

### 关键纠偏（已用实测替换原估算）

- **calldata 已到极致**：原始 317 B/tx，zstd 后仅 63 B/tx（5× 压扁），且其中 83.6% 是单例 32B word（固有熵）。**不要再碰 calldata。**
- **签名是盘上头号**：原始只占 14.4%，但因为是唯一不被压缩的大字段，**zstd 后占 43.2%**。这是唯一的大杠杆。
- 其余所有结构字段（to+access+value+gas+nonce+caps）合计仅 ~22 B/tx（14.6%）。

---

## 2. 当前格式的两个隐含依赖（必须先讲清）

当前 `body_compact.go` **既不存 tx hash，也不存 sender**：

- **tx hash**：读取时用含 R/S/V 的完整 tx 重算 `keccak(rlp)`。
- **sender**：用 R/S/V 做 ecrecover 恢复。

所以 R/S/V 不只是"签名"，它同时支撑了 `tx.Hash()` 和 `From`。一旦去掉签名：

- 想保留 `eth_getTransactionByHash` / RPC 返回里的 canonical hash → **必须显式存 32B hash**（无法重算）。
- 想保留 `From`（转账账本必要信息）→ **必须显式存 sender**（无法 ecrecover）。

这把"去掉签名"自然分成两档（见 §4）。

---

## 3. 不可约的熵地板（为什么字典化收益有限）

用户提的"4B from-ID / 4B to-ID / 8B storage-ID 字典 + 只用一次的怎么压"，实测回答：

- **To 地址**：100万 unique / 640万非创建 tx，**76.2% 只用一次**。
- **32B ABI word**：1075万 unique，**83.6% 只出现一次**。
- **sender**：重用高（10万 tx 仅 4.1万 unique），字典化便宜。

字典对**重复值**有效，对**单例**无效——单例在字典里仍要存满 20B/32B，再加一个 ref，比 raw 还大。而且 **zstd 已经在 segment（8192 块）内做了字典去重**：重复地址/word 第二次出现近乎免费。

所以显式语义字典相对 zstd 的**增量**收益只在两处：

1. **跨 segment 复用**：zstd 字典是 per-segment 的；一个热地址跨多个 segment 反复出现时，全局字典能省 zstd 省不到的部分。
2. **更窄的 ID**：全局 dict 给热地址分配 3–4B ID，比 zstd 的熵编码略紧。

> 单例（76% 地址、83.6% word）是密码学级随机，**任何方案都打不穿它们的固有熵**。能做的只是"不要为同一个值付两次费"——这正是极致压缩 snapshot 的同一个道理：全局字典覆盖热 key，长尾单例保持 raw。

---

## 4. 分层方案

正交两个维度：**(a) 签名/hash 怎么取舍**（大杠杆，账本语义）× **(b) 全局字典 + calldata 字裁剪**（小杠杆，对任何档叠加）。

### 维度 a — 签名/hash 取舍

| 档 | 存什么 | 去什么 | post-zstd B/tx（实测） | 盘上 Δ | 丢失的 RPC 能力 |
|---|---|---|---:|---:|---|
| **L** 当前（线级保真） | R/S/V | — | 150.6 | 基线 | 无 |
| **F1** 账本+可查 hash | hash(32B) + from-ID + to-ID | R/S/V | 115.1 | **−23.6%** | tx 响应无 r/s/v；`eth_getRawTransaction` 不可（无法还原原始 RLP） |
| **F2** 纯账本 | from-ID + to-ID | R/S/V + hash | **83.1** | **−44.8%** | 上面全部 + `eth_getTransactionByHash`、响应里无 canonical hash |

> 实测（8192 块，122万 tx）：from-ID 列 zstd 后 **2.18 B/tx**（36万 unique sender），to-ID 列 **1.58 B/tx**（vs to-20B 列 6.25）。
> F2 = 去 sig(−65.00) + from-ID(+2.18) + to 换 to-ID(−4.67) = 150.6 → **83.1 B/tx**。
> F1 的 32B hash 不可压（+32 B/tx），是它只省 23.6% 的原因。
> 全局 from/to 字典是 store-wide sidecar：post-merge 全量 unique 地址 × 20B（数千万级 ≈ 1GB 量级），摊到 26 亿 tx ≈ **0.4 B/tx**，可忽略。

**账本必要信息在 F1/F2 全部保留**：from、to、value、nonce、gas、calldata（合约参数）、type、accessList、withdrawals。能正常服务 `eth_getBlockByNumber`（含解码 tx：from/to/value/input/gas）、`eth_getBalance`/`eth_call`（state）、`eth_getTransactionReceipt`、`eth_getLogs`。

### F1.5 — tx hash 用 MPHF 索引，不存 hash 值（~2 B/tx 而非 32）

关键区分：`eth_getTransactionByHash(H)` 需要的不是"存下每条 tx 的 32B hash"，而是 **hash → 位置** 的查找。

- **hash 的值不可压**：keccak，每条 tx 唯一，复用率 0% → 字典/zstd 都无效（这是 F1 +32B 的根源）。
- **hash 的索引可压到 ~2 B/tx**：用代码库已有的 **RecSplit MPHF + fingerprint**（`lib/state` history 冷存同款，见 [[RecSplit History Segments]] / [[RecSplit No Fingerprint]]）：
  - MPHF 把 26 亿已知 hash 映射到 `[0,N)` 唯一槽，**不存 key**，~1.8 bit/key。
  - 槽位按 (block,index) 规范顺序排 → 槽号 = 全局 tx 序号 → 配每块 tx 数前缀和反推 (block,index)，**location 也不用存**。
  - 4B xxhash fingerprint 拒绝集合外 hash（MPHF 对未知 key 返回垃圾槽）。
  - **实测（254万 tx，`bodyc-hashidx-proto`）：.mphf 0.214 + .kv 8.465 + .idx 0.125 = 8.80 B/tx**（4B fp）；2B fp 可降到 ~6.8。in-set 21万查询 0 错，10万集合外 hash 0 误判。

> 纠正早先"~2 B/tx"的乐观估计：MPHF **映射本身**只 0.21 B/tx，但索引还得存"答案"（location，序号 ~3B）+ fingerprint（4B 不可压）。真实 ≈ **8.8 B/tx**，仍比存 32B hash 小 3.6×。而且**任何要支持 getTransactionByHash 的档都需要这个 hash→location 索引**——MPHF 只是它的紧凑形式。

| 能力 | F2 | **F1.5 (F2 + MPHF 索引)** | F1 (存 32B hash + 索引) |
|---|---|---|---|
| `eth_getTransactionByHash(H)` 找到并解码 tx | ❌ | ✅（响应里的 hash 字段 = 把请求的 H 回显，免费） | ✅ |
| `eth_getBlockByNumber(fullTx)` 每条 tx 的 `hash` 字段 | ❌ | ❌（需 ordinal→hash 反查 = 又要存 32B） | ✅ |
| `eth_getRawTransaction` / 响应 r,s,v | ❌ | ❌ | ❌ |
| 区块数据 post-zstd B/tx | 83.1 | 83.1 | 115.1 |
| + hash→location 索引 | — | +8.8 | +8.8 |
| 合计 B/tx | **83.1** | **~92** | ~124 |
| 394GB → | ~217 | **~239 GB (−39%)** | ~322 GB |

> **F1.5 用 ~9 B/tx 把 `getTransactionByHash` 查回来**——代价只剩"block 列表里的 tx.hash 字段填不出"（没法从 tx 反算 hash），以及 r/s/v、getRawTransaction。F1 多花 32B/tx 只为能在 block 列表里回显 hash，且同样要那个索引。

### 维度 b — 叠加项（对 L/F1/F2 都适用，单独看收益都是个位数 %）

1. **全局地址字典**：跨 segment 的 from/to/accessList-address → 3–4B 全局 ID，单例走 raw escape。预估再省 calldata+地址盘上的 3–6%。
2. **selector 列**：69,608 个 unique 4B selector → 全局字典，2B/tx（zstd 也能吃掉大半，净收益小）。
3. **calldata 32B 字裁剪**：每个 ABI 字剥前导零按 trimmed-u256 存。原始省 65.7%，但**zstd 已经吃掉零字节**，净增量只有 calldata 盘上的 ~5–15%。
4. **accessList slot 字典**：8B 全局 slot-ID + raw escape，单例多、收益小（accessList 本身只 5.9%）。

> 叠加项合计大约再省 **5–12% 盘上**，工程量却不小（要维护全局字典、解码端反查、跨 segment 一致性）。性价比远低于维度 a 的签名取舍。

---

## 5. 盘上大小投影（post-merge ~2.6B tx, 394.2 GB 基线，实测每列 zstd）

| 方案 | post-zstd B/tx | post-merge 总量 | 相对基线 | getTxByHash |
|---|---:|---:|---:|---|
| L 当前 | 150.6 | **394 GB** | — | ✅ |
| F1（去签名，留 32B hash + 索引） | ~124 | **~322 GB** | −18% | ✅ + block 列表 hash 字段 |
| **F1.5（F2 + MPHF hash 索引）** | **~92** | **~239 GB** | **−39%** | ✅（查找）；block 列表 hash 字段 ❌ |
| **F2（去签名+hash，无索引）** | **83.1** | **~217 GB** | **−44.8%** | ❌ |

> 数字均为实测：区块数据 L 148.7→F2 81.3（`bodyc-f2-proto`，254万 tx，−45.3%），hash 索引 8.8 B/tx（`bodyc-hashidx-proto`）。L 的 150.6 是 per-column zstd 和；端到端合并缓冲 L=148.7、F2=81.3。

> calldata 字典/字裁剪、accessList slot 字典等"维度 b"叠加项：calldata 已 zstd 到极致（63 B/tx 全是单例熵），叠加项总收益 < 个位数 %，**不做**。
> 真正的肉只有签名（43%）。F2 兑现 45%，是设计的终点。

---

## 6. 建议

- **推荐 F1.5**（甜点）：F2 的 −45% 几乎全保留，但用 RecSplit MPHF 索引（~2 B/tx）把 `eth_getTransactionByHash` 查回来。代价只剩 block 列表 hash 字段 + r/s/v + getRawTransaction。复用代码库已有 RecSplit 设施。
- 若 block 列表必须带 tx.hash（标准浏览器）→ 退 **F1**（存 32B，−24%）。若纯内部账本、连 getTxByHash 都不要 → **F2**（−45%）。
- 三档共同改动：encoder 去 R/S/V 列 + 全局 address 字典 sidecar；decoder 把 `From` 从 ecrecover 改成读 from-ID。F1.5/F2 还需把 `tx.Hash()` 从"计算"改成"索引/不可用"。
- 维度 b（calldata 字裁剪、selector/slot 字典）**不做**：calldata 已 zstd 到极致（63 B/tx 单例熵），增量 < 个位数 %，复杂度高，纯亏。
- **保留 L 档源数据只读**（`D:/n42-eth1`），F2 作为派生只读副本（像本次 post-merge 裁剪一样），可随时重建、不污染源。

### F2 的硬代价（务必确认）

- ❌ `eth_getTransactionByHash`、`eth_getRawTransaction`、响应里的 canonical tx hash / r,s,v。
- ✅ 账本语义、state、receipt、logs、按块按 index 取 tx 全部正常。

适合**内部/账本分析型 full 节点**；若要对外做标准 JSON-RPC（钱包/浏览器依赖 tx hash 查询），需要 F1（留 hash，−11%）或保持 L。

---

## 7. RPC API 损失真实评估（逐方法）

去签名的根本后果不止"丢 r/s/v"——它让节点**无法从 tx 内容反算出 canonical tx hash**（hash = keccak(含签名的 RLP)）。这个能力一旦失去，会波及**所有在响应里内嵌 `transactionHash` 的接口**，这是最被低估的损失。

### 7.1 一个关键不对称

| 方向 | 能否恢复 | 代价 |
|---|---|---|
| **hash → location**（按 hash 查找） | ✅ MPHF 索引（F1.5） | ~9 B/tx |
| **location → hash**（要输出某条 tx 的 hash） | ❌ 只能存 32B hash（F1） | 32 B/tx 不可压 |

> 即：F1.5 的索引能让你**按 hash 查到** tx（查询时 hash 是输入，回显即可），但**无法为一条你按位置取到的 tx 生成它的 hash**。凡是响应里要"我告诉你这条 tx 的 hash"的接口，F1.5/F2 都填不出——只有存了 32B hash 的 F1 能填。

### 7.2 逐方法损失

**完全不受影响**（不碰 tx 签名/hash，读 state 或 header/计数）：
- `eth_blockNumber` `eth_chainId` `eth_syncing` `eth_gasPrice` `eth_feeHistory` `eth_maxPriorityFeePerGas`
- `eth_getBalance` `eth_getCode` `eth_getStorageAt` `eth_getTransactionCount` `eth_call` `eth_estimateGas` `eth_getProof`（纯 state，全保真）
- `eth_getBlockTransactionCountBy*` `eth_getUncleCountBy*`（只要计数）
- 块头哈希 `block.hash`、tx 响应里的 `blockHash`、`from`（from-ID 恢复，反而比 ecrecover 更省）、`to`/`value`/`nonce`/`gas`/`input`/`type`/`accessList` — **账本内容全保真**

**受影响**（按损失程度排序）：

| 接口 | F2 | F1.5 | F1（存32B hash） | 损失性质 |
|---|---|---|---|---|
| `eth_getLogs` / `eth_getFilterLogs` | log 内容✅但 **`transactionHash` 空** | 同 F2 ❌ | ✅ | **严重**：The Graph/indexer/dapp 后端靠 log.transactionHash 回查 tx |
| `eth_getBlockByNumber/Hash`（tx 列表/fullTx） | tx 内容✅但 **每条 hash 空** | 同 F2 ❌ | ✅ | **严重**：浏览器、`fullTx=false` 的 hash 列表也填不出 |
| `eth_getBlockReceipts` | receipt✅但 **`transactionHash` 空** | 同 F2 ❌ | ✅ | **严重**：批量收据回查 tx 断链 |
| `eth_getTransactionByHash(H)` | ❌ 查不到 | ✅ 查到（hash 回显输入） | ✅ | 中：F1.5 修复 |
| `eth_getTransactionReceipt(H)` | ❌ 查不到 | ✅ 查到 | ✅ | 中：F1.5 修复（按 hash 键） |
| `eth_getTransactionByBlockNumberAndIndex` | 内容✅但 hash/r/s/v 空 | 同 F2 | hash✅, r/s/v 空 | 中：按位置取，hash 仍填不出（除非 F1） |
| `eth_getRawTransaction*` | ❌ | ❌ | ❌（无 r/s/v 无法还原签名 RLP） | 低-中：再广播/桥用；**连 F1 都不行** |
| tx 响应的 `r` `s` `v` 字段 | null | null | null | 低：钱包/浏览器几乎不读 |

### 7.3 按"能不能当公共 JSON-RPC 节点"定档

- **L（基线）**：100% 标准兼容。
- **F1（−18%）**：标准兼容，**唯一缺口 = `r/s/v` 字段为 null + `getRawTransaction` 不可用**。浏览器/钱包/dapp/indexer 几乎不受影响（它们基本不用原始签名）。→ **这是现实中"还能对外服务"的压缩档**。
- **F1.5（−39%）**：`getTransactionByHash`/`getTransactionReceipt` 能用，但 **`getLogs`/`getBlockReceipts`/块 tx 列表里的 `transactionHash` 全空** + r/s/v + getRawTransaction。→ **会打断 log-消费型 dapp/indexer/浏览器**，只适合"按 hash 点查 + state"的受限场景。
- **F2（−45%）**：无任何 hash 能力。→ **仅内部账本/分析节点**（按 block/address 查，不需要 tx hash）。

### 7.4 真实结论

> **签名（−45% 的大肉）和"对外 RPC 兼容"本质冲突。** 因为去签名 = 失去反算 tx hash 的能力 = 所有内嵌 `transactionHash` 的响应（getLogs、getBlockReceipts、块列表）都断。
>
> - 要**对外公共 RPC** → 只能到 **F1（−18%）**，且仍丢 r/s/v + getRawTransaction。想再往下，`transactionHash` 必断，不再是合规 JSON-RPC 节点。
> - **F1.5/F2 的 −39%/−45% 适合内部/分析/账本节点**：自己按 block/address/hash 点查，不对外承诺 transactionHash。
>
> 选档不是"省多少"，而是"这个节点对谁服务"：对外 = F1；对内分析 = F2；介于之间且只需按 hash 点查 = F1.5。

---

## 8. 外部研究与现有工程对比（2026-06 web 调研）

调研了 Erigon、go-ethereum、L2、学术界的 body 压缩做法。**结论先行：没有"更神奇"的通用压缩器能超过 zstd 处理这种数据**——签名/calldata 已在熵地板，Erigon/era1 实测同级或更差。真正的杠杆只有两个：**去签名（语义裁剪，本文 F1/F2）** 和 **历史过期（EIP-4444，整段卸载）**。

### 8.1 Erigon compress（`erigon-lib/compress`）— 同级比率，赢在随机访问

算法：在 superstring 里用 **patricia 树** 找重复 pattern（`MinPatternScore=1024`）→ **双层 Huffman**（pattern 码 + position 码）→ **动态规划**求每条 word 的最优 pattern 切分。比 zstd 复杂得多，但：

- **比率 ~1.90×（≈0.53×）on transactions** —— 和我们的 zstd 0.57× 基本一样。**没有比率红利。**
- 真正的优势是 **word 级随机访问**：能单独解出一条 tx，不必膨胀整个 8192-块段（zstd block 必须整段解压）。
- 全 `.seg` 一个全局字典，能抓 zstd per-segment 窗口漏掉的跨段 pattern——但净比率仍≈zstd。

> 启示：若 F1/F2 要按 hash **点查单条 tx**，zstd 整段解压浪费。我们的 MPHF coldstore 已用 64-entry zstd page 做随机访问，方向与 Erigon 一致；不需要移植 Erigon 的 Huffman。

### 8.2 era1（go-ethereum/Nimbus，EIP-4444 后全历史事实标准）— 比我们更松

格式：`snappyFramed(rlp(header/body/receipts))`，8192-块批 + accumulator + block index。**用的是 snappy，不是 zstd。** snappy 偏速度、比率弱于 zstd（~1.5–2× vs ~2–3×）。

> **我们的 zstd bodyc 已经比官方 era1 更紧。** era1 的价值是标准化、可索引、accumulator 证明，不是压缩比。

### 8.3 L2 calldata 5×（Sequence / Optimism）— 不适用于历史

Sequence 在 Arbitrum/OP/Base 上把 calldata 压 ~5×、gas 省 ~50%：零字节游程 + 地址/selector 字典 + 常见模式字典。但这是 **在 tx 生成时重编码 calldata**。历史 tx 的 calldata 是**已签名 payload 的一部分，不可改**；事后只能像 zstd 那样无损压（我们已做）。techniques 与 zstd 已提取的冗余重叠，**对历史 body 无额外红利**。

### 8.4 BLS 签名聚合（ethresear.ch / Wonderboom）— 未来协议，救不了历史

把多签压成一签是协议级改动（未来 tx）。**已存在的 secp256k1 EOA 签名无法事后聚合。** 对历史 body 零帮助。

### 8.5 EIP-4444 历史过期 — 主网对 400GB body 的真实答案

不是压缩，是**过期**：客户端停止在 p2p 提供、并本地修剪 **超过 ~1 年**（`HISTORY_PRUNE_EPOCHS=82125`）的 header/body/receipt；靠 **弱主观性 checkpoint** 引导；历史数据卸载到 **Portal Network / torrents / era1**（1-of-N 信任）。**2025 年已上线，所有 EL 客户端支持 partial expiry。** 动机原文：历史 block+receipt >400GB，过期后大幅降盘。

> 这彻底重构了"full 节点存所有 post-merge body"的前提：**主网 full 节点只留 ~1 年 body/receipt，其余卸载。** 我们 394GB 全量 post-merge 的设定，本身就比主网激进。

### 8.6 弱主观性/finality 给 F2 背书，但也划清边界

研究共识：finalized 历史不可逆，**信任型节点无需再验签**——这正是 F2 去签名的逻辑依据。但主网仍保留签名，因为 **p2p / era1 对外服务需要 canonical（含签名）形式** 供其他节点验 hash。所以去签名只对 **不对外提供 canonical 历史的节点** 成立——与本文 §7.3 的"F2=内部分析节点"完全一致。

### 8.7 综合结论与最优架构

1. **别找魔法压缩器**：Erigon 双 Huffman ≈ zstd、era1 snappy < zstd、L2 5× 不适用、BLS 救不了历史。这类数据（签名+单例 calldata word）就在熵地板。
2. **两个真杠杆，正交可叠加**：
   - **去签名**（F1/F2，本文）：语义裁剪，−18%（对外）/−45%（内部）。
   - **历史过期**（EIP-4444）：只留近 ~1 年，冷段卸载到 era1/torrent。**这个杠杆比压缩大得多**——若只留 1 年 post-merge body，盘上从 394GB 降到几十 GB 量级。
3. **推荐最优组合**：`zstd bodyc（已 > era1）` + `EIP-4444 式近窗保留 + 冷段 era1/torrent 卸载` + `保留的内部副本可选 F1 去签名` + `MPHF tx 索引（随机访问，已实现）`。压缩到此为止，**下一步的大收益在"过期+卸载"，不在"再压"。**

---

## 附：复测命令

```
go build -o bodyc-entropy.exe ./cmd/bodyc-entropy/
bodyc-entropy.exe --dir D:/n42-eth1-postmerge --start 20000000 --count 100000 --sendersample 200000
```
