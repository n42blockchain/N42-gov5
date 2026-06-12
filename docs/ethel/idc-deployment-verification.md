# IDC 节点部署与 stateless 验证手册（2026-06-12）

切换到新 replay 数据后，开 IDC serve 节点 + 手机/minimal 客户端验证的操作手册，
含支持检查结论、部署命令、固定验证清单、测试覆盖矩阵与缺口。

数据体积/模式总览见 `docs/ethel/storage-matrix-2026-06.md`；三层信任模型见
`docs/ethel/stateless-verification.md`。

---

## 1. 切换新数据：支持检查结论

**结论：serve / produce / client 全部通过命令行参数指向数据目录，本会话的 replay
产出（headerc / bodyc / witness / anchorc / codes）格式未变，切换=改目录参数即开箱支持。**

- **无版本配置字段**：数据格式硬编码（headerc columnar Marshal、bodyc ethel wire、
  witness proto v1 length-prefixed、anchorc compact-wire BlockProof）。切换新数据
  *不需要*改代码，只要新数据用同一格式（本会话未改这些格式）。
- **gold gate 仍是 header.Root**：DATC build / bpp anchor 生产都对真 header 逐块（或逐窗）
  校验，新数据进 serve 前已被生产侧验证过。
- **缺口（DATC 尚未挂 serve）**：`n42-datc proof` 能产**全历史任意高度** EIP-1186 proof，
  但 serve 的 `/account-proof` 走 MDBX trieDB（仅 latest）。手机要验证**历史高度** proof
  需给 serve 加 `/datc-proof?addr=&slot=&at=N` endpoint 调 DATC（25M 构建完成后接线，
  见 §5 待办）。当前 serve 支持 latest proof + anchor 历史 root 信任。

---

## 2. IDC serve 节点部署

二进制 `cmd/n42-stateless-serve`，backend = `freezerBackend`，HTTP endpoints 见 §4。

```bash
n42-stateless-serve \
  --headers  D:/n42-eth1/chain/freezer        `# headerc (① header 链)` \
  --bodies   D:/n42-eth1/chain/freezer        `# bodyc (witness 上下文/full)` \
  --witness  D:/N42-eth1177/chain/freezer     `# witness (② 重放，服务手机)` \
  --anchors  <anchorc 目录>                    `# bpp 产出 (③ MPT anchor)` \
  --anchor-k 1000                             `# 锚点周期(固定)；变周期靠 anchorc.blocks 边车` \
  --codes    <codes-freezer 目录>             `# code 按需 (内容寻址, /code)` \
  --chaindata <MDBX>                          `# 可选: Code 表回退 + /account-proof latest` \
  --trie      <MDBX>                          `# 可选: HashedAccounts+TrieOf*, EIP-1186 latest` \
  --port 8080
```

**切换新数据**：把 `--headers/--witness/--anchors/--codes` 指向新 replay 输出目录。
多 IDC：每台同样命令、不同机器；手机/聚合器按 distinct-signer 阈值取多签 anchor。

**数据消费矩阵**（哪个 endpoint 读哪个源）：

| endpoint | 数据源 | 服务对象 |
|---|---|---|
| `/head` `/header` | headerc | 所有 client ① |
| `/anchor` `/anchor-heights` | anchorc + .blocks 边车 | minimal/手机 ③ |
| `/witness` | witness freezer | 手机 ② 重放 |
| `/code` `/code-z` | codes-freezer → MDBX Code | 手机/minimal 按需 code |
| `/block` `/body` | bodyc | full/archive |
| `/account-proof` | trieDB (MDBX) | latest EIP-1186 |
| `/attest` `/attest-status` | 聚合器内存池 | 多 IDC 多签 |
| `/health` | — | 监控 |

---

## 3. 手机 / minimal 客户端验证

入口 `cmd/evmsdk`（gomobile，`mobile_minimal.go`）→ `stateless.MinimalClient`。
`MobileMinimalSync()` 驱动 `Sync()`，三层信任（`minimal_client.go:90`）：

1. **①** `HeaderChain.Extend` — 拉 `/header`，parentHash 链连续性；
2. **②** `ExecVerify` — 拉 `/witness`，本地 EVM 重放 → receiptRoot（code 不足时 `/code` 按需，
   keccak 自验 + 本地永续缓存，mobile `mobileCodeCacheCap=1024`）；
3. **③** anchor 高度拉 `/anchor` → `VerifyAgainstChain` — 从 header.Root 用 pre-state
   MPT multiproof + changes 重建，对齐链根。

手机本地**无完整 trie/树**，状态正确性由 anchor（多 IDC 签名）背书 +（按需）EIP-1186
inclusion proof（`VerifyAccountInclusion`，`eip1186.go`）。

---

## 4. 固定验证清单（切换新数据后逐项跑）

### A. 代码回归（合成数据，CI/本机，秒级）

```bash
go test -tags 'nosqlite,noboltdb' ./internal/ethel/stateless/... -count=1
```
覆盖：传输往返、anchor codec、真实状态 anchor + code 经 HTTP 的 client 验证
（`TestHTTPStateAnchorEndToEnd`，本会话补）、producer→verify 闭环、聚合器多签、
HeaderChain、EIP-1186、minimal/full/archive 客户端、rate-limit/caps。**任何一项红 = 别上线。**

### B. 真实 freezer 冒烟（切换后必跑，证明数据完整 + ①②③ 对齐）

```bash
n42-stateless-e2e \
  --headers D:/n42-eth1/chain/freezer --bodies D:/n42-eth1/chain/freezer \
  --witness D:/N42-eth1177/chain/freezer --codes <codes 目录> \
  --datadir <MDBX 或 ""(纯在线)> --anchors <anchorc 目录> \
  --from <近期块> --count 100 --k 1000
