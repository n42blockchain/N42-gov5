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

当前盘上（zstd 后）≈ **278 B/tx**（394.2 GB / ~1.4B post-merge tx，估）。zstd 比 ≈ 0.62×。

### 关键纠偏：压缩后各字段占比 ≠ 原始占比

zstd 会把 calldata 里 65.7% 的零字节几乎免费吃掉，但**对随机的签名无能为力**。所以：

> **签名在原始里只占 14.4%，在盘上（zstd 后）占 ~23–25%** —— 因为它是唯一不被压缩的大字段。
>
> 反过来 calldata 原始 71%，盘上掉到约 50%（零字节蒸发）。

**结论：盘上唯一的大杠杆是去掉签名，不是 calldata 的字典化。** 用户的"去掉 R/S/V"直觉，在压缩后的真实占比下比原始数字看起来还要划算。

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

| 档 | 存什么 | 去什么 | 盘上 Δ（估） | 丢失的 RPC 能力 |
|---|---|---|---:|---|
| **L** 当前（线级保真） | R/S/V | — | 基线 278 B/tx | 无 |
| **F1** 账本+可查 hash | hash(32B) + from-ID | R/S/V | **−11%** | tx 响应无 r/s/v；`eth_getRawTransaction` 不可（无法还原原始 RLP） |
| **F2** 纯账本 | from-ID + to-ID | R/S/V + hash | **−23%** | 上面全部 + `eth_getTransactionByHash`、响应里无 canonical hash |

> from-ID 是低熵（sender 高度重用），盘上压到 ~1–2 B/tx；F1 的 hash(32B) 不可压，是它只省 11% 的原因。
> F2 把签名 65B 换成 ~2B 的 from-ID/to-ID，几乎全额兑现 23% 的签名盘上份额。

**账本必要信息在 F1/F2 全部保留**：from、to、value、nonce、gas、calldata（合约参数）、type、accessList、withdrawals。能正常服务 `eth_getBlockByNumber`（含解码 tx：from/to/value/input/gas）、`eth_getBalance`/`eth_call`（state）、`eth_getTransactionReceipt`、`eth_getLogs`。

### 维度 b — 叠加项（对 L/F1/F2 都适用，单独看收益都是个位数 %）

1. **全局地址字典**：跨 segment 的 from/to/accessList-address → 3–4B 全局 ID，单例走 raw escape。预估再省 calldata+地址盘上的 3–6%。
2. **selector 列**：69,608 个 unique 4B selector → 全局字典，2B/tx（zstd 也能吃掉大半，净收益小）。
3. **calldata 32B 字裁剪**：每个 ABI 字剥前导零按 trimmed-u256 存。原始省 65.7%，但**zstd 已经吃掉零字节**，净增量只有 calldata 盘上的 ~5–15%。
4. **accessList slot 字典**：8B 全局 slot-ID + raw escape，单例多、收益小（accessList 本身只 5.9%）。

> 叠加项合计大约再省 **5–12% 盘上**，工程量却不小（要维护全局字典、解码端反查、跨 segment 一致性）。性价比远低于维度 a 的签名取舍。

---

## 5. 盘上大小投影（post-merge ~1.4B tx, 394.2 GB 基线）

| 方案 | 盘上 B/tx（估） | post-merge 总量 | 相对基线 |
|---|---:|---:|---:|
| L 当前 | 278 | **394 GB** | — |
| F1（去签名，留 hash） | ~247 | **~350 GB** | −11% |
| F2（去签名+hash） | ~215 | **~305 GB** | −23% |
| F2 + 全局字典 + 字裁剪 | ~190 | **~270 GB** | **−31%** |

> 注：投影里签名按不可压 65B 直接扣减、from-ID 按 ~2B 计；±5% 量级取决于实际 zstd 行为，落地后用 `bodyc-entropy` 复测。

---

## 6. 建议

- **大头先吃 F2**：去 R/S/V + hash，存 from-ID/to-ID。一步拿到 ~23%，工程改动集中在 encoder 的 tx 列 + 一个全局 address 字典 sidecar，decoder 端把 `tx.Hash()`/`From` 从"计算"改成"读存储 ID"。
- 维度 b 的字典/字裁剪**先不做**：相对 zstd 增量个位数 %，复杂度高，等 F2 落地用实测决定是否值得。
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
