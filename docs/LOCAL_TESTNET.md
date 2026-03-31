# N42 本地数据清洗与单节点测试网

## 概述

从原始链数据（v5 mainnet, chain ID 94）通过完整 EVM 重放，清洗为新格式数据，并启动长期运行的单节点测试网。

---

## 数据清洗流程

### 源数据

| 项 | 值 |
|----|-----|
| 路径 | `D:\N42\v5\mainnet\chaindata\` |
| 大小 | 36 GB |
| 块高度 | ~11.7M |
| Chain ID | 94 |
| 共识 | APoS |
| 账户数 | ~5M (Account 表 4,986,242 条) |

### 清洗方式

**完整 EVM 重放** — 不是简单复制，而是从 genesis 开始重新执行每笔交易：

```bash
./n42 replay-v2 \
  --source D:\N42\v5\mainnet \
  --target D:\N42\mpt-test \
  --tree mpt \
  --batch 10000 \
  --output stats.json \
  --log replay.log \
  --leaf-journal leaves.journal
```

### 清洗内容

| 步骤 | 说明 |
|------|------|
| Genesis 初始化 | 2,322 个预分配账户 + 硬分叉余额（30 亿 N42 at block 7,601,200 提前到 genesis） |
| EVM 执行 | 每笔交易通过 `internal.ApplyTransaction()` 完整执行（同 geth 的 `Process()` 路径） |
| 区块奖励 | 从源块 `Body.Rewards` 直接应用（不需要共识引擎） |
| Gap 填充 | 源链时间间隔 > 15 秒时插入空块（8 秒间隔），保持块号连续、时间单调递增 |
| State Root | MPT 标准（HexPatriciaHashed, Keccak256），与 geth 算法一致 |
| Receipt | 完整计算并验证 ReceiptHash |
| ChangeSet | 每块记录状态变更历史（支持 unwind/历史查询） |
| BlockWitness | 每块记录 EVM 执行的最小状态输入（手机 SDK 用） |
| LeafJournal | 每块记录叶子变更（可用于重建 BMT/JMT/Verkle 树） |

### 清洗结果

| 指标 | MPT 模式 |
|------|---------|
| 总块数 | 11,719,350（含 gap 填充） |
| 耗时 | 35 分钟 |
| 速度 | 5,600 blk/s |
| 交易成功 | 21,477,417 |
| 交易失败 | 1 |
| 输出大小 | ~25 GB |
| LeafJournal | 2.4 GB |

### 输出目录结构

```
D:\N42\mpt-test\
├── chaindata\          # MDBX 数据库
│   └── mdbx.dat        # 所有表：Account, Storage, Code, Block*, Receipt, ChangeSet, History, BlockWitness, BMTNode...
├── checkpoint.json     # 最后块号和 hash
├── stats.json          # 重放统计（交易数、速度、receipt 匹配率等）
├── replay.log          # 结构化日志
└── leaves.journal      # 叶子变更日志（2.4 GB，用于树重建）
```

### MDBX 主要表

| 表 | 条目数 | 大小 | 说明 |
|----|--------|------|------|
| Account | ~5M | ~300 MB | 全部账户最新状态 |
| Storage | ~126K | ~14 MB | 合约存储槽 |
| Code | ~43 | ~350 KB | 合约字节码 |
| AccountChangeSet | ~53M | ~2.5 GB | 历史状态变更（支持 unwind） |
| AccountHistory | ~5.2M | ~650 MB | 历史索引 |
| BlockWitness | ~11.7M | ~2 GB | 每块执行见证 |
| Receipt | ~9.8M | ~3.3 GB | 交易收据 |
| BlockBody | ~11.7M | ~720 MB | 块体（交易列表） |
| BlockTransaction | ~21.5M | ~5.2 GB | 交易数据 |

---

## 端口分配

```
用途              测试节点(长期)  开发调试(临时)  默认(生产)
──────────────────────────────────────────────────────
P2P Discovery     62015 UDP     63015 UDP     61015 UDP
P2P Communication 62016 TCP     63016 TCP     61016 TCP
JSON-RPC HTTP     21012         22012         20012
JSON-RPC WS       21013         22013         20013
Authenticated RPC 21014         22014         20014
pprof             7060          7061          6060
MCP Server        -             -             8553
Message Stream    -             -             8554
gRPC KV           -             -             9090
```

---

## 启动长期测试节点

### 前提
- 已完成数据清洗（上述 replay-v2 流程）
- Binary 已编译：`go build -o build/bin/n42.exe ./cmd/n42/`

### 启动命令

```bash
./build/bin/n42.exe \
  --data.dir D:\N42\mpt-test \
  --chain mainnet_v2 \
  --http --http.addr 0.0.0.0 --http.port 21012 \
  --http.api eth,web3,net,txpool,debug \
  --ws --ws.addr 0.0.0.0 --ws.port 21013 \
  --authrpc.port 21014 \
  --pprof --pprof.port 7060 \
  --p2p.listen.port 62016 \
  --p2p.discovery.port 62015 \
  --mine \
  --p2p.no-discovery \
  --log.level info