```
对真实数据跑 ①header 链 + ②真 witness EVM 重放 + ③anchor 验证。**注意高度对齐**：
witness/codes/senders 必须 ≥ 测试块高（codes-freezer by-addr 完整、MDBX by-hash 不全）。
失败块多为数据覆盖边缘，非验证 bug（见 [[mpt_stateless_p8]] 纪律）。

### C. 活 IDC HTTP E2E（serve 起来后，证明传输栈）

```bash
n42-stateless-serve ... --port 8080 &        # 起 IDC
n42-stateless-client-test --idc http://127.0.0.1:8080 --from <N> --count 50
```
minimal client 走真 HTTP 拉 anchor/header/code/witness，端到端验证。纯在线模型
（`--datadir "" --codes ""`）= 手机形态：零本地 state，code 按需取 + keccak 验 + 缓存。

### D. DATC 全历史 proof 抽查（25M 构建完成后）

```bash
n42-datc verify --out <DATC 目录> --samples 100         # 历史 root 字节恒等重建
n42-datc proof  --out <DATC 目录> --addr 0x.. --slots 0x.. --at <历史高度>
```
proof 子命令产 EIP-1186 形状结果并**独立 hash-chain 走读自验**（对齐真 header.Root 才输出）。

---

## 5. 测试覆盖矩阵与缺口

| 验证路径 | 覆盖 | 测试 |
|---|---|---|
| ① HeaderChain 扩展/修剪/gap | ✅ 单元 | `headerchain_test.go` |
| ③ 真实 anchor 生产+验证 | ✅ 单元 | `anchor_test.go` `producer_proof_test.go` |
| ③ anchor compact-wire codec | ✅ 单元 | `proofwire_test.go` |
| **serve→HTTP→client 往返（空态①③）** | ✅ 单元 | `serve/http_test.go::TestHTTPEndToEnd` |
| **serve→HTTP→client 往返（真态③+code）** | ✅ 单元（本会话补） | `serve/state_e2e_test.go::TestHTTPStateAnchorEndToEnd` |
| /code 往返 + keccak 自验 | ✅ 单元 | `serve/server_test.go` + 上条 |
| 多签聚合 finalize/fork-split | ✅ 单元 | `aggregator_test.go` |
| ② witness 重放（合成空块） | ✅ 单元 | `minimal_verify_test.go` |
| ② witness 重放（真 mainnet freezer） | ⚠️ 仅 E2E 工具 | `cmd/n42-stateless-e2e`（无 go test 回归） |
| code 三层（cold→hot→IDC fetch） | ⚠️ 仅 E2E 工具 | 同上 |
| **DATC 历史 proof → serve endpoint** | ❌ 未接线 | 待办（§1 缺口） |
| 多 IDC 网络 producer→aggregator | ⚠️ 单机聚合已测 | 无分布式 E2E |
| anchorc.blocks 边车容错 | ⚠️ 路径存在 | 无 corruption 测试 |

**待办（优先级）**：① DATC proof 挂 serve `/datc-proof`（25M 完成后，手机验历史高度）；
② 真 witness 重放的 go-test 回归（需小份固定 freezer fixture）；③ 多 IDC 网络 E2E。

---

## 6. 速查：切换新数据上线流程

1. 生产侧确认新 replay 输出齐全：headerc / witness / acctcs / storcs / codes（+ bpp 跑出 anchorc）。
2. `go test ./internal/ethel/stateless/...`（清单 A）— 代码无回归。
3. `n42-stateless-e2e`（清单 B）— 新数据 ①②③ 对齐、覆盖高度足够。
4. `n42-stateless-serve` 指向新目录起 IDC；`n42-stateless-client-test`（清单 C）— 传输栈通。
5. 多 IDC 各自起；手机/聚合器配 IDC 列表 + 多签阈值。
6. （25M 完成后）`n42-datc verify/proof`（清单 D）— 全历史 proof 能力就绪，接 serve endpoint。
