# Hash 自验证 + MPHF 索引:可复用点研究

> 技术核心(本会话在 txindex 上落地):**查询本身携带 hash,故索引不必存 hash**。
> 只存 `MPHF(hash) → 位置`(~2–4 bit/key),读出目标后**重算 hash 与查询比对**即可拒 phantom
> (verify-and-continue)。把 32B key 从索引里彻底拿掉 + 天然防伪。

## 已落地 / 已在用

| 场景 | 状态 | 自验证依据 |
|---|---|---|
| **codeHash → code** | 早已在用(witness reader) | 重算 `keccak256(code)==codeHash` |
| **txHash → tx**(getTransactionByHash) | 本会话完成(caf3cbbd) | 读块按 hash 找 tx(`newRPCTransactionFromBlockHash`) |

模式已立足,下面是**还能复用**的地方。

## 机会 1(最大):blockHash → number(`HeaderNumber` 表)

**现状**:`rawdb.ReadHeaderNumber` 读 MDBX `modules.HeaderNumber` 表 = **32B hash → 8B number**,25M 块 ≈ **~1GB+**(B-tree 开销另算),随链增长。**29 处**依赖它:
getBlockByHash、getHeaderByHash、getBlockTransactionCountByHash、getUncle*ByBlockHash、
getTransactionByBlockHashAndIndex、getLogs(blockHash)、filters、receipts-by-blockHash……

**自验证可行性**:`header.Hash()`(common/block/header.go:112 = `rlpHash`)**完全可由 header 重算**。

**改造(已实现 commit c5962f82)**:冷层 **`internal/blockhashindex`**(与 txindex 同构):
- `MPHF(blockHash) → relBlock`(1:1,无 dat,RecSplit 直接返回 relBlock)。Enums:false + 关 LFP → MPHF(~2bit)+relBlock(3字节,1M段)≈ **~3.25 B/key**;25M 块 ≈ **~80 MB**(对比 ~1GB MDBX,**~12×**;mmap off-heap、可冷卸载)。
  > 注:早先口算的 ~10MB 是**纯 MPHF**(slot≠number,不可用)——同 txindex"1.8bit"教训,可用索引须存 relBlock。
- verifier = 读该 number 的 **header**(只读头,便宜),重算 `header.Hash()==query`;phantom → 不符 → 续探/返回 not-found。
- 一体大段(0..cutoff 不可变)+ 最近 1M/段;mmap 读取(堆 ~0);archive/window 两 profile(EIP-4444 Full 只留近 1 年)。
- 接线:`API.SetBlockHashIndex(svc)`,`BlockChain().GetBlockByHash` / `ReadHeaderNumber` 在 MDBX miss 后查冷层。复用 cmd/txindex-rebuild 的工具骨架 + cscompact mmap reader + verify-and-continue。

> 验证读 header-only(便宜),最终 getBlockByHash 才读整块 body —— 读放大可控。

## 机会 2:txHash → receipt(getTransactionReceipt)——零新索引

`getTransactionReceipt`(api_transaction.go:125)解析 txHash→block 的方式**与 getTransactionByHash 完全相同**(rawdb,MDBX miss 即断)。
**直接复用刚接的 `txlookup.Service` 冷层**:txHash → block(verifier 已确认含此 hash)→ 读 receipts → 取该 index 的 receipt。自验证依据 = tx 在 (block,index) 处 hash==query;receipt 集另由 header `receiptsRoot` 背书。**无需任何新索引**,照搬 getTransactionByHash 的冷层调用即可。

## 自验证原则(可推广判据)

凡 **"X-hash → 位置"** 且 **X 的 hash 可由读出的目标重算** 的查找,都能:
丢掉存储的 32B hash → `MPHF(hash)→位置` + 读出后重算比对。
适用:header hash、tx hash、code hash(均 keccak 可重算)。

