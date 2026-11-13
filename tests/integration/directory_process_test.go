//go:build integration && (darwin || linux)

package integration_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func directoryViewer(t *testing.T, agent, root, history string) *viewer {
	t.Helper()
	return startViewerArgs(t, "follow", "--agent", agent, "--dir", root, "--history", history, "--poll", "100ms", "--include-content")
}

func directoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func directoryBoundary(t *testing.T, v *viewer, kind, path string, allowHistory bool) []event {
	t.Helper()
	var events []event
	for {
		e := v.nextEvent(t)
		events = append(events, e)
		if !allowHistory && e.ContentCaptured {
			t.Fatalf("startup history leaked: %s", e.Raw)
		}
		if e.Kind == kind && (path == "" || e.Source.Path == path) {
			return events
		}
	}
}

func TestDirectoryProcessesNewAndResumedSessions(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			root := directoryRoot(t)
			oldDir := filepath.Join(root, "2026", "11", "12")
			if err := os.MkdirAll(oldDir, 0700); err != nil {
				t.Fatal(err)
			}
			oldPath := filepath.Join(oldDir, "rollout-existing.jsonl")
			oldWriter := startFixtureWriter(t, oldPath)
			oldWriter.send(t, "append", seed(agent, "existing-session"))
			v := directoryViewer(t, agent, root, "none")
			startup := directoryBoundary(t, v, "source.ready", oldPath, false)
			if startup[len(startup)-1].SessionID != "existing-session" {
				t.Fatal("skipping history lost native context")
			}
			// Completion after the attachment boundary must retain its full record.
			request := tool(agent, "existing-session", false)
			oldWriter.send(t, "append", request[:len(request)/2])
			oldWriter.send(t, "append", request[len(request)/2:])
			assertEvent(t, v.next(t, "tool.requested"), agent, "existing-session", "native-tool", oldPath, 1, true)
			newDir := filepath.Join(root, "2026", "11", "13", "new-project")
			if err := os.MkdirAll(newDir, 0700); err != nil {
				t.Fatal(err)
			}
			newPath := filepath.Join(newDir, "rollout-new.jsonl")
			newWriter := startFixtureWriter(t, newPath)
			newWriter.send(t, "append", seed(agent, "new-session"))
			// Discovery may race the writer's seed; consume both readiness and seed
			// barriers in either order without accepting a prompt echo as a tool.
			var ready, message bool
			for !ready || !message {
				e := v.nextEvent(t)
				switch e.Kind {
				case "source.discovered", "session.metadata":
				case "source.ready":
					ready = e.Source.Path == newPath
				case "message.user":
					assertEvent(t, e, agent, "new-session", "native-seed", newPath, 1, true)
					message = true
				default:
					t.Fatalf("unexpected new-source event: %s", e.Raw)
				}
			}
			// Both independent writers remain observed without switching --file.
			oldWriter.send(t, "append", tool(agent, "existing-session", true))
			newWriter.send(t, "append", tool(agent, "new-session", false))
			found := map[string]string{}
			for i := 0; i < 2; i++ {
				e := v.nextEvent(t)
				found[e.SessionID] = e.Kind
			}
			if found["existing-session"] != "tool.result" || found["new-session"] != "tool.requested" {
				t.Fatalf("cross-source state bleed: %+v", found)
			}
			v.stop(t, os.Interrupt)
			oldWriter.send(t, "append", tool(agent, "existing-session", false))
			newWriter.send(t, "append", tool(agent, "new-session", true))
			oldWriter.assertUnchanged(t)
			newWriter.assertUnchanged(t)
		})
	}
}

func TestDirectoryProcessesHistoryAndRecovery(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			root := directoryRoot(t)
			path := filepath.Join(root, "rollout-session.jsonl")
			writer := startFixtureWriter(t, path)
			writer.send(t, "append", seed(agent, "original"))
			v := directoryViewer(t, agent, root, "all")
			startup := directoryBoundary(t, v, "source.ready", path, true)
			messages := 0
			for _, e := range startup {
				if e.Kind == "message.user" {
					messages++
				}
			}
			if messages != 1 {
				t.Fatal("history mode did not include seed exactly once")
			}
			writer.send(t, "hide", nil)
			v.next(t, "source.unavailable")
			writer.send(t, "show", nil)
			v.next(t, "source.available")
			writer.send(t, "rotate", seed(agent, "replacement"))
			v.next(t, "source.replaced")
			readSeed(t, v, agent, "replacement", path, 2, true)
			writer.send(t, "append", tool(agent, "replacement", false))
			assertEvent(t, v.next(t, "tool.requested"), agent, "replacement", "native-tool", path, 2, true)
			v.stop(t, os.Interrupt)
			writer.assertUnchanged(t)
			// A separate finite inspection reprocesses history without changing it.
			cmd := exec.Command(agentviewBinary, "inspect", "--agent", agent, "--dir", root)
			var out, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("directory inspect: %v %s", err, stderr.String())
			}
			if strings.Contains(out.String(), payloadCanary) || !strings.Contains(out.String(), `"kind":"message.user"`) {
				t.Fatal("inspection history/content policy failed")
			}
			writer.assertUnchanged(t)
		})
	}
}
