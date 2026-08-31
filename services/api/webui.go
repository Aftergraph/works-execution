// Package api — minimal web execution view (RFC-0007).
//
// Server-rendered HTML, zero build tooling, zero JS dependencies.
// Pages:
//
//	GET /v1/ui                 → work list (state, repo, age, duration)
//	GET /v1/ui/works/{id}      → work detail: DAG nodes, attempts,
//	                             evidence, log tails
//	GET /v1/ui/runners         → runner pool + heartbeat liveness
//
// All pages are read-only projections of the same data the API
// serves — no mutation endpoints here by design (the UI is safe to
// expose behind a reverse proxy; mutations still require the Bearer
// API surface).
//
// Auth model: /v1/ui is PUBLIC-READ when WebUIConfig.Public is true
// (default false → same Bearer requirement as other /v1 endpoints).
// Operators who want privacy put the UI behind Cloudflare Access or
// set Public=false and pass a token.
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

var uiFuncs = template.FuncMap{
	"ageOf": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return time.Since(t).Round(time.Second).String()
	},
	"stateBadge": func(s workgraph.State) string {
		switch s {
		case workgraph.StateSucceeded:
			return "ok"
		case workgraph.StateFailed:
			return "fail"
		case workgraph.StateRunning, workgraph.StateVerifying:
			return "run"
		case workgraph.StateQueued, workgraph.StateCreated, workgraph.StatePlanning:
			return "queued"
		default:
			return "other"
		}
	},
	"shortID": func(id string) string {
		if len(id) > 18 {
			return id[:18] + "…"
		}
		return id
	},
}

const uiCSS = `
:root { color-scheme: dark; }
* { box-sizing: border-box; }
body { font: 14px/1.5 -apple-system, "Segoe UI", Roboto, sans-serif;
       background: #0d1117; color: #c9d1d9; margin: 0; padding: 24px; }
a { color: #58a6ff; text-decoration: none; }
a:hover { text-decoration: underline; }
h1 { font-size: 20px; margin: 0 0 4px; }
h2 { font-size: 15px; margin: 20px 0 8px; color: #8b949e; text-transform: uppercase; letter-spacing: .05em; }
.sub { color: #8b949e; margin-bottom: 20px; }
table { border-collapse: collapse; width: 100%; margin-bottom: 16px; }
th, td { text-align: left; padding: 6px 10px; border-bottom: 1px solid #21262d; }
th { color: #8b949e; font-weight: 500; font-size: 12px; text-transform: uppercase; }
tr:hover td { background: #161b22; }
.badge { display: inline-block; padding: 1px 8px; border-radius: 10px; font-size: 12px; font-weight: 600; }
.ok      { background: #1a3520; color: #3fb950; }
.fail    { background: #3d1418; color: #f85149; }
.run     { background: #0c2d6b; color: #58a6ff; }
.queued  { background: #302a12; color: #d29922; }
.other   { background: #21262d; color: #8b949e; }
pre { background: #010409; border: 1px solid #21262d; border-radius: 6px;
      padding: 12px; overflow-x: auto; font-size: 12px; max-height: 340px; overflow-y: auto; }
.card { background: #0d1117; border: 1px solid #21262d; border-radius: 6px; padding: 14px 16px; margin-bottom: 12px; }
.muted { color: #8b949e; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
footer { margin-top: 28px; color: #8b949e; font-size: 12px; }
`

const uiListTmpl = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>works — execution view</title>
<style>{{template "css"}}</style></head><body>
<h1>works <span class="muted">execution view</span></h1>
<div class="sub">{{.Count}} work(s) — most recent first — <a href="/v1/ui/runners">runner pool</a></div>
<table>
<tr><th>Work</th><th>State</th><th>Source</th><th>Repository</th><th>Age</th></tr>
{{range .Works}}
<tr>
  <td><a href="/v1/ui/works/{{.ID}}">{{shortID .ID}}</a></td>
  <td><span class="badge {{stateBadge .State}}">{{.State}}</span></td>
  <td class="muted">{{.Source.Type}}</td>
  <td>{{.Source.Repository}}</td>
  <td class="muted">{{ageOf .CreatedAt}} ago</td>
</tr>
{{end}}
</table>
<footer>works-execution — read-only view — <span class="mono">{{.Now}}</span></footer>
</body></html>`

const uiDetailTmpl = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>works — {{.Work.ID}}</title>
<style>{{template "css"}}</style></head><body>
<h1><a href="/v1/ui">←</a> work <span class="mono">{{.Work.ID}}</span>
    <span class="badge {{stateBadge .Work.State}}">{{.Work.State}}</span></h1>
<div class="sub">
  {{.Work.Source.Type}} · {{.Work.Source.Repository}}
  {{if .Work.Source.SHA}} · <span class="mono">{{slice 0 8 .Work.Source.SHA}}</span>{{end}}
  {{if .Work.Source.Branch}} · {{.Work.Source.Branch}}{{end}}
  · created {{ageOf .Work.CreatedAt}} ago
  {{if .Work.Source.HTMLURL}} · <a href="{{.Work.Source.HTMLURL}}">repo ↗</a>{{end}}
</div>

<h2>Graph</h2>
{{range .Nodes}}
<div class="card">
  <strong>{{.ID}}</strong>
  {{if .Lease}}<span class="badge {{stateBadge .State}}">{{.State}}</span>{{else}}<span class="badge other">pending</span>{{end}}
  <span class="muted">{{if .Dur}}· {{.Dur}}{{end}} {{if .Exit}}· exit {{.Exit}}{{end}}</span>
  <div class="mono muted" style="margin-top:6px">{{.Run}}</div>
  {{if .LogTail}}
  <pre>{{.LogTail}}</pre>
  {{end}}
  {{range .Evidence}}
    <div class="mono muted">evidence: {{.Type}} = {{.Result}}{{if .Err}} — {{.Err}}{{end}}</div>
  {{end}}
</div>
{{end}}

<h2>Attempts</h2>
<table>
<tr><th>Node</th><th>Worker</th><th>Status</th><th>Exit</th><th>Started</th><th>Duration</th></tr>
{{range .Attempts}}
<tr><td>{{.NodeID}}</td><td class="mono">{{.WorkerID}}</td>
    <td>{{.Status}}</td><td>{{.ExitCode}}</td>
    <td class="muted">{{ageOf .StartedAt}} ago</td>
    <td class="muted">{{if .FinishedAt}}{{ageOf .FinishedAt}}{{else}}running{{end}}</td></tr>
{{end}}
</table>

<footer>works-execution — read-only view — <span class="mono">{{.Now}}</span></footer>
</body></html>`

