// Package api — minimal web execution view (RFC-0007, v2: live).
//
// Server-rendered HTML shell + progressive enhancement via vanilla
// JS (no frameworks, no build step, ~200 lines of client code).
// Pages:
//
//	GET /v1/ui                 → live work list (SSE-driven)
//	GET /v1/ui/works/{id}      → work detail: DAG nodes, attempts,
//	                             evidence, live log tails
//	GET /v1/ui/runners         → runner pool + heartbeat liveness
//
// Live model: the shell renders server-side (first paint works with
// JS disabled); once loaded, the client opens ONE EventSource to
// /v1/ui/events and patches rows in place (no full reloads). Relative
// timestamps tick client-side. Respectful of reduced-motion.
//
// All pages are read-only projections — no mutation endpoints here.
// Auth: /v1/ui requires Bearer unless WebUIConfig.Public.
package api

import (
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// WebUIConfig gates the execution view.
type WebUIConfig struct {
	// Public, when true, serves /v1/ui without a Bearer token
	// (read-only pages). Default false: /v1/ui sits behind
	// requireBearer like the rest of the API.
	Public bool
}

// uiFuncs includes every helper the templates reference. Registered
// here (not in a later init) so the package-level Parse below sees
// them all — html/template resolves functions at parse time.
var uiFuncs = template.FuncMap{
	"ageOf": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return time.Since(t).Round(time.Second).String()
	},
	"stateBadge": func(s workgraph.State) string {
		return stateBadgeClass(s)
	},
	"shortID": func(id string) string {
		if len(id) > 18 {
			return id[:18] + "…"
		}
		return id
	},
	"slice8": func(s string) string {
		if len(s) > 8 {
			return s[:8]
		}
		return s
	},
	"badgeFor": func(state string) string { return stateBadgeClass(workgraph.State(state)) },
}

const uiCSS = `
:root {
  color-scheme: dark;
  --bg: #0d1117; --panel: #161b22; --border: #21262d; --border-soft: #30363d;
  --fg: #e6edf3; --muted: #8b949e; --accent: #58a6ff;
  --ok: #3fb950; --fail: #f85149; --warn: #d29922; --run: #58a6ff;
  --radius: 8px; --mono: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
}
* { box-sizing: border-box; }
html { scrollbar-color: var(--border-soft) var(--bg); }
body { font: 14px/1.55 -apple-system, "Segoe UI", Roboto, sans-serif;
       background: var(--bg); color: var(--fg); margin: 0; padding: 20px clamp(12px, 4vw, 40px); }
a { color: var(--accent); text-decoration: none; border-radius: 4px; }
a:hover { text-decoration: underline; }
:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
h1 { font-size: 18px; margin: 0; display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
h2 { font-size: 12px; margin: 22px 0 8px; color: var(--muted); text-transform: uppercase;
     letter-spacing: .08em; font-weight: 600; }
.sub { color: var(--muted); margin: 6px 0 18px; display: flex; gap: 14px; align-items: center; flex-wrap: wrap; }
.live-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--ok);
            box-shadow: 0 0 6px var(--ok); display: inline-block; }
.live-dot[data-state="offline"] { background: var(--fail); box-shadow: 0 0 6px var(--fail); }
.live-dot[data-state="connecting"] { background: var(--warn); box-shadow: 0 0 6px var(--warn); }
@media (prefers-reduced-motion: reduce) { .live-dot { box-shadow: none; } }
table { border-collapse: collapse; width: 100%; margin-bottom: 8px; }
th, td { text-align: left; padding: 7px 10px; border-bottom: 1px solid var(--border); }
th { color: var(--muted); font-weight: 600; font-size: 11px; text-transform: uppercase;
     letter-spacing: .06em; position: sticky; top: 0; background: var(--bg); }
tbody tr { transition: background .12s ease; }
tbody tr:hover { background: var(--panel); }
@media (prefers-reduced-motion: reduce) { tbody tr { transition: none; } }
.badge { display: inline-flex; align-items: center; padding: 1px 9px; border-radius: 999px;
         font-size: 11.5px; font-weight: 600; letter-spacing: .02em; white-space: nowrap; }
.ok     { background: rgba(63,185,80,.14);  color: var(--ok); }
.fail   { background: rgba(248,81,73,.14);  color: var(--fail); }
.run    { background: rgba(88,166,255,.14); color: var(--run); }
.queued { background: rgba(210,153,34,.14); color: var(--warn); }
.other  { background: var(--border); color: var(--muted); }
pre { background: #010409; border: 1px solid var(--border); border-radius: var(--radius);
      padding: 10px 12px; overflow: auto; font: 12px/1.5 var(--mono);
      max-height: 300px; margin: 8px 0 0; }
.card { background: var(--panel); border: 1px solid var(--border); border-radius: var(--radius);
        padding: 12px 14px; margin-bottom: 10px; }
.card-head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.card-head .name { font-weight: 600; }
.muted { color: var(--muted); }
.mono { font-family: var(--mono); font-size: 12px; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 10px; }
@media (prefers-reduced-motion: no-preference) {
  .flash { animation: flash 1.2s ease-out; }
  @keyframes flash { 0% { background: rgba(88,166,255,.18); } 100% { background: transparent; } }
}
.empty { color: var(--muted); padding: 24px 0; text-align: center; }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
button.copy { background: var(--border); color: var(--fg); border: 0; border-radius: 6px;
              padding: 3px 10px; font-size: 11.5px; cursor: pointer; }
button.copy:hover { background: var(--border-soft); }
footer { margin-top: 26px; color: var(--muted); font-size: 12px; display: flex; gap: 16px; flex-wrap: wrap; }
`

