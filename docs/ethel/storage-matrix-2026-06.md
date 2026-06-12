# eth-el 存储矩阵与压榨台账(2026-06-12 实测)

四种节点模式 × 存储工件的权威对照:格式、实测体积、压缩状态、模式归属、压榨路线。
所有体积为 2026-06-12 D: 盘实测(25M ≈ 块高 24.998M;DATC 全量构建进行中)。

## 0. 实测总账

字节级压缩采样结论:**所有 freezer 表已压缩**(gzip 复压 1.00-1.06x),
朴素再压缩无空间;以下全部压榨都在**语义/格式/冗余**层。

| 工件 | 位置 | 实测 | 格式/压缩状态 | 压榨后目标 |
|---|---|---|---|---|
| bodyc(全 bodies)| n42-eth1/freezer | **573.7 GB** | columnar+压缩,含签名,全史在本地 | **~91.5 GB 热段**(F2 去签名 −45% + EIP-4444 冷段 torrent 卸载,工具已实测 PASS)|
| storcs(存储增量)| N42-eth1177/freezer | 266.6 GB | V2 forward,压缩 | 保持(DATC 源 + 档案值库)|
| witness(ZK 见证)| N42-eth1177/freezer | 171.6 GB | stream v1,压缩,~7.8KB/块 | 保持(mobile/stateless 必需;语义去重 vs changesets 属研究项)|
| receipts | n42-eth1/freezer | **169.9 GB** | 旧格式 | **~19 GB**(compact 9x 已是 replay 目标库全局默认 89eab467,此库待转换)|
| acctcs(账户增量)| N42-eth1177/freezer | 138.7 GB | V2 forward,压缩 | 保持(同 storcs)|
| senders(ecrecover 缓存)| **两库各一份** | **38.3+37.8 GB** | 纯缓存,可再生 | **38 GB 单份共享**(−38)|
| accthist+storhist | n42-eth1/freezer | **38.7 GB** | MPHF/RecSplit 历史索引 | **0**(DATC chg+leaf 取代;25M verify 过后退役)|
| txindex | n42-eth1/freezer | 12.3 GB | 分段+LFP+自校验+mmap,~25.7-33.7bit/tx | ✓ 已达熵线(~30bit/tx 排列熵下限)|
| headerc | n42-eth1/freezer | 4.5 GB | compact 2.5x | ✓ 完成(待刷新到 tip>24.998M)|
| codes | **三份**(两库+独立 25252)| 5.6+5.7+5.7 | 内容寻址 | **5.7 单份**(−11.3)|
| MDBX 状态(1177)| N42-eth1177/mdbx.dat | **278 GB** | 实测 live 168:Account 24.4+Storage 127.5(plain)+Code 16;**110 GB = freelist 空洞** | 压实 −110(#2b);合并见 §3 |
| MDBX 状态(hashed)| N42-hashed/chaindata | **218 GB** | 实测 live 184:HashedAccount 31.2+HashedStorage 97.5+Trie* 30.4+Code 15.8+近期CS 9.5 | Code 与 1177 重复 16 GB;合并见 §3 |
| snapshot | N42-snap | 68.3 GB | .ef/.idx/.val/**.val.zst 双份**+codedict+引导 mdbx 16GB | **~35 GB**(去 .val 明文双份 −28.4;引导 mdbx 可再生)|
| DATC(在建)| n42-datc-eth25m | 143→投影 ~150-250 | node MDBX + leaf/chg zstd 段(W=1024)| **lean 变体 −60-70%**(key-only 叶 + EF 索引,值回源 changesets)|
| anchors | n42-idc-anchors-25m | 4.6 GB | anchorc 压缩 freezer + blocks 边车 | ✓ |
| bpp 临时 trie | n42-bpp-trie-25m | **252 GB** | bpp 构建中间产物,anchors 已提取 | **0**(可再生 6-10h;删除需确认)|

历史副本/试验库(处置需用户确认,非本文档范围):N42-eth1177-test 882GB、
n42-release 1005GB、pevm 728GB、n42-chaindata.bak 246GB、N42 469GB、e340 857GB。

## 1. 四模式 × 工件矩阵

```
工件            mobile   minimal   full   archive-fullhistory
headerc          IDC↓      ✓        ✓        ✓
anchors          IDC↓      ✓        ✓        ✓(生产侧)
witness          IDC按需    -        -        ✓(服务 mobile + 重放)
codes          IDC按需  codedict   F2提取+IDC按需  ✓(全量,服务下游)
snapshot          -        ✓     引导可选       -
Hashed*+TrieOf*   -        -        ✓        ✓
bodyc 热段(F2)    -        -        ✓        ✓
bodyc 冷段        -        -      torrent    torrent(1-of-N 做种)
receipts(compact) -        -        ✓        ✓
txindex           -        -        ✓        ✓
senders           -        -        ✓        ✓(单份共享)
acctcs/storcs     -        -        -        ✓(权威增量+DATC 值库)
DATC              -        -        -        ✓(全历史 getProof)
accthist/storhist -        -        -        退役(被 DATC 取代)
```
`IDC↓` = 从 full/archive 节点(IDC)按需拉取并本地校验(code 验 keccak、witness 验 stateRoot、anchor 验 header 链)。

### 各模式实际账(2026-06-12 修订,区分"下载"与"落盘")

**minimal ≈ 30 GB 下载**(用户口径实测吻合):周 snapshot zst 24 GB
(accounts.val.zst 3.46 + storage.val.zst 18.33 + ef/idx ~2.1 + codedict 0.07)
+ headerc 4.5 GB → 追赶到高度后 12s 跟随。
**验证模型 = anchor 信任,本地无任何完整 trie/树结构**:状态正确性由
多 IDC 签名的 anchor(BlockProof 对 header.Root 自验 + distinct-signer
阈值)背书 —— minimal 不下载 HashedAccount/HashedStorage,不持有也不
维护 TrieOf*,不本地计算 state root;执行读直接走 snapshot
.ef/.idx/.val + 追块 overlay。落盘 = 下载 + 小型 overlay(每周新快照
重置)。Hashed* / Trie* 全套只存在于 full 与 archive。

**full ≈ 142 GB 下载 / ~300 GB 落盘**:
- 下载:snapshot 25 + 热 bodies F2 91.5 + receipts(compact)19 + txindex 12.3
  + headerc 4.5 + anchors 4.6 ≈ 157 → **senders、codes 双双出账后 ~142 GB**。
  **senders 不下载**:F2 格式内嵌 From(AddrDict 驻留,toF2Tx 编码时写入),
  热段自带;冷段 torrent 原始体含签名可 ecrecover。38 GB 全史 senders 出账。
  **codes 不整表下载**:热段部署的合约 code 从 F2 体提取(constructor 内嵌
  runtime code 的标准形态),其余按需 IDC RPC(内容寻址 keccak 自验 + 本地
  永续缓存 —— 与 mobile 同一套已 E2E 验证机制,4872 distinct code 实测);
  15.8 GB Code 表变成按热度自然增长的缓存(热工作集 ~1-3 GB)。
- 落盘额外:snapshot 物化为 Hashed*(HashedAccount 31.2 + HashedStorage 97.5,
  N42-hashed 实测)+ TrieOf*(30.4,可本地 rebuild-trie 重建,下载可免)
  + Code 缓存(增长态)+ 近期 changesets 9.5(unwind 窗口)。

**archive ≈ 1.0-1.2 TB 落盘**(压榨后;现状 ~2.5 TB):
full 之上 + acctcs/storcs 405 + witness 171.6 + DATC lean ~80-150
+ plain 态(replay 源,压实后 ~168;见 §2 #2b)+ senders 一份 38
(witness-replay 管线输入;改读 F2 内嵌可再省,排研究项)。

## 2. 压榨路线(按 GB 排序,均为语义级)

| # | 动作 | 节省 | 前置 | 状态 |
|---|---|---|---|---|
| 1 | bodyc → F2 + EIP-4444 冷卸载 | **−482 GB** | DATC 构建完成(IO 互斥);工具 n42-bodyc-f2/n42-history-expiry/n42-cold-seed 已实测 | 排队 |
| 2 | bpp 临时 trie 删除 | −252 GB | 确认 anchors 完整 + 用户批准(可再生) | 待确认 |
| 2b | eth1177 MDBX 压实(mdbx_copy -c) | **−110 GB** | 实测 live 仅 168 GB(Account 24.4+Storage 127.5+Code 16),文件 278 GB = 110 GB freelist 空洞;零语义风险 | 排队(等 DATC 释放 IO)|
| 3 | receipts → compact 9x | −151 GB | DATC 完成;compact 编解码已是全局默认 | 排队 |
| 4 | DATC lean 转换(key-only 叶+EF) | −90~170 GB | 25M 构建+verify 完成 | 排队 |
| 5 | senders:full 模式整表出账(F2 内嵌 From);archive 留单份 | **−76→−38 GB** | F2 已含 From 字段(toF2Tx);eth1 份是 eth1177 份的严格字节前缀,删 eth1 份;witness-replay 管线指向 eth1177 份 | 可执行 |
| 6 | accthist/storhist 退役 | −38.7 GB | DATC 25M verify 100/100 + historicalstate 切换 DATC 读路径 | 排队 |
| 7 | snapshot 去 .val 明文双份 | −28.4 GB | 确认 snapshot-import 读 .zst 路径 | 待验证 |
| 8 | codes 三份→一份 | −11.3 GB | 各工具 --codes 参数指向单份 | 可执行 |
| 9 | 状态库合并议题(1177 的 278 vs hashed 的 218) | −150~200 GB | 深度工程:两库表布局服务不同管线(replay/witness 源 vs 生产 full);风险高收益大,单独立项 | 议题 |

合计:档案模式 ~2.5 TB → **~1.1-1.4 TB**(−45~55%),
其中零风险队列(1+3+4+5+6+7+8)即可达 −800 GB。

## 3. 关系与依赖图(谁派生谁,决定"删谁不疼")

```
geth ancient(1527GB,外部权威,绝对只读)
  └─bodies──→ bodyc(本地服务副本)──F2/4444──→ 热91.5GB + torrent冷段
  └─receipts→ receipts(compact 后 19GB)
ethexec 重放(eth1177 datadir)
  ├─→ acctcs/storcs(权威增量,405GB)──┬─→ DATC(全历史 proof)
  │                                   └─→ rebuild-state(任意重建)
  ├─→ witness(171.6GB)──→ mobile/stateless 服务 + witness-replay 校验
  └─→ senders(缓存,38GB,可再生)
acctcs/storcs + headerc ──bpp──→ anchors(4.6GB)+ 临时trie(252GB,可再生可删)
Hashed*(canonical)──snapshot-export──→ N42-snap──→ minimal 引导
DATC ⊃ accthist/storhist 的全部查询能力(后者退役)
```
可再生性排序(磁盘紧张时的牺牲顺序):bpp-trie > senders > snapshot 引导 mdbx >
accthist/storhist > DATC(可重建 ~1天) > bodyc(geth ancient 可重导) >
witness/acctcs/storcs(重放数天级,最后动)。

## 4. 已到熵线、不再投入的项

- txindex(~30bit/tx 排列熵下限,实测 25.7-33.7bit)
- headerc(compact 2.5x;字段级已剥离)
- 所有 freezer 字节级压缩(实测 1.00-1.06x 复压)
- witness 体积(7.8KB/块 = MPT-stateless 的 1/36;witness↔changesets 语义去重为开放研究项)
- DATC chg/leaf 段(zstd 6.8-15x 已做;哈希熵 508MB/2M 是硬地板,见 eip1186 研究文档 §5)
```
