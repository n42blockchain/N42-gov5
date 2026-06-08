<#
.SYNOPSIS
  EIP-4444 cold-history offload for the BLS-resealed chain.

.DESCRIPTION
  Exports the old (cold) portion of the chain to EraE archive segments — the
  distributable, content-addressed cold history — leaving recent blocks "hot"
  in the MDBX chaindata. Mirrors Ethereum's EIP-4444 history expiry: full nodes
  keep ~1 year of recent history hot and serve older history from the EraE
  segments (which can be seeded over BitTorrent, see n42-bls-publish.ps1).

  Run this AFTER replay-v2 --bls-reseal finishes (the target datadir must be
  complete). The export is read-only; it does NOT prune the MDBX — pruning the
  offloaded blocks is a separate, deliberate step (see -PruneHint output).

.PARAMETER Datadir   Converted chain datadir (replay-v2 --target).
.PARAMETER EraOut    Output directory for .era segments.
.PARAMETER Head      Chain head block (replay-v2 'to=' value).
.PARAMETER HotBlocks Recent blocks to keep hot (default ~1y @ 12s = 2,629,800).
.PARAMETER SegmentSize Blocks per .era file (default 8192, matches replay-v2).
.PARAMETER N42       Path to the n42 binary.

.EXAMPLE
  pwsh scripts/n42-bls-cold-offload.ps1 -Head 12679790
#>
param(
  [string]$Datadir     = "D:\mainnet-bls",
  [string]$EraOut      = "D:\mainnet-bls-era",
  [uint64]$Head        = 12679790,
  [uint64]$HotBlocks   = 2629800,
  [uint64]$SegmentSize = 8192,
  [string]$N42         = "C:\N42\N42-gov5\build\bin\n42.exe"
)

$ErrorActionPreference = "Stop"

if (-not (Test-Path "$Datadir\chaindata")) { throw "datadir not found / not converted yet: $Datadir" }
if (-not (Test-Path $N42))                  { throw "n42 binary not found: $N42" }

# Cold range = [0, Head - HotBlocks). Older than ~1 year is offloaded.
$coldTo = if ($Head -gt $HotBlocks) { $Head - $HotBlocks } else { 0 }
if ($coldTo -eq 0) { Write-Host "Chain shorter than hot window ($HotBlocks); nothing to offload."; exit 0 }

New-Item -ItemType Directory -Force $EraOut | Out-Null
Write-Host "=== EIP-4444 cold offload ==="
Write-Host "  datadir   : $Datadir"
Write-Host "  head      : $Head   hot: last $HotBlocks   cold: [0, $coldTo)"
Write-Host "  era out   : $EraOut  (segment $SegmentSize blocks)"

$sw = [System.Diagnostics.Stopwatch]::StartNew()
& $N42 era export --source $Datadir --output $EraOut --from 0 --to $coldTo --segment-size $SegmentSize
if ($LASTEXITCODE -ne 0) { throw "era export failed (exit $LASTEXITCODE)" }
$sw.Stop()

$eraFiles = Get-ChildItem $EraOut -Filter *.era -ErrorAction SilentlyContinue
$eraGB = if ($eraFiles) { [math]::Round(($eraFiles | Measure-Object Length -Sum).Sum / 1GB, 2) } else { 0 }
Write-Host "=== done in $($sw.Elapsed) — $($eraFiles.Count) .era files, $eraGB GB ==="
Write-Host ""
Write-Host "PruneHint: blocks [0,$coldTo) are now archived in $EraOut and can be"
Write-Host "  pruned from $Datadir\chaindata to reclaim space. Restore any range with:"
Write-Host "  $N42 era import --input $EraOut --target <datadir>"