// uiJS is the progressive-enhancement layer. Loaded with `defer` on
// every page; no-ops gracefully when APIs are missing.
const uiJS = `
(function () {
  "use strict";
  var $ = function (s, el) { return (el || document).querySelector(s); };

  // ---- relative timestamps that tick --------------------------------
  function tickAges() {
    document.querySelectorAll("[data-age]").forEach(function (el) {
      var t = parseInt(el.getAttribute("data-age"), 10);
      if (!t) return;
      var d = Math.max(0, Math.round((Date.now() - t) / 1000));
      el.textContent = fmtDur(d) + " ago";
    });
  }
  function fmtDur(s) {
    if (s < 60) return s + "s";
    if (s < 3600) return Math.floor(s / 60) + "m" + (s % 60) + "s";
    if (s < 86400) return Math.floor(s / 3600) + "h" + Math.floor((s % 3600) / 60) + "m";
    return Math.floor(s / 86400) + "d" + Math.floor((s % 86400) / 3600) + "h";
  }
  tickAges();
  setInterval(tickAges, 1000);

  // ---- copy buttons ---------------------------------------------------
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-copy]");
    if (!btn) return;
    navigator.clipboard.writeText(btn.getAttribute("data-copy")).then(function () {
      var old = btn.textContent;
      btn.textContent = "copied ✓";
      setTimeout(function () { btn.textContent = old; }, 1200);
    });
  });

  // ---- SSE live updates -----------------------------------------------
  var dot = $(".live-dot");
  function setDot(state) { if (dot) dot.setAttribute("data-state", state); }
  if (window.EventSource) {
    var es = new EventSource("/v1/ui/events");
    es.onopen = function () { setDot("live"); };
    es.onerror = function () { setDot("connecting"); };
    es.addEventListener("work", function (ev) {
      var d = JSON.parse(ev.data);
      if (d.state === "DELETED") {
        var row = document.querySelector('[data-work="' + d.id + '"]');
        if (row) row.remove();
        return;
      }
      var row = document.querySelector('[data-work="' + d.id + '"]');
      if (row) {
        // Patch in place: state badge + flash.
        var b = row.querySelector(".badge");
        if (b) {
          b.className = "badge " + badgeClass(d.state);
          b.textContent = d.state;
        }
        row.classList.remove("flash");
        void row.offsetWidth; // restart animation
        row.classList.add("flash");
      } else if (typeof loadWorks === "function") {
        loadWorks(); // new row: cheap full-list refresh (rendered HTML)
      }
      // Detail page: refresh node cards.
      if (typeof loadDetail === "function" && detailWorkID === d.id) loadDetail();
    });
    es.addEventListener("runner", function (ev) {
      if (typeof loadRunners === "function") loadRunners();
    });
  } else {
    setDot("offline");
  }

  function badgeClass(state) {
    switch (state) {
      case "SUCCEEDED": return "ok";
      case "FAILED": return "fail";
      case "RUNNING": case "VERIFYING": return "run";
      case "QUEUED": case "CREATED": case "PLANNING": return "queued";
      default: return "other";
    }
  }

  // ---- page-specific fragments ----------------------------------------
  // Detail page + runners page fetch their own rendered fragments and
  // swap the container innerHTML (server stays the single renderer).
  window.detailWorkID = (function () {
    var m = location.pathname.match(/\/v1\/ui\/works\/(wrk_[a-z0-9]+)/);
    return m ? m[1] : null;
  })();
  window.loadDetail = null;
  window.loadRunners = null;
  window.loadWorks = null;

  if (window.detailWorkID) {
    var container = $("#live-detail");
    if (container) {
      var reload = function () {
        fetch("/v1/ui/fragments/work/" + window.detailWorkID)
          .then(function (r) { return r.text(); })
          .then(function (html) {
            container.innerHTML = html;
            tickAges();
          });
      };
      // One immediate refresh (server-rendered shell may be stale),
      // then rely on SSE triggers.
      reload();
      window.loadDetail = reload;
    }
  }
  if ($("#live-runners")) {
    var rr = function () {
      fetch("/v1/ui/fragments/runners").then(function (r) { return r.text(); })
        .then(function (html) {
          $("#live-runners").innerHTML = html;
          tickAges();
        });
    };
    window.loadRunners = rr;
  }
  if ($("#live-works")) {
    var rw = function () {
      fetch("/v1/ui/fragments/works").then(function (r) { return r.text(); })
        .then(function (html) {
          $("#live-works").innerHTML = html;
          tickAges();
        });
    };
    window.loadWorks = rw;
    // Initial paint already server-rendered; refresh only on demand
    // (new work arrives → SSE handler calls loadWorks()).
  }
})();
`

