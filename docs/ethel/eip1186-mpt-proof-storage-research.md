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

---

# 第二部分:全历史任意高度证明 —— 熵下界与"深度自适应时间检查点"设计

日期:2026-06-10(目标升级:任意 (acct, slot, **任意历史块 N**) 的 EIP-1186 证明,
涉及的 trie 数据极致压缩或突破传统实现)

## 5. 问题的真正形状:熵下界

**定理(非正式)**:全部历史上每个块的每个 trie 节点字节,都是
`叶子 changesets + 创世状态` 的确定性函数(结构由键集导出,哈希自底向上重算)。
所以**全历史证明的信息熵下界 = changesets 本身**——归档节点反正必须存它。
一切额外存储都只是"用空间换查询时延"的缓存,设计空间是一条时延-空间曲线:

| 设计 | 额外存储(主网 25M 块级) | 单证明时延 | 备注 |
|---|---|---|---|
| geth archive(全部节点版本,hash 键) | **10-20 TB** | <1ms | 每键 33B 无局部性,经典爆炸 |
| 全部分支版本,path-major diff 编码(≈erigon3 commitment+history) | ~0.8-1.5 TB | ms | 哈希(33B/子×每写×每层)是随机数,zstd 压不动 |
| 纯重算(只有 changesets) | **+0** | 分钟-小时 | 顶层 sibling 哈希要扫 as-of-N 的海量叶子,不可用 |
| 惰性物化(查询驱动 memo 缓存) | 随查询增长 | 首查慢/复查 <1ms | 匹配真实负载(热点账户反复查),可与任何方案叠加 |
| **深度自适应时间检查点(DATC,本研究提出)** | **~130-250 GB**(α=16) | **~5-20 ms** | 见 §6;比 geth archive 小 50-100× |

关键的量纲事实:每个叶子写恰好触碰**每一层**的一个节点 → 节点版本总数
= T_w(全史叶子写)× 平均深度,与怎么切分层无关。主网 25M 块 T_w≈5-10B、
深度̄≈5 → **~25-50B 个节点版本**。存哈希(33B,随机不可压)就是 ~1-1.5TB
的硬地板——**除非不存哈希而存"重算的入场券"**。这正是 DATC 的出发点。

## 6. DATC:深度自适应时间检查点(Depth-Adaptive Temporal Checkpointing)

### 6.1 核心洞察

深度 d 的一个节点,每块被触碰的概率 ≈ min(1, C/16^d)(C=每块变更键数):
根每块都变,深层节点几乎不变。传统方案对所有层用同一时间粒度存版本,
深层存了海量"几乎没变"的版本。**让每层的检查点周期随深度指数增长**:

```
E_d = α · 16^d / C̄        (层 d 的 epoch 长度,α = 每节点每 epoch 期望变更数)
```

每个节点在"它自己的" epoch 里期望恰好变 α 次 —— **把变更率在所有深度上
归一化**。这是把 QMDB undo 窗口的思想(存最小 delta、查询时 scratch 重算)
推广到全历史:MPT 内部节点会变(QMDB slot 不变),所以窗口必须按深度分层。

### 6.2 存储内容与算账

每层 d、每个 epoch 结束时,对该 epoch 内**变过的**节点存一条记录:

```
key   = (d, path, epochIdx)                     — 排序后 delta≈0
value = 节点字节(epoch 末状态,masks+children)  ≈ 36B 摊薄
      + epoch 内变更表 [(childNibble, blockΔ)]  ≈ 3B × α
```

- 版本数 = 每层 T_w/α,共 depth̄ 层 → **T_w·depth̄/α 条**;
- 变更表总条目 = T_w·depth̄(每叶写在每层祖先出现一次)× 3B;
- **主网(T_w=7B, depth̄=5, α=16):版本 2.2B×~36B ≈ 79GB + 变更表 105GB
  ≈ 184GB**;T_w=5B → ~130GB。α 是单旋钮:α=64 → 存储 ÷4、查询 ×4。
- **N42 自有链(T_w≈1 亿, depth̄≈6):≈ 2-3 GB —— 全历史证明几乎免费**。
- 死合约零成本(没变就没记录);USDC 这类热合约自动按其变更率付费。
- 全部记录不可变、按 epoch 追加 → 完全契合静态月度分段哲学,build 后只增不改。

