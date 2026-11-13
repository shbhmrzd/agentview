package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shbhmrzd/agentview/internal/observe"
)

func TestTextOutputSummarizesStartupAndRepeatedDiagnostics(t *testing.T) {
	var out bytes.Buffer
	w := newEventOutput(&out, "text", "follow", "none")
	source := observe.Source{Path: "/test/session.jsonl", Kind: "jsonl"}
	write := func(e observe.Event) {
		t.Helper()
		e.Agent = "codex"
		e.Source = source
		if err := w.write(e); err != nil {
			t.Fatal(err)
		}
	}
	write(observe.Event{Kind: "source.discovered"})
	write(observe.Event{Kind: "directory.ready"})
	for i := 0; i < 100; i++ {
		write(observe.Event{Kind: "record.oversized", ObservationPhase: "history"})
	}
	write(observe.Event{Kind: "source.ready"})
	for i := 0; i < 100; i++ {
		write(observe.Event{Kind: "record.unsupported", NativeType: "event_msg:item_completed"})
		write(observe.Event{Kind: "record.ignored", NativeType: "response_item:reasoning", Content: "EXCLUDED_REASONING", ContentCaptured: true})
	}
	write(observe.Event{Kind: "tool.requested", SessionID: "session", ToolName: "Bash", Content: "go test ./...", ContentCaptured: true})
	text := out.String()
	if strings.Contains(text, "EXCLUDED_REASONING") || strings.Count(text, "event_msg:item_completed") != 1 || strings.Count(text, "\n") != 4 {
		t.Fatalf("noisy text output: %s", text)
	}
	if !strings.Contains(text, "100 oversized historical records") || !strings.Contains(text, "Bash go test ./...") {
		t.Fatalf("lost summary/activity: %s", text)
	}
}

func TestOutputBoundsControlsAndPreservesRawJSON(t *testing.T) {
	events := []observe.Event{
		{Agent: "claude", Kind: "message.user", SessionID: "s\x1b[2J", ContentCaptured: true, Content: "line\n\x1b[2J" + strings.Repeat("a", 2000)},
		{Agent: "claude", Kind: "record.ignored", NativeType: "reasoning", Detail: "Intentionally omitted"},
	}
	for _, format := range []string{"text", "jsonl"} {
		var out bytes.Buffer
		w := newEventOutput(&out, format, "inspect", "all")
		for _, e := range events {
			if err := w.write(e); err != nil {
				t.Fatal(err)
			}
		}
		if bytes.Contains(out.Bytes(), []byte{0x1b}) {
			t.Fatal("raw terminal control escaped output")
		}
		if format == "text" {
			if out.Len() > 1200 || !strings.Contains(out.String(), "truncated") || strings.Count(out.String(), "\n") != 1 {
				t.Fatalf("unbounded terminal output: %s", out.String())
			}
		} else {
			decoder := json.NewDecoder(&out)
			for _, expected := range events {
				var actual observe.Event
				if err := decoder.Decode(&actual); err != nil || actual.Kind != expected.Kind || actual.Content != expected.Content {
					t.Fatalf("raw output changed: %+v %v", actual, err)
				}
			}
		}
	}
	var out bytes.Buffer
	if format, err := outputFormat(&out, "auto"); err != nil || format != "jsonl" {
		t.Fatal("redirected output default changed")
	}
	if _, err := outputFormat(&out, "bad"); err == nil {
		t.Fatal("invalid output format accepted")
	}
}
