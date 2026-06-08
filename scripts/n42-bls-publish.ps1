<#
.SYNOPSIS
  Build and package a distributable snapshot of the BLS-resealed chain.

.DESCRIPTION
  Produces the artifacts a new node downloads to bootstrap the BLS-resealed
  chain without replaying it:
    1. State snapshot  — reth-snapshot-export over the N42 PlainState
                         (Account/Storage), for fast state bootstrap.
    2. Cold history    — EraE segments (run n42-bls-cold-offload.ps1 first, or
                         pass -RunColdOffload to do it here).
    3. Tier manifest   — n42-eth-manifest (sha256 of every artifact).
    4. Torrent         — n42-eth-torrent (BitTorrent for 1-of-N distribution).

  Run AFTER replay-v2 --bls-reseal finishes.

.PARAMETER Datadir   Converted chain datadir (replay-v2 --target).
.PARAMETER Head      Chain head block (replay-v2 'to=' value).
.PARAMETER StageRoot Archive root assembled for manifest/torrent (snapshot/ + era/).
.PARAMETER Network   Network name embedded in the manifest/torrent.
.PARAMETER Trackers  Comma-separated BitTorrent announce URLs.
.PARAMETER Webseeds  Comma-separated HTTP webseed bases (BEP19).
.PARAMETER RunColdOffload  Also run the EraE cold offload first.

.EXAMPLE
  pwsh scripts/n42-bls-publish.ps1 -Head 12679790 `
       -Trackers "udp://tracker.opentrackr.org:1337/announce" `
       -Webseeds "https://dist.n42.io/mainnet-bls/"
#>
param(
  [string]$Datadir   = "D:\mainnet-bls",
  [uint64]$Head      = 12679790,
  [string]$StageRoot = "D:\mainnet-bls-dist",
  [string]$Network   = "mainnet-bls",
  [string]$Trackers  = "",
  [string]$Webseeds  = "",
  [switch]$RunColdOffload,
  [string]$Bin       = "C:\N42\N42-gov5\build\bin"
)

$ErrorActionPreference = "Stop"
$snapExp  = Join-Path $Bin "reth-snapshot-export.exe"
$manifest = Join-Path $Bin "n42-eth-manifest.exe"
$torrent  = Join-Path $Bin "n42-eth-torrent.exe"
$snapVer  = Join-Path $Bin "n42-snapshot-verify.exe"

foreach ($p in @($snapExp,$manifest,$torrent)) { if (-not (Test-Path $p)) { throw "missing tool: $p" } }
if (-not (Test-Path "$Datadir\chaindata")) { throw "datadir not converted yet: $Datadir" }

$snapDir = Join-Path $StageRoot "snapshot"
$eraDir  = Join-Path $StageRoot "era"
New-Item -ItemType Directory -Force $snapDir,$eraDir | Out-Null

# --- 1. State snapshot from N42 PlainState (Account/Storage) ---
Write-Host "=== [1/4] state snapshot -> $snapDir ==="
& $snapExp --db "$Datadir\chaindata" --n42 --table both --out $snapDir --end-block $Head
if ($LASTEXITCODE -ne 0) { throw "reth-snapshot-export failed ($LASTEXITCODE)" }

# Optional sanity check of the exported snapshot against the live DB.
if (Test-Path $snapVer) {
  Write-Host "  verifying snapshot (sample)..."
  & $snapVer --reth "$Datadir\chaindata" --snap $snapDir --acc-prefix "accounts.0-$Head" --sto-prefix "storage.0-$Head" --n 20000 2>$null
}

# --- 2. Cold history (EraE) ---
if ($RunColdOffload) {
  Write-Host "=== [2/4] cold offload (EraE) -> $eraDir ==="
  & (Join-Path $PSScriptRoot "n42-bls-cold-offload.ps1") -Datadir $Datadir -EraOut $eraDir -Head $Head
} else {
  Write-Host "=== [2/4] cold offload skipped (run n42-bls-cold-offload.ps1 -EraOut $eraDir, or pass -RunColdOffload) ==="
}

# --- 3. Tier manifest (sha256 of every artifact) ---
$manOut = Join-Path $StageRoot "manifest-full.json"
Write-Host "=== [3/4] manifest -> $manOut ==="
& $manifest --datadir $StageRoot --mode full --network $Network --height $Head --out $manOut
if ($LASTEXITCODE -ne 0) { throw "n42-eth-manifest failed ($LASTEXITCODE)" }

# --- 4. Torrent for 1-of-N distribution ---
Write-Host "=== [4/4] torrent ==="
$torArgs = @("--datadir", $StageRoot, "--manifest", $manOut, "--update-manifest")
if ($Trackers) { $torArgs += @("--tracker", $Trackers) }
if ($Webseeds) { $torArgs += @("--webseed", $Webseeds) }
& $torrent @torArgs
if ($LASTEXITCODE -ne 0) { throw "n42-eth-torrent failed ($LASTEXITCODE)" }

Write-Host ""
Write-Host "=== publish complete: $StageRoot ==="
Get-ChildItem $StageRoot -Recurse -File | Group-Object { $_.Directory.Name } |
  ForEach-Object { "{0,-12} {1,5} files {2,8:N1} GB" -f $_.Name, $_.Count, (($_.Group | Measure-Object Length -Sum).Sum/1GB) }
