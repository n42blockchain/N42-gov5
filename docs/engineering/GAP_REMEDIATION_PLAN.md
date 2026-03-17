# N42 评价提升修复计划（仓库核对版）

> 计划日期：2026-03-16
> 输入基线：[`docs/GAP_ANALYSIS.md`](../GAP_ANALYSIS.md)
> 目标：通过代码与测试补强，把“代码存在”模块提升为“已验证”，而不是通过修改文案提升结论
> 当前状态：Phase 1-6 已完成，结果已回写到 [`docs/GAP_ANALYSIS.md`](../GAP_ANALYSIS.md)

---

## 零、执行结果

本轮已经完成的升格项：

| 模块 | 新增测试文件 | 当前 `Test` 数 | 验收命令 |
|---|---|---:|---|
| `internal/api/graphql` | `internal/api/graphql/handler_test.go` | 16 | `go test -count=1 ./internal/api/graphql` |
| `accounts/external` | `accounts/external/backend_test.go` | 6 | `go test -count=1 ./accounts/external` |
| `cmd/clef` | `cmd/clef/signer_test.go` | 11 | `go test -count=1 ./cmd/clef` |
| `internal/mev` | `internal/mev/boost_test.go` | 6 | `go test -count=1 ./internal/mev` |
| `internal/txspool/encrypted` | `internal/txspool/encrypted/encrypted_pool_test.go` | 8 | `go test -count=1 ./internal/txspool/encrypted ./internal/txspool/...` |

本轮顺带修掉的真实实现问题：

1. `internal/api/graphql` 现在会拒绝过大 body、拒绝畸形 hash/address/number 输入，不再把坏参数静默降级成“最新块”或零值地址。
2. `cmd/clef` 与 `accounts/external` 的 `account_signTransaction` 现在协议对齐了：Clef 返回真正的 signed tx raw，客户端优先按 `Raw` 回解交易；`accounts/external` 也补发了 `accessList`。
3. `internal/txspool/encrypted` 现在会在缺少 decryptor 时显式返回错误，`GetBySender` 也改成了 defensive copy，不再暴露池内指针。
4. `common/transaction` 的 protobuf round-trip 现在会保留 `AccessList`；`api/protocol/types_pb.Transaction` 的 SSZ round-trip 现在会保留 `accessList`、blob 字段和 post-quantum 字段，不再在 P2P SSZ 路径丢字段。

最终总回归结果：

1. `go vet ./...` 通过
2. `go test ./...` 通过，其中 `./tests` 包耗时 `188.613s`
3. `go build ./...` 通过

协议层补强验收命令：

1. `go test -count=1 ./common/transaction ./accounts/external ./cmd/clef`
2. `go test -count=1 ./api/protocol/types_pb ./common/block ./internal/txspool/... ./internal/p2p/...`
3. `go test -count=1 ./api/protocol/types_pb ./internal/p2p/...`

扩展验证 sweep（无需新增测试文件）：

1. `go test -count=1 ./internal/zkprover ./internal/zkverifier ./contracts/deposit ./internal/tracing ./internal/download ./internal/node ./internal/consensus/apoa ./internal/consensus/apos ./common/crypto/stark ./common/crypto/bls ./common/crypto/kzg ./cmd/evmsdk`
2. `go test -count=1 ./internal/tracers/... ./internal/api/filters ./contracts/pqregistry ./internal/metrics/prometheus ./internal/network/...`
3. `go test -count=1 ./internal/miner/builder`

本轮顺带补列进 `docs/GAP_ANALYSIS.md` 的现有已验证模块：

| 模块 | 现有证据 | 验收命令 |
|---|---|---|
| `internal/zkprover` | 15 个 Go 文件，69 个 `Test` | `go test -count=1 ./internal/zkprover` |
| `internal/zkverifier` | 2 个 Go 文件，18 个 `Test` | `go test -count=1 ./internal/zkverifier` |
| `internal/download` | 11 个 Go 文件，8 个 `Test` | `go test -count=1 ./internal/download` |
| `internal/consensus/apoa` | 6 个 Go 文件，17 个 `Test` | `go test -count=1 ./internal/consensus/apoa` |
| `internal/consensus/apos` | 12 个 Go 文件，36 个 `Test` | `go test -count=1 ./internal/consensus/apos` |
| `internal/tracing` | 3 个 Go 文件，9 个 `Test` | `go test -count=1 ./internal/tracing` |
| `internal/metrics/prometheus` | 8 个 Go 文件，1 个 `Test` | `go test -count=1 ./internal/metrics/prometheus` |
| `internal/tracers` | 27 个 Go 文件，14 个 `Test` | `go test -count=1 ./internal/tracers/...` |
| `internal/api/filters` | 8 个 Go 文件，16 个 `Test` | `go test -count=1 ./internal/api/filters` |
| `internal/node` | 17 个 Go 文件，30 个 `Test` | `go test -count=1 ./internal/node` |
| `contracts/deposit` | 5 个 Go 文件，3 个 `Test` | `go test -count=1 ./contracts/deposit` |
| `contracts/pqregistry` | 2 个 Go 文件，22 个 `Test` | `go test -count=1 ./contracts/pqregistry` |
| `common/crypto/bls` | 18 个 Go 文件，34 个 `Test` | `go test -count=1 ./common/crypto/bls` |
| `common/crypto/stark` | 2 个 Go 文件，18 个 `Test` | `go test -count=1 ./common/crypto/stark` |
| `common/crypto/kzg` | 4 个 Go 文件，19 个 `Test` | `go test -count=1 ./common/crypto/kzg` |
| `cmd/evmsdk` | 12 个 Go 文件，17 个 `Test` | `go test -count=1 ./cmd/evmsdk` |
| `internal/miner/builder` | 4 个 Go 文件，8 个 `Test` | `go test -count=1 ./internal/miner/builder` |
| `internal/network` | 15 个 Go 文件，7 个 `Test` | `go test -count=1 ./internal/network/...` |