const uiShellHead = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark">
<title>works — execution view</title>
<style>{{template "css"}}</style>
<script defer>{{template "js"}}</script>
</head>
<body>`

const uiShellFoot = `<footer>
<span><span class="live-dot" data-state="connecting" aria-hidden="true"></span> <span id="live-label">connecting…</span></span>
<span>works-execution</span>
<span class="mono">{{.Now}}</span>
</footer>
</body></html>`

const uiListTmpl = `{{define "uiListTmpl"}}
<header>
<h1>works <span class="muted" style="font-weight:400">execution view</span></h1>
<div class="sub">
  <span>{{.Count}} work(s)</span>
  <a href="/v1/ui/runners">runner pool</a>
</div>
</header>
<main>
<h2 class="sr-only">Works</h2>
<div id="live-works">
{{template "worksBody" .}}
</div>
</main>
{{end}}` + uiShellFoot

// worksBody — the work table, also served standalone at
// /v1/ui/fragments/works for live swap.
const worksBodyTmpl = `
{{define "worksBody"}}
<table>
<thead><tr><th>Work</th><th>State</th><th>Source</th><th>Repository</th><th>Age</th></tr></thead>
<tbody>
{{range .Works}}
<tr data-work="{{.ID}}">
  <td><a href="/v1/ui/works/{{.ID}}" class="mono">{{shortID .ID}}</a></td>
  <td><span class="badge {{stateBadge .State}}">{{.State}}</span></td>
  <td class="muted">{{.Source.Type}}</td>
  <td>{{.Source.Repository}}</td>
  <td class="muted" data-age="{{.CreatedAt.UnixMilli}}">—</td>
</tr>
{{else}}
<div class="empty">no works yet — submit one via <span class="mono">works run</span> or push to a connected repo</div>
{{end}}
</tbody>
</table>
{{end}}`

const uiDetailTmpl = `{{define "uiDetailTmpl"}}
<header>
<h1><a href="/v1/ui" aria-label="back to list">←</a>
    <span class="mono">{{.Work.ID}}</span>
    <span class="badge {{stateBadge .Work.State}}">{{.Work.State}}</span>
    <button class="copy" data-copy="{{.Work.ID}}" aria-label="copy work id">copy id</button></h1>
