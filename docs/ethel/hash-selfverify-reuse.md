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

**剩余优化(非阻塞)**:Enums:true EF 压 relBlock 进一步缩小;一体大段减扇出;冷层命中读放大(verifier 读 header + 最终读 block,header-only 已较省)。
