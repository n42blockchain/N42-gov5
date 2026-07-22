# Launch a RANGE of isolated test-fleet nodes for the chaos suite, on the
# qs_epoch_test chain (chainId 95). -First..-Last inclusive. Nodes 0..6 are genesis
# validators; 7..10 are observers that can be added later via reconfiguration.
# Resumes from an existing datadir (so it doubles as "restart node N").
param(
  [int]$First = 0,
  [int]$Last = 6,
  [int]$TxGenMax = 10,        # simulated txs/block on node 0 only (0 = off)
  [int]$MaxPeers = 40
)
. "$PSScriptRoot\lib.ps1"
$ErrorActionPreference = "Stop"
$env:N42_MDBX_MAPSIZE_GB = "32"
$vmap = Get-ValidatorMap
$faucetKey = '922c1ad85fb8691315b1ae54b39f7111ae3cfb2c36b038740af36844e9673eee' # dev faucet (chainspec devFaucetAddress)

foreach ($i in $First..$Last) {
  $addr = $vmap[$i].addr; $blsPriv = $vmap[$i].priv
  $d = "$($env:CHAOS_DATAROOT)$i"
  New-Item -ItemType Directory -Force "$d\keystore" | Out-Null
  [System.IO.File]::WriteAllText("$d\keystore\bls_$addr.key", $blsPriv)
  [System.IO.File]::WriteAllText("$d\network-keys", ($Global:ChaosNetKeyBytes[$i] * 32))

  $peers = @()
  foreach ($j in 0..10) { if ($j -ne $i) { $peers += '--p2p.peer'; $peers += "/ip4/127.0.0.1/tcp/$(64000+$j)/p2p/$($Global:ChaosPeerIds[$j])" } }

  $a = @('--chain','qs_epoch_test','--profile','n42','--data.dir',$d,
         '--engine.miner','--engine.etherbase',$addr,
         '--p2p.no-discovery','--p2p.local-ip','127.0.0.1','--p2p.host-ip','127.0.0.1',
         '--p2p.tcp-port',"$(64000+$i)",'--p2p.udp-port',"$(65000+$i)",
         '--p2p.min-sync-peers','0','--p2p.max-peers',"$MaxPeers",
         '--authrpc','--authrpc.addr','127.0.0.1','--authrpc.port',"$(8651+$i)",'--authrpc.jwtsecret',$env:CHAOS_JWT,
         '--http','--http.addr','127.0.0.1','--http.port',"$(20112+$i)",'--http.api','eth,web3,net,admin',
         '--mobileverify.http',"127.0.0.1:$((22012+$i))",
         '--pprof','--pprof.port',"$((6190+$i))") + $peers
  if ($TxGenMax -gt 0 -and $i -eq 0) { $a += @('--dev.txgen','--dev.txgen.max',"$TxGenMax",'--dev.txgen.key',$faucetKey) }

  $p = Start-Process -FilePath $env:CHAOS_BIN -ArgumentList $a `
        -RedirectStandardOutput "$d\run.log" -RedirectStandardError "$d\run.err" `
        -WindowStyle Hidden -PassThru
  $role = if ($i -le 6) { 'validator' } else { 'observer' }
  Write-Host "node$i ($role) PID $($p.Id) http :$((20112+$i)) authrpc :$((8651+$i))"
}