---

## 一、计划边界

本计划只处理两类事项：

1. 仓库内已经存在实现，但当前证据强度不足的模块。
2. 能通过本仓库代码、测试和命令结果直接证明改进的事项。

本计划暂不承诺以下高成本能力：

1. staged sync
2. Portal Network
3. `io_uring` 异步磁盘 I/O
4. 独立 RPCDaemon 进程

原因很简单：当前仓库核对结果是“未发现实现”，这些不是补几条测试就能升格的项目，不适合作为短期评价提升项。

---

## 二、原始客观基线（执行前）

| 模块 | 当前结论 | 当前证据 | 证据缺口 | 最小升格条件 |
|---|---|---|---|---|
| `cmd/clef` | 代码存在 | 4 个 Go 文件，`go test -count=1 ./cmd/clef` 编译通过，`[no test files]` | 无同目录测试 | 补单元测试并跑绿 |
| `accounts/external` | 代码存在 | 1 个 Go 文件，`go test -count=1 ./accounts/external` 编译通过，`[no test files]` | 无同目录测试 | 补 IPC/RPC 客户端测试并跑绿 |
| `internal/api/graphql` | 代码存在 | 5 个 Go 文件，`go test -count=1 ./internal/api/graphql` 编译通过，`[no test files]` | 无 handler/resolver 测试 | 补解析与响应测试并跑绿 |
| `internal/mev` | 代码存在 | 3 个 Go 文件，`go test -count=1 ./internal/mev` 编译通过，`[no test files]` | 无 relay/auction 测试 | 补 HTTP/竞价路径测试并跑绿 |
| `internal/txspool/encrypted` | 代码存在 | 3 个 Go 文件，随 `./internal/txspool/...` 编译通过，`[no test files]` | 无加解密/池行为测试 | 补加密池测试并跑绿 |

统一升格规则：

1. 仅“可编译”不能升格为“已验证”。
2. 至少要有同目录自动化测试，或可复现的更高层集成测试。
3. 测试必须覆盖成功路径和至少一个失败/边界路径。
4. 若测试暴露真实缺陷，优先修代码，不允许为了测试通过而降低断言。

---

## 三、执行顺序

按“投入最小、评价提升最快”排序：

1. `internal/api/graphql`
2. `accounts/external`
3. `cmd/clef`
4. `internal/mev`
5. `internal/txspool/encrypted`

排序依据：

1. 前三项都是外部接口层，新增测试后最容易把“代码存在”提升为“已验证”。
2. 后两项带密码学/网络输入和并发状态，测试成本更高，但一旦补齐，文档证据会明显增强。

---

## 四、分阶段计划

### Phase 1：GraphQL 接口证据补强

目标：把 `internal/api/graphql` 从“只验证到编译”提升到“接口行为已验证”。

执行项：

1. 为 `Handler.ServeHTTP` 补请求方法、非法 JSON、超大 body、未知 query、错误响应编码测试。
2. 为 `executeQuery` 补 `block`、`transaction`、`account`、`logs` 四条根查询路由测试。
3. 为 `extractInlineNumber`、`extractInlineHash`、`extractInlineAddress`、变量提取逻辑补边界测试。
4. 如测试暴露 resolver 或 handler 的 nil/格式问题，顺手做最小代码修复。

验收命令：

```bash
go test -count=1 ./internal/api/graphql
```

升格条件：

1. 同目录新增 `*_test.go`
2. 关键错误路径和成功路径均可重复通过

### Phase 2：外部签名器客户端与 Clef

目标：把 `accounts/external` 与 `cmd/clef` 从“命令/客户端代码存在”提升到“核心签名路径已验证”。

执行项：

