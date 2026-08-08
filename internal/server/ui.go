package server

import (
	"expvar"
	"fmt"
	"net/http"
)

// Self-telemetry (roadmap v0.8): investigation counters exposed as expvar
// on GET /metrics. Read-only, no auth, documented as localhost telemetry.
var (
	investigations   = expvar.NewInt("kubetective_investigations_total")
	investigationErr = expvar.NewInt("kubetective_investigation_errors_total")
	investigationsOK = expvar.NewInt("kubetective_investigations_ok_total")
)

// ---- Read-only web UI (v0.8, roadmap) -------------------------------------
// Two plain-HTML pages served by `kubetective serve`. No framework, no write
// endpoints, no auth: documented as localhost-only. The pages call the REST
// API from the browser.

const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8"><title>KubeTective</title>
<style>body{font-family:system-ui,sans-serif;max-width:60rem;margin:2rem auto;padding:0 1rem;color:#222}
table{border-collapse:collapse;width:100%}td,th{border-bottom:1px solid #ddd;padding:.4rem;text-align:left}
a{color:#0b57d0;text-decoration:none}code{background:#f4f4f4;padding:0 .2rem}</style>
</head>
<body>
<h1>KubeTective</h1>
<p>Recorded incidents (read-only). Investigate live incidents from the CLI:
<code>kubetective investigate &lt;resource&gt; --since=30m</code></p>
<table><thead><tr><th>Incident</th></tr></thead><tbody id="rows">
<tr><td>loading...</td></tr></tbody></table>
<script>
fetch('/v1/incidents').then(r=>r.json()).then(d=>{
  const rows=d.incidents.map(id=>'<tr><td><a href="/incidents/'+encodeURIComponent(id)+'">'+id+'</a></td></tr>').join('');
  document.getElementById('rows').innerHTML=rows||'<tr><td>no incidents yet</td></tr>';
}).catch(e=>{document.getElementById('rows').innerHTML='<tr><td>error: '+e+'</td></tr>';});
</script>
</body></html>`

const incidentHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8"><title>Incident - KubeTective</title>
<style>body{font-family:system-ui,sans-serif;max-width:70rem;margin:2rem auto;padding:0 1rem;color:#222}
table{border-collapse:collapse;width:100%;margin:.5rem 0}td,th{border-bottom:1px solid #ddd;padding:.3rem;text-align:left;vertical-align:top}
code{background:#f4f4f4;padding:0 .2rem}pre{background:#f4f4f4;padding:.5rem;overflow-x:auto}</style>
</head>
<body>
<p><a href="/">&larr; incidents</a></p>
<h1 id="id">loading...</h1>
<div id="body"></div>
<script>
const id=decodeURIComponent(location.pathname.split('/').pop());
document.getElementById('id').textContent=id;
fetch('/v1/incidents/'+encodeURIComponent(id)).then(r=>r.json()).then(inc=>{
  const esc=s=>String(s??'').replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
  const meta=inc.meta||{};
  const findings=(inc.result&&inc.result.findings)||[];
  const hyps=(inc.result&&inc.result.hypotheses)||[];
  const rows=(inc.observations||[]).slice(0,200).map(o=>
    '<tr><td><code>'+esc(o.kind)+'</code></td><td>'+esc(o.resource.namespace+'/'+o.resource.name)+'</td>'+
    '<td><pre>'+esc(JSON.stringify(o.payload))+'</pre></td></tr>').join('');
  document.getElementById('body').innerHTML=
    '<table><tr><th>target</th><td><code>'+esc(meta.target)+'</code></td></tr>'+
    '<tr><th>created</th><td>'+esc(meta.created||meta.user_note)+'</td></tr>'+
    '<tr><th>observations</th><td>'+(inc.observations||[]).length+'</td></tr></table>'+
    '<h2>Findings</h2><table>'+(findings.map(f=>'<tr><td><code>'+esc(f.analyzer)+'</code></td><td>'+esc(f.title)+'</td></tr>').join('')||'<tr><td>none</td></tr>')+'</table>'+
    '<h2>Hypotheses</h2><table>'+(hyps.map(h=>'<tr><td>'+esc(h.category)+'</td><td>'+esc(h.claim)+'</td><td>'+(h.score?Math.round(h.score.score*100)+'%':'')+'</td></tr>').join('')||'<tr><td>none</td></tr>')+'</table>'+
    '<h2>Observations (first 200)</h2><table><thead><tr><th>kind</th><th>resource</th><th>payload</th></tr></thead><tbody>'+rows+'</tbody></table>';
}).catch(e=>{document.getElementById('body').innerHTML='<p>error: '+e+'</p>';});
</script>
</body></html>`

func (s *REST) handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, indexHTML)
}

func (s *REST) handleIncidentUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, incidentHTML)
}

func (s *REST) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	expvar.Handler().ServeHTTP(w, r)
}
