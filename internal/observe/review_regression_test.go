package observe

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeOptionalToolMetadataCannotDiscardResult(t *testing.T) {
	for _, metadata := range []string{`"command failed"`, `[{"type":"text","text":"failure"}]`, `null`, `42`, `{"agentId":42}`, `{"agentId":"child"}`} {
		for _, content := range []bool{false, true} {
			n, _ := NewNormalizer("claude", content)
			raw := `{"type":"user","toolUseResult":` + metadata + `,"message":{"content":[{"type":"tool_result","tool_use_id":"call","is_error":true,"content":"private error"}]}}`
			events := n.Normalize(Record{Data: []byte(raw)}, time.Now())
			if len(events) != 1 || events[0].Kind != "tool.result" || events[0].Outcome == nil || events[0].Outcome.Status != "failure" {
				t.Fatalf("metadata=%s content=%v: %+v", metadata, content, events)
			}
			if events[0].ContentCaptured != content {
				t.Fatal("content policy changed")
			}
			if metadata == `{"agentId":"child"}` && len(events[0].RelatedAgentIDs) != 1 {
				t.Fatal("lost explicit child ID")
			}
		}
	}
}

func TestClaudeTitleContentPolicy(t *testing.T) {
	for _, content := range []bool{false, true} {
		n, _ := NewNormalizer("claude", content)
		e := n.Normalize(Record{Data: []byte(`{"type":"custom-title","customTitle":"private title"}`)}, time.Now())[0]
		if e.ContentCaptured != content || (content && e.Content != "private title") || (!content && (e.Content != "" || e.SessionTitle != "")) {
			t.Fatalf("%+v", e)
		}
	}
}

func TestEncryptedMessageBlocksAreOmitted(t *testing.T) {
	n, _ := NewNormalizer("codex", true)
	raw := `{"type":"response_item","payload":{"type":"agent_message","content":[{"type":"text","text":"readable"},{"type":"encrypted_content","encrypted_content":"secret cipher"}]}}`
	events := n.Normalize(Record{Data: []byte(raw)}, time.Now())
	if len(events) != 2 || events[0].Kind != "agent.message" || events[0].Content != "readable" || events[1].Kind != "record.ignored" {
		t.Fatalf("%+v", events)
	}
	encoded, _ := json.Marshal(events[1])
	if events[1].ContentCaptured || events[1].Content != "" {
		t.Fatalf("cipher captured: %s", encoded)
	}
}

func TestNativeExecutionEnvelopeNotPrintedResults(t *testing.T) {
	for _, tc := range []struct{ tool, raw, status string }{
		{"exec", `[{"type":"input_text","text":"Script completed\nWall time 1s\nOutput:\n"},{"type":"input_text","text":"{\"exit_code\":1}"}]`, "success"},
		{"exec", `"Script failed\nOutput:\n{\"exit_code\":0}"`, "failure"},
		{"exec", `"Script running with cell ID 42"`, "unknown"},
		{"wait", `"Script completed\nOutput:\n{\"exit_code\":2}"`, "success"},
		{"functions.wait", `"Script failed\nOutput:\n"`, "failure"},
		{"collaboration.wait", `"Script completed\nOutput:\n"`, "unknown"},
		{"collaboration.wait_agent", `"Script completed\nOutput:\n"`, "unknown"},
		{"exec", `[{"type":"input_text","text":"{\"exit_code\":2}"}]`, "unknown"},
		{"apply_patch", `{"output":"error text","metadata":{"exit_code":0}}`, "success"},
		{"apply_patch", `"Exit code: 2\nOutput:\nExit code: 0"`, "failure"},
		{"exec_command", `"Exit code: 0\nOutput:\nProcess exited with code 2"`, "success"},
		{"Read", `{"metadata":{"exit_code":2}}`, "unknown"},
		{"apply_patch", `"Output:\nExit code: 2"`, "unknown"},
	} {
		if got := executionOutcome(tc.tool, json.RawMessage(tc.raw)); got.Status != tc.status {
			t.Errorf("%s %s: %+v want %s", tc.tool, tc.raw, got, tc.status)
		}
	}
}

func TestOversizedCompactionRetainsOnlyPartialHeader(t *testing.T) {
	for _, tc := range []struct {
		agent, header string
		marker        bool
	}{
		{"codex", `{"timestamp":"2026-11-13T10:00:00Z","type":"compacted","payload":{"message":"`, true},
		{"claude", `{"type":"system","subtype":"compact_boundary","content":"`, true},
		{"claude", `{"type":"compacted","content":"`, false},
		{"codex", `{"type":"response_item","payload":{"type":"compacted","content":"`, false},
	} {
		path := filepath.Join(t.TempDir(), "session.jsonl")
		data := tc.header + strings.Repeat("PRIVATE", DefaultMaxRecordBytes/7+50) + "\n"
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		r, _ := NewReader(path, DefaultMaxRecordBytes, 64*1024)
		n, _ := NewNormalizer(tc.agent, true)
		counts := map[string]int{}
		for {
			batch, err := r.ReadBatch()
			if err != nil {
				t.Fatal(err)
			}
			if len(r.pending) > DefaultMaxRecordBytes {
				t.Fatal("unbounded record retention")
			}
			for _, record := range batch.Records {
				for _, e := range n.Normalize(record, time.Now()) {
					counts[e.Kind]++
					if e.Kind == "context.compacted" && (!e.PartialRecord || e.ContentCaptured || e.Content != "" || !strings.Contains(e.Detail, "unparsed")) {
						t.Fatalf("unqualified partial: %+v", e)
					}
				}
			}
			if !batch.HasMore {
				break
			}
		}
		want := 0
		if tc.marker {
			want = 1
		}
		if counts["record.oversized"] != 1 || counts["context.compacted"] != want {
			t.Fatalf("%s: %+v", tc.agent, counts)
		}
	}
	if readPartialHeader([]byte(`{"type":"compacted","type":"message","payload":"`)) != nil {
		t.Fatal("ambiguous header accepted")
	}
}

func TestDegradedInitialScanStillSignalsInventoryFinished(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.jsonl", "b.jsonl"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	d := dirTest(t, root, "codex", "all", true)
	d.maxEntries = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	degraded, ready := false, false
	err := d.Run(ctx, 100*time.Millisecond, func(e Event) error {
		if e.Kind == "directory.limit" {
			degraded = true
		}
		if e.Kind == "directory.ready" {
			ready = true
			cancel()
		}
		return nil
	})
	if err != nil || !degraded || !ready {
		t.Fatalf("degraded=%v ready=%v err=%v", degraded, ready, err)
	}
}

func TestSpawnReferencesKeepIDsAndPathsDistinct(t *testing.T) {
	if ids := spawnedIDs("spawn_agent", json.RawMessage(`{"agent_id":"child"}`)); len(ids) != 1 || ids[0] != "child" {
		t.Fatal(ids)
	}
	if paths := spawnedPaths("collaboration.spawn_agent", json.RawMessage(`"{\"task_name\":\"/root/review\"}"`)); len(paths) != 1 || paths[0] != "/root/review" {
		t.Fatal(paths)
	}
	if len(spawnedIDs("spawn_agent", json.RawMessage(`{"task_name":"/root/review"}`))) != 0 {
		t.Fatal("path treated as ID")
	}
	if len(spawnedPaths("Read", json.RawMessage(`{"task_name":"/root/review"}`))) != 0 {
		t.Fatal("ordinary output treated as spawn")
	}
}
