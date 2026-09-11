#!/usr/bin/env bash
# Summarise "blockimport phases" lines from a replay log: mean per phase over
# blocks with txs >= MIN (default 100000). Usage: replay-phases.sh <log> [MIN]
L=$1; MIN=${2:-100000}
grep -o 'blockimport phases.*' "$L" | sed 's/msg="//' | awk -v min="$MIN" '
function ms(v,  n) { # go duration -> ms
  if (v ~ /ms$/) { sub(/ms$/,"",v); return v+0 }
  if (v ~ /µs$/ || v ~ /us$/) { sub(/[µu]s$/,"",v); return v/1000 }
  if (v ~ /ns$/) { sub(/ns$/,"",v); return v/1e6 }
  if (v ~ /m[0-9.]+s$/) { split(v,a,"m"); sub(/s$/,"",a[2]); return a[1]*60000+a[2]*1000 }
  if (v ~ /s$/) { sub(/s$/,"",v); return v*1000 }
  return v+0
}
{
  delete f; for (i=1;i<=NF;i++) if (split($i,kv,"=")==2) f[kv[1]]=kv[2]
  if (f["txs"]+0 < min) next
  n++; for (k in f) if (k!="n" && k!="txs" && k!="gas") sum[k]+=ms(f[k])
}
END {
  if (!n) { print "no blocks >= " min " txs"; exit }
  printf "%d full blocks; mean ms:", n
  split("hdr body align recov prep exec root proc valid write total", order, " ")
  for (i=1;i<=11;i++) { k=order[i]; if (k in sum) printf " %s=%.0f", k, sum[k]/n }
  print ""
}'
