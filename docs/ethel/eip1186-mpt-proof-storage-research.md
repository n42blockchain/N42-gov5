# EIP-1186 MPT 字节兼容证明:海量 trie 节点存储的深度研究

日期:2026-06-10
问题:要让 eth_getProof 返回 zkEVM / ETH 工具链认可的标准 EIP-1186(keccak MPT)证明,
是否需要给 reth-hashed 格式补"全节点字节"存储?极致压缩怎么做?有没有突破性替代?

**结论(先说答案):不需要全节点字节存储。erigon 布局的 TrieAccount/TrieStorage
(已在 D:/N42-hashed 盘上,30.4 GB)本身就是"极致压缩"的全节点存储——它是信息论
意义上接近最小的证明缓存,且 bpp 已在生产里用它以 ~1ms/块 的速度产出 EIP-1186
形状的 multiproof。缺的只是一个 RPC 包装层,不是存储工程。**

---

## 1. 现状盘点:仓库里其实有三套 MPT 证明栈

| 栈 | 数据布局 | 现状 | 单证明延迟 | EIP-1186 兼容 |
|---|---|---|---|---|
| **A. erigon 布局(live eth-el 链)** | TrieAccount/TrieStorage(BranchNodeCompact)+ HashedAccount/HashedStorage 叶子 | **盘上现成**,bpp 生产验证 | **~1ms 级**(bpp 每块全部变更键的 multiproof ≈1ms) | ✅ 账户+per-account 存储双层 |
| B. reth 格式归档栈(mptbuild/mpttrie/mptproof) | AccountsTrie/StoragesTrie + Dense V1/V2 | G1 Dense 完成过(已删);V2 有 extension bug 被禁用 | 账户 <1ms(dense);存储被 200K 叶上限卡死 | ⚠️ 账户 ✅;存储是**统一树 ≠ EIP-1186** |
| C. HPH(--tree mpt replay 链) | CommitmentBranches(HPH 分支) | GenerateWitness → `trie.Trie.Prove` 链路存在(= erigon3 的 eth_getProof 实现方式) | ms 级(O(路径 unfold)) | ✅(HPH stateRoot 字节兼容) |

### 1.1 真实数据(D:/N42-hashed,25.19M 块 ETH 主网镜像,218 GB)

```
HashedAccount     390,300,750 条   31.2 GB   (账户叶子)
HashedStorage   1,580,731,211 条   97.5 GB   (存储叶子,DupSort per-account)
TrieAccount        29,224,934 条    5.2 GB   (账户 trie 上层分支缓存)
TrieStorage       157,022,724 条   25.2 GB   (per-account 存储 trie 分支缓存)
Code                2,407,848 条   15.8 GB
```

分支缓存合计 **30.4 GB** = 叶子数据的 ~24%,这就是当前为"快速证明"付出的全部存储。

### 1.2 此前"分钟级"误判的澄清

本会话早先把 MPT 证明定性为"FullAccountProofBytes 分钟级、需要大工程"。深挖后纠正:

- 分钟级是**栈 B 的 legacy 路径**(reth 格式不单独存 <32B 内联节点,旧实现用
  LeafSource 枚举重建内联 sibling,每个 ~30s)。
- 栈 B 的 **Dense V1 路径 2026-05-22 已在真实数据构建并验证**:USDC 账户证明
  9 节点 / 3779 B / **<1ms**(docs/proof-archive/usdc_v1dense_*.log);代价
  AccountsDense ~12 GB + StoragesDense ~45 GB。
- 栈 B 真正的死结不是速度,是**架构**:它的存储树是统一树(composite key
  keccak(addr)||keccak(slot)),存储证明对不上 EIP-1186 的 per-account 语义;
  且稀疏 keccak 空间里重合约的子树塌缩到持久化分支以下,触发 >200K 叶枚举上限。
- **栈 A 没有这两个问题**:TrieStorage 本来就是 per-account;每个持久化分支
  以下的子树对应 HashedAccount/HashedStorage 的**连续键区间**,重建 = 一次
  有界 cursor range scan(均值 ~20-30 叶),这正是 bpp ~1ms/块的原因。

---

## 2. 压缩原理:为什么"全节点字节"本质上是冗余的

MPT 的全部节点字节都可由两样东西**确定性导出**:

1. **叶子集合**(HashedAccount/HashedStorage,反正必须存);
2. **树结构**(每个分支的 16-bit state_mask/tree_mask)。

节点 RLP 里占体积大头的是子节点哈希(每个 33 B),而哈希全部可以从叶子
自底向上重算——所以任何"节点字节存储"都只是**用空间换重算时间的缓存**,
问题只在于缓存哪一层、缓存多少:

