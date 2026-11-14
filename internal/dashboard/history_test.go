package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shbhmrzd/agentview/internal/observe"
)

func TestPagedHistoryPreservesAllRecordsAndCrossPagePairs(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(root, "rollout-test.jsonl")
	var data strings.Builder
	data.WriteString(`{"type":"session_meta","payload":{"id":"parent"}}` + "\n")
	for i := 0; i < 198; i++ {
		fmt.Fprintf(&data, "{\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":\"message %d\"}}\n", i)
	}
	data.WriteString(`{"type":"response_item","payload":{"type":"function_call","call_id":"call","name":"exec_command","arguments":"payload"}}` + "\n")
	data.WriteString(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"call","output":"{\"exit_code\":2}"}}` + "\n")
	data.WriteString(`{"type":"response_item","payload":{"type":"message","role":"assistant","content":"follow-up"}}` + "\n")
	if err := os.WriteFile(path, []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	s, _ := NewServer(Options{Roots: []Root{{Agent: "codex", Path: root}}, IncludeContent: true, Poll: time.Second, MaxRecordBytes: 1024}, "127.0.0.1:7433")
	s.Store.Apply(observe.Event{Agent: "codex", Source: observe.Source{Path: path, Generation: 1}})
	get := func(cursor string) historyPage {
		t.Helper()
		r := httptest.NewRequest("GET", "http://"+s.authority+"/api/history?id="+sourceID("codex", path)+"&cursor="+cursor, nil)
		r.Header.Set("Authorization", "Bearer "+s.token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var page historyPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	a := get("")
	b := get(a.Next)
	if len(a.Events) != 199 || len(b.Events) != 2 || b.Next != "" {
		t.Fatalf("page loss: %d %d next=%s", len(a.Events), len(b.Events), b.Next)
	}
	if b.Events[0].Request == nil || b.Events[0].Request.Content != "payload" || !b.Events[0].Error || len(b.Events[1].PrecedingCalls) != 1 {
		t.Fatalf("lost cross-page evidence: %+v", b.Events)
	}
	if a.Source.ReaderID == "" || a.Source.ReaderID != b.Source.ReaderID {
		t.Fatal("page reader identity changed")
	}
	if retry := get(a.Next); len(retry.Events) != 2 {
		t.Fatal("retry lost page")
	}
}

func TestRedactedExportOmitsPrivateFieldsAndMarksCompletion(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(root, "rollout-PRIVATE_FIXTURE.jsonl")
	data, _ := os.ReadFile("../../testdata/native-shapes/codex.jsonl")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	s, _ := NewServer(Options{Roots: []Root{{Agent: "codex", Path: root}}, IncludeContent: true, Poll: time.Second, MaxRecordBytes: 4096}, "127.0.0.1:7433")
	s.Store.Apply(observe.Event{Agent: "codex", Source: observe.Source{Path: path, Generation: 1}})
	r := httptest.NewRequest("GET", "http://"+s.authority+"/api/export?id="+sourceID("codex", path), nil)
	r.Header.Set("Authorization", "Bearer "+s.token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, "PRIVATE_FIXTURE") || strings.Contains(body, root) || strings.Contains(body, "fixture-parent") || !strings.Contains(body, `"complete":true`) || !strings.Contains(body, `"status":"failure"`) {
		t.Fatal(body)
	}
}

func TestHistoryRejectsReplacementBetweenPages(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(root, "rollout.jsonl")
	line := `{"type":"response_item","payload":{"type":"message","role":"user","content":"hello"}}` + "\n"
	os.WriteFile(path, []byte(strings.Repeat(line, 1100)), 0600)
	p, err := observe.NewHistoryPager(root, path, "codex", observe.NormalizerOptions{}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = p.Next(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path+".tmp", []byte(line), 0600)
	os.Rename(path+".tmp", path)
	for i := 0; i < 3; i++ {
		_, _, err = p.Next(context.Background(), 1000)
		if err != nil {
			return
		}
	}
	t.Fatal("replacement mixed into paged history")
}

func TestHistoryContinuesBeyondFormer64MiBLimit(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(root, "rollout-large.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	blank := strings.Repeat("\n", 1024*1024)
	for i := 0; i < 65; i++ {
		if _, err := f.WriteString(blank); err != nil {
			t.Fatal(err)
		}
	}
	_, err = f.WriteString(`{"type":"response_item","payload":{"type":"message","role":"user","content":"beyond old cap"}}` + "\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	p, err := observe.NewHistoryPager(root, path, "codex", observe.NormalizerOptions{IncludeContent: true}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for page := 0; page < 100; page++ {
		events, more, err := p.Next(context.Background(), 200)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			found = found || e.Content == "beyond old cap"
		}
		if !more {
			break
		}
	}
	if !found {
		t.Fatal("later history was omitted")
	}
}

func TestDirectoryWarningOnlyClearsOnExplicitRecovery(t *testing.T) {
	s := NewStore(false)
	e := observe.Event{Agent: "codex", Source: observe.Source{Path: "root"}, Kind: "directory.limit", Detail: "incomplete"}
	s.Apply(e)
	e.Kind = "directory.ready"
	s.Apply(e)
	if s.Snapshot().Roots[0].Status != "degraded" || s.Snapshot().Diagnostics["directory.limit"] != 1 {
		t.Fatal("warning was lost")
	}
	e.Kind = "directory.recovered"
	s.Apply(e)
	if s.Snapshot().Roots[0].Status != "watching" {
		t.Fatal("recovery not visible")
	}
}

func TestDuplicateCallIDsDoNotInventRequestResultPairs(t *testing.T) {
	c := observe.NewCallTracker(nil)
	defer c.Close()
	a := observe.Event{Kind: "tool.requested", ToolCallID: "same"}
	c.Apply(&a)
	c.Apply(&a)
	b := observe.Event{Kind: "tool.result", ToolCallID: "same"}
	c.Apply(&b)
	entry, _ := displayEvent(b)
	if entry.Request != nil || entry.Gap == "" {
		t.Fatal("ambiguous call paired")
	}
}
func TestOrphanRequestsCannotStarveLaterPairing(t *testing.T) {
	c := observe.NewCallTracker(nil)
	defer c.Close()
	for i := 0; i < 300; i++ {
		e := observe.Event{Kind: "tool.requested", ToolCallID: fmt.Sprint(i), Content: "request"}
		c.Apply(&e)
	}
	recent := observe.Event{Kind: "tool.result", ToolCallID: "299"}
	c.Apply(&recent)
	if recent.Call.Request == nil || recent.Call.Request.ToolCallID != "299" {
		t.Fatal("recent pair lost")
	}
	old := observe.Event{Kind: "tool.result", ToolCallID: "0"}
	c.Apply(&old)
	if old.Call.Request != nil || !strings.Contains(old.Call.Detail, "evicted") {
		t.Fatal("evicted request paired")
	}
	if requestTitle("<local-command-caveat>Caveat: local command</local-command-caveat>") != "" {
		t.Fatal("caveat used as title")
	}
}