**不适用**(诚实边界):
- receiptsRoot / stateRoot 是**整集 Merkle** 验证,非逐键查找(它们验证"一批",不是"一条 hash→位置")。
- 按 topic/bloom 的 log 过滤**非 hash 键**,无可重算的单一 hash。
- getTransactionReceipt 的 receipt 本身不被单独 hash 寻址 → 经 tx hash + receiptsRoot 间接验证(见机会 2)。

## 落地状态(均已实现)

1. **机会 2 ✅**(cf05e305):txlookup.Service 冷层接进 getTransactionReceipt —— 零新索引。
2. **机会 1 ✅**(c5962f82):`internal/blockhashindex` 冷层 + `cmd/blockhash-rebuild` + getBlockByHash 接线 + node opt-in `N42_BLOCKHASH_DIR`。HeaderNumber 从 ~1GB MDBX 卸到 ~80MB mmap 冷段,getBlockByHash 家族(29 处)受益。

## 尺寸与编码:纠正 + 最终决定(2026-06-04,经实测/信息论核对)

**纠正先前的错误估计**(防被旧表误导):
- ❌ "③ 关 LFP → ~4.4 bit/key" —— **错**。来源是"Enums:true 把 offset 压到 ~2.5bit"的乐观假设,被 `internal/blockhashindex/enums_measure_test` 推翻:`Enums:true` 对 dense offset **反而更大**(27.85 vs 25.72 bit/key)。**Enums:true 不是压缩手段,别开。**
- ❌ "blockhash ~10MB(纯 MPHF 1.8bit)" —— **错**。纯 MPHF 1.8bit 只给一个**乱序内部槽号(排列)**,**不是** block number / tx 顺序号。要拿真定位必须存 relBlock/ordinal。

**信息论下限(为什么省不掉)**:把 N 个随机 hash 一一对应到 N 个顺序号,是个排列,信息量 = log₂(N!)/N ≈ log₂N − 1.44 bit/key。
- txbyhash(N≈3.5B):定位 ≈ log₂(3.5B) ≈ 31.7 bit/tx(= block 24.6bit + 块内 idx 7.1bit);+MPHF ≈ **~32 bit/tx ≈ 4 B/tx**;**单探紧排极限 ≈ 30.3 bit/tx ≈ 13.3 GB**。
- 实测盘上 txindex = 33.7 bit/key(含 8bit LFP)= 12.3GB/3.13B。**关 LFP(自校验)≈ 25.7 bit/key ≈ ~11GB**;**留 LFP ≈ 33.7 bit/key ≈ ~14.7GB**(3.5B)。
- 压缩杠杆 = offset 位宽 = **段大小**(1M段3B→80MB级/64K段2B→小,但小段=多段=扇出),**非 Enums**。

**cscompact 2GB/文件硬上限 → 两者根本差异**:
- **blockhash**(25M key ≈ 106MB < 2GB)→ **可单段**:已做 `internal/blockhashindex` **单段全历史 + 无 LFP + 自校验(重算 header.Hash())+ 热 MDBX**(commit abb304a0)。扇出=1。
- **txbyhash**(3.5B key ≈ 14GB ≫ 2GB)→ **物理上无法单段**(现有 txindex 已是 9 个 cdat)→ **被迫分段**。

**最终决定(txbyhash)= 分段 + LFP + verify-and-continue + mmap ≈ 14.7GB**:
- **mmap**(①6fed9750):查找堆 ≈0(冷查询曾把全段 ri 读进堆 12+GB)。
- **LFP**:错段 found=false **不读块**(255/256)→ 块读 ≈1(答案块);L0 热表吸收近期查询(根本不碰 L1)。
- **verify-and-continue**(③14687036):纠 1/256 跨段假阳性(读块核 hash,不符续探)→ 不返错块。
- 备选 ~11GB = 关 LFP + verify(省盘但 cold/missing 查询逐段读块扇出)。减扇出可调大 SegmentSize(1M→4M:25段→7段,~+1-2GB)。

> 一句话:1.8bit 是"hash→乱序槽"(不含顺序号);顺序号是独立 ~30bit/tx 信息;blockhash 小可单段无LFP,txbyhash 大必分段、留 LFP 换廉价查找。
