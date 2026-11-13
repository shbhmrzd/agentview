//go:build integration && (darwin || linux)

package integration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shbhmrzd/agentview/internal/dashboard"
)

func TestDashboardProcessesDiscoverAndFollow(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			root := directoryRoot(t)
			writer := startFixtureWriter(t, filepath.Join(root, "rollout-parent.jsonl"))
			writer.send(t, "append", seed(agent, "parent"))
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, agentviewBinary, "serve", "--agent", agent, "--dir", root, "--listen", "127.0.0.1:0", "--poll", "100ms", "--include-content")
			stderr, err := os.Create(filepath.Join(t.TempDir(), "server.stderr"))
			if err != nil {
				t.Fatal(err)
			}
			defer stderr.Close()
			cmd.Stderr = stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			stopped := false
			defer func() {
				if !stopped {
					cmd.Process.Kill()
					cmd.Wait()
				}
			}()
			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() {
				t.Fatal("dashboard did not print URL")
			}
			address, err := url.Parse(strings.TrimPrefix(scanner.Text(), "AgentView dashboard: "))
			if err != nil {
				t.Fatal(err)
			}
			fragment, _ := url.ParseQuery(address.Fragment)
			token := fragment.Get("token")
			address.Fragment = ""
			client := &http.Client{Timeout: time.Second}
			fetch := func(path string, authorized bool) ([]byte, int) {
				t.Helper()
				req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimSuffix(address.String(), "/")+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				if authorized {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				return body, resp.StatusCode
			}
			if _, status := fetch("/api/sessions", false); status != 401 {
				t.Fatalf("unauthorized: %d", status)
			}
			wait := func(check func(dashboard.Snapshot) bool) dashboard.Snapshot {
				t.Helper()
				deadline := time.Now().Add(operationDeadline)
				for time.Now().Before(deadline) {
					body, status := fetch("/api/sessions", true)
					if status != 200 {
						t.Fatalf("snapshot: %d %s", status, body)
					}
					var snap dashboard.Snapshot
					if err := json.Unmarshal(body, &snap); err != nil {
						t.Fatal(err)
					}
					if check(snap) {
						return snap
					}
					time.Sleep(30 * time.Millisecond)
				}
				t.Fatal("dashboard condition timed out")
				return dashboard.Snapshot{}
			}
			snap := wait(func(s dashboard.Snapshot) bool { return len(s.Sessions) == 1 && s.Sessions[0].Status == "watching" })
			parentID := snap.Sessions[0].ID
			if snap.Sessions[0].LiveCount != 0 || snap.Sessions[0].LastUpdateAt != "" {
				t.Fatal("old history presented as live")
			}
			body, status := fetch("/api/history?id="+parentID, true)
			if status != 200 || !strings.Contains(string(body), payloadCanary) {
				t.Fatalf("history: %d %s", status, body)
			}
			writer.send(t, "append", tool(agent, "parent", false))
			wait(func(s dashboard.Snapshot) bool {
				return s.Sessions[0].LiveCount == 1 && s.Sessions[0].LastUpdateAt != ""
			})
			body, status = fetch("/api/session?id="+parentID, true)
			if status != 200 || !strings.Contains(string(body), "tool.requested") {
				t.Fatalf("live: %s", body)
			}
			childPath := filepath.Join(root, "rollout-child.jsonl")
			if agent == "claude" {
				childPath = filepath.Join(root, "parent", "subagents", "agent-child.jsonl")
				if err := os.MkdirAll(filepath.Dir(childPath), 0700); err != nil {
					t.Fatal(err)
				}
			}
			child := startFixtureWriter(t, childPath)
			if agent == "codex" {
				child.send(t, "append", []byte(`{"ordinal":0,"type":"session_meta","payload":{"id":"child","session_id":"parent","parent_thread_id":"parent","subagent_history_start_ordinal":3}}`+"\n"+`{"ordinal":1,"type":"session_meta","payload":{"id":"parent"}}`+"\n"+`{"ordinal":2,"type":"response_item","payload":{"type":"message","role":"user","content":"inherited context"}}`+"\n"))
			} else {
				child.send(t, "append", []byte(`{"type":"user","sessionId":"child","agentId":"child","message":{"content":"child seed"}}`+"\n"))
			}
			wait(func(s dashboard.Snapshot) bool {
				for _, item := range s.Sessions {
					if item.SessionID == "child" && item.Context.ParentThreadID == "parent" && item.Status == "watching" {
						if item.LastUpdateAt != "" {
							t.Fatal("discovered history presented as new activity")
						}
						return true
					}
				}
				return false
			})
			body, status = fetch("/api/agents?id="+parentID, true)
			var family dashboard.AgentTree
			if status != 200 || json.Unmarshal(body, &family) != nil || len(family.Sessions) != 2 || family.Limited {
				t.Fatalf("agent family: %d %s", status, body)
			}
			for _, item := range family.Sessions {
				if item.SessionID == "parent" && (item.Overview == nil || item.Overview.FirstRequest == nil) {
					t.Fatal("parent request missing from family projection")
				}
			}
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			stopped = true
			// Killing the observer leaves both independent writers alive and writable.
			writer.send(t, "append", tool(agent, "parent", true))
			child.send(t, "append", tool(agent, "child", false))
			writer.assertUnchanged(t)
			child.assertUnchanged(t)
		})
	}
}
