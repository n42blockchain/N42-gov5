# Witness-replay 输入管线:source → output + 续传

> 目的:为 witness-replay 32-worker 吞吐实测(任务 #2)准备**一致的**(bodies + witness + codes + senders)输入集。
> 关键纪律:**witness 必须由它将被回放的同一份 bodies 生成**。三类源(geth / reth MDBX / reth static_files)同源(同一 ETH mainnet),只是高度可能不同。
>
> 性能区间选择、版本指纹、fail-fast gate、worker sweep 和结果口径见
> [`witness-replay-benchmark-runbook.md`](./witness-replay-benchmark-runbook.md)。
> 本文只解决输入制作；不要用 genesis 空块 smoke 选择 worker，也不要把
> `--continue-on-error` / `--skip-verify` 的跑数当作验证通过。

## 1. 原始 ETH 源(只读,权威)

| 源 | 路径 | 内容 | 高度 |
|---|---|---|---|
| geth ancient | `d:\geth\geth\chaindata\ancient\chain` | headers / **bodies(RLP,真 ETH tx)** / receipts / hashes | frozen **25,199,130** |
| reth MDBX | `d:\reth2k\db\mdbx.dat` | PlainState(accounts/storage)、Bytecodes、senders | 2140 GB |
| reth static_files | `e:\reth2\static_files` | headers / receipts / transactions 分段 | 10040 段 |

geth ancient 是**区块(bodies)权威源**;reth 提供**状态/codes/senders**。三者同链,要升高度先把 geth/reth 各自 sync 到更高,再跑转化。

## 2. 转化工具

### `ethexec`(cmd/ethexec/main.go)= witness 生成器(核心)
- **IN** `--ancient <geth ancient>` (区块)
- **OUT** `--datadir <MDBX>`(执行产出 PlainState)+ 输出 freezer `<datadir>/chain/freezer/`:**acctcs + storcs + witness**
- 关键 flag:`--start/--end` `--commit`(默认 10000)`--verify` `--no-witness` `--no-outputs` `--gogc` `--memory-limit-gb` `--dirty-space-mb`
- **续传**:ethel progress marker + 状态原子提交(SafeNoSync);崩溃后从最近 durable checkpoint 续。`--start` 省略 = auto-resume from `DbInfo/ethel_progress`;`--start 0` 显式 = 清 PlainState 从 genesis 重建。

### 配套
- `code-import2fz` → **codes freezer**(`codes.cidx/codes.NNNN.cdat`,地址索引)= witness-replay bytecode 源,免 MDBX、从 genesis 可用
- `ethexec receipt-copy` → `receipts.cidx`
- `ethexec rebuild-state`(`--start` 省略 auto-resume)→ 把 acctcs/storcs 应用进 MDBX PlainState
- `ethexec reset-progress` → 强设 progress marker(不动 PlainState)
- senders = **输入缓存**(从 reth senders 或 ecrecover 预算),witness-replay `--senders` 用以免 ecrecover

## 3. 消费者:witness-replay(cmd/witness-replay/main.go)
```
witness-replay \
  --input-headers-bodies <geth ancient | n42-eth1 bodyc/headerc> \
  --input-witness        <ethexec 输出 freezer(acctcs/storcs/witness)> \
  --codes-freezer        <codes 目录>   # 或 --datadir <MDBX> 走 Code 表 \
  --senders              <senders 目录> \
  --workers 32 --no-output --gogc 300 --mem-limit-gb 16   # 纯吞吐
```
`--no-output`=不写 CS(纯 CPU-bound EVM,验 Go 上限);`--skip-verify`/`--continue-on-error` 仅用于对**陈旧** witness 跑数(块会早退,吞吐数无效)。

## 4. 盘上现状(指纹)

| 目录 | 表 | 类型 |
|---|---|---|
| D:/N42-eth1177 | acctcs codes senders storcs **witness** | ethexec witness 产出(无自带 bodies) |
| D:/N42-eth1177-test | + bodies headers(**cidx 空 16B**) | 半成品 |
| D:/n42-eth1/chain | bodyc headerc codes receipts senders txindex accthist storhist | catchup 节点 datadir(非 witness) |

## 5. 决定性发现:witness **没坏**,坑在 codes 源不全(2026-06-03)
witness-replay 的 witness 是**按 tx 执行顺序的状态读取流**(`witness_replay_reader.go`:WitnessReplayReader 顺序出队,地址/slot 靠重跑 EVM 隐式重建)。**缺某合约 bytecode → EVM 误把合约当 EOA → stream 错位 → 下游读到垃圾 nonce**(reader 注释明确警告)。

- 初测 N42-eth1177 witness 配 **不全的 codes-freezer**(62MB):block 1M 读到 `0x32Be…2D88` nonce=172321(≠mainnet 17387)+ `bytecode not in codes-freezer` → 误判"witness 不对齐"。
- 排除回归:**旧二进制(c231e253,witness 录制期)与今 HEAD 结果完全一致** → 非回放端代码问题。
- **改用完整 MDBX Code 表(`--datadir D:/N42-eth1177`,codeHash 键、内容寻址、历史正确)**:block **1M/8M/16M/24M/25M 全部 `failed=0`、真实 gas/txs**。⇒ witness 完整可用。

## 6. 一致输入集(现成,无需重生)
```
witness-replay \
  --input-headers-bodies d:/geth/geth/chaindata/ancient/chain \   # geth ancient(或 n42-eth1 bodyc)
  --input-witness        D:/N42-eth1177/chain/freezer \
  --datadir              D:/N42-eth1177 \   # 完整 MDBX Code(关键,勿只用 62MB codes-freezer)
  --output /tmp/wr-dummy --workers 32 --no-output --gogc 300 --mem-limit-gb 16
```
注:`--datadir` 的 N42-eth1177 MDBX(278GB)只读 Accede,**非 bpp 的 N42-hashed**,不冲突。32-worker 全量实测仍应等 bpp 释放 CPU/内存(否则两者争 CPU,吞吐数无意义)。

§2 的 `ethexec` 重生**仅用于"续传到更高高度"**(geth/reth sync 后 auto-resume 扩展 witness),不是当前实测所需。

## 7. 资源约束
- bpp(PID 3187731,~90GB,生产 MPT 锚定)**不得停**;当前 free ~3.3GB。
- ethexec 是 CPU+state 重活,与 bpp 争 CPU/RAM → **生成/续传须等 bpp 释放内存**。本文档的调查/读码/秒级只读探测不受影响。