### 6.3 查询算法(单证明 O(α·depth̄²) 级)

```
nodeAt(P, d, N):
  rec   = seekFloor(d, P, epochOf(N,d))          # 1-2 次点读:版本+变更表
  base  = rec.bytes                              # epoch 起点的节点状态
  for (c, b) in rec.changes where b ≤ N:         # 期望 ≤ α 个
      base.child[c] = hash(nodeAt(P+c, d+1, N))  # 递归;叶层 → historicalstate MPHF O(1)
  return base
proof(key, N) = [nodeAt(根..叶路径每层, N)] + 同层 sibling 即 base 中现成哈希
```

- 每层 ≤α 次递归、每次 1-2 点读 + 几次 hash → **~5-20ms/证明**(I/O 主导);
- 根 = nodeAt("",0,N) 必须等于 header(N).Root —— 每次查询自带完备性校验
  (和 QMDB 窗口 provider 同纪律:重建根≠header 根就拒绝出证明);
- exclusion proof 自然支持(下降到分歧节点,sibling 都在 base 里);
- 节点创建/删除跨 epoch:版本记录带墓碑/新建标记;
- 最近一个未封口的 epoch 没有记录 → 路由给 latest/recent-window 路径(已有)。

### 6.4 与已有资产的关系

- **叶子值 as-of-N**:`historicalstate`(snapshot+MPHF+fp)已存在,O(1) 读;
- **构建**:对 changesets 做一次顺序回放(无 EVM),按层维护 epoch 缓冲落盘
  ——小时级,一次性;也可从 bpp witness 流导出;
- **bpp witness 档案的再认识**:每块 BlockProof 恰好捕获该块新建的所有节点
  版本 → 全史 witness 留存 = block-major 的全版本库(TB 级、查询局部性差)。
  DATC 是同一信息的 path-major + 深度分层重组——查询友好且小一个量级;
- **递进部署**:latest(路线A)→ 最近窗口(unwind overlay)→ DATC 全历史,
  三层共享同一 provider 接口,查询按高度路由。

## 7. 最终推荐(对齐"全历史任意高度"目标)

1. **近期落地**:路线 A(latest,+0 存储)+ erigon 式 unwind 窗口(最近 N 块);
2. **全历史**:实现 DATC —— 主网级 ~130-250GB(α=16)/ 自有链 ~2-3GB,
   单证明 ~5-20ms,比 geth archive 小 50-100×,比"存全部分支版本"小 ~5-8×,
   且贴着熵下界只差一个常数(α 可调);
3. **先原型验证常数**:在 N42 自有链(2-3GB 全量)上 build DATC 并对
   replay 留存的每块 header.Root 抽样万级 (key,N) 全验 + 计时 —— 数学假设
   (键均匀分布、α 预算控住递归)在真实数据上的常数一锤定音,然后再决定
   是否对 25M 块主网镜像构建。

## 8. 风险与开放问题

- 偏斜负载:热点前缀(交易所、USDC)让局部递归超 α 预算——按 6.2 的
  per-node 计费天然自适应,但 p99 时延要实测;
- changesets 完整性:D:/N42-hashed 的 changesets 仅从迁移点(~22.x M)开始
  (实测 59M+89M=148M 条/9.5GB)——主网全史 DATC 需要从 genesis 的完整
  changesets(可由 witness/重放重建,数据工程而非设计问题);
- 存储 trie 用 (addrHash‖path) 键,同一套 epoch 调度天然 per-account 自适应。

---

# 第三部分:DATC 原型实测(2026-06-11,cmd/n42-datc,2M 真实主网块)

## 9. 验证结果

- **builder 金门**:genesis→2,000,000 每块重建 root == 真实主网 header.Root
  (changesets→TrieRootComputer 增量,13m31s = 2465 blk/s,逐块校验全开)。
- **verify 全验**:**100/100 随机历史高度的 state root 纯从 DATC 记录重建,
  字节恒等**(浅层 record 路径 + fringe/存储叶史 fold);时延 p50 192ms /
  p99 1.28s(被重合约存储 fold 主导,leafReads 最高 2.3M)。
- verify 双层验证逼出 **5 个真 bug**(金门只保 trie 本身;叶史/索引完整性
  必须靠历史重建来暴露):storage-only 变更漏标账户索引、SELFDESTRUCT 墓碑
  跳丢(storcs 自带 wipe 条目被 skip 吞掉)、DupSort 枚举永空(40B key 假设)、
  同块 create+destruct 幽灵存储、%x 自定义 Formatter 干扰诊断 grep。

