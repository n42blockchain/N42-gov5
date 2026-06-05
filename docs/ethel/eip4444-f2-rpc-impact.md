# EIP-4444 冷段卸载 + F2 去签名 —— RPC 能力损失分析

> 实测边界(`n42-history-expiry --dir D:/n42-eth1/chain/freezer --mode cdat --dryrun`,
> 2026-06-05,tip≈25,108,480):
> - 保留窗口 `window=2,629,800` 块(≈1 earth year @12s,= EIP-4444 `HISTORY_PRUNE_EPOCHS` 量级)。
> - **冷/热边界 = block 22,478,680**(HotFromSeg 2743)。
> - **冷段:283 个 `bodyc.NNNN.cdat`,475.9 GB**(block 0 .. ~22,470,655)。
> - **热段:53 个 cdat,91.5 GB**(block ~22,437,888 .. tip)—— 这就是用户口中"约 96 GB":**Full 节点 EIP-4444 后保留的热 body**。

本文回答:卸载/去签名后**哪些 RPC 受影响、影响哪个块范围、对 RWA(real-world-asset)意味着什么**。

两个独立杠杆,影响面不同:

| 杠杆 | 改什么 | 影响范围 | 性质 |
|---|---|---|---|
| **EIP-4444 冷段卸载** | 冷段 body(+era 模式含 receipts)移出热节点 | **block < 22,478,680**(冷区) | **延迟降级**,非丢失(1-of-N 冷取回) |
| **F2 去签名** | 全部保留 body 去掉 r/s/v 签名 + canonical hash | **所有块**(若 F2 作存储格式) | **永久丢失**(签名不可重建) |

---

## 一、EIP-4444 冷段卸载

### 1.1 卸载了什么
- `--mode cdat`:把冷区 `bodyc` 列式段(283 个,475.9 GB)登记进 `manifest-cdat.json`,由 `n42-cold-seed`
  物理 relocate 到归档/torrent;热节点本地只留 91.5 GB 热段。
- `--mode era`:冷段重编码为 EraE(含 receipts),interop 友好但比 cdat 大 ~2.5×。
- **headers 不卸载**:header 链全程保留(轻、且是信任根 + parentHash 链),所以"按高度/哈希取 header"永远可用。
- **state 不受 EIP-4444 影响**:`eth_getBalance/getCode/getStorageAt/call`(在 latest 或 hot 区)照常;
  深历史 state 是 archive 节点的事,与 body 过期正交。

### 1.2 冷区(block < 22,478,680)受影响的 RPC
全部为**降级到冷取回**(coldresolve `ErrBodyTrimmed` → TorrentFetcher 1-of-N 段拉取),不是返回错误 ——
**前提是节点能访问 ≥1 个 seeder/archive**;纯孤立的裁剪节点对冷区这些方法返回 `ErrBodyTrimmed`:

| RPC | 依赖 | 冷区行为 |
|---|---|---|
| `eth_getBlockByNumber/Hash`(fullTx=true) | body(tx 列表) | 冷取回该段后正常 |
| `eth_getBlockTransactionCountBy*` | body | 冷取回 |
| `eth_getTransactionByHash` / `*ByBlock{Hash,Number}AndIndex` | body | 冷取回 |
| `eth_getRawTransactionBy*` | body(且需签名,见 §二) | 冷取回 + 受 F2 叠加限制 |
| `eth_getTransactionReceipt` / `eth_getBlockReceipts` | receipts(仅 `--mode era` 卸载;cdat 模式 receipts 另存) | 冷取回 |
| `eth_getLogs`(范围落在冷区) | receipts/logs | 冷取回(逐段) |
| `trace_block` / `trace_transaction` / `trace_filter` | body + 重放 | 冷取回 body 后重放 |
| `debug_traceBlockBy*` / `debug_traceTransaction` | body + 重放 | 冷取回 body 后重放 |
| `eth_getUncle*` | body(ommers;post-merge 恒空) | 冷取回 |

### 1.3 冷区**不受影响**(始终本地可服务)
- `eth_blockNumber` / `eth_chainId` / `net_version`
- `eth_getBlockByNumber/Hash`(fullTx=false 的 **header 字段**:hash/parentHash/stateRoot/…)—— header 全留。
- `eth_getBalance/getCode/getStorageAt/getTransactionCount`(latest/hot 区 state)
- `eth_call` / `eth_estimateGas`(latest/hot 区)
- 热区(block ≥ 22,478,680)的**所有**上表方法 —— 本地热段直接服务。

### 1.4 范围小结
- **冷区 = block 0 .. 22,470,655**(283 段):上表方法走冷取回(有 seeder 则可用、延迟高)。
- **热区 = block 22,478,680 .. tip**(~1 年):全功能本地服务。
- 边界段 cdat 0283 跨界 → 整段保留为热(对齐 down 到段)。

---

## 二、F2 去签名(对**所有**保留 body 生效,若选 F2 作存储格式)

