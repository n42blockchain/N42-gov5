# Chaos scenarios for the HotStuff dynamic-reconfiguration fleet. Runs failure
# scenarios sequentially; after each, verifies the chain RECOVERS — resumes
# producing (head advances) AND all UP nodes agree on one hash. Aborts (leaving the
# fleet frozen for diagnosis) on the first non-recovery. Logs to chaos-log.txt.
#
#   pwsh test/hotstuff-chaos/chaos.ps1
#
# Prereqs: setup.ps1 has run, and the 11-node fleet is up and producing
# (deploy-fleet.ps1 -First 0 -Last 6 ; deploy-fleet.ps1 -First 7 -Last 10).
. "$PSScriptRoot\lib.ps1"
$LOG = "$PSScriptRoot\chaos-log.txt"; "" | Out-File $LOG -Encoding utf8
function Log($m){ $t=(Get-Date).ToString('HH:mm:ss'); ("$t  $m")|Out-File $LOG -Append -Encoding utf8; Write-Host "$t  $m" }
$vmap = Get-ValidatorMap
$AllPorts = @(8651..8661)

# Recovery = liveness (head advances >=2) AND safety (all UP nodes one hash).
function Wait-Recover($sec, $label){
  $dl=(Get-Date).AddSeconds($sec); $startMax=-1
  while((Get-Date) -lt $dl){
    Start-Sleep -Seconds 3
    $up=@(Get-UpNodes); if($up.Count -eq 0){ continue }
    $hs=@(); foreach($n in $up){ $hs+=Get-NodeHeight $n }
    $vals=@($hs | Where-Object {$_ -ge 0}); if($vals.Count -eq 0){ continue }
    $mx=($vals|Measure-Object -Maximum).Maximum; $mn=($vals|Measure-Object -Minimum).Minimum
    if($startMax -lt 0){ $startMax=$mx }
    if(($mx-$mn) -le 2){
      $hx='0x'+([Convert]::ToString([int64]$mn,16))
      $gb='{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["'+$hx+'",false],"id":1}'
      $uh=@(); foreach($n in $up){ $p=20112+$n; try{$uh+=(Invoke-RestMethod -Uri "http://127.0.0.1:$p" -Method Post -ContentType 'application/json' -Body $gb -TimeoutSec 3).result.hash}catch{} }
      $uniq=($uh|Select-Object -Unique).Count
      if($uniq -gt 1){ return @{ok=$false; reason="FORK ($uniq hashes at block $mn)"; head=$mx} }
      if($uniq -eq 1 -and $mx -ge $startMax+2){ return @{ok=$true; head=$mx; up=$up.Count} }
    }
  }
  return @{ok=$false; reason="NOT-RECOVERED (head ~$startMax, up=$(@(Get-UpNodes).Count), not producing/converging)"; head=$startMax}
}
function Assert-Recover($sec,$label){
  $r=Wait-Recover $sec $label
  if($r.ok){ Log "  RECOVERED ${label}: head=$($r.head) up=$($r.up) all converged" }
  else { Log "  xx INSTABILITY in ${label}: $($r.reason)"; Log "=== ABORT at ${label} — fleet frozen for diagnosis ==="; exit 1 }
}
function Reconfig($action,$node){
  $m = if($action -eq 'add'){'admin_proposeAddValidator'}else{'admin_proposeRemoveValidator'}
  $pm = if($action -eq 'add'){'["'+$vmap[$node].addr+'","'+$vmap[$node].pub+'"]'}else{'["'+$vmap[$node].addr+'"]'}
  foreach($p in $AllPorts){ Invoke-AuthRpc $p $m $pm | Out-Null }
}

Log "=== CHAOS START (7 validators 0-6, 4 observers 7-10) baseline head=$(Get-NodeHeight 0) ==="

# S1: single validator crash + restart (quorum 5/6 holds -> chain keeps producing)
Log "S1: crash validator node3 (6/7 up >= quorum 5, chain should keep producing)"
Stop-ChaosNode 3; Start-Sleep -Seconds 2
Assert-Recover 60 "S1-during-crash"
Log "  restart node3"; Start-ChaosNode 3
Assert-Recover 90 "S1-after-restart"

# S2: quorum LOSS then restore (kill f+1=3 -> below quorum -> halt -> recover)
Log "S2: crash 3 validators (2,4,6) -> below quorum 5; chain SHOULD halt, then restore"
Stop-ChaosNode 2; Stop-ChaosNode 4; Stop-ChaosNode 6; Start-Sleep -Seconds 3
$a=Get-NodeHeight 0; Start-Sleep -Seconds 15; $b=Get-NodeHeight 0
Log "  4/7 up: head $a -> $b ($(if($b -le $a+1){'HALTED as expected (no quorum)'}else{'still producing?!'}))"
Log "  restore node2,4,6"; Start-ChaosNode 2; Start-ChaosNode 4; Start-ChaosNode 6
Assert-Recover 120 "S2-recovery-after-quorum-loss"

# S3: majority crash (kill 4) + full restart
Log "S3: crash 4 validators (1,3,5,6) then restart all"
Stop-ChaosNode 1; Stop-ChaosNode 3; Stop-ChaosNode 5; Stop-ChaosNode 6; Start-Sleep -Seconds 10
Log "  restarting"; Start-ChaosNode 1; Start-ChaosNode 3; Start-ChaosNode 5; Start-ChaosNode 6
Assert-Recover 120 "S3-majority-crash-recovery"

# S4: flapping node (rapid crash/restart x4) — flaky-network / partition-flap sim
Log "S4: flap node5 (rapid crash+restart x4)"
for($i=1;$i -le 4;$i++){ Stop-ChaosNode 5; Start-Sleep -Seconds 4; Start-ChaosNode 5; Start-Sleep -Seconds 8 }
Assert-Recover 90 "S4-flapping-recovery"

# S5: reconfiguration churn while a validator is crashed
Log "S5: reconfig add node7 while node4 is crashed (then restore node4)"
Stop-ChaosNode 4
Reconfig 'add' 7
Start-Sleep -Seconds 5; Start-ChaosNode 4
Assert-Recover 120 "S5-reconfig-under-crash"

Log "=== CHAOS DONE: all scenarios recovered. final head=$(Get-NodeHeight 0) up=$(@(Get-UpNodes).Count)/11 ==="
