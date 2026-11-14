package dashboard

import (
	"encoding/json"
	"fmt"
	"github.com/shbhmrzd/agentview/internal/observe"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStabilizationCursorInvalidationNeedsRefresh(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(root, "rollout.jsonl")
	line := `{"type":"response_item","payload":{"type":"message","role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(path, []byte(strings.Repeat(line, 1100)), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(Options{Roots: []Root{{Agent: "codex", Path: root}}, Poll: time.Second, MaxRecordBytes: 1024}, "127.0.0.1:7433")
	if err != nil {
		t.Fatal(err)
	}
	s.Store.Apply(observe.Event{Agent: "codex", Source: observe.Source{Path: path, Generation: 1}})
	get := func(cursor string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://"+s.authority+"/api/history?id="+sourceID("codex", path)+"&cursor="+cursor, nil)
		r.Header.Set("Authorization", "Bearer "+s.token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	first := get("")
	if first.Code != 200 {
		t.Fatal(first.Body.String())
	}
	var p historyPage
	json.Unmarshal(first.Body.Bytes(), &p)
	if err := os.Rename(path, path+".away"); err != nil {
		t.Fatal(err)
	}
	failure := get(p.Next)
	if err := os.Rename(path+".away", path); err != nil {
		t.Fatal(err)
	}
	retry := get(p.Next)
	refresh := get("")
	t.Logf("read failure=%d, after restoring source retry=%d, refresh=%d; retry=%q", failure.Code, retry.Code, refresh.Code, retry.Body.String())
	if retry.Code != 409 || refresh.Code != 200 || !strings.Contains(failure.Body.String(), `"recovery":"refresh"`) || !strings.Contains(retry.Body.String(), `"recovery":"refresh"`) {
		t.Fatal("invalid cursor did not provide actionable refresh recovery")
	}
}

func TestMetricsAndPayloadUseSameAmbiguityDecision(t *testing.T) {
	c := observe.NewCallTracker(nil)
	defer c.Close()
	var m Metrics
	for i, name := range []string{"first", "second"} {
		e := observe.Event{Kind: "tool.requested", ToolCallID: "same", ToolName: name, OccurredAt: fmt.Sprintf("2026-01-01T00:00:0%dZ", i)}
		c.Apply(&e)
		m.apply(e)
	}
	e := observe.Event{Kind: "tool.result", ToolCallID: "same", OccurredAt: "2026-01-01T00:00:02Z"}
	c.Apply(&e)
	duration := m.apply(e)
	entry, _ := displayEvent(e)
	if duration != nil || entry.DurationMS != nil || entry.Request != nil || entry.CorrelationStatus != "ambiguous" || !m.Partial {
		t.Fatalf("inconsistent evidence: %+v %+v", entry, m)
	}
	for _, tool := range m.Tools {
		if tool.Timed != 0 || tool.Results != 0 {
			t.Fatal("ambiguous metric", tool)
		}
	}
}

func TestFailureSummaryCountsToolAPIAndHookEvidence(t *testing.T) {
	var m Metrics
	yes := true
	for _, kind := range []string{"tool.result", "api.error", "hook.summary"} {
		m.apply(observe.Event{Kind: kind, IsError: &yes, Outcome: &observe.Outcome{Status: "failure"}})
	}
	m.apply(observe.Event{Kind: "tool.result", Outcome: &observe.Outcome{Status: "unknown"}})
	if m.FailureEvidence != 3 || m.ReportedErrors != 1 || m.APIErrors != 1 || m.HookErrors != 1 || m.UnknownOutcomes != 1 {
		t.Fatalf("%+v", m)
	}
	if requestTitle("Caveat: inspect this failure") != "Caveat: inspect this failure" {
		t.Fatal("ordinary user prompt hidden")
	}
}

func TestSessionSearchCrossesPagesAndKeepsDiagnosticsAndEvidence(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(root, "rollout-search.jsonl")
	var data strings.Builder
	for i := 0; i < 5200; i++ {
		data.WriteString(`{"type":"response_item","payload":{"type":"message","role":"user","content":"filler"}}` + "\n")
	}
	data.WriteString("not-json\n")
	data.WriteString(`{"type":"response_item","payload":{"type":"function_call","call_id":"searched","name":"exec_command","arguments":"complete request"}}` + "\n")
	data.WriteString(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"searched","output":"{\"exit_code\":2}"}}` + "\n")
	data.WriteString(`{"type":"response_item","payload":{"type":"message","role":"assistant","content":"later reply"}}` + "\n")
	if err := os.WriteFile(path, []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	s, _ := NewServer(Options{Roots: []Root{{Agent: "codex", Path: root}}, IncludeContent: true, Poll: time.Second, MaxRecordBytes: 4096}, "127.0.0.1:7433")
	s.Store.Apply(observe.Event{Agent: "codex", Source: observe.Source{Path: path, Generation: 1}})
	for _, query := range []url.Values{{"kind": {"failures"}}, {"call": {"searched"}, "kind": {"tool.requested"}}, {"after_call": {"searched"}}, {"query": {"later reply"}}} {
		params := query
		params.Set("id", sourceID("codex", path))
		entries := []Entry{}
		diagnostics := 0
		var reader string
		for page := 0; page < 40; page++ {
			r := httptest.NewRequest("GET", "http://"+s.authority+"/api/history?"+params.Encode(), nil)
			r.Header.Set("Authorization", "Bearer "+s.token)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			var p historyPage
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if reader != "" && p.Source.ReaderID != reader {
				t.Fatal("search changed reader")
			}
			reader = p.Source.ReaderID
			entries = append(entries, p.Events...)
			for _, n := range p.Diagnostics {
				diagnostics += n
			}
			if p.Next == "" {
				if p.Scanned != 5204 {
					t.Fatal("missing coverage", p.Scanned)
				}
				break
			}
			params.Set("cursor", p.Next)
		}
		if len(entries) != 1 || diagnostics != 1 {
			t.Fatalf("query=%v entries=%d diagnostics=%d", query, len(entries), diagnostics)
		}
		if query.Get("kind") == "failures" && (entries[0].Request == nil || entries[0].Request.Content != "complete request") {
			t.Fatal("search lost prior request")
		}
		if query.Get("after_call") != "" && entries[0].Content != "later reply" {
			t.Fatal("following reply missing")
		}
	}
	if s.evidenceBudget.Used() != 0 {
		t.Fatal("finished history retains pairing budget", s.evidenceBudget.Used())
	}
}
