# mainnet_qmdb 7-Node Deployment Guide
### replay-v2 data sync → HotStuff live block production → 7-node convergence

This is the operational runbook for standing up the `mainnet_qmdb` chain: a
HotStuff-BFT, QMDB-state, ETH-RLP-encoded self-chain. It covers preparing the
chain data with `replay-v2`, launching the 7 validator nodes on the replayed
data, and verifying convergence.

All of the code referenced here is on `main` (merge `6c3ebfe8`):
- chainspec `params/chainspecs/mainnet_qmdb.json`
- `replay-v2` (`cmd/n42` → `internal/replay/engine_v2.go`)
- HotStuff transport: `internal/sync/rpc_block_push.go` (direct push),
  `rpc_block_by_hash.go` (fetch-on-miss), `rpc_catchup.go` (lagging-node catch-up)
- ETH-RLP encoding: `common/block` (`TxRoot`, `Block.EncodeRLP`, `rlpHash`),
  `internal/sync/rpc_chunked_response.go`, `internal/p2p/broadcaster.go`
- per-chain ETH-RLP gate: `block.UseEthereumTxRoot` (set from `StateScheme=="qmdb"`)

> **Encoding note.** `mainnet_qmdb` uses Ethereum-standard RLP for block/header/tx
> transport **and** for the consensus tx-root (`TxHash = DeriveShaErigon(EthTransactions)`).
> The block hash is `keccak256(rlp(header))`. Legacy native chains (e.g. n42
> mainnet) keep their proto encoding — the per-chain gate isolates this, so the
> two never mix.

---

## 0. Prerequisites

```bash
# Build the node (CGO required for MDBX; build tags applied by the Makefile).
make n42
# → build/bin/n42  (or build/bin/n42.exe on Windows)

# Windows note: cap the MDBX map size so 7 instances don't exhaust paging.
export N42_MDBX_MAPSIZE_GB=8        # PowerShell: $env:N42_MDBX_MAPSIZE_GB="8"

# External-sort temp dir for replay (point at a disk with >130GB free).
export N42_ETL_TMPDIR='D:/etl-tmp'  # PowerShell: $env:N42_ETL_TMPDIR='D:/etl-tmp'
```

### 0.1 Generate the 7 validator key sets (deterministic)

The 7 validators are derived deterministically so every operator reproduces the
same set. **Back up the BLS pool** — losing it loses the validator identities.

```bash
# BLS validator keys (committee of 7). Address = keccak256(pubkey)[12:].
# Writes keystore/bls_<addr>.key per validator.
build/bin/n42 ... # see cmd/n42-blspool: -count 7 -seed 0x4242...42

# libp2p peer IDs from each node's fixed P2P network key (cmd/peerid).
# These become the --p2p.peer multiaddrs in the mesh below.
```

The chainspec `params/chainspecs/mainnet_qmdb.json` already pins the 7 validator
addresses; the keystore files must match.

---

## 1. replay-v2 — prepare the chain data

`replay-v2` reads a source chain DB and re-produces every block under the
`mainnet_qmdb` rules: QMDB state root, HotStuff-ready headers, **ETH-RLP tx-root**
(`DeriveShaErigon`), re-encoded block/tx. It writes a self-contained target DB
that the 7 nodes boot from.

```bash
# Sync genesis..N into a fresh target DB.
build/bin/n42 replay-v2 \
  --source D:/N42/v5/mainnet \      # source chain DB (mainnet_v2, chainId 94)
  --target D:/n42-qmdb-data \       # output DB the nodes will run on
  --chain  mainnet_qmdb \           # selects ETH-RLP tx-root + QMDB + HotStuff config
  --tree   qmdb \                   # QMDB state commitment
  --from   0 \
  --to     1000                     # raise --to for a longer prefix
```

Expected tail of the log (success):

```
replay-v2 complete  blocks=1000  txReplayed=..  txFailed=0  receiptMatch=1000  receiptMismatch=0
```

- `receiptMatch == blocks` and `receiptMismatch=0` means every block re-executed
  identically (EVM-faithful). State root is unchanged; only `TxHash`/block-hash
  reflect the ETH-RLP encoding.
- `replay-v2` calls `NewEngineV2`, which sets `block.UseEthereumTxRoot` from the
  target chain's `StateScheme=="qmdb"` — so the replayed roots match what the
  live nodes will produce.

> **Discipline (learned the hard way):** always `export N42_ETL_TMPDIR` to a disk
> with ≥130GB free before a full-chain replay; the default C: temp will run out
> mid-run. Use `Ctrl+C` (SIGINT) to stop a long replay — it flushes + commits
> before exiting; never `kill -9`.

