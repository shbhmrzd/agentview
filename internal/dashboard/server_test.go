package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shbhmrzd/agentview/internal/observe"
)

func TestDashboardAuthorizationAndReadOnlyAPI(t *testing.T) {
	root := t.TempDir()
	s, err := NewServer(Options{Roots: []Root{{Agent: "codex", Path: root}}, Poll: time.Second, MaxRecordBytes: 1024}, "127.0.0.1:7433")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, host, origin, token, method string
		status                            int
	}{
		{"/", s.authority, "", "", "GET", 200},
		{"/app.js", s.authority, "", "", "GET", 200},
		{"/helpers.mjs", s.authority, "", "", "GET", 200},
		{"/conversations.mjs", s.authority, "", "", "GET", 200},
		{"/tour.mjs", s.authority, "", "", "GET", 200},
		{"/theme.js", s.authority, "", "", "GET", 200},
		{"/themes.css", s.authority, "", "", "GET", 200},
		{"/api/sessions", s.authority, "", "", "GET", 401},
		{"/api/agents?id=unknown", s.authority, "", "", "GET", 401},
		{"/api/agents?id=unknown", s.authority, "", s.token, "GET", 404},
		{"/api/sessions", s.authority, "", s.token, "GET", 200},
		{"/api/sessions", s.authority, "https://unrelated.example", s.token, "GET", 403},
		{"/api/sessions", "unrelated.example", "", s.token, "GET", 403},
		{"/api/sessions", s.authority, "", s.token, "POST", 405},
		{"/api/history?id=/etc/passwd", s.authority, "", s.token, "GET", 404},
		{"/store.jsonl", s.authority, "", s.token, "GET", 404},
	} {
		r := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
		if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("missing isolation")
		}
		if tc.path == "/" && strings.Contains(w.Body.String(), s.token) {
			t.Fatal("token leaked in unauthenticated asset")
		}
	}
	if err := Serve(context.Background(), "0.0.0.0:0", s.options, os.Stdout); err == nil {
		t.Fatal("accepted public listener")
	}
}

func TestDashboardWatchHistoryAndNewFile(t *testing.T) {
	for _, content := range []bool{false, true} {
		t.Run(map[bool]string{false: "metadata", true: "content"}[content], func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			path := filepath.Join(root, "rollout-parent.jsonl")
			seed := `{"type":"session_meta","payload":{"id":"root","session_id":"root","cwd":"/example/project"}}` + "\n" + `{"type":"response_item","payload":{"type":"message","role":"user","content":"PRIVATE_CANARY <script>alert(1)</script>"}}` + "\n"
			if err := os.WriteFile(path, []byte(seed), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := NewServer(Options{Roots: []Root{{Agent: "codex", Path: root}}, IncludeContent: content, Poll: 100 * time.Millisecond, MaxRecordBytes: observe.DefaultMaxRecordBytes}, "127.0.0.1:7433")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); s.Watch(ctx) }()
			defer func() { cancel(); <-done }()
			wait := func(check func() bool) {
				t.Helper()
				deadline := time.Now().Add(4 * time.Second)
				for time.Now().Before(deadline) {
					if check() {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("watch condition timed out")
			}
			id := sourceID("codex", path)
			wait(func() bool { item, _, ok := s.Store.Session(id); return ok && item.Status == "watching" })
			item, events, _ := s.Store.Session(id)
			if item.LiveCount != 0 || len(events) != 0 || item.Metrics.Messages != 1 {
				t.Fatal("startup history leaked into live")
			}
			request := func(path string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodGet, "http://"+s.authority+path, nil)
				r.Header.Set("Authorization", "Bearer "+s.token)
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				return w
			}
			w := request("/api/history?id=" + id)
			if w.Code != 200 {
				t.Fatalf("history: %s", w.Body.String())
			}
			if strings.Contains(w.Body.String(), "PRIVATE_CANARY") != content {
				t.Fatal("history content policy failed")
			}
			if strings.Contains(w.Body.String(), "<script>") {
				t.Fatal("JSON HTML escaping lost")
			}
			child := filepath.Join(root, "rollout-child.jsonl")
			if err := os.WriteFile(child, []byte(`{"type":"session_meta","payload":{"id":"child","session_id":"root","parent_thread_id":"root"}}`+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			wait(func() bool {
				item, _, ok := s.Store.Session(sourceID("codex", child))
				return ok && item.Context.ParentThreadID == "root"
			})
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.WriteString(`{"type":"response_item","payload":{"type":"message","role":"assistant","content":"LIVE_CANARY"}}` + "\n")
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			wait(func() bool { item, _, _ := s.Store.Session(id); return item.LiveCount == 1 })
			w = request("/api/session?id=" + id)
			var result struct {
				Events []Entry `json:"events"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Events) != 1 || result.Events[0].Captured != content {
				t.Fatalf("live: %s", w.Body.String())
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			request("/api/history?id=" + id)
			after, _ := os.ReadFile(path)
			info, _ := os.Stat(path)
			if string(before) != string(after) || info.Mode().Perm() != 0600 {
				t.Fatal("observer modified source")
			}
		})
	}
}
