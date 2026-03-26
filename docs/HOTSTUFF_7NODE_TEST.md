# HotStuff-2 BFT 共识 — 7 节点本地测试指南

## 前提

- Go 1.25+
- 在 `C:\N42\N42-gov5` 目录下操作
- 已编译: `go build -o ./build/bin/n42.exe ./cmd/n42`

## 一、生成测试网配置

```bash
go run ./cmd/hotstuff-testnet
```

输出 `hotstuff_testnet/` 目录:
- `genesis.json` — 7 验证者 genesis（chainId=1143, period=4s）
- `node0~6/keystore/` — 每个节点的 BLS 密钥

## 二、初始化 7 个节点

```bash
for i in 0 1 2 3 4 5 6; do
  ./build/bin/n42.exe init --data.dir hotstuff_testnet/node$i hotstuff_testnet/genesis.json
done
```

所有节点应输出相同的 genesis hash。

## 三、启动 7 个节点

每个节点用不同端口，**先不互连**：

```bash
# 设置环境（Git Bash 防止路径转换）
export MSYS_NO_PATHCONV=1

# 节点地址（从 genesis.json 的 validators 字段）
ADDRS=(
  "0x99ae5ce42e9fbfc70a8ff27ab23ae4717d88ebbd"
  "0x90ecbcbbfd33ab0de7e5d0c3d913e77ee0677d10"
  "0x82579531092aa81c75f943a2e8d50d77eb8ebb47"
  "0xa2a628f2ca037e5dd8e55bbec856da6d2407d3cb"
  "0xa5b46f2e5727079881bc9a2f21c8a44c1b5405cf"
  "0xb3fd2f0506735b81048523c5229b3ca18087e31d"
  "0x8bab5d41c3e0be2686e8ee92df442f97b61baa44"
)

for i in 0 1 2 3 4 5 6; do
  P2P=$((30300+i))
  UDP=$((31300+i))
  HTTP=$((28500+i))
  ./build/bin/n42.exe \
    --data.dir hotstuff_testnet/node$i \
    --p2p.tcp-port $P2P --p2p.udp-port $UDP \
    --http --http.port $HTTP --http.api "eth,net,web3,admin" \
    --mine --etherbase "${ADDRS[$i]}" \
    --log.level info \
    --p2p.no-discovery --p2p.min-sync-peers 0 --p2p.max-peers 20 \
    > hotstuff_testnet/node$i.log 2>&1 &
done
```

等 5 秒让节点启动。

## 四、建立全 mesh 连接

**关键步骤**——7 个节点必须互相连接（quorum=5，需要消息能到达 5+ 节点）：

```bash
# 获取每个节点的 peer ID
PEERIDS=()
for i in 0 1 2 3 4 5 6; do
  HTTP=$((28500+i))
  PID=$(curl -s http://127.0.0.1:$HTTP \
    -X POST -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","method":"admin_nodeInfo","params":[],"id":1}' \
    | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
  PEERIDS+=("$PID")
  echo "Node $i: $PID"
done

# 每个节点连接到所有其他节点
for i in 0 1 2 3 4 5 6; do
  HTTP=$((28500+i))
  for j in 0 1 2 3 4 5 6; do
    [ "$i" = "$j" ] && continue
    P2P=$((30300+j))
    MADDR="/ip4/127.0.0.1/tcp/$P2P/p2p/${PEERIDS[$j]}"
    curl -s http://127.0.0.1:$HTTP \
      -X POST -H "Content-Type: application/json" \
      -d "{\"jsonrpc\":\"2.0\",\"method\":\"admin_addPeer\",\"params\":[\"$MADDR\"],\"id\":1}" \
      > /dev/null 2>&1
  done
done

echo "Full mesh connected."
```

## 五、等待共识收敛

连接后等 **60 秒**。初始阶段节点各自 timeout 推进 view，经过若干轮 timeout 交换后 view 同步，共识开始正常出块。

## 六、验证

### 检查区块号

```bash
for i in 0 1 2 3 4 5 6; do
  HTTP=$((28500+i))
  BLOCK=$(curl -s http://127.0.0.1:$HTTP \
    -X POST -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
    | grep -o '"result":"[^"]*"' | cut -d'"' -f4)
  PEERS=$(curl -s http://127.0.0.1:$HTTP \
    -X POST -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","method":"net_peerCount","params":[],"id":1}' \
    | grep -o '"result":"[^"]*"' | cut -d'"' -f4)
  echo "Node $i: block=$BLOCK ($((BLOCK))) peers=$PEERS ($((PEERS)))"
done
```

**预期**:
- 每个节点 peers=6
- 区块号接近（差距 < 200，因为初始阶段各自出块）

### 检查共识 commit

```bash
grep "block committed" hotstuff_testnet/node0.log | tail -5
grep "block committed" hotstuff_testnet/node3.log | tail -5
```

**预期**: 两个节点在相同 view 提交相同 block hash：
```
node0: block committed hash=4af888… view=561
node3: block committed hash=4af888… view=561  ← 同一个 hash!
```

### 检查无 panic/严重错误