<div class="sub">
  <span>{{.Work.Source.Type}}</span>
  {{if .Work.Source.Repository}}<span>{{.Work.Source.Repository}}</span>{{end}}
  {{if .Work.Source.SHA}}<span class="mono">{{slice8 .Work.Source.SHA}}</span>{{end}}
  {{if .Work.Source.Branch}}<span>{{.Work.Source.Branch}}</span>{{end}}
  {{if .Work.Source.HTMLURL}}<a href="{{.Work.Source.HTMLURL}}" target="_blank" rel="noopener">repo ↗</a>{{end}}
  <span class="muted">created <span data-age="{{.Work.CreatedAt.UnixMilli}}">—</span></span>
</div>
</header>
<main id="live-detail">
{{template "workBody" .}}
</main>
{{end}}` + uiShellFoot

// uiWorkBody is the shared detail fragment: also served standalone at
// /v1/ui/fragments/work/{id} for live swapping.
const uiWorkBodyTmpl = `
{{define "workBody"}}
<h2>Graph</h2>
{{range .Nodes}}
<div class="card">
  <div class="card-head">
    <span class="name">{{.ID}}</span>
    {{if .Lease}}<span class="badge {{badgeFor .State}}">{{.State}}</span>
    {{else}}<span class="badge other">pending</span>{{end}}
    {{if .Dur}}<span class="muted mono">{{.Dur}}</span>{{end}}
    {{if .Exit}}<span class="muted mono">exit {{.Exit}}</span>{{end}}
  </div>
  <div class="mono muted" style="margin-top:6px; white-space:pre-wrap;">{{.Run}}</div>
  {{if .LogTail}}<pre aria-label="log output">{{.LogTail}}</pre>{{end}}
  {{range .Evidence}}
    <div class="mono muted" style="margin-top:6px">evidence: {{.Type}} = {{.Result}}{{if .Err}} — <span style="color:var(--fail)">{{.Err}}</span>{{end}}</div>
  {{end}}
</div>
{{else}}
<div class="empty">no nodes</div>
{{end}}

<h2>Attempts</h2>
<table>
<thead><tr><th>Node</th><th>Worker</th><th>Status</th><th>Exit</th><th>Started</th><th>Finished</th></tr></thead>
<tbody>
{{range .Attempts}}
<tr><td>{{.NodeID}}</td><td class="mono">{{.WorkerID}}</td><td>{{.Status}}</td><td>{{.ExitCode}}</td>
    <td class="muted"><span data-age="{{.StartedAt.UnixMilli}}">—</span></td>
    <td class="muted">{{if .FinishedAt.IsZero}}running{{else}}<span data-age="{{.FinishedAt.UnixMilli}}">—</span>{{end}}</td></tr>
{{else}}
<tr><td colspan="6" class="empty">no attempts yet</td></tr>
{{end}}
</tbody>
</table>
{{end}}`

const uiRunnersTmpl = `{{define "uiRunnersTmpl"}}
<header>
<h1>runner pool <a href="/v1/ui" style="font-size:13px">← works</a></h1>
<div class="sub"><span>{{.Count}} runner(s)</span><span class="muted">stale = heartbeat older than 30s</span></div>
</header>
<main id="live-runners">
{{template "runnerBody" .}}
</main>
{{end}}` + uiShellFoot

const uiRunnerBodyTmpl = `
{{define "runnerBody"}}
<table>
<thead><tr><th>Runner</th><th>Trust</th><th>Lifecycle</th><th>Pool</th><th>Heartbeat</th><th>OS/Arch</th></tr></thead>
<tbody>
{{range .Runners}}
<tr data-runner="{{.ID}}">
  <td class="mono">{{.ID}}</td>
  <td>{{.Trust}}</td><td>{{.Lifecycle}}</td><td>{{if .Pool}}<span class="badge other">{{.Pool}}</span>{{else}}<span class="muted">—</span>{{end}}</td>
  <td>{{if .Stale}}<span class="badge fail">stale</span>{{else}}<span class="badge ok">{{.Heartbeat}}</span>{{end}}</td>
  <td class="muted">{{.OSArch}}</td>
