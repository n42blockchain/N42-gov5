# N42 本地数据清洗与测试网

## 概述

从原始链数据（v5 mainnet, chain ID 94）通过完整 EVM 重放，清洗为新格式数据，直接启动节点运行。

---

## 第一步：编译

```bash
make n42
```

---

## 第二步：数据清洗（EVM 重放）

### 源数据

| 项 | 值 |
|----|-----|
| 路径 | `D:\N42\v5\mainnet\chaindata\` |
| 大小 | 36 GB |
| 块高度 | ~11.9M |
| Chain ID | 94 |

### 重放命令

```bash
./build/bin/n42 replay-v2 \
  --source D:/N42/v5/mainnet \
  --target D:/N42/mpt-full \
  --chain mainnet_v2 \
  --tree mpt \
  --batch 100000 \
  --jmt=true \
  --lthash=false \
  --fill-gaps
```

**关键参数：**

| 参数 | 说明 | 推荐值 |
|------|------|--------|
| `--chain mainnet_v2` | 使用 v2 链配置（所有分叉从 genesis 激活） | `mainnet_v2` |
| `--tree mpt` | HPH 标准以太坊 MPT 状态树 (O(dirty), 2900 blk/s) | `mpt` |
| `--batch 100000` | 每批块数（影响内存和提交频率） | 100000 |
| `--fill-gaps` | 源链时间间隔 > 15s 时插入空块 | 启用 |

### 重放写入内容

| 数据 | 说明 |
|------|------|
| Genesis Block 0 | canonical hash + ChainConfig + TD + HeadBlockHash |
| PlainState | 全部账户和 storage 最新状态 |
| Block Headers/Bodies | 完整块头（含 Difficulty, Extra, MixDigest 等字段） |
| HeadBlockHash | 每块更新，节点启动读取 |
| ChainConfig | 写入 genesis hash 关联，节点启动读取 |
| ChangeSet + History | 每块状态变更历史 |
| Receipts | 交易收据 |
| BlockWitness | 每块 EVM 执行见证 |
| MPT Branches | HPH 中间 trie 节点 |

### 预期结果

| 指标 | 值 |
|------|-----|
| 总块数 | ~11,928,120 |
| 耗时 | ~1h8m |
| 速度 | ~2,900 blk/s |
| 交易 | ~21.9M |
| 输出大小 | ~24 GB |

---

## 第三步：验证（可选）

```bash
# HPH 全量 rebuild — 从 PlainState 计算 root
./build/bin/n42 rebuild-mpt --datadir D:/N42/mpt-full

# CalcTrieRoot 交叉验证 — 独立算法，root 应完全一致
./build/bin/n42 rebuild-trie --datadir D:/N42/mpt-full
```

---

## 第四步：启动节点

> **chain 名字必须统一使用 `mainnet_v2`**（和 replay 一致）

### RPC 节点（不出块）

```bash
./build/bin/n42 \
  --data.dir D:/N42/mpt-full \
  --chain mainnet_v2 \
  --http --http.addr 0.0.0.0 --http.port 21012 \
  --http.api eth,web3,net,txpool,debug \
  --ws --ws.addr 0.0.0.0 --ws.port 21013 \
  --p2p.no-discovery \
  --p2p.tcp-port 62016 \
  --p2p.udp-port 62015 \
  --log.level info
```

### 出块节点

```bash
# 1. 创建 keystore 账户
./build/bin/n42 account new --data.dir D:/N42/mpt-full

# 2. 记下输出的地址，启动时指定为 etherbase
./build/bin/n42 \
  --data.dir D:/N42/mpt-full \
  --chain mainnet_v2 \
  --mine \
  --etherbase 0x<你的地址> \
  --http --http.addr 0.0.0.0 --http.port 21012 \
  --http.api eth,web3,net,txpool,debug \
  --p2p.no-discovery \
  --log.level info
```

### Windows 后台运行

```powershell
Start-Process -NoNewWindow -FilePath ".\build\bin\n42.exe" -ArgumentList @(
  "--data.dir", "D:\N42\mpt-full",
  "--chain", "mainnet_v2",
  "--http", "--http.addr", "0.0.0.0", "--http.port", "21012",
  "--http.api", "eth,web3,net,txpool,debug",
  "--p2p.no-discovery",
  "--log.level", "info"
) -RedirectStandardOutput "D:\N42\mpt-full\node.log" `
  -RedirectStandardError "D:\N42\mpt-full\node-err.log"
```

---

## 端口分配

| 用途 | 测试节点 | 默认 |
|------|---------|------|
| P2P Discovery (UDP) | 62015 | 61015 |
| P2P Communication (TCP) | 62016 | 61016 |
| JSON-RPC HTTP | 21012 | 20012 |
| JSON-RPC WS | 21013 | 20013 |

---

## 健康检查

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

# 余额查询
curl -s http://localhost:21012 \
  -d '{"jsonrpc":"2.0","method":"eth_getBalance","params":["0x0011b94f614b1dcbf7326384e3ee4d6da2596da7","latest"],"id":1}'
```

| 参数 | 值 |
|------|-----|
| RPC URL | `http://localhost:21012` |
| WebSocket | `ws://localhost:21013` |
| Chain ID | 94 |
| Currency | N42 |

---

## 完整流程（一键）

```bash
# 编译
make n42

# 重放清洗（~1h8m）
./build/bin/n42 replay-v2 \
  --source D:/N42/v5/mainnet \
  --target D:/N42/mpt-full \
  --chain mainnet_v2 \
  --tree mpt --batch 100000 \
  --jmt=true --lthash=false --fill-gaps

# 启动节点
./build/bin/n42 \
  --data.dir D:/N42/mpt-full \
  --chain mainnet_v2 \
  --http --http.addr 0.0.0.0 --http.port 21012 \
  --http.api eth,web3,net,txpool,debug \
  --p2p.no-discovery --log.level info
```

---

## 修改后重新清洗

修改代码后可随时重新重放，产生新的干净数据：

```bash
# 删除旧数据
rm -rf D:/N42/mpt-full

# 重新编译
make n42

# 重新重放
./build/bin/n42 replay-v2 \
  --source D:/N42/v5/mainnet \
  --target D:/N42/mpt-full \
  --chain mainnet_v2 \
  --tree mpt --batch 100000 \
  --jmt=true --lthash=false --fill-gaps

# 直接启动
./build/bin/n42 --data.dir D:/N42/mpt-full --chain mainnet_v2 --http ...
```

重放输出是自包含的——包含 genesis block、ChainConfig、HeadBlockHash、所有 block/state/receipt 数据。节点可以直接加载运行，无需额外修复步骤。

---

## 注意事项

1. **chain 名字统一** — replay 和 node 都用 `mainnet_v2`，不能混用 `mainnet`
2. **数据自包含** — 重放输出包含完整的 genesis block 0 + ChainConfig，节点直接可用
3. **出块需要 keystore** — `--mine` 需要 `account new` 创建的 key
4. **Gap 填充** — 源链时间间隔 > 15s 时插入空块，目标链块号与源链可能不同
5. **MPT root** — HPH 增量计算，O(dirty)，标准以太坊算法，`rebuild-mpt` / `rebuild-trie` 可验证
6. **断点续传** — replay-v2 在 batch 边界自动 checkpoint，中断后可从最后 batch 恢复
