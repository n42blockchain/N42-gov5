# Erigon 近期修复 → N42 借鉴审计(2026-06-04)

对照 Erigon 反复修的几类问题,审计 N42 自己的对应代码,按"必要性 × 有效性 × 风险"给借鉴决策。
方法:不逐个拉 Erigon commit(无法可靠获取),而是**用 Erigon 修过的 bug 类别,审 N42 自己的实现**(可验证、可落地)。5 个子代理并行审 5 个领域。

## 总决策表

| 领域 | N42 现状 | 必要性 | 借鉴决策 |
|---|---|---|---|
| **并行执行正确性** | 两套路径都默认关;path2(主循环可达)缺 storage-wipe 隔离 | **高(结构性)/ 低(默认关)** | **借鉴 Erigon 教训 + 先加门控警告** |
| Engine API fail-closed | 默认全校验且 fail-closed;两个 fail-open 逃生口 | 中 | **借鉴:修 1a/2a** |
| Engine slice 所有权 | RLP 解码全新分配 + 深拷贝,**已干净** | — | 无需借鉴 |
| BLOCKHASH read-view | 与执行同 tx,**已一致**(免疫 Erigon BlockOverlay 类) | 低 | 不搬代码 |
| BLOCKHASH 回填续传 | 中途 kill 留窗口空洞(只查窗口底)+ 不校验 peer header | 中 | **补测试(eldevp2p 零测试)+ 小修** |
| Commitment 并行字节所有权 | cell 全定长数组深拷贝、per-worker buffer/RoTx,**已干净** | — | 不搬代码,做 checklist |
| Commitment 并行 root 验证 | 并行=顺序 仅单测保证,运行时只有 header 比对 | 中 | **跑 -race + 禁 --no-header+concurrent** |
| P2P libp2p(N42链) | outbound queue/validate queue/scoring/semaphore,**优于 Erigon 基线** | — | 无需借鉴 |
| WS RPC 限速 | **完全无限速**(绕过 HTTP limiter) | 中-高 | **借鉴:WS 接限速+连接上限** |
| devp2p 入站(eth-el) | 无 per-peer 限速;当前 push 多为 no-op | 高(结构性) | **在实现 tx/block import 前先加限速** |

---

## 1. 并行执行正确性(重点)

**两套 Block-STM,都默认关**:
- **path1** `modules/state/parallel_executor.go` + `mv_*.go`(ethexec `--parallel-evm`,EXPERIMENTAL,默认 off)——**较好**:有 `mvKeyTagWipe` SELFDESTRUCT/CREATE2 wipe 标记 + 块内读隔离 + commit 排序,均有单测。短板:(a) 不产 changeset → **不支持 unwind**(已围栏);(b) wipe-writer 版本恢复靠 `rs[len(rs)-1]` 脆弱。
- **path2** `internal/parallel/*` + `internal/parallel_processor.go`(主 `BlockChain.insertChain` 可达,`internal/blockchain.go:868`,由 config `parallel_evm` 开,**`cmd/n42` 无 CLI flag,只能配置文件开**)——**更危险**:`ParallelStateReader.ReadAccountStorage`(`state_reader.go:72`)**完全不查 suicide/wipe** → 同块内对已 SELFDESTRUCT 地址的 slot 读到**陈旧值**(正是 Erigon 反复修的 GetCommittedState-must-consult-wipe 类);incarnation 也被 stub 掉。它产 changeset(reorg OK)但会**忠实持久化错误状态**。

**借鉴决策**(必要性:默认关→live 紧迫低,但 path2 可被配置静默开启→footgun):
1. **立即(本次):给 path2 加 EXPERIMENTAL 门控警告** —— config `parallel_evm` 开启时打响亮 warning(已知 SELFDESTRUCT/CREATE2 块内隔离不正确),与 ethexec 的 EXPERIMENTAL 提示对齐。纯安全 log,不改共识逻辑。
2. **(排期)把 path1 的 wipe 标记 shadow-read 移植进 `internal/parallel`**(`ReadAccountStorage` 查 suicide/wipe 标记 + 版本比较,镜像 `EVMStateView.ReadStorage`)——这才是 Erigon 修法的本体。
3. **(排期)统一到一套实现**,别养两套 Block-STM(弱的那套必烂)。
4. path1 上线前补 changeset/unwind(当前正确围栏,保持)。
5. EIP-161 空账户并行删除:**需专门测试**(静读无法确认对错)。

## 2. Engine API / Payload 校验

