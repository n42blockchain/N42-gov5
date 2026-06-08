<#
.SYNOPSIS
  End-to-end validation of the BLS-resealed chain using the REAL converted data.

.DESCRIPTION
  Starts n42-consensus-rest read-only against the converted datadir (works even
  while replay-v2 is still running — MDBX allows concurrent readers), then for a
  set of sample blocks across the chain it:
    - fetches the consensus evidence (BLS QC),
    - fetches the committee (members + who signed),
    - CRYPTOGRAPHICALLY VERIFIES the QC (/verify re-derives the committee,
      aggregates signer pubkeys and checks the aggregate signature),
    - fetches the pool sizing.
  A non-zero exit code means at least one sampled block's QC failed to verify.

.PARAMETER Datadir  Converted chain datadir (replay-v2 --target).
.PARAMETER Seed     32-byte hex master seed used to generate the voter pool.
.PARAMETER Port     Local port for the REST server.

.EXAMPLE
  pwsh scripts/n42-bls-e2e-verify.ps1
#>
param(
  [string]$Datadir = "D:\mainnet-bls",
  [string]$Seed    = "0x03c75de6b57f3563919956d11700f1d0c932e3c157506b23ed2c40d3ca47bb2f",
  [int]$Port       = 8557,
  [string]$RestBin = "C:\N42\n42-consensus-rest.exe"
)

$ErrorActionPreference = "Stop"
if (-not (Test-Path $RestBin))            { throw "REST binary not found: $RestBin (go build ./cmd/n42-consensus-rest)" }
if (-not (Test-Path "$Datadir\chaindata")){ throw "datadir not found: $Datadir" }
$base = "http://127.0.0.1:$Port/n42/consensus/v1"

Write-Host "=== starting n42-consensus-rest (read-only) on :$Port ==="
$errLog = Join-Path $env:TEMP "n42-rest-$Port.err"
$proc = Start-Process -FilePath $RestBin `
  -ArgumentList "--datadir",$Datadir,"--addr",":$Port","--seed",$Seed `
  -PassThru -NoNewWindow -RedirectStandardError $errLog

try {
  # Wait for /health.
  $up = $false
  foreach ($i in 1..30) {
    try { if ((Invoke-RestMethod "http://127.0.0.1:$Port/health" -TimeoutSec 2).status -eq "ok") { $up = $true; break } } catch {}
    Start-Sleep -Milliseconds 700
  }
  if (-not $up) { Write-Host (Get-Content $errLog -Tail 10 -ErrorAction SilentlyContinue); throw "server did not come up" }

  # Resolve current converted head.
  $head = [uint64](Invoke-RestMethod "$base/block/latest/evidence" -TimeoutSec 10).blockNumber
  Write-Host "converted head = $head"

  # Sample blocks across the chain (skip any > head).
  $candidates = @(1, 100, 1000, 50000, 250000, [uint64]($head/4), [uint64]($head/2), [uint64]($head*3/4), $head) |
                Where-Object { $_ -ge 1 -and $_ -le $head } | Sort-Object -Unique
  Write-Host "sampling $($candidates.Count) blocks: $($candidates -join ', ')`n"

  $pass = 0; $fail = 0
  $fmt = "{0,-10} {1,-8} {2,-8} {3,-10} {4}"
  Write-Host ($fmt -f "block","commit","signers","active","verify")
  Write-Host ("-"*60)
  foreach ($b in $candidates) {
    $ev  = Invoke-RestMethod "$base/block/$b/evidence"  -TimeoutSec 30
    $com = Invoke-RestMethod "$base/block/$b/committee" -TimeoutSec 30
    $ver = Invoke-RestMethod "$base/block/$b/verify"    -TimeoutSec 30
    $pool= Invoke-RestMethod "$base/pool/$b"  -TimeoutSec 30
    $ok = [bool]$ver.valid
    if ($ok) { $pass++ } else { $fail++ }
    Write-Host ($fmt -f $b, $com.committeeSize, "$($ver.signerCount)", $pool.activePoolSize, $(if($ok){"OK"}else{"FAIL: $($ver.reason)"}))
    # sanity: committee size and signer count should match the configured committee
    if ($com.committeeSize -ne $ver.committeeSize) { Write-Host "  WARN committeeSize mismatch"; }
  }

  # Spot-check a validator identity + duties around a sampled block.
  $val = Invoke-RestMethod "$base/validator/0" -TimeoutSec 30
  Write-Host "`nvalidator[0] pubkey=$($val.pubkey.Substring(0,18))... address=$($val.address)"

  Write-Host "`n=== e2e result: $pass verified, $fail failed (head $head) ==="
  if ($fail -gt 0) { exit 1 }
}
finally {
  if ($proc -and -not $proc.HasExited) { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue }
}