| 方案 | 缓存内容 | 25M 块 ETH 主网量级 | 单证明读取 |
|---|---|---|---|
| geth hash-based(全节点字节,keccak→RLP) | 所有节点 | ~250-300 GB 级(经验值) | O(深度) 点读,快 |
| geth PBSS(path-based 单版本) | 所有节点按 path | ~200-250 GB 级 | O(深度) 点读,快 |
| reth 格式 Dense V1(栈 B) | 所有持久化分支的全槽位 | +57 GB | 每 hop 1 点读,<1ms |
| Dense V2(leaf-marker,叶哈希读时重建) | 同上但单叶槽位只存 1B 标记 | 账户 1.79 GB(实测 -81%);存储估 ~10 GB | 每 hop 1 点读 + 每标记 1 叶 seek |
| **erigon-compact(栈 A,现状)** | **只缓存上层分支**(下层子树=连续叶区间即时重建) | **30.4 GB(已在盘上)** | O(深度) 分支读 + 1 次有界叶 range scan,~1ms 级 |

erigon-compact 已经体现了 V2 的核心思想(哈希能从叶子便宜重建的就不存)并
更进一步(整个下层子树都不存)。**它比全节点字节存储小 ~7-10×,而 bpp 证明
其速度足够**。"极致压缩"不需要新发明——盘上这 30.4 GB 就是答案;真正的
"突破性替代"= 把读取路径做对(有界连续区间重建),而不是把所有字节落盘。

> 若仍追求进一步压缩:TrieOf* 的 hash_mask 哈希(33B/子)是剩余大头,理论上
> 也可读时重建(把 range-scan 边界放宽一层),换 ~2-3× 空间但单证明多几次
> range scan——在 30.4 GB(总库 14%)的基数上收益不值得复杂度,不建议。

---

## 3. 推荐路线

### 路线 A(主推):erigon 布局 RPC 接线 —— 零新增存储,ms 级,严格 EIP-1186

为 live eth-el 链(及一切 erigon 布局库)实现 `MPTStateProofProvider`:

1. 单键 RetainList:`wr.AddHashedKey(keccak(addr))` + 每个 slot 的
   `addrHash||slotHash`(复用 bpp `buildBlockProof` 的检索逻辑,
   cmd/n42-stateless-blockproof-produce/main.go:430);
2. 一次 retain-bounded `CalcTrieRoot`(FlatDBTrieLoader 两级)收 witness 节点;
3. 按 EIP-1186 组装 accountProof/storageProof 数组(bpp 的 BlockProof 已是
   RLP 节点集,P8 消费端验证过 3398 anchor);
4. 挂成 `StateProofProvider`(与 QMDB/JMT provider 同接口),
   Descriptor=ethereum-mpt/canonical-eip1186。

- 存储:**+0**;延迟:低 ms(bpp 每块全部变更键 ≈1ms,单键更少);
- 历史高度:可叠加与 QMDB 同思路的窗口(erigon 本就有 changesets,
  unwind-overlay 即可),或先 latest-only;
- 工作量:中等(机器全部存在,抽取 + provider + 测试)。

### 路线 C(并行小件):HPH GenerateWitness → trie.Prove —— 自有 replay 链

`--tree mpt` 转换链(PersistentMPTRootComputer + CommitmentBranches)用
erigon3 同款:对 (账户, slots) 集合跑 `GenerateWitness` → `witnessTrie.Prove`
(lib/commitment/trie/proof.go:29)提取标准节点。零新增存储,ms 级。

### 不建议的方向

- **给 reth-hashed 补全节点字节表**(本研究的原始命题):+200-300 GB 级,
  而收益(点读 vs 有界 range scan)在 ~1ms 基线上可忽略——被路线 A 替代。
- **继续栈 B 的统一存储树**:存储证明永远对不上 EIP-1186;Phase A.5
  per-account 重建 = 重做一遍栈 A 已有的东西。Dense V1/V2 的编码技术
  (leaf-marker)保留为参考,V2 extension bug 不再值得修。

---

## 4. 验证计划(路线 A 落地时)

1. 单测:合成 trie 上 provider 输出 ↔ `VerifyStandardProof`(mptproof 已有的
   独立 EIP-1186 oracle)互验;不存在键的 exclusion proof。
2. 真实数据:D:/N42-hashed 抽样 1000 账户 + 重合约(USDC 等)slots,
   逐个对 header.Root 验证 + 计时(目标 p99 < 10ms)。
3. 交叉:同一 (addr,slot) 对 ../reth 节点的 eth_getProof 输出做字节 diff。
