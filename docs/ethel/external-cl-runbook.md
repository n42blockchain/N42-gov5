# 外部 CL Runbook — eth-el 真链 catch-up + 12s live

**Date:** 2026-05-23
**Status:** ✅ 验证 Engine API 真实现完整。Operator setup ~1h。

## 关键背景

`cmd/eth-el --caplin.enabled` 只开 MDBX 不做 sync — Caplin 是 Phase 6 stub。
但 `internal/ethel/engineapi/service.go` 注册的 engine_* JSON-RPC 全部**真实现**：

- HTTP listener + JWT (HS256, Engine API §3.1)
- engine_newPayloadV1/V2/V3/V4 — 真跑 EVM
- engine_forkchoiceUpdatedV1/V2/V3 — 真更新 canonical head
- 17 个 engine namespace 方法全注册

所以外部 CL (Lighthouse / Prysm) 可以正常接入。

## 部署架构

```
[Ethereum mainnet]
       │ beacon block + execution payload
       ▼
[Lighthouse/Prysm beacon node]   ← 标准 CL，跑 ports 5052 (HTTP) + 9000 (P2P)
       │ engine_newPayloadV3
       │ engine_forkchoiceUpdatedV3
       │ (JWT HS256 auth)
       ▼ TCP :20014
[cmd/eth-el (auth-RPC listener)]
       │ ExecutePayload → EVM → state root verify → MDBX commit
       ▼
[D:/N42-eth1177 chaindata + freezer]
```

## 前置数据

- D:/N42-eth1177 datadir（已有 25.1M blocks PlainState + freezer，~278 GB MDBX）
- 必须先 `mv mdbx.dat → chaindata/mdbx.dat`（见 docs/ethel/eth-el-datadir-layout.md）
- 不需要 HashedAccount/Storage 表（Engine API 不读，只 HPH root verify 时用，按需 init）

## Step 1 — 生成 JWT secret

```powershell
# 32 字节 hex（geth/erigon/lighthouse 标准）
$secret = -join (1..64 | ForEach-Object { '{0:x}' -f (Get-Random -Min 0 -Max 15) })
$secret | Out-File -Encoding ascii D:/N42-eth1177/jwt.hex
```

或者让 eth-el 自动生成（路径不存在时它会写一个）。

## Step 2 — 启动 eth-el (EL side)

```powershell
# Build with n42el tag
cd C:/N42/N42-gov5
go build -tags n42el -o D:/tmp/eth-el.exe ./cmd/eth-el

# Launch — disable Caplin stub, ONLY enable engineAPI
D:/tmp/eth-el.exe `
    --datadir D:/N42-eth1177 `
    --bootstrap.enabled=false `
    --caplin.enabled=false `
    --engine.enabled `
    --engine.host=127.0.0.1 `
    --engine.port=20014 `
    --engine.jwt=D:/N42-eth1177/jwt.hex
```

预期日志：
```
eth-el: storage opened chaindata="D:/N42-eth1177/chaindata" freezer="..."
eth-el: engineAPI listening addr=127.0.0.1:20014 jwt=D:/.../jwt.hex namespaces=engine
Already caught up frozen=25101867 head=25101866
eth-el: live mode entered (waiting for ctx.Done)
```

## Step 3 — 启动 Lighthouse (CL side)

**安装** — Linux/Mac 用 Docker，Windows 用 release binary：

```powershell
# Windows release（约 100 MB）
Invoke-WebRequest -Uri "https://github.com/sigp/lighthouse/releases/latest/download/lighthouse-v5.x.x-x86_64-windows.tar.gz" -OutFile lighthouse.tar.gz
# 解压 → D:/lighthouse/lighthouse.exe
```

**首次 checkpoint sync**（绕过几年 beacon history，~5 min）：

```powershell
D:/lighthouse/lighthouse.exe bn `
    --network mainnet `
    --datadir D:/lighthouse-data `
    --execution-endpoint http://127.0.0.1:20014 `
    --execution-jwt D:/N42-eth1177/jwt.hex `
    --checkpoint-sync-url https://mainnet.checkpoint.sigp.io `
    --disable-deposit-contract-sync `
    --http `
    --http-address 127.0.0.1 `
    --http-port 5052
```

可用 checkpoint URLs（避免单点）：
- https://mainnet.checkpoint.sigp.io
- https://sync-mainnet.beaconcha.in/
- https://beaconstate.info

## Step 4 — 观察 catch-up

eth-el 日志会出现：
```
Payload executed block=25101867 root=0x... txs=N gas=N
Payload executed block=25101868 root=0x... ...
...
```

55K blocks × ~3-5 s 单块 EVM ≈ 3-5 hours 到 tip（与节点 EVM 速度相关，参考机器配置）。

Lighthouse 日志会出现：
```
INFO Synced     slot=N head_slot=N peers=N
```

## Step 5 — 12s live loop

到 tip 后：
- Lighthouse 每 12s 收一个新 slot
- 调 eth-el engine_newPayloadV3 → ExecutePayload → commit
- 调 engine_forkchoiceUpdatedV3 → 更新 head

eth-el 应该稳定每 12s 输出一行 "Payload executed"。

## 验证健康

```powershell
# eth-el (via auth RPC) — needs JWT in header
# 简单测试 engine_exchangeCapabilities
$jwt = # JWT 工具生成 HS256 token from secret
$body = '{"jsonrpc":"2.0","method":"engine_exchangeCapabilities","params":[[]],"id":1}'
Invoke-RestMethod -Method POST -Uri http://127.0.0.1:20014 `
    -Headers @{"Authorization"="Bearer $jwt"} `
    -Body $body -ContentType "application/json"

# Lighthouse status
Invoke-RestMethod http://127.0.0.1:5052/eth/v1/node/syncing
```

## 故障排查

| 症状 | 原因 | 修复 |
|---|---|---|
| eth-el "DISCARDING freezer tail" | datadir 上有 top-level mdbx.dat | `mv mdbx.dat chaindata/`（见 layout doc）|
| Lighthouse "execution layer not synced" | eth-el state 落后 tip | 正常 catch-up 中，等 |
| Lighthouse "missing JWT" | path/permission 错 | 检查 secret 文件 32B hex |
| eth-el "state root mismatch" | EVM/state code 与 mainnet 分歧 | 单块 EVM bug，加 trace bisect |
| eth-el silent no-op | --caplin.enabled 误开 + 没开 --engineapi.enabled | 改成 caplin=false engineapi=true |
| Lighthouse 12s 后没新 payload | beacon peers 失联 | 检查 9000 udp/tcp 端口 firewall |

## 不需要的

- Caplin（Phase 6 stub）— `--caplin.enabled=false`
- HashedAccount/Storage 表 — Engine API 不读
- TrieOfAccounts/Storage — 同上
- 自己的 sentinel / p2p beacon — Lighthouse 全包

## 与 ethexec freezer-replay 的区别

| | ethexec replay | external CL + eth-el |
|---|---|---|
| 输入 | 本地 witness freezer | 实时 mainnet beacon |
| 上限 | freezer 末块 (25.1M) | mainnet tip + live |
| 速度 | ~2900 blk/s（无网络）| ~1-3 blk/s（mainnet density）|
| 用途 | 重建 state / 验证 archive | 真链上线节点 |

## 关联文档

- docs/ethel/eth-el-datadir-layout.md — chaindata/ subdir 要求
- docs/ethel/catchup-from-eth1177-recipe.md — 老 recipe（含 stale Caplin 误导信息，本文取代）
- memory/project_caplin_stub_reality.md — Caplin 状态 + 修正
