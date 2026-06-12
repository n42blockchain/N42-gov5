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
| MDBX 状态(1177)| N42-eth1177/mdbx.dat | **278 GB** | Hashed*+Trie*+Code+PlainState(replay/witness/DATC 源库)| 见 §3 合并议题 |
| MDBX 状态(hashed)| N42-hashed/chaindata | **218 GB** | 生产 full 节点(Hashed* ~129+TrieOf* ~30+Code ~18)| 同上 |
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
codes            IDC按需    ✓        ✓        ✓
snapshot          -        ✓     引导可选       -
Hashed*+TrieOf*   -        ✓**      ✓        ✓
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
`✓**` = minimal 由 snapshot-direct 引导出头部状态,不回放历史。

本地落盘量级:mobile ~MB 级;minimal ~250 GB(状态+快照);
full ~400 GB(压榨后);archive ~1.1-1.4 TB(压榨后,现状 ~2.5 TB)。

## 2. 压榨路线(按 GB 排序,均为语义级)

| # | 动作 | 节省 | 前置 | 状态 |
|---|---|---|---|---|
| 1 | bodyc → F2 + EIP-4444 冷卸载 | **−482 GB** | DATC 构建完成(IO 互斥);工具 n42-bodyc-f2/n42-history-expiry/n42-cold-seed 已实测 | 排队 |
| 2 | bpp 临时 trie 删除 | −252 GB | 确认 anchors 完整 + 用户批准(可再生) | 待确认 |
| 3 | receipts → compact 9x | −151 GB | DATC 完成;compact 编解码已是全局默认 | 排队 |
| 4 | DATC lean 转换(key-only 叶+EF) | −90~170 GB | 25M 构建+verify 完成 | 排队 |
| 5 | senders 去重(单份共享) | −38 GB | 两管线指向同一副本(路径参数化已支持) | 可执行 |
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