F2 = 去 r/s/v 签名(65 B/tx,唯一大杠杆,−44.8%)+ from 改存 from-ID + 不存 canonical hash。
设计依据:finalized 历史不可逆,**信任型节点无需再验签**。代价是签名/canonical-hash 不可复现。

### 2.1 永久丢失(不可重建,无冷取回可救 —— 除非保留 L 档源)
| 能力 | 原因 |
|---|---|
| `eth_getRawTransactionByHash` / `*ByBlock*AndIndex` | RAW = 签名后的 RLP;去了 r/s/v 无法重组签名字节 |
| tx 响应里的 `v`/`r`/`s` 字段 | 不存 → 返回空/null |
| 历史 tx 的 `eth_sendRawTransaction` 重广播 | 同上,造不出签名字节 |
| block tx-**列表**(fullTx=false)里的 canonical `tx.hash` | hash 不可重算;需可选 `f2.txhashes` sidecar(~32 B/tx)才回得来 |
| `eth_getTransactionByHash`(裸 F2,无索引) | hash→位置 不可重算;需 **F1.5 MPHF 索引**(`f2.txhash.*`,~2–10 B/tx)才查得回 |

### 2.2 F2 下仍正常(账本视图全保留)
from、to、value、nonce、gas、input(calldata/合约参数)、type、accessList、withdrawals、
blob 字段(t3 BlobFeeCap/BlobHashes)、7702 auth(t4 AuthList 含其自身 v/r/s):
- `eth_getBlockByNumber/Hash`(账本字段)、`eth_getTransactionReceipt`、`eth_getLogs`、
  `eth_getBalance`/`eth_call`(state)—— 全部正常。

### 2.3 档位选择(已实现,opt-in)
- **F1.5(推荐甜点)**:F2 + MPHF hash 索引 → 把 `eth_getTransactionByHash` 查回来。
  仅剩 block 列表 hash 字段 + r/s/v + getRawTransaction 不可用。−39%。
- **F1**(存 32B hash + 索引):标准浏览器友好,−24%。
- **F2**(纯账本,无索引):−44.8%,连 getTxByHash 都放弃 —— 仅内部/账本分析节点。
- **L 档源 `D:/n42-eth1` 始终只读保留**:F2/F1.5 是派生只读副本,可随时重建,且需要 canonical
  历史(p2p / era1 对外、getRawTransaction)时回到 L 档。

---

## 三、RWA(real-world-asset)专项

RWA 代币(链上国债、地产、商品)对**全历史事件日志**有合规/审计/溯源刚需(自发行起的全部
Transfer / mint / burn / 合规事件)。两个杠杆对 RWA 的影响**截然不同**:

- **F2 对 RWA 日志无影响**:logs/receipts 是**独立的表**,F2 只动 body 的签名列。RWA 的
  `eth_getLogs`(按合约地址 + topic 过滤)读 receipts,F2 节点照常服务。✅
- **EIP-4444 才是 RWA 的真问题**:冷区 receipts/logs 被卸载后,"查这枚 RWA 代币自发行(可能 2–3 年前,
  落在冷区)以来的全部转移"在裁剪型 Full 节点上**只覆盖热的 ~1 年窗口**。深历史需:
  1. **冷取回**(coldresolve 1-of-N,需 `--mode era` 卸载含 receipts 的冷段)—— 可用但逐段延迟高,
     不适合"全历史一次扫"的审计型重查询;
  2. **专用事件索引器**(`internal/exex/extensions/ai_indexer` 或外部 subgraph)—— 把 RWA 相关 logs
     **独立持久化**,不随 body/receipts 过期,这是审计型 RWA 服务的推荐架构;
  3. **archive 节点**(`n42-cold-seed --profile archive` 全留 header+body+receipts)作权威回填源。

**结论(RWA)**:对外做 RWA 合规/审计 RPC 的节点**不应**是"EIP-4444 裁剪 + 纯 F2",而应:
保留 archive 或挂事件索引器供深历史 `eth_getLogs`;F2 本身不挡 RWA 日志查询,但去签名节点
不能对外提供 canonical 历史 tx(getRawTransaction/重广播)。

---

## 四、命令(本会话用到)

```
# 冷热边界 + 体积(只读,秒级)
n42-history-expiry --dir D:/n42-eth1/chain/freezer --mode cdat --dryrun

# 产冷段卸载 manifest(只读源,写 manifest-cdat.json;物理 relocate 由 n42-cold-seed 做)
n42-history-expiry --dir D:/n42-eth1/chain/freezer --out D:/n42-cold-offload --mode cdat

# F2 去签名全量转换(派生只读副本,不污染源 L 档)
n42-bodyc-f2 --dir D:/n42-eth1/chain/freezer --senders D:/N42-eth1177/chain/freezer \
  --out D:/n42-bodyc-f2 --write --txhashes --stream --limit 0
```
