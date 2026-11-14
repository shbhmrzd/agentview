package dashboard_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shbhmrzd/agentview/internal/dashboard"
	"github.com/shbhmrzd/agentview/internal/observe"
)

func start(t *testing.T, root string) *dashboard.Server {
	t.Helper()
	server, err := dashboard.NewServer(dashboard.Options{Roots: []dashboard.Root{{Agent: "codex", Path: root}}, IncludeContent: true, Poll: 100 * time.Millisecond, MaxRecordBytes: observe.DefaultMaxRecordBytes}, "127.0.0.1:7449")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); server.Watch(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return server
}
func wait(t *testing.T, condition func() bool) {
	t.Helper()
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("setup timeout")
}
func history(t *testing.T, s *dashboard.Server, id string) []dashboard.Entry {
	t.Helper()
	u, _ := url.Parse(s.URL())
	fragment, _ := url.ParseQuery(u.Fragment)
	r := httptest.NewRequest("GET", "http://"+u.Host+"/api/history?id="+id, nil)
	r.Header.Set("Authorization", "Bearer "+fragment.Get("token"))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("history status %d: %s", w.Code, w.Body.String())
	}
	var v struct {
		Events []dashboard.Entry `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v.Events
}
func seed(text string) []byte {
	return []byte(`{"type":"session_meta","payload":{"id":"main"}}` + "\n" + `{"type":"response_item","payload":{"type":"message","role":"user","content":"` + text + `"}}` + "\n")
}

func TestInitialCoverageLimitRemainsVisible(t *testing.T) {
	root := t.TempDir()
	path := root
	for i := 0; i < 70; i++ {
		path = filepath.Join(path, "nested")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "rollout-hidden.jsonl"), seed("hidden"), 0600); err != nil {
		t.Fatal(err)
	}
	s := start(t, root)
	wait(t, func() bool {
		snap := s.Store.Snapshot()
		return len(snap.Roots) > 0 && snap.Roots[0].Status == "degraded"
	})
	time.Sleep(250 * time.Millisecond)
	snap := s.Store.Snapshot()
	t.Logf("root=%s sources=%d diagnostics=%v detail=%q", snap.Roots[0].Status, len(snap.Sessions), snap.Diagnostics, snap.Roots[0].Detail)
	if snap.Roots[0].Status != "degraded" {
		t.Fatal("coverage limit was overwritten by ready; dashboard reports watching with no diagnostic")
	}
}
func TestHistoryAndLiveGenerationsHaveDistinctReaderNamespaces(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout-main.jsonl")
	if err := os.WriteFile(path, seed("before"), 0600); err != nil {
		t.Fatal(err)
	}
	s := start(t, root)
	wait(t, func() bool {
		snap := s.Store.Snapshot()
		return len(snap.Sessions) == 1 && snap.Sessions[0].Status == "watching"
	})
	swap := filepath.Join(root, "replacement.tmp")
	if err := os.WriteFile(swap, seed("after"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(swap, path); err != nil {
		t.Fatal(err)
	}
	wait(t, func() bool {
		snap := s.Store.Snapshot()
		return snap.Sessions[0].Generation == 2 && snap.Sessions[0].Metrics.Messages == 1
	})
	session := s.Store.Snapshot().Sessions[0]
	events := history(t, s, session.ID)
	t.Logf("current source generation=%d, history generation=%d, history content=%q", session.Generation, events[0].Source.Generation, events[0].Content)
	if session.ReaderID == "" || events[0].Source.ReaderID == "" || events[0].Source.ReaderID == session.ReaderID {
		t.Fatal("independent history/live generation counters lack distinct reader namespaces")
	}
}
func TestHistoryMissingTimestampIsNotUnlabelledWallClock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout-main.jsonl")
	if err := os.WriteFile(path, seed("no timestamp"), 0600); err != nil {
		t.Fatal(err)
	}
	s := start(t, root)
	wait(t, func() bool {
		return len(s.Store.Snapshot().Sessions) == 1 && s.Store.Snapshot().Sessions[0].Status == "watching"
	})
	id := s.Store.Snapshot().Sessions[0].ID
	a := history(t, s, id)
	time.Sleep(5 * time.Millisecond)
	b := history(t, s, id)
	t.Logf("unchanged historical event time changed from %s to %s", a[0].Time, b[0].Time)
	raw, _ := json.Marshal(b[0])
	if a[0].Time != "" || b[0].Time != "" || !strings.Contains(string(raw), "time_status") {
		t.Fatal("read time is shown as event time with no missing/source-time provenance")
	}
}
