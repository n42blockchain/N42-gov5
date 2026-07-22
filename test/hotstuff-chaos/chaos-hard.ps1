# Aggressive sustained-churn probe. Hammers the fleet with rapid, scattered crashes
# for ~90s (validators restart at scattered views / lose the in-memory config), then
# STOPS all churn, restores every node, and checks whether the chain RECOVERS. A
# stable, fully-up fleet that stays stuck is a liveness failure.
#
# This probe found the durability bug fixed in commit 0f98b96b: after a
# reconfiguration, a crashed+restarted node reverted to the genesis validator set and
# could not verify the reconfigured set's QCs, stalling the chain. Run it AFTER a
# reconfiguration (e.g. after chaos.ps1, or add a validator first) to exercise that
# path. Logs to chaos-hard-log.txt.
#
#   pwsh test/hotstuff-chaos/chaos-hard.ps1
. "$PSScriptRoot\lib.ps1"
$LOG = "$PSScriptRoot\chaos-hard-log.txt"; "" | Out-File $LOG -Encoding utf8
function Log($m){ $t=(Get-Date).ToString('HH:mm:ss'); ("$t  $m")|Out-File $LOG -Append -Encoding utf8; Write-Host "$t  $m" }

# deterministic victim cycle over the 7 validators (no Get-Random dependence)
$seq = 0
function Next-Victim { $script:seq++; return ($script:seq * 3) % 7 }

Log "=== HARD CHURN START baseline head=$(Get-NodeHeight 0) ==="
# ~90s of rapid scattered crashes; keep <=2 validators down at a time so it hovers
# around the quorum edge (5 of 7) and forces frequent view changes + restarts.
$down = @(); $end = (Get-Date).AddSeconds(90)
while((Get-Date) -lt $end){
  if($down.Count -ge 2){ $r=$down[0]; $down=@($down[1..($down.Count-1)]); Start-ChaosNode $r }
  $v = Next-Victim; while($down -contains $v){ $v = Next-Victim }
  Stop-ChaosNode $v; $down += $v
  Start-Sleep -Seconds 4
}
Log "  churn ended (up=$(@(Get-UpNodes).Count)); restoring ALL validators"
foreach($n in 0..6){ if(-not (Test-NodeUp $n)){ Start-ChaosNode $n } }
$dl=(Get-Date).AddSeconds(40); while((Get-Date) -lt $dl){ if(@(Get-UpNodes).Count -eq 11){ break }; Start-Sleep -Seconds 2 }
Log "  all-up=$(@(Get-UpNodes).Count)/11; checking recovery (up to 180s)"

# Recovery: head must advance >=3 while all nodes are up.
$startH = Get-NodeHeight 0; $rec = $false; $dl = (Get-Date).AddSeconds(180)
while((Get-Date) -lt $dl){
  Start-Sleep -Seconds 5
  $h = Get-NodeHeight 0
  if($h -ge $startH+3){ $rec=$true; Log "  RECOVERED: head $startH -> $h (chain resumed)"; break }
}
if(-not $rec){
  $views = @(); foreach($n in 0..6){
    $m = Select-String -Path "$($env:CHAOS_DATAROOT)$n\run.log" -Pattern 'view timed out.*view=(\d+)' -ErrorAction SilentlyContinue | Select-Object -Last 1
    $vv = if($m -and $m.Line -match 'view=(\d+)'){$Matches[1]}else{'?'}; $views += "n${n}:v$vv"
  }
  Log "  !! NOT-RECOVERED: head stuck at $(Get-NodeHeight 0); validator views: $($views -join ' ')"
}
Log "=== HARD CHURN DONE head=$(Get-NodeHeight 0) up=$(@(Get-UpNodes).Count)/11 ==="
