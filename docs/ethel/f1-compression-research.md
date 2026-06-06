# F1 档极限压缩 —— 联网研究报告(2026-06-05)

> 方法:deep-research 工具,5 角度并行 WebSearch → 抓 19 源 → 86 claims → 3 票对抗验证。
> ⚠️ 本轮工作流有 StructuredOutput bug,多数 verify agent 弃权(0-0 abstain ≠ 驳倒,是"本轮未独立复核")。
> 故下分三档可信度:**已确认(3-0/2-1)** / **本轮未复核(0-0,源是一手但未对抗验证)** / **被驳/无据**。
> 我们内部数字(L ~149 B/tx、F2 ~82 B/tx)是**自测**,非外部来源 —— 研究证实这点。

F1 档定义:保留每笔 tx 的 canonical 32B 哈希(`eth_getTransactionByHash` + 哈希自引用),去/聚合 ECDSA 签名。

---

## 一、核心结论:聚合在 L1 存储上**不是出路**(反直觉,已确认)

**ECDSA 的 pubkey 可从 r/s/v 恢复(sender recovery)—— 这其实是个存储优势。换 BLS 就丢了。**

- **EIP-7591**(BLS tx type 0x04)实测算账(EIP 原文,**3-0 确认**):
  - 去掉 ECDSA 省 65B/tx,但 **BLS pubkey 不能从签名恢复 → 必须每 tx 存 48B pubkey**,加每块 96B 聚合签名。
  - 净省 = `-96 + (65-48)×150 ≈ 2454 B` 在 ~160KB 块上 ≈ **仅 ~1.5%**。
- **含义**:BLS 聚合省的是**验证成本**(一个 96B 聚合签名批验全块),**不是存储**。在 L1 历史盘上,48B pubkey 几乎抵消 65B ECDSA 删除。**聚合对"链上验证/带宽"是大杠杆,对"历史归档盘字节"是小杠杆。**

Vitalik 那个 "签名 68B→0.5B、ETH 转账 112B→12B" 的表(**3-0 确认是原文**)有个**致命前提**:
- 0.5B 签名是 **rollup-native BLS + 跨 tx 摊销**(~1 聚合/100 tx),且 **不保留 canonical ECDSA 哈希**。
- 所以 **12B/tx 是 rollup calldata 目标,不适用于必须保 canonical 哈希的 L1 F1 档**。

## 二、唯一"保哈希又去 ECDSA"的确认路径 = 改 tx 类型(EIP-7591)

- EIP-7591 把 tx_hash 重定义为 `keccak256(0x04 || rlp([chain_id, nonce, …字段…, sender]))`,**digest 里不含签名** → 哈希可从结构化字段重算(**2-1 确认**)。
- 这正是"协议原生用可聚合签名 + 重定义哈希"的路 —— 但:
  - EIP 是 **Draft/Stagnant(未上线)**,0x04 还跟已发布的 EIP-7702 **冲突**。
  - 对**已有历史**(secp256k1 ECDSA,哈希已 baked-in 签名)**无效** —— 历史哈希永远需要原始签名才能重算。

**对历史数据的硬事实(本会话已多次确认):去掉 ECDSA 后 canonical 哈希不可重算 → 必须显式存/索引 32B 哈希。聚合改变不了这点。**

## 三、SNARK/STARK 批验签名(选项 1c)—— 无可靠数据,被驳

- "BEATS 用 IVC+Spartan 批验 2^20 ECDSA 签名" + "批验加速 48–240×" 两条**都未通过验证**(1-0 / 0-1 被驳/无据)。
- 结论:"用一个有效性证明替代逐笔签名"方向**缺乏确认的实测数字**。且即便成立,也只省**验证**不省**历史哈希存储**(同 §一逻辑)。

## 四、32B 哈希的存储/索引下限(本轮未复核,但是经典 CS 结果)

> 以下来自一手论文(arxiv),但本轮 verify 弃权 —— 标注为"文献已知、待独立复核"。与我们 txindex 自测一致。

- MPHF 信息论下限 **≈1.44 bit/key**(= log₂e);RecSplit **~1.56 bit/key**;PtrHash 默认 **2.4 / 紧凑 2.12 bit/key**。
- 即:`hash→位置` 的 MPHF 索引开销 ~**1.5–2.4 bit/tx**(不是字节)。
- 但这只是"已知全集时的定位索引"。要支持**任意 hash 查询 + 自验证**,得能区分"在不在 + 防碰撞",我们 txindex 自测的真值是 **~13–25 bit/tx**(留/关 LFP),≈1.6–3.1 B/tx —— 远小于存全 32B。
- "截断 32B + 自验证" 在去签名后**做不到自验证**(§本会话已结论);截断只能靠"读端已持全 tx"的场景(Bitcoin compact-block 用 32-bit 短 hash,8× 压缩,但那是 relay 不是归档)。

