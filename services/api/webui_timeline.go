// Package api — durable work-event journal timeline (RFC-0007, k-038).
//
// The work detail page gains a "Journal" section: the last 50 rows of the
// work_events table (via store.ListWorkEventsAfter), rendered server-side
// as SEQ | TIME | TYPE | SUMMARY. The summary is a compact, work_id-
// independent reading of data_json — e.g. state changes render "FROM -> TO"
// from the payload's from/state fields.
//
// XSS law: every dynamic string passes through esc() (HTML escaping)
// before splicing into the markup. The journal is a projection of upstream
// data; nothing here trusts it — a hostile data_json (e.g. one containing
// a <script> tag) renders inert as &lt;script&gt;.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/services/work/store"
)

// uiJournalLimit caps the timeline length (task spec: last 50 events).
const uiJournalLimit = 50

// esc HTML-escapes a dynamic string for use in the server-rendered UI.
// All dynamic text nodes must go through it — the repo XSS-laws its UIs
// (text nodes only, escape everything).
func esc(s string) string {
	return html.EscapeString(s)
}

// journalLister is the minimal journal surface the timeline needs.
// *store.SQLiteStore satisfies it directly; the interface keeps the
// renderer tolerant of stores that don't implement the journal (empty
// timeline instead of a broken page).
type journalLister interface {
	ListWorkEventsAfter(ctx context.Context, workID string, after int64, limit int) ([]store.WorkEvent, error)
	LatestWorkEventSequence(ctx context.Context, workID string) (int64, error)
}

// journalRow is one rendered timeline row. Every dynamic string is
// pre-escaped via esc(); renderers splice them as text nodes only.
type journalRow struct {
	Seq  string // global journal sequence
	Time string // observed_at, trimmed to seconds
	Type string // event type (work.state.changed, ...)
	Data string // compact summary
}

// journalRows fetches the last `limit` journal rows for a Work (highest
// sequence up to head) and renders them. Errors and stores without journal
// support degrade to an empty timeline — the journal is a read-only view
// and must never break the detail page.
func (s *Server) journalRows(ctx context.Context, workID string, limit int) []journalRow {
	// The journal read must not stall the detail page: the SSE handler
	// uses the same 1.5s cap for store reads.
	cctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()

	l, ok := s.Store.(journalLister)
	if !ok {
		return nil
	}
	head, err := l.LatestWorkEventSequence(cctx, workID)
	if err != nil {
		return nil
	}
	after := head - int64(limit)
	if after < 0 {
		after = 0
	}
	rows, err := l.ListWorkEventsAfter(cctx, workID, after, limit)
	if err != nil {
		return nil
	}
	out := make([]journalRow, 0, len(rows))
	for _, ev := range rows {
		out = append(out, journalRow{
			Seq:  fmt.Sprintf("%d", ev.Sequence),
			Time: ev.ObservedAt.UTC().Format("2006-01-02 15:04:05"),
			Type: ev.Type,
			Data: journalSummary(ev),
		})
	}
	return out
}

// journalSummary builds the compact, work_id-independent data summary for
// one journal row. State transitions render "FROM -> TO" from the
// payload's from/state fields; other types fall back to readable
// key=value pairs. The returned string is RAW (unescaped) — callers must
// esc() it before splicing into HTML.
func journalSummary(ev store.WorkEvent) string {
	var d map[string]any
	if err := json.Unmarshal(ev.Data, &d); err != nil || d == nil {
		return ""
	}
	// work_id is redundant on the detail page (the page is scoped to one
	// work); drop it from the summary.
	delete(d, "work_id")

	// State transitions: prefer from/state for the compact FROM -> TO form.
	from, hasFrom := d["from"]
	to, hasState := d["state"]
	if hasFrom && hasState {
		return fmt.Sprintf("%v -> %v", from, to)
	}
	if hasState {
		return fmt.Sprintf("-> %v", to)
	}

	// Fallback: compact key=value pairs in stable key order.
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%v", k, d[k])
	}
	return b.String()
}

// sortStrings is a tiny in-place insertion sort so the summary is
// deterministic without importing sort for one call site.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// renderJournal builds the Journal section HTML for a Work's detail page
// in the repo's fmt + esc style: fixed markup, escaped text nodes. It
// never fails the page — store errors degrade to the empty-state row.
// Callers must pass the result through template.HTML (it is fully
// escaped here; double-escaping would corrupt it).
func (s *Server) renderJournal(ctx context.Context, workID string) string {
	rows := s.journalRows(ctx, workID, uiJournalLimit)
	var b strings.Builder
	b.WriteString("<h2>Journal</h2>\n<table>\n")
	b.WriteString("<thead><tr><th>Seq</th><th>Time</th><th>Type</th><th>Summary</th></tr></thead>\n<tbody>\n")
	if len(rows) == 0 {
		b.WriteString(`<tr><td colspan="4" class="empty">no journal events yet</td></tr>` + "\n")
	}
	for _, r := range rows {
		// Every dynamic value goes through esc() — text nodes only.
		fmt.Fprintf(&b,
			"<tr><td class=\"mono\">%s</td><td class=\"muted mono\">%s</td><td class=\"mono\">%s</td><td class=\"mono\">%s</td></tr>\n",
			esc(r.Seq), esc(r.Time), esc(r.Type), esc(r.Data))
	}
	b.WriteString("</tbody>\n</table>\n")
	return b.String()
}