---

## 2. Launch the 7 nodes on the replayed data

Each node gets its own copy of the replayed DB, its BLS key, its etherbase
(= validator address), and static `--p2p.peer` multiaddrs to the other 6.

### 2.1 PowerShell launch script (local 7-node mesh)

```powershell
$env:N42_MDBX_MAPSIZE_GB="8"
# priv[i] = (etherbase address, BLS key hex); nk[i] = network key; pid7[i] = peer id
# (use the keys/ids generated in step 0.1; the values below are the deterministic test set)
$priv=@(@('d2a316...','2fa3ad...'),@('f7dc5c...','5c359b...'), <#...7 entries...#> )
$nk=@('0e3b3c...','1111...','2222...','3333...','4444...','5555...','6666...')
$pid7=@('16Uiu2HAmSMtn...','16Uiu2HAmHzBk...', <#...7 peer ids...#> )
# Development load only. Keep this secret outside source control. Leave empty
# to disable the built-in native/ERC-20 transaction generator.
$txgenKey=$env:N42_DEV_TXGEN_KEY

function BuildArgs($i){
  $maddr=@{}; foreach($k in 0..6){$maddr[$k]="/ip4/127.0.0.1/tcp/$(62000+$k)/p2p/$($pid7[$k])"}
  $peers=@(); foreach($j in 0..6){ if($j -ne $i){ $peers+='--p2p.peer'; $peers+=$maddr[$j] } }
  $nodeArgs=@('--chain','mainnet_qmdb','--profile','n42',
           '--data.dir',"D:/n42-qmdb-node$i",
           '--engine.miner','--engine.etherbase',"0x$($priv[$i][0])",
           '--p2p.no-discovery',
           '--p2p.tcp-port',"$(62000+$i)",'--p2p.udp-port',"$(63000+$i)",
           '--p2p.min-sync-peers','0',
           '--http','--http.addr','127.0.0.1','--http.port',"$(20012+$i)",
           '--http.api','eth,web3,net,txpool,n42') + $peers
  # Run one generator only. Seven independent generators multiply load and
  # make nonce/faucet diagnosis needlessly noisy.
  if($i -eq 0 -and $txgenKey){
    $nodeArgs += @('--dev.txgen','--dev.txgen.max','31','--dev.txgen.key',$txgenKey)
  }
  return $nodeArgs
}

# Seed each node's data dir from the replayed DB + place its keys.
foreach($i in 0..6){
  $d="D:/n42-qmdb-node$i"
  if(-not(Test-Path $d)){ Copy-Item -Recurse -Force D:/n42-qmdb-data $d }
  New-Item -ItemType Directory -Force "$d/keystore" | Out-Null
  [System.IO.File]::WriteAllText("$d/keystore/bls_$($priv[$i][0]).key", $priv[$i][1])
  [System.IO.File]::WriteAllText("$d/network-keys", $nk[$i])
}

# Start all 7.
foreach($i in 0..6){
  Start-Process -FilePath 'C:\N42\N42-gov5\build\bin\n42.exe' -ArgumentList (BuildArgs $i) `
    -RedirectStandardOutput "D:/n42-qmdb-node$i/run.log" `
    -RedirectStandardError  "D:/n42-qmdb-node$i/run.err" -WindowStyle Hidden
}
```

### 2.2 Key flags explained

| flag | why |
|------|-----|
| `--chain mainnet_qmdb` | selects the HotStuff + QMDB + ETH-RLP config (and the ETH-RLP tx-root gate via `StateScheme=="qmdb"`) |
| `--engine.miner --engine.etherbase 0x<addr>` | this node produces blocks as validator `<addr>` (must match a keystore BLS key) |
| `--p2p.no-discovery` + `--p2p.peer <multiaddr>` ×6 | static full mesh; no DHT for a known validator set |
| `--p2p.min-sync-peers 0` | same-height nodes would otherwise deadlock in initial-sync (`found=0 need=1`); 0 lets HotStuff take over immediately |
| `--dev.txgen --dev.txgen.max 31 --dev.txgen.key ...` | optional node-0-only mixed load: auto-funds ten accounts, deploys/seeds a test ERC-20, then emits about 70% native and 30% ERC-20 transfers |
| `N42_MDBX_MAPSIZE_GB=8` | bounds each MDBX map so 7 local instances fit in RAM/paging |

> For a real multi-host deployment, replace `127.0.0.1` with routable IPs in the
> `--p2p.peer` multiaddrs and run one node per host.

---

## 3. Verify convergence

