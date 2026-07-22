# One-time setup for the HotStuff chaos suite: builds the test binary, generates the
# deterministic validator pool (11 keys) and a JWT secret. Re-runnable (idempotent).
#
#   pwsh test/hotstuff-chaos/setup.ps1
#
# The validator pool contains BLS PRIVATE KEYS and is written to test/hotstuff-chaos/
# validators.md — it is deterministic (seed below) and .gitignore'd; never commit it.
. "$PSScriptRoot\lib.ps1"
$ErrorActionPreference = "Stop"
$repo = Split-Path $PSScriptRoot -Parent | Split-Path -Parent
$seed = "0x4242424242424242424242424242424242424242424242424242424242424242"

Push-Location $repo
try {
  Write-Host "building test binary + tools ..."
  & go build -tags "nosqlite,noboltdb" -o build\bin\n42-epoch-test.exe .\cmd\n42
  if ($LASTEXITCODE -ne 0) { throw "build n42-epoch-test failed" }
  & go build -o build\bin\n42-blspool.exe .\cmd\n42-blspool
  if ($LASTEXITCODE -ne 0) { throw "build n42-blspool failed" }

  Write-Host "generating 11-validator pool -> $($env:CHAOS_POOL)"
  & build\bin\n42-blspool.exe -count 11 -seed $seed -out $env:CHAOS_POOL | Out-Null

  if (-not (Test-Path $env:CHAOS_JWT)) {
    Write-Host "generating JWT secret -> $($env:CHAOS_JWT)"
    $bytes = New-Object byte[] 32
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    "0x" + ([System.BitConverter]::ToString($bytes) -replace '-','').ToLower() | Set-Content $env:CHAOS_JWT -NoNewline
  }
  $vmap = Get-ValidatorMap
  Write-Host "setup done: $($vmap.Count) validators; binary=$($env:CHAOS_BIN); datadir root=$($env:CHAOS_DATAROOT)"
  Write-Host "next: pwsh test/hotstuff-chaos/deploy-fleet.ps1 -First 0 -Last 6  (then -First 7 -Last 10 for observers)"
} finally { Pop-Location }
