package consensusrest

import "net/http"

// ExplorerHandler serves a tiny self-contained HTML page that visualises a
// block's mobile-BLS consensus: the QC verification status, the 512-member
// committee participation grid, and the voter-pool growth. It calls the REST
// routes on the same origin, so it works whether served standalone or mounted
// on the node's HTTP port.
func ExplorerHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(explorerHTML))
	})
}

const explorerHTML = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>N42 Mobile-BLS Consensus Explorer</title>
<style>
 :root{--bg:#0d1117;--panel:#161b22;--line:#30363d;--fg:#c9d1d9;--mut:#8b949e;--ok:#3fb950;--bad:#f85149;--acc:#58a6ff}
 *{box-sizing:border-box} body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.5 -apple-system,Segoe UI,Roboto,monospace}
 header{padding:18px 24px;border-bottom:1px solid var(--line);display:flex;align-items:center;gap:16px;flex-wrap:wrap}
 h1{font-size:18px;margin:0;color:#fff} .sub{color:var(--mut);font-size:12px}
 .bar{margin-left:auto;display:flex;gap:8px}
 input,button{background:var(--panel);color:var(--fg);border:1px solid var(--line);border-radius:6px;padding:7px 12px;font:inherit}
 button{cursor:pointer;color:var(--acc)} button:hover{border-color:var(--acc)}
 main{padding:24px;display:grid;gap:16px;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));max-width:1200px}
 .card{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:16px}
 .card h2{margin:0 0 12px;font-size:13px;color:var(--mut);text-transform:uppercase;letter-spacing:.5px}
 .kv{display:flex;justify-content:space-between;gap:12px;padding:4px 0;border-bottom:1px dashed #21262d}
 .kv b{color:#fff;font-weight:600} .mono{font-family:ui-monospace,monospace;word-break:break-all}
 .verdict{font-size:34px;font-weight:700;text-align:center;padding:8px}
 .ok{color:var(--ok)} .bad{color:var(--bad)}
 .grid{display:grid;grid-template-columns:repeat(32,1fr);gap:2px;margin-top:8px}
 .cell{aspect-ratio:1;border-radius:2px;background:#21262d} .cell.s{background:var(--ok)} .cell.u{background:var(--bad)}
 .poolbar{height:14px;border-radius:7px;background:#21262d;overflow:hidden;margin-top:8px}
 .poolfill{height:100%;background:linear-gradient(90deg,#1f6feb,#58a6ff)}
 .full{grid-column:1/-1} .err{color:var(--bad)}
</style></head><body>
<header>
 <h1>N42 Mobile-BLS Consensus</h1><span class="sub">explorer</span>
 <div class="bar"><input id="blk" value="latest" size="12" placeholder="block / latest"><button onclick="load()">Load</button></div>
</header>
<main id="out"><div class="card full">Loading <code>latest</code>…</div></main>
<script>
const B="/n42/consensus/v1";
const $=(h)=>{const d=document.createElement('div');d.innerHTML=h;return d.firstElementChild};
async function j(u){const r=await fetch(u);if(!r.ok)throw new Error((await r.json()).error||r.status);return r.json()}
function kv(k,v,mono){return '<div class="kv"><span>'+k+'</span><b'+(mono?' class="mono"':'')+'>'+v+'</b></div>'}
function short(s,n){n=n||20;return s&&s.length>n?s.slice(0,n)+'…'+s.slice(-6):s}
async function load(){
 const id=document.getElementById('blk').value.trim()||'latest';
 const out=document.getElementById('out');out.innerHTML='<div class="card full">Loading <code>'+id+'</code>…</div>';
 try{
  const ev=await j(B+'/block/'+id+'/evidence');
  const n=ev.blockNumber;
  const [com,ver,pool]=await Promise.all([j(B+'/block/'+n+'/committee'),j(B+'/block/'+n+'/verify'),j(B+'/pool/'+n)]);
  const cells=com.members.map(m=>'<div class="cell '+(m.signed?'s':'u')+'" title="#'+m.index+(m.signed?' signed':' missing')+'"></div>').join('');
  const poolPct=(pool.activePoolSize/pool.totalPoolSize*100).toFixed(1);
  out.innerHTML=
   '<div class="card"><h2>Block</h2>'+kv('number',n)+kv('view',ev.view)+kv('hash',short(ev.blockHash),1)
     +kv('signers',ver.signerCount+' / '+ver.committeeSize)+'</div>'
  +'<div class="card"><h2>QC Verification</h2><div class="verdict '+(ver.valid?'ok':'bad')+'">'+(ver.valid?'✓ VALID':'✗ INVALID')+'</div>'
     +kv('aggregate sig',short(ev.aggregateSignature),1)+(ver.reason?kv('reason',ver.reason):'')+'</div>'
  +'<div class="card"><h2>Voter Pool @ block</h2>'+kv('active',pool.activePoolSize.toLocaleString())
     +kv('total',pool.totalPoolSize.toLocaleString())+kv('committee',pool.committeeSize)+kv('fully ramped',pool.fullyRamped)
     +'<div class="poolbar"><div class="poolfill" style="width:'+poolPct+'%"></div></div></div>'
  +'<div class="card full"><h2>Committee participation ('+com.committeeSize+' members · '+com.signedCount+' signed)</h2><div class="grid">'+cells+'</div></div>'
  +(ev.hasMobile?'<div class="card full"><h2>Mobile parallel-verification</h2>'+kv('receipts root',short(ev.mobileReceiptsRoot),1)+kv('participants',ev.mobileParticipantCount)+kv('mobile agg sig',short(ev.mobileAggregateSignature),1)+'</div>':'');
 }catch(e){out.innerHTML='<div class="card full err">Error: '+e.message+'</div>'}
}
document.getElementById('blk').addEventListener('keydown',e=>{if(e.key==='Enter')load()});
load();
</script></body></html>`