```powershell
function Rpc($p){ ([Convert]::ToInt64((Invoke-RestMethod -Uri "http://127.0.0.1:$p" `
  -Method Post -Body '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' `
  -ContentType 'application/json').result,16)) }

# All 7 heights should advance together.
20012..20018 | ForEach-Object { "port $_ = $(Rpc $_)" }

# Same block number on every node must have ONE hash (true convergence).
function RpcHash($p,$n){ (Invoke-RestMethod -Uri "http://127.0.0.1:$p" -Method Post `
  -Body "{`"jsonrpc`":`"2.0`",`"method`":`"eth_getBlockByNumber`",`"params`":[`"$n`",false],`"id`":1}" `
  -ContentType 'application/json').result.hash }
$h = 20012..20018 | ForEach-Object { RpcHash $_ "0x3ed" }   # e.g. block 1005
"unique hashes = " + ($h | Select-Object -Unique).Count       # MUST be 1
```

**Healthy result:** all 7 heights advance together (~12–15 blocks/s locally) and
the unique-hash count is **1** at every height. Block hash = `keccak256(rlp(header))`.

When transaction generation is enabled, confirm both the pool and the executed
contract path instead of relying only on the `TxGen started` line:

```powershell
# The node log should repeatedly report submitted > 0 and failed = 0, plus one
# ERC20 deployment and a ten-account seed after each generator restart.
Select-String -Path 'D:/n42-qmdb-node0/log/n42.json.log' -Pattern 'TxGen|ERC20' |
  Select-Object -Last 30

# Latest blocks should contain value transfers and calls whose input begins
# with a9059cbb (ERC-20 transfer(address,uint256)).
$body='{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest",true],"id":1}'
(Invoke-RestMethod -Uri 'http://127.0.0.1:20012' -Method Post -Body $body `
  -ContentType 'application/json').result.transactions |
  Select-Object hash,to,value,input

# The simulated mobile committee is stored as consensus evidence, not in the
# block body's `verifier` reward list. Expect signerCount/mobileParticipantCount
# 0x200 (512), hasMobile=true, and a non-zero aggregate signature.
$body='{"jsonrpc":"2.0","method":"n42_getConsensusEvidence","params":["latest"],"id":1}'
(Invoke-RestMethod -Uri 'http://127.0.0.1:20012' -Method Post -Body $body `
  -ContentType 'application/json').result |
  Select-Object blockNumber,blockHash,signerCount,hasMobile,mobileParticipantCount,aggregateSignature
```

---

## 4. Lagging-node catch-up (automatic)

A node that starts late, restarts, or briefly forks at startup is recovered
automatically — no operator action:

- **fetch-on-miss** (`rpc_block_by_hash.go`): a follower that got a Proposal but
  not the block requests it by hash and imports it to cast its vote.
- **direct push** (`rpc_block_push.go`): the leader streams each sealed block to
  every peer (reliable; single-publisher gossip alone doesn't form a mesh).
- **height-based catch-up** (`rpc_catchup.go`): every 8s a node behind its peers
  pulls the converged chain by range (`BodiesByRange`) and `InsertChain` reorgs
  onto it (ForkChoice is height-driven for HotStuff). It fetches only from
  `self+1` upward so it never re-pulls pre-HotStuff replay blocks.

To test: stop one node for a minute, restart it — it rejoins and catches up to
the 7/7 height on its own.

---

## 5. Clean shutdown

```powershell
# Each node owns D:/n42-qmdb-nodeN/n42.pid. On Windows the stop command uses
# AttachConsole + GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT), then waits for the
# node to flush and exit. It does not fall back to a forced kill on timeout.
$bin='C:\N42\N42-gov5\build\bin\n42.exe'
0..6 | ForEach-Object {
  & $bin stop --data.dir "D:/n42-qmdb-node$_" --timeout 120s
}
```

Do not replace this with `Stop-Process -Force` or `taskkill /F`: those bypass
the graceful flush. After a successful stop the PID file is removed and the
data directory is reusable on next start (resume from the persisted head).

---

## 6. Verified status

This pipeline has been validated end-to-end on `main`:
- `replay-v2 --chain mainnet_qmdb --tree qmdb` → `receiptMatch=1000/0`, state root
  stable, ETH-RLP tx-root.
- 7 nodes booting the replayed DB → **7/7 convergence**, identical block hashes,
  height advancing ~12–15 blocks/s.
- Delayed-start and stop/restart nodes auto-recover to 7/7.
- Full proto→RLP migration (transport + gossip + consensus tx-root) audited
  (correctness / consistency / performance) — RLP is ~2× smaller and ~4× faster
  than proto on the block path.
