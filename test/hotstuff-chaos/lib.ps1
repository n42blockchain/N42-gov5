# Shared helpers for the HotStuff dynamic-reconfiguration chaos suite.
# Dot-source this from the other scripts:  . "$PSScriptRoot\lib.ps1"
#
# Fleet layout (localhost, isolated qs_epoch_test chain, chainId 95):
#   node i:  http 20112+i   authrpc 8651+i   p2p tcp 64000+i   udp 65000+i
#   nodes 0..6 are genesis validators; 7..10 join later via reconfiguration.

$ErrorActionPreference = "Continue"

# ---- config (override via env before dot-sourcing, or edit here) ----
if (-not $env:CHAOS_DATAROOT) { $env:CHAOS_DATAROOT = "E:\qs-test-node" }   # per-node datadir prefix
if (-not $env:CHAOS_BIN)      { $env:CHAOS_BIN      = (Join-Path (Split-Path $PSScriptRoot -Parent | Split-Path -Parent) "build\bin\n42-epoch-test.exe") }
if (-not $env:CHAOS_JWT)      { $env:CHAOS_JWT      = "$PSScriptRoot\jwt.hex" }
if (-not $env:CHAOS_POOL)     { $env:CHAOS_POOL     = "$PSScriptRoot\validators.md" }
$ChaosProc = "n42-epoch-test"   # process name (without .exe) of the test binary

# Fixed network keys (c1..cb repeated 32x) and their libp2p peer IDs (from cmd/peerid).
$Global:ChaosPeerIds = @(
 "16Uiu2HAmBuubN9pAPeZ5xu1HtZ4afzRiC7ajwXNvEE18UoYMM6Se","16Uiu2HAmB7SMNsTuan2Ydr2A3TiYJ8jjGXeuUFj3cNNL1CdQgF2i",
 "16Uiu2HAkyyKcnrur2T3xGspjDYwed2ERPmNegFaXtWZL1TmVoet2","16Uiu2HAmLsaiqMmJ5ZXfLF15L7cZVnk6y7YYQpje74hhX9H39tKv",
 "16Uiu2HAmCWXYo8xqXcMA3x35dQoBuppqGn8yT8bQdwA4BEWRwfmT","16Uiu2HAmRFZy9EuRFEBDExzht7F8QZTPtcNSup8gWgz4qy3kqnKb",
 "16Uiu2HAm6xc7GRfbHJsqDNkbgjf7wVbpXfxAngmG88aUb56sbcqK","16Uiu2HAm4pgRo77WwgiHtK6uqu53DEoRFSakwJTPYzw62zmVMyAo",
 "16Uiu2HAm2e8EBFEUXRBaKfYmMPYMQwWWUHaSjDit4SRFFXUwzXQf","16Uiu2HAmBRypmoKZXWyxTEVSZmeuRPYcpeB2WZFooiPs8ELy9ASU",
 "16Uiu2HAkxfAKB63JnGQenGyP6KSfxmhmdH4sbhx57nV4fgcbN1B5")
$Global:ChaosNetKeyBytes = @('c1','c2','c3','c4','c5','c6','c7','c8','c9','ca','cb')

# ---- validator pool (index -> @{addr; pub}) ----
function Get-ValidatorMap {
  $m = @{}
  foreach ($l in Get-Content $env:CHAOS_POOL) {
    if ($l -match '^(\d+),(0x[0-9a-fA-F]+),([0-9a-fA-F]+),([0-9a-fA-F]+)') {
      $m[[int]$Matches[1]] = @{ addr = $Matches[2]; pub = '0x' + $Matches[3]; priv = $Matches[4] }
    }
  }
  return $m
}

# ---- JWT (HS256) for the authenticated reconfig RPC ----
function Get-JwtKey {
  $hex = ((Get-Content $env:CHAOS_JWT -Raw).Trim()) -replace '^0x',''
  $k = [byte[]]::new($hex.Length/2)
  for ($i=0; $i -lt $hex.Length; $i+=2) { $k[$i/2] = [Convert]::ToByte($hex.Substring($i,2),16) }
  return $k
}
function New-JwtToken {
  $enc = [System.Text.Encoding]::UTF8
  function b64([byte[]]$b){ [Convert]::ToBase64String($b).TrimEnd('=').Replace('+','-').Replace('/','_') }
  $h = b64 ($enc.GetBytes('{"alg":"HS256","typ":"JWT"}'))
  $iat = [int][double]::Parse((Get-Date -UFormat %s))
  $p = b64 ($enc.GetBytes('{"iat":'+$iat+'}'))
  $mac = [System.Security.Cryptography.HMACSHA256]::new((Get-JwtKey))
  $s = b64 ($mac.ComputeHash($enc.GetBytes("$h.$p")))
  "$h.$p.$s"
}
# authenticated JSON-RPC call to a node's authrpc port
function Invoke-AuthRpc($port, $method, $paramsJson) {
  try {
    (Invoke-WebRequest -Uri "http://127.0.0.1:$port" -Method Post -ContentType 'application/json' `
      -Headers @{Authorization="Bearer $(New-JwtToken)"} `
      -Body ('{"jsonrpc":"2.0","method":"'+$method+'","params":'+$paramsJson+',"id":1}') `
      -UseBasicParsing -TimeoutSec 6).Content
  } catch { "ERR $($_.Exception.Message)" }
}

# ---- node control ----
$Global:ChaosBody = '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
function Get-NodePid($n) {
  (Get-CimInstance Win32_Process -Filter "Name='$ChaosProc.exe'" |
    Where-Object { $_.CommandLine -match "$([regex]::Escape($env:CHAOS_DATAROOT))$n\b" } |
    Select-Object -First 1).ProcessId
}
function Test-NodeUp($n) { [bool](Get-NodePid $n) }
function Get-UpNodes { 0..10 | Where-Object { Test-NodeUp $_ } }
function Get-NodeHeight($n) {
  $port = 20112 + $n
  try { [Convert]::ToInt64((Invoke-RestMethod -Uri "http://127.0.0.1:$port" -Method Post -ContentType 'application/json' -Body $Global:ChaosBody -TimeoutSec 3).result,16) }
  catch { -1 }
}
# HARD-kill a node (simulates a crash / network loss). NOT named Kill: that is a
# Stop-Process alias and aliases outrank functions in PowerShell command resolution.
function Stop-ChaosNode($n) {
  $procId = Get-NodePid $n
  if ($procId) { Stop-Process -Id ([int]$procId) -Force -ErrorAction SilentlyContinue }
}
# launch (or restart) a single node from its datadir
function Start-ChaosNode($n, [int]$txGenMax = 0) {
  Start-Process pwsh -ArgumentList '-NoProfile','-File',"$PSScriptRoot\deploy-fleet.ps1",'-First',"$n",'-Last',"$n",'-TxGenMax',"$txGenMax" -WindowStyle Hidden
}