```bash
for i in 0 1 2 3 4 5 6; do
  echo "Node $i: $(grep -ci 'panic\|CRITICAL' hotstuff_testnet/node$i.log) critical errors"
done
```

## 七、动态增删验证者节点

### 7.1 移除节点

通过 admin RPC 提议移除 node6：

```bash
# 在任意节点上执行（通常在 node0）
curl -s http://127.0.0.1:28500 -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"admin_proposeRemoveValidator","params":["0x8bab5d41c3e0be2686e8ee92df442f97b61baa44"],"id":1}'
```

**预期**: `{"jsonrpc":"2.0","id":1,"result":null}`（null 表示成功）

### 7.2 查看待处理变更

```bash
curl -s http://127.0.0.1:28500 -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"admin_pendingReconfigChanges","params":[],"id":1}'
```

**预期**:
```json
{"result":{"committed":true,"hasPending":true,"pendingAdds":0,"pendingRemoves":1}}
```

字段说明：
- `hasPending: true` — 有待处理的变更
- `committed: true` — 变更已被 CommitQC 确认（等待 epoch 边界激活）
- `pendingRemoves: 1` — 1 个节点待移除

### 7.3 添加节点

需要新节点的地址和 BLS 公钥（48 字节 hex）：

```bash
# 新节点的 BLS 公钥从 genesis.json 的 validators 字段获取
curl -s http://127.0.0.1:28500 -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"admin_proposeAddValidator","params":["0xNEW_ADDRESS","0xBLS_PUBKEY_HEX"],"id":1}'
```

**安全限制**:
- BLS 公钥必须是有效的 48 字节 BLS12-381 公钥
- 不能添加已在集合中的地址
- 不能添加已在 pending 列表中的地址
- 如果已有 staged transition（pending 但已 committed），新提案会被拒绝

### 7.4 变更生效时机

变更**不会立即生效**。流程：

```
1. Propose（提案）→ 加入 pending 列表
2. Commit（确认）→ 包含变更的区块被 CommitQC 确认
3. Epoch Boundary（epoch 边界）→ 新集合在下一 epoch 激活
```

genesis.json 中 `epochLength: 1000`，即每 1000 个 view 切换一次 epoch。

### 7.5 验证变更结果

epoch 切换后检查日志：
```bash
grep "epoch transition\|staged\|reconfig" hotstuff_testnet/node0.log | tail -5
```

**预期**: `hotstuff: epoch transition epoch=1 validators=6`（从 7 减到 6）

## 八、停止所有节点

```bash
pkill -f "n42.exe.*hotstuff_testnet"
```

## 八、清理重新测试

```bash
for i in 0 1 2 3 4 5 6; do
  rm -rf hotstuff_testnet/node$i/chaindata hotstuff_testnet/node$i/LOCK
done
# 重新从步骤二开始
```

## 已验证的结果（2026-03-26）

### 共识出块

| 指标 | 值 |
|------|-----|
| 节点数 | 7 |
| Quorum | 5/7 (BFT: f=2) |
| 出块间隔 | 4 秒 |
| 60 秒共识事件 | 2594 |
| View 进度 | 1 → 565 |
| 共享区块 | ✅ 相同 block hash |
| Panic | 0 |

### 动态增删节点

| 操作 | 结果 |
|------|------|
| `admin_proposeRemoveValidator` | ✅ 成功（result: null） |
| `admin_proposeAddValidator` (无效 BLS key) | ✅ 正确拒绝（invalid BLS public key） |
| `admin_pendingReconfigChanges` | ✅ 返回 pending 状态 |
| 多次 remove 同时 pending | ✅ 允许（committed=false 时可叠加） |
| staged 后再 propose | ✅ 被拒绝（EpochTransitionAlreadyStaged） |

## 常见问题

**Q: 节点各自出块，区块号差距很大？**
A: 需要 full mesh 连接（步骤四）。只连 node0 不够——GossipSub 需要 mesh 才能传递共识消息。

**Q: "view timed out" 一直出现？**
A: 正常。前几轮 view timeout 是 view synchronization 过程。经过若干轮 timeout 交换后节点 converge 到同一 view。

**Q: Git Bash 里 multiaddr 路径被转换？**
A: 必须 `export MSYS_NO_PATHCONV=1`，否则 `/ip4/...` 被转成 `C:/Program Files/Git/ip4/...`。

**Q: "unknown ancestor" 错误？**
A: 初始阶段各节点独立出块产生的分叉。共识收敛后不再出现。

**Q: remove validator 后节点数没变？**
A: 变更在 epoch 边界才生效。genesis 的 `epochLength: 1000` 意味着每 1000 个 view 切换。可以改小此值加速测试。

**Q: `admin_proposeAddValidator` 返回 "invalid BLS public key"？**
A: BLS 公钥必须是有效的 48 字节 BLS12-381 公钥（hex 编码 96 字符 + 0x 前缀）。用 `go run ./cmd/hotstuff-testnet` 生成测试密钥。

**Q: epoch 切换后 hotstuff_getCurrentView 不可用？**
A: HotStuff 查询 RPC 在 `hotstuff` 命名空间下，需要启动时 `--http.api "eth,net,web3,admin,hotstuff"` 才能访问。