## 五、字段列式压缩(本轮未复核,blog/教程源)

- value 科学计数法 mantissa×10^exp:18B→~3B(整值 2B);**3-0 确认是 Vitalik 原文技术**。
- to 地址全局字典/registry(Arbitrum address table):20B→~3–4B,例子 calldata 91% 削减。
- calldata short-ABI:ERC-20 transfer 68B→35B(~48.5%)。
- ⚠️ 这些都是 **rollup calldata** 数字,搬到 L1 历史 body 收益相近但需自测(我们 bodyc 实测 calldata 已 zstd 到 63B,83.6% 单例 word 打不穿)。

## 六、现有格式(本轮未复核,但格式已知)

- **era1**:`snappyFramed(rlp(body))` —— 整 body 一个 snappy,**保全签名、无列式/字段压缩**。即 era1 ≈ 我们的 L 档(snappy 而非 zstd)。我们 bodyc(列式+全局字典+zstd)已 > era1。
- geth freezer(snappy)、erigon/reth static files、EIP-4444、Portal Network 的 per-tx 盘字节:**本轮无确认数字**(open question)。

---

## 七、F1 档定位综合(回答"能压到多少")

| 档 | 签名 | 哈希 | 实测/估算 B/tx | 自验证 |
|---|---|---|---|---|
| **L**(全签名) | 存 r/s/v(65B) | 可重算 | ~149(自测) | ✅ 逐 tx 密码学 |
| **F1**(保哈希去签名) | 去 ECDSA | **必须存/索引 32B** | 见下 | ❌(去签名即失) |
| **F2**(去哈希) | 去 ECDSA | 不存 | ~82(自测) | ❌ |

**F1 的字节构成** = F2(~82,账本+from-ID,去签名)**+ 哈希成本**:
- 存全 32B hash → +32B → ~**114 B/tx**(= 我们设计表的 F1 −24%,与自测一致)。
- 改用 MPHF 索引(F1.5)→ +~1.6–3 B/tx → ~**84–85 B/tx**,但只回 `getTransactionByHash` 查询、block 列表 hash 字段仍需 sidecar(+32B/tx)。

**关键洞察(本研究最大价值):**
1. **聚合不救 L1 历史存储** —— BLS 净省仅 ~1.5%(48B pubkey 抵消),0.5B 签名是 rollup 专属且不保哈希。
2. **保 canonical 哈希 + 去 ECDSA 在历史数据上根本矛盾** —— 哈希 baked-in 签名,去了就得存哈希。唯一根治 = 协议原生改 tx 类型(EIP-7591,未上线)。
3. **F1 的真实地板** = 账本字段(可压到 ~17–20B)+ **不可压的哈希熵地板(全存 32B 或索引 ~1.6–3B)** + 随机 calldata 单例。哈希是 F1 区别于 F2 的全部成本。

## 八、未决问题(open questions,需后续证据)

1. MPHF/RecSplit/Elias-Fano 的 hash→位置 索引真实 bit/tx(本轮未复核;与我们 txindex 自测对齐即可)。
2. era1/geth-freezer/reth-static/EIP-4444 的真实 per-tx 盘字节(锚定 F1/F2/L)。
3. 是否有"对原始 ECDSA 出有效性证明、同时保 canonical keccak 哈希可重算"的方案(而非像 7591 重签 BLS)—— 未找到确认数据。
4. F1 约束下逐 tx 严格熵地板的拆分(不可压 32B 哈希 vs 可压账本 vs 随机 calldata)。

## 来源(一手优先)
- EIP-7591(BLS tx type,primary):https://eips.ethereum.org/EIPS/eip-7591
- Vitalik rollup 压缩表(primary):https://vitalik.eth.limo/general/2021/01/05/rollup.html
- ethresear.ch 跨-tx BLS 聚合:https://ethresear.ch/t/adding-cross-transaction-bls-signature-aggregation-to-ethereum/7844
- MPHF 下限(arxiv,未复核):1910.06416 / 2502.15539
- era1 格式(primary,未复核):github eth-clients/e2store-format-specs era1.md
- calldata 压缩(blog,未复核):sequence.xyz / l2fees.info / ethereum.org short-abi / rareskills l2-calldata
- 被驳:BEATS IVC 批验 ECDSA(springer 11704-025-41269-5)