1. 给 `accounts/external/backend.go` 增加 mock IPC JSON-RPC server 测试，覆盖 `listAccounts`、`pingVersion`、`SignData`、`SignText`、`SignTx`。
2. 补 unsupported passphrase API 的错误断言，避免未来静默行为漂移。
3. 给 `cmd/clef/rules.go` 增加规则装载、额度限制、地址白名单、拒绝路径测试。
4. 给 `cmd/clef/audit.go` 增加日志落盘与关闭行为测试。
5. 给 `cmd/clef/signer.go` 增加 `ListAccounts`、`Version`、拒签交易/数据、成功签名的测试。
6. 如测试暴露 JSON 序列化、规则边界或空指针问题，做最小修复。

验收命令：

```bash
go test -count=1 ./accounts/external ./cmd/clef
```

升格条件：

1. `accounts/external` 至少覆盖账户列举和三类签名 RPC
2. `cmd/clef` 至少覆盖 rules、audit、signer 三块核心逻辑

### Phase 3：MEV 竞价与 Builder 接口

目标：把 `internal/mev` 从“实现存在”提升到“竞价和 relay 交互已验证”。

执行项：

1. 给 `RelayClient` 增加 `httptest.Server` 用例，覆盖成功响应、非法 JSON、HTTP 非 2xx、空 bid。
2. 给 `Auction.CompareAndSelect` 和 `RunAuction` 增加本地价值 vs relay bid 的边界测试。
3. 给 `BuilderAPI.ProposeBlindedBlock` 增加 miner 未设置、relay 无结果、正常返回测试。
4. 如测试暴露 nil bid、空 pubkey、body 解析等问题，做最小修复。

验收命令：

```bash
go test -count=1 ./internal/mev
```

升格条件：

1. Relay HTTP 输入与竞价决策逻辑均有测试
2. 至少一条 builder API 端到端单元路径打通

### Phase 4：加密交易池

目标：把 `internal/txspool/encrypted` 从“实现存在”提升到“加密池行为已验证”。

执行项：

1. 给 `encryptAESGCM` / `decryptAESGCM` 补正常解密、坏 key、坏 ciphertext 测试。
2. 给 `SimpleKeyper` 补 epoch key 生成、读取、清理测试。
3. 给 `ThresholdDecryptor` 补参数校验和错误路径测试。
4. 给 `EncryptedPool` 补 `Add`、`GetPending`、`DecryptForBlock`、`Prune`、`GetBySender` 测试。
5. 如测试暴露并发、排序、过期高度或 sender 索引错误，做最小修复。

验收命令：

```bash
go test -count=1 ./internal/txspool/encrypted
go test -count=1 ./internal/txspool/...
```

升格条件：

1. 核心加密辅助函数有单元测试
2. 池行为对高度、发送者和过期清理有覆盖

### Phase 5：文档回写与总回归

目标：只在代码证据到位后回写评价文档。

执行项：

1. 逐项复核哪几个模块可以从“代码存在”改写成“已验证”。
2. 更新 `docs/GAP_ANALYSIS.md` 中对应行，不提前升格。
3. 在 `docs/DEVLOG.md` 记录新增测试和真实修复点，不写主观评分。
4. 统一跑总回归，确保评价提升不是靠局部绿测换来的。

验收命令：

```bash
go test ./...
go vet ./...
make build
```

### Phase 6：现有测试模块扩展核对

目标：把原表里遗漏、但仓库内已经具备同目录测试的模块补列进 `docs/GAP_ANALYSIS.md`。

执行项：

1. 盘点同目录已带测试的功能模块，只选有明确功能边界的包，不把纯内部工具碎片硬拆成能力项。
2. 统计每个模块的 Go 文件数、代码行数、`Test`/`Benchmark` 数。
3. 真实跑包级 `go test -count=1`，只把通过的模块升格进“已核实能力”表。
4. 回写 `docs/GAP_ANALYSIS.md` 和本计划，不提前宣称未跑过的模块。

本轮已完成的补列对象：

1. `internal/zkprover` / `internal/zkverifier`
2. `internal/download`
3. `internal/consensus/apoa` / `internal/consensus/apos`
4. `internal/tracing` / `internal/metrics/prometheus` / `internal/tracers`
5. `internal/api/filters`
6. `internal/node`
7. `contracts/deposit` / `contracts/pqregistry`
8. `common/crypto/bls` / `common/crypto/stark` / `common/crypto/kzg`
9. `cmd/evmsdk`
10. `internal/miner/builder`
11. `internal/network`

---

## 五、每阶段产出要求

每完成一个阶段，都必须同步产出：

1. 新增或修订的测试文件
2. 如有必要的代码修复
3. 该阶段实际跑过的命令
4. 更新后的结论变更

禁止事项：

1. 只改文档，不补证据
2. 用 `t.Skip` 或弱断言掩盖真实缺陷
3. 把“编译通过”写成“功能已验证”

---

## 六、完成定义

本计划完成时，至少应满足：

1. 上述 5 个“代码存在”模块中，多数已具备同目录自动化测试。
2. `docs/GAP_ANALYSIS.md` 中“代码存在”条目数量明显下降，且每次升格都有对应命令和测试文件。
3. 整体结论仍然只基于仓库源码、测试和命令结果，不引入外部宣传口径。
