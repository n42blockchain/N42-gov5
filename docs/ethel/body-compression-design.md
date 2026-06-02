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
  - 1–2B fingerprint 拒绝集合外 hash（MPHF 对未知 key 返回垃圾槽）。
  - 合计 **≈ 2 B/tx**。

| 能力 | F2 | **F1.5 (F2 + MPHF 索引)** | F1 (存 32B hash) |
|---|---|---|---|
| `eth_getTransactionByHash(H)` 找到并解码 tx | ❌ | ✅（响应里的 hash 字段 = 把请求的 H 回显，免费） | ✅ |
| `eth_getBlockByNumber(fullTx)` 每条 tx 的 `hash` 字段 | ❌ | ❌（需 ordinal→hash 反查 = 又要存 32B） | ✅ |
| `eth_getRawTransaction` / 响应 r,s,v | ❌ | ❌ | ❌ |
| post-zstd B/tx | 83.1 | **~85** | 115.1 |
| 394GB → | ~217 | **~222 GB (−43.6%)** | ~301 GB |

> **F1.5 几乎不牺牲 F2 的 45%，却把 `getTransactionByHash` 查回来了**——代价只剩"block 列表里的 tx.hash 字段填不出"（因为没法从 tx 反算 hash），以及 r/s/v、getRawTransaction。对多数用途这是比 F1 更优的点。

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
| F1（去签名，留 32B hash） | 115.1 | **~301 GB** | −23.6% | ✅ + block 列表 hash 字段 |
| **F1.5（F2 + MPHF hash 索引）** | **~85** | **~222 GB** | **−43.6%** | ✅（查找）；block 列表 hash 字段 ❌ |
| **F2（去签名+hash，无索引）** | **83.1** | **~217 GB** | **−44.8%** | ❌ |

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

## 附：复测命令

```
go build -o bodyc-entropy.exe ./cmd/bodyc-entropy/
bodyc-entropy.exe --dir D:/n42-eth1-postmerge --start 20000000 --count 100000 --sendersample 200000
```
