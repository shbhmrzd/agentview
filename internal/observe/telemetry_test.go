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

func TestCodexTelemetryContextAndReset(t *testing.T) {
	if coordination("functions.wait", `{"cell_id":"execution-cell"}`) != nil {
		t.Fatal("execution wait mislabeled as agent coordination")
	}
	if c := coordination("wait", `{"ids":["child"]}`); c == nil || c.Target != "child" {
		t.Fatal("agent wait target lost")
	}
	n, _ := NewNormalizer("codex", false)
	source := Source{Path: "fixture", Generation: 1}
	read := func(raw string) Event { return n.Normalize(Record{Source: source, Data: []byte(raw)}, time.Now())[0] }
	meta := read(`{"type":"session_meta","payload":{"id":"child","session_id":"root","cwd":"/workspace","source":{"subagent":{"thread_spawn":{"parent_thread_id":"root","agent_nickname":"Reviewer","agent_role":"review","agent_path":"/root/review"}}}}}`)
	if meta.AgentContext.ParentThreadID != "root" || meta.AgentContext.Name != "Reviewer" || meta.GroupSessionID != "root" || meta.WorkingDirectory != "/workspace" {
		t.Fatalf("missing context: %+v", meta)
	}
	read(`{"type":"turn_context","payload":{"model":"test-model","effort":"high"}}`)
	usage := read(`{"type":"token_usage_record","payload":{"response_id":"r1","thread_token_usage":{"input_tokens":100,"cached_input_tokens":80,"output_tokens":10,"total_tokens":110}}}`)
	if usage.Kind != "usage.snapshot" || usage.AgentContext.Model != "test-model" || usage.UsageBasis != "codex_thread_cumulative" || *usage.Usage.Total != 110 {
		t.Fatalf("usage %+v", usage)
	}
	foreign := read(`{"type":"token_usage_record","payload":{"thread_id":"another-thread","thread_token_usage":{"total_tokens":999}}}`)
	if foreign.Usage != nil {
		t.Fatal("usage attributed to the wrong native thread")
	}
	call := read(`{"type":"response_item","payload":{"type":"function_call","name":"collaboration.send_message","call_id":"c","arguments":"{\"target\":\"child\",\"message\":\"PRIVATE_MESSAGE\"}"}}`)
	if call.Coordination == nil || call.Coordination.Target != "child" || call.Content != "" {
		t.Fatalf("coordination/content: %+v", call)
	}
	encoded, _ := json.Marshal(call)
	if strings.Contains(string(encoded), "PRIVATE_MESSAGE") {
		t.Fatal("metadata leaked message")
	}
	invalid := read(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":-1}}}}`)
	if invalid.Usage != nil {
		t.Fatal("negative usage accepted")
	}
	source.Generation++
	reset := read(`{"type":"response_item","payload":{"type":"message","role":"user","content":"hello"}}`)
	if reset.AgentContext.Model != "" || reset.AgentContext.ParentThreadID != "" || reset.WorkingDirectory != "" || reset.SessionID != "" {
		t.Fatalf("generation retained context: %+v", reset)
	}
}

func TestClaudeUsageAndReasoningOmission(t *testing.T) {
	n, _ := NewNormalizer("claude", true)
	data := `{"type":"assistant","sessionId":"s","agentId":"child","cwd":"/work","message":{"id":"m1","model":"test-model","usage":{"input_tokens":3,"cache_read_input_tokens":50,"cache_creation_input_tokens":7,"output_tokens":10},"content":[{"type":"thinking","thinking":"NEVER_EXPOSE"},{"type":"text","text":"hello"}]}}`
	events := n.Normalize(Record{Source: Source{Generation: 1}, Data: []byte(data)}, time.Now())
	last := events[len(events)-1]
	if last.UsageBasis != "claude_message" || last.UsageID != "m1" || *last.Usage.Cached != 50 || last.AgentContext.AgentID != "child" {
		t.Fatalf("usage: %+v", last)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), "NEVER_EXPOSE") {
		t.Fatal("reasoning leaked")
	}
}

func TestCodexInheritedParentHistoryCannotStealChildIdentity(t *testing.T) {
	n, _ := NewNormalizer("codex", true)
	read := func(raw string) Event {
		return n.Normalize(Record{Source: Source{Path: "child-rollout", Generation: 1}, Data: []byte(raw)}, time.Now())[0]
	}
	read(`{"ordinal":0,"type":"session_meta","payload":{"id":"child","session_id":"root","parent_thread_id":"root","agent_nickname":"Reviewer","subagent_history_start_ordinal":3}}`)
	inheritedMeta := read(`{"ordinal":1,"type":"session_meta","payload":{"id":"root","session_id":"root"}}`)
	inheritedMessage := read(`{"ordinal":2,"type":"response_item","payload":{"type":"message","role":"user","content":"INHERITED_PARENT_PROMPT"}}`)
	if inheritedMeta.SessionID != "child" || !inheritedMeta.Inherited || inheritedMessage.Content != "" || !inheritedMessage.Inherited {
		t.Fatal("parent context leaked into child projection")
	}
	own := read(`{"ordinal":3,"type":"response_item","payload":{"type":"message","role":"assistant","content":"child work"}}`)
	if own.Inherited || own.SessionID != "child" || own.Content != "child work" || own.AgentContext.Name != "Reviewer" || own.AgentContext.ParentThreadID != "root" {
		t.Fatalf("child context corrupted: %+v", own)
	}
	// Older sources can replay metadata without ordinals; first owner still wins.
	late := read(`{"type":"session_meta","payload":{"id":"root","cwd":"/parent"}}`)
	if late.SessionID != "child" || late.AgentContext.Name != "Reviewer" || late.Kind != "record.ignored" {
		t.Fatal("unbounded parent metadata replaced owner")
	}
}

func TestReadHistoryBoundedCancelledAndSymlinkSafe(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(root, "session.jsonl")
	line := `{"type":"user","sessionId":"s","message":{"content":"hello"}}` + "\n"
	if err := os.WriteFile(path, []byte(strings.Repeat(line, 3)), 0600); err != nil {
		t.Fatal(err)
	}
	var events []Event
	limited, err := ReadHistory(context.Background(), root, path, "claude", NormalizerOptions{}, 1024, int64(len(line)*2), func(e Event) { events = append(events, e) })
	if err != nil || !limited || len(events) != 2 {
		t.Fatalf("limit: %v %v %d", limited, err, len(events))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadHistory(ctx, root, path, "claude", NormalizerOptions{}, 1024, 1000, func(Event) {}); err == nil {
		t.Fatal("cancel ignored")
	}
	link := filepath.Join(root, "link.jsonl")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHistory(context.Background(), root, link, "claude", NormalizerOptions{}, 1024, 1000, func(Event) {}); err == nil {
		t.Fatal("followed symlink")
	}
}