```

### Windows 后台运行

```powershell
Start-Process -NoNewWindow -FilePath ".\build\bin\n42.exe" -ArgumentList @(
  "--data.dir", "D:\N42\mpt-test",
  "--chain", "mainnet_v2",
  "--http", "--http.addr", "0.0.0.0", "--http.port", "21012",
  "--http.api", "eth,web3,net,txpool,debug",
  "--ws", "--ws.addr", "0.0.0.0", "--ws.port", "21013",
  "--authrpc.port", "21014",
  "--pprof", "--pprof.port", "7060",
  "--p2p.listen.port", "62016",
  "--p2p.discovery.port", "62015",
  "--mine",
  "--p2p.no-discovery",
  "--log.level", "info"
) -RedirectStandardOutput "D:\N42\mpt-test\node.log" -RedirectStandardError "D:\N42\mpt-test\node-err.log"
```

---

## 健康检查

### 快速检查

```bash
# 块高度
curl -s http://localhost:21012 \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","id":1}'

# 链 ID
curl -s http://localhost:21012 \
  -d '{"jsonrpc":"2.0","method":"eth_chainId","id":1}'

# 最新 header
curl -s http://localhost:21012 \
  -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest",false],"id":1}'
```

### 完整检查脚本

```bash
bash scripts/check_node.sh http://localhost:21012
```

检查内容：块高度、链 ID、Header 21 字段、出块状态、Peer 数、Gas 价格、TX Pool。

### 区块浏览器连接

| 参数 | 值 |
|------|-----|
| RPC URL | `http://localhost:21012` |
| WebSocket | `ws://localhost:21013` |
| Chain ID | 94 |
| Currency | N42 |

---

## Header 格式

21 字段 ETH Pectra 兼容：

| # | 字段 | 类型 | 说明 |
|---|------|------|------|
| 1 | parentHash | Hash | 父块哈希 |
| 2 | sha3Uncles | Hash | 空（post-merge） |
| 3 | miner | Address | 出块者 |
| 4 | stateRoot | Hash | MPT state root |
| 5 | transactionsRoot | Hash | 交易根 |
| 6 | receiptsRoot | Hash | 收据根 |
| 7 | logsBloom | Bloom | 日志布隆过滤器 |
| 8 | difficulty | uint256 | 0（post-merge） |
| 9 | number | uint256 | 块号 |
| 10 | gasLimit | uint64 | Gas 上限 |
| 11 | gasUsed | uint64 | 实际 Gas 使用 |
| 12 | timestamp | uint64 | 时间戳 |
| 13 | extraData | bytes | N42 扩展数据 |
| 14 | mixHash | Hash | prevRandao |
| 15 | nonce | [8]byte | 0（post-merge） |
| 16 | baseFeePerGas | uint256 | EIP-1559 基础费 |
| 17 | withdrawalsRoot | *Hash | EIP-4895（Shanghai） |
| 18 | blobGasUsed | *uint64 | EIP-4844（Cancun） |
| 19 | excessBlobGas | *uint64 | EIP-4844（Cancun） |
| 20 | parentBeaconBlockRoot | *Hash | EIP-4788（Cancun） |
| 21 | requestsRoot | *Hash | EIP-7685（Pectra） |

### Extra 字段布局（规划中）

```
[HotStuff QC: magic(4B)+view(8B)+QC(~170B)]
[Proposer BLS seal: 96B]
[LtHashRoot: 32B]
[JMT root: 32B]
[BMT root: 32B]
[Verkle root: 32B]
[Mobile BLS sig: 96B]
[Mobile bitmap: 64B]
```

---

## 多树架构

| 树类型 | CLI 参数 | 状态 | 说明 |
|--------|---------|------|------|
| MPT | `--tree mpt` | 生产就绪 | 标准 ETH，HexPatriciaHashed |
| BMT | `--tree bmt` | 生产就绪 | Binary Blake3，内容寻址 |
| JMT | `--tree jmt` | 生产就绪 | Jellyfish 16-ary，Blake3 |

Header.Root 始终使用当前激活的树类型。其他树的 root 可放入 Extra。

---

## 注意事项

1. **不要修改 genesis** — 清洗后的数据已包含固定的创世块，chain ID = 94
2. **Gap 填充** — 源链时间间隔 > 15 秒的区域会插入空块，目标链块号与源链不同
3. **AppendDup 升序** — ChangeSet 表要求块号严格升序，跨 batch 通过 `replay_chain_head` 跟踪
4. **MPT branches** — 内存中 ~12K 条目（~1.2 MB），不会无限增长
5. **单节点出块** — APoS 共识，无需 quorum（HotStuff 需要 4+ 节点）
6. **LeafJournal** — 2.4 GB 文件可用于重建任何树（BMT/JMT/Verkle），不需要重跑 EVM
