# DATC：归档 head 到链头之间的证明——方案（2026-09-19，已定、未实施）

> 总文档 `datc-archive-plus.md` §9 的第一项。用户决定：**记下方案，以后再做。** 本文没有任何已实现的部分；成本均为估计。

## 问题

DATC 归档按周更新，head 落后链头最多一周。这一段高度上 `eth_getProof` 落回 eth-el 自己的路径，而 eth-el 没有 MPT 证明提供者：
实测返回的是共享 handler 的占位结果（一条 32 字节的哈希，不是可验证的证明）；测试节点上 `latest` 返回 null。

## 为什么不从链头状态往回推

重算 N 处的证明需要路径外每个兄弟子树在 N 处的哈希。一周约 1200 万次账户写入，账户树第 4 层只有 6.5 万个单元，几乎每个单元都脏了，
顶层兄弟得整棵重折。这个办法只在窗口很小（几十个块）时划算。结论同成本定律：要么存、要么折。

## 方案：三层读取 + eth-el 自己当 DATC 的构建器

| 层 | 覆盖 | 数据在哪 | 谁写 |
|---|---|---|---|
| 冷层 | [0, C) | 现在的 zstd 段，不变 | 定期归并 |
| 热尾 | [C, F]，F = 已 finalized 的头 | 归档 MDBX 里的几张表（DatcLeafA/S、DatcStoRoot、DatcAccNode、DatcStorNode） | eth-el 执行每个块时顺带写 |
| 未 finalized 窗口 | (F, tip]，约 64–96 块 | 节点自己的链头状态 | `internal/mptproof` 的历史叶子覆盖 + 子树重建（脏集很小） |

依据（2026-09-19 在代码里核对过）：
- eth-el 验每个块的 stateRoot 用的是 `commitment.TrieRootComputer`（`internal/ethel/hashstate.go`），与 DATC 构建器同一个实现，同样持久化
  `TrieOfAccounts` / `TrieOfStorage`；DATC 取稠密节点的钩子（`lib/trie`、`modules/state/commitment`）就挂在这个实现上。
  所以不需要第二份 262 GB 的状态，也不需要旁路构建进程。
- eth-el 已有并列的先例：`CSFreezerSink`（`internal/ethel/cs_freezer.go`），带 `Rewind`（重组）。

**DATC sink**（与 `CSFreezerSink` 并列，每个块写）：叶行；`sr` 行；账户树根和第 3 层的逐块记录；对 `ns.ladders` 里的合约，第 D−1 层节点的
孩子哈希——直接从 `TrieOfStorage` 读，相当于 v3 构建器的"逐块截断层"，键布局与 `ns` 完全相同（`pathLen | addrHash | path | block(4)`），
所以热尾里不需要 `derive-ns`。主网每块约 250 次账户变更、几千次槽写入，12 秒一块，开销可以忽略。

**读取端联合读取**：热尾的行总比段里的新。floor 查找先查热尾再查段；折叠做两路有序归并，同一个键取热尾里不超过 N 的那行，没有再取段里的。
热尾一周估计几个 GB，留在 MDBX 里不压缩。

**归并**：每天或每周把热尾写成 spill 并入段，再截断热尾——就是现在周更新的收尾，只是数据源换了。归并要重写所有桶（800 GB，30–40 分钟），
只能低频跑，这正是需要热尾、而不是"把周更新跑得更勤"的原因。

**重组**：热尾只写到 finalized（或滞后固定块数）；DATC sink 沿用 `CSFreezerSink.Rewind` 的语义。

## 分期

1. **零代码过渡**：`weekly` 改成每天跑（续跑 + `derive-ns --from` + 收尾 ≈ 1–1.5 小时），滞后 7 天 → 1 天。前提：执行节点每天同步 acctcs/storcs/headerc。
2. **读取端**（估 2–3 天）：热尾表 + 联合读取（floor、折叠两处）。测试：同一条合成链按"全部进段"和"后半段在热尾"两种方式读，每个高度的根和证明逐字节一致。
3. **eth-el 的 DATC sink**（估一周）：接在状态根计算的钩子上；`Rewind`；启动时对齐热尾 head 与节点 head，缺口用变更集补。
   验收：真节点跑一周，`TestDATCGetProofMainnet` 的活节点模式（`DATC_RPC_URL`）打链头附近的高度——这类节点持有这些新区块头，`verify=header` 真正生效。
4. **之后**：未 finalized 窗口；新上榜合约的在线晋级（现在由 `derive-plan` 离线做，在线要维护每合约的出生键计数）。

## 开工前要先确认的

- 变更集的来源现在是 Windows 的 `D:/N42-eth1177`；Linux 上的 eth-el 是快照启动。周更新文档写的是快照模式不产出变更集，但该节点日志显示它在逐块执行
  （`eldevp2p: imported batch ... tExec=...`）。要让 Linux 服务节点自己开 CS sink 和 DATC sink，先确认快照启动模式下 CS sink 是否启用；不行就从一份对应高度的状态接上。
- 热尾放在归档的 `mdbx.dat` 里还是节点自己的 chaindata 里：前者读取端改动最小（`datc.Archive` 已经开着它），但节点要对归档目录有写权限、服务副本
  （`serving-copy`）就不再是纯只读；后者相反。倾向前者。