## 10. 存储常数实测(2M 块,T_w=15.86M 叶写,C̄≈7.9)

| 表 | 条数 | 大小 | 备注 |
|---|---|---|---|
| DatcAccNode/StorNode | 6.98M / 8.67M | 6.4 + 1.0 GB | epoch 末**全节点快照**(上层 ffff 行 ~550B) |
| DatcAccChg/StorChg | 63.3M / 17.0M | 3.0 + 2.0 GB | 变更窗口(MDBX key+页开销主导) |
| DatcLeafA/LeafS | 12.4M / 3.4M | 1.5 + 0.65 GB | 叶史(≈changesets 的 key-major 重排,归档本有) |
| **合计** | | **14.6 GB** | mdbx.dat 16 GB |

**与设计估算的偏差(关键发现)**:node 表实测 ~918B/记录 vs 设计 36B/版本
(差 ~25x)——设计假设 **diff 编码**(每版本只存变更子哈希),原型为了先
验证正确性存了全节点。chg 表同理(聚合行可压 5-10x)。结论:**直存版机制
正确但尺寸是设计的 ~40x;diff 编码 + chg 每 epoch 聚合行是 scale 必做项**,
做完回到设计带(主网 25M ≈ 150-400GB)。

## 11. 13M / 25M 外推与下一步

- **N42 自有链 13M**(T_w≈1 亿,C̄~2.4):直存 ~10-20GB / build ~1.5h,
  **现在就能跑**;diff 后 ~2-4GB。
- **ETH 25M**(T_w≈5-7B):直存 ~5TB / build ~4-7 天 ✗ → 必须 diff 编码
  (写入量与体积同降 ~5-10x)+ 可选并行化 → ~150-400GB / 1-2 天 ✓。
- 时延优化(已知杠杆,未做):重合约存储树的 record 深度按子树规模自适应、
  热点 (path, N) memo 缓存;p50 预期回 ~10ms 级。
- builder 优化已落:按层分桶 pending(~8x)、排序批量写、keccak 缓存、
  d0 变更行删除、batch 20K 防 MDBX spill;pprof 终态 = cgocall 42% +
  keccak 10% + GenStructStep 22%(引擎地板)。

## 12. diff 编码 + 聚合行实测(2026-06-11,commit 1757bc5f)

三项落地(重建 + verify **100/100 不变**,时延同级,build 13m31s→10m49s):

| 表 | 直存版 | diff 版 | 倍率 |
|---|---|---|---|
| AccNode(FULL 每 8 epoch/路径 + DIFF=新掩码+变更子哈希) | 6.4 GB | 2.1 GB | 3.0x |
| StorNode(**纯墓碑跳写**:从未存在的路径不写墓碑) | 1.0 GB / 8.67M 行 | **0** | ∞ |
| AccChg(每 (path,epoch,批段) 一行,varint 事件包) | 3.0 GB / 63.3M 行 | 709 MB / 9.0M 行 | 4.2x |
| StorChg | 2.0 GB | 1.1 GB | 1.8x |
| 叶史(LeafA/S,≈changesets 重排,可换 historicalstate) | 2.15 GB | 2.15 GB | 下界 |
| **合计** | **14.6 GB** | **6.5 GB** | **2.2x**(纯 DATC 开销 12.4→3.9 GB = 3.2x) |

**摊薄成本:~410B/叶写(含叶史)/ ~245B/叶写(纯 DATC)** vs 设计估算 26B/写
——剩余 ~10x 差距的构成与下一阶段杠杆:① MDBX 行开销(key ~40-50B+页)
主导短行 → **静态段文件**(epoch 不可变,天然契合月度分段哲学)可消 2-4x;
② d1 等上层每 epoch 全 16 子变 → diff≈full,可上层独立调参(更长 epoch
或纯 full+zstd 段);③ F/α 调参。**外推修正**:当前格式 N42 13M 链
(T_w≈1 亿)≈ **25-40GB 可直接跑**;ETH 25M(T_w≈5-7B)≈ 1.2-2.9TB,
仍需段文件化(②①)落到数百 GB —— 机制已验证,剩余是存储工程而非设计风险。