**默认良好**:执行后比对 gas/receiptsRoot/logsBloom/stateRoot/requestsHash,任一不符 → INVALID(fail-closed)。slice 所有权**干净**(RLP 解码全新分配 + overlay 深拷贝)。
**两个 fail-open 逃生口(借鉴 Erigon fail-closed,修)**:
- **1a**:env `N42_GAS161`(`gasDiag`)置位时,gas/receipts/bloom mismatch 降级为 non-fatal **继续接受**(`engine_state_adapter.go:464-470/515-527`)。诊断不应改变 accept/reject → **改成只加日志、不放行**(或限 debug build)。
- **2a**:validate-only 路径前提缺失(blk/parent/BlockChain nil)返回 `(nil,nil,nil)` → 上层判 **VALID**(`engine_payload_stateful.go:195-213` + `engine_api_v1.go:730`)。**应返回 SYNCING/error,不能落 VALID**。
- 4a(低,预防):`GetBlobsV1` 返回别名 slice(当前 bundle 恒空),将来填 blob 前 clone。

## 3. BLOCKHASH / 同步窗口

read-view **已一致**(`getHeader` 读执行同 tx,`engine_state_adapter.go:322`)→ 免疫 Erigon BlockOverlay 类。opcode 256 窗口边界正确(`instructions.go:515`)。
**真 gap(补测试为主,eldevp2p 零测试文件)**:
- **续传空洞**:`backfilled` 是内存 bool;peer 短回复时写入部分窗口后 kill,`haveHeader(from)` 只查**窗口底** → 中段空洞被静默判完成 → 该高度 BLOCKHASH 返 0 → 可能 revert(`downloader.go:959-1001`)。
- **不校验 peer header**:回填的 header 按 peer 自报 hash 直接 `WriteCanonicalHash`,无父链/与已提交 head 比对 → 恶意/坏 peer 可塞错 canonical。
- 建议:加 `eldevp2p` 测试(partial-window 续传最高价值)+ 小修(查窗口顶/全程,或回填后比对 head hash)。

## 4. Commitment / Trie / Root(审计 checklist,不搬代码)

**已干净**:branch cell 全定长数组、mount 深拷贝(`hex_patricia_hashed.go:239`)、per-worker encode buffer/keccak、ETL 拷贝(`etl/buffers.go:90`)、**per-worker 独立 RoTx**(匹配此前 cgocheck 修复)。
**gap/checklist**:
- **跑 `go test -race -run TestConcurrentMPT -count=5` 大 key 集**(无 -race 实证,A4 root 字段读、B2 ctxCloser 错误路径需确认)。
- 并行=顺序 root **仅单测**保证;运行时只有 header 比对(C2)。**禁 `--no-header` + concurrent**(否则可能持久化未验证根)。
- B3:concurrent worker 读**已提交快照**非主 RwTx 未提交写 —— 当前 bulk driver 安全(walk 中不改 plain state),但是**隐式不变量,应在调用点断言/注释**。
- 可把 `VerifyBranchHashes`(`lib/commitment/verify.go`,已存在的完整性原语)抽样接进 bulk-rebuild 后自检。
- 注:并行 commitment 当前是 **bulk/离线工具**(非 live 共识),但产物成为 canonical state → 仍要紧。

## 5. P2P / RPC 抗压

**libp2p(N42 链)优于 Erigon 基线**:outbound queue 1024、validate queue 1024、peer scoring、订阅 semaphore 256(`internal/p2p/pubsub.go`、`subscriber.go`)——无需借鉴。
**gap(借鉴)**:
- **WS 完全无限速**(`ServeHTTP` 把 WS 直接派给 `ws.ServeHTTP`,绕过 per-IP limiter;`wsConfig` 无 rateLimiter 字段)→ **P1:WS 接限速 + 连接数/消息速率上限**。
- HTTP 限速默认关(`HTTPRateLimit` 默认 0)→ 建议非 0 默认或公网强制提示。
- 无全局过载阀(503)→ P2:in-flight 信号量,饱和返 503,护重 RPC(eth_getLogs/debug_trace)。
- **eth-el devp2p 入站无 per-peer 限速**:当前 NewBlockHashes/Tx/NewBlock 多为 no-op(暂无内存放大),但**一旦实现 tx/block import(代码全是 TODO),无任何 buffer 上限/限速 → 单 peer 灌爆内存**。**P1:落地 import 前先加 per-peer 漏桶(复用 `internal/p2p/leakybucket`)+ 有界缓存**。

---

## 推荐落地顺序(安全×价值)

**本次可做(低风险/隔离)**:
- ① path2 EXPERIMENTAL 门控警告(纯 log,防 footgun)。
- ② Engine 1a:`N42_GAS161` 不再改变 accept/reject(只日志)。

**排期(共识相邻,需全验证 + 机器空闲)**:
- ③ path2 移植 path1 的 wipe shadow-read(Erigon 修法本体)+ 统一两套。
- ④ Engine 2a:validate-only 缺前提 → SYNCING/error。
- ⑤ WS 限速 + devp2p 入站限速(P2P 抗压)。
- ⑥ Commitment `-race` 跑 + 禁 `--no-header`+concurrent。

**只补测试(不搬代码)**:
- ⑦ eldevp2p BLOCKHASH 回填续传/partial-window 测试。

**不借鉴(已优于或已干净)**:Engine slice 所有权、BLOCKHASH read-view、libp2p fan-out、commitment 字节所有权。