</tr>
{{else}}
<div class="empty">no runners registered</div>
{{end}}
</tbody>
</table>
{{end}}`

// uiPage wraps shared template data.
type uiPage struct {
	Count int
	Now   string
}

// RegisterUI mounts the execution-view pages on the mux. Called from
// Routes() when WebUIConfig != nil.
func (s *Server) RegisterUI(mux *http.ServeMux) {
	var h http.Handler = http.HandlerFunc(s.uiDispatch)
	if s.WebUI == nil || !s.WebUI.Public {
		h = s.requireBearer(h)
	}
	mux.Handle("/v1/ui", h)
	mux.Handle("/v1/ui/", h)
}

// uiDispatch routes the pages + fragments + SSE.
func (s *Server) uiDispatch(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/ui"), "/")
	switch {
	case path == "":
		s.uiList(w, r)
	case path == "runners":
		s.uiRunners(w, r)
	case path == "events":
		s.uiEvents(w, r)
	case path == "fragments/works":
		s.uiFragmentWorks(w, r)
	case path == "fragments/runners":
		s.uiFragmentRunners(w, r)
	case strings.HasPrefix(path, "works/"):
		s.uiWork(w, r, strings.TrimPrefix(path, "works/"))
	case strings.HasPrefix(path, "fragments/work/"):
		s.uiFragmentWork(w, r, strings.TrimPrefix(path, "fragments/work/"))
	default:
		writeError(w, http.StatusNotFound, "not_found", r.URL.Path)
	}
}

// uiTmpl parses a named set of templates once (compile-time safety:
// a broken template panics at boot, not per-request).
var uiTmpl = template.Must(template.New("ui").Funcs(uiFuncs).Parse(
	`{{define "css"}}` + uiCSS + `{{end}}` +
		`{{define "js"}}` + uiJS + `{{end}}` +
		worksBodyTmpl +
		uiWorkBodyTmpl + uiRunnerBodyTmpl +
		uiListTmpl + uiDetailTmpl + uiRunnersTmpl))

func uiExec(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = uiTmpl.ExecuteTemplate(w, name, data)
}

// uiList renders the live work list shell.
func (s *Server) uiList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListWorks(r.Context(), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	uiExec(w, "uiListTmpl", struct {
		uiPage
		Works []*workgraph.Work
	}{uiPage{Count: len(list), Now: time.Now().UTC().Format(time.RFC3339)}, list})
}

// uiFragmentWorks renders only the work-list table (live swap target).
func (s *Server) uiFragmentWorks(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListWorks(r.Context(), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	uiExec(w, "worksBody", struct {
		uiPage
		Works []*workgraph.Work
	}{uiPage{Count: len(list), Now: time.Now().UTC().Format(time.RFC3339)}, list})
}

// uiFragmentRunners renders only the runners table.
func (s *Server) uiFragmentRunners(w http.ResponseWriter, r *http.Request) {
	rows := s.runnerRows()
	uiExec(w, "runnerBody", struct {
		uiPage
		Runners []runnerRow
	}{uiPage{Count: len(rows), Now: time.Now().UTC().Format(time.RFC3339)}, rows})
}

// nodeView is the per-node card on the detail page.
type nodeView struct {
	ID       string
	Run      string
	State    string
	Lease    bool
	Dur      string
	Exit     *int
	LogTail  string
	Evidence []evidenceView
}

type evidenceView struct {
	Type   string
	Result string
	Err    string
}

// uiWork renders one work: nodes with state + log tail, attempts.
func (s *Server) uiWork(w http.ResponseWriter, r *http.Request, id string) {
	wk, err := s.Store.GetWork(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "work_not_found", id)
		return
	}
	nodes, attempts := s.workViews(wk)
	uiExec(w, "uiDetailTmpl", struct {
		uiPage
		Work     *workgraph.Work
		Nodes    []nodeView
		Attempts []workgraph.Attempt
	}{
		uiPage:   uiPage{Count: len(nodes), Now: time.Now().UTC().Format(time.RFC3339)},
		Work:     wk,
		Nodes:    nodes,
		Attempts: attempts,
	})
}

// uiFragmentWork renders only the detail body (live swap target).
func (s *Server) uiFragmentWork(w http.ResponseWriter, r *http.Request, id string) {
	wk, err := s.Store.GetWork(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "work_not_found", id)
		return
	}
	nodes, attempts := s.workViews(wk)
	uiExec(w, "workBody", struct {
		Nodes    []nodeView
		Attempts []workgraph.Attempt
	}{nodes, attempts})
}

// workViews converts a Work into node cards + attempts for templates.
func (s *Server) workViews(wk *workgraph.Work) ([]nodeView, []workgraph.Attempt) {
	latest := map[string]*workgraph.Attempt{}
	for i := range wk.Attempts {
		a := &wk.Attempts[i]
		if cur, ok := latest[a.NodeID]; !ok || a.StartedAt.After(cur.StartedAt) {
			latest[a.NodeID] = a
		}
	}
	evids := map[string][]evidenceView{}
	for _, e := range wk.Evidence {
		ev := evidenceView{Type: e.Type, Result: e.Result}
		if e.Details != nil {
			if s2, ok := e.Details["error"].(string); ok && s2 != "" {
				ev.Err = s2
			}
		}
		evids[e.NodeID] = append(evids[e.NodeID], ev)
	}

	nodes := make([]nodeView, 0, len(wk.Graph.Nodes))
	for nid, n := range wk.Graph.Nodes {
		nv := nodeView{ID: nid, Run: n.Run}
		if a, ok := latest[nid]; ok {
			nv.Lease = true
			nv.State = a.Status
			if !a.FinishedAt.IsZero() {
				nv.Dur = a.FinishedAt.Sub(a.StartedAt).Round(time.Millisecond).String()
			}
			ex := a.ExitCode
			nv.Exit = &ex
		}
		if s.ArtifactsDir != "" {
			if tail, err := s.readLogTail(wk.ID, nid); err == nil && tail != "" {
				nv.LogTail = tail
			}
		}
		nv.Evidence = evids[nid]
		nodes = append(nodes, nv)
	}
	return nodes, wk.Attempts
}

// readLogTail returns the last 4KB of a node's artifact log.
func (s *Server) readLogTail(workID, nodeID string) (string, error) {
	logPath := filepath.Join(s.ArtifactsDir, workID, nodeID+".log")
	f, err := os.Open(logPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	const max = 4096
	offset := int64(0)
	if st.Size() > max {
		offset = st.Size() - max
	}
	buf := make([]byte, st.Size()-offset)
	if _, err := f.ReadAt(buf, offset); err != nil {
		return "", err
	}
	return string(buf), nil
}

// runnerRow is one runner table row (shared by page + fragment).
type runnerRow struct {
	ID        string
	Trust     string
	Lifecycle string
	Pool      string
	Heartbeat string
	Stale     bool
	OSArch    string
}

func (s *Server) runnerRows() []runnerRow {
	var rows []runnerRow
	if s.RunnerRegistry == nil {
		return rows
	}
	cutoff := time.Now().Add(-3 * defaultHeartbeatInterval)
	for _, id := range s.RunnerRegistry.List() {
		if id == nil {
			continue
		}
		row := runnerRow{
			ID:        id.RunnerID,
			Trust:     string(id.TrustClass),
			Lifecycle: string(id.LifecycleState),
		}
		for _, l := range id.Capabilities.Labels {
			if strings.HasPrefix(l, "pool:") {
				row.Pool = strings.TrimPrefix(l, "pool:")
				break
			}
		}
		if len(id.Capabilities.OS) > 0 {
			row.OSArch = id.Capabilities.OS[0]
		}
		if len(id.Capabilities.Arch) > 0 {
			row.OSArch += "/" + id.Capabilities.Arch[0]
		}
		if id.LastHeartbeatAt != nil {
			row.Heartbeat = time.Since(*id.LastHeartbeatAt).Round(time.Second).String() + " ago"
			row.Stale = id.LastHeartbeatAt.Before(cutoff)
		} else {
			row.Stale = true
		}
		rows = append(rows, row)
	}
	return rows
}

// uiRunners renders the pool page shell.
func (s *Server) uiRunners(w http.ResponseWriter, r *http.Request) {
	rows := s.runnerRows()
	uiExec(w, "uiRunnersTmpl", struct {
		uiPage
		Runners []runnerRow
	}{uiPage{Count: len(rows), Now: time.Now().UTC().Format(time.RFC3339)}, rows})
}

// keep json referenced for future SSE payload helpers here.
var _ = json.Marshal