const uiRunnersTmpl = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>works — runners</title>
<style>{{template "css"}}</style></head><body>
<h1>runner pool <a href="/v1/ui" style="font-size:14px">← works</a></h1>
<div class="sub">{{.Count}} runner(s) — stale = heartbeat older than 30s</div>
<table>
<tr><th>Runner</th><th>Trust</th><th>Lifecycle</th><th>Pool</th><th>Heartbeat</th><th>OS/Arch</th></tr>
{{range .Runners}}
<tr>
  <td class="mono">{{.ID}}</td>
  <td>{{.Trust}}</td><td>{{.Lifecycle}}</td><td>{{.Pool}}</td>
  <td>{{if .Stale}}<span class="badge fail">stale</span>{{else}}<span class="badge ok">{{.Heartbeat}}</span>{{end}}</td>
  <td class="muted">{{.OSArch}}</td>
</tr>
{{end}}
</table>
<footer>works-execution — read-only view — <span class="mono">{{.Now}}</span></footer>
</body></html>`

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

// uiDispatch routes the three pages.
func (s *Server) uiDispatch(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/ui")
	path = strings.Trim(path, "/")
	switch {
	case path == "":
		s.uiList(w, r)
	case path == "runners":
		s.uiRunners(w, r)
	case strings.HasPrefix(path, "works/"):
		s.uiWork(w, r, strings.TrimPrefix(path, "works/"))
	default:
		writeError(w, http.StatusNotFound, "not_found", r.URL.Path)
	}
}

// uiRender executes one of the UI templates. The {{template "css"}} template is
// pre-defined (not appended at parse time) so all three templates can
// reference it; html/template escapes data automatically.
func uiRender(w http.ResponseWriter, tmplSrc string, data any) {
	tmpl := template.Must(template.New("ui").Funcs(uiFuncs).Parse(
		`{{define "css"}}` + uiCSS + `{{end}}` + tmplSrc))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(w, data)
}

// uiList renders the work list.
func (s *Server) uiList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListWorks(r.Context(), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	uiRender(w, uiListTmpl, struct {
		uiPage
		Works []*workgraph.Work
	}{uiPage{Count: len(list), Now: time.Now().UTC().Format(time.RFC3339)}, list})
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

func sliceStr(a, b int, s string) string {
	if len(s) <= b {
		return s
	}
	return s[a:b]
}

// uiWork renders one work: nodes with state + log tail, attempts.
func (s *Server) uiWork(w http.ResponseWriter, r *http.Request, id string) {
	wk, err := s.Store.GetWork(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "work_not_found", id)
			return
		}
		writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}

	// Index attempts + evidence by node for the cards.
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
			if s, ok := e.Details["error"].(string); ok && s != "" {
				ev.Err = s
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
		// Log tail (best-effort; failures surface via evidence error).
		if s.ArtifactsDir != "" {
			tail, err := s.readLogTail(wk.ID, nid)
			if err == nil && tail != "" {
				nv.LogTail = tail
			}
		}
		nv.Evidence = evids[nid]
		nodes = append(nodes, nv)
	}

	uiRender(w, uiDetailTmpl, struct {
		uiPage
		Work     *workgraph.Work
		Nodes    []nodeView
		Attempts []workgraph.Attempt
		Slice    func(a, b int, s string) string
	}{
		uiPage:   uiPage{Count: len(nodes), Now: time.Now().UTC().Format(time.RFC3339)},
		Work:     wk,
		Nodes:    nodes,
		Attempts: wk.Attempts,
		Slice:    sliceStr,
	})
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

// runnerRow is one runner table row.
type runnerRow struct {
	ID        string
	Trust     string
	Lifecycle string
	Pool      string
	Heartbeat string
	Stale     bool
	OSArch    string
}

// uiRunners renders the pool page.
func (s *Server) uiRunners(w http.ResponseWriter, r *http.Request) {
	var rows []runnerRow
	if s.RunnerRegistry != nil {
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
				age := time.Since(*id.LastHeartbeatAt).Round(time.Second)
				row.Heartbeat = age.String() + " ago"
				row.Stale = id.LastHeartbeatAt.Before(cutoff)
			} else {
				row.Stale = true
			}
			rows = append(rows, row)
		}
	}
	uiRender(w, uiRunnersTmpl, struct {
		uiPage
		Runners []runnerRow
	}{uiPage{Count: len(rows), Now: time.Now().UTC().Format(time.RFC3339)}, rows})
}

// keep json import used (stateBadge helpers may evolve to JSON data).
var _ = json.Marshal
