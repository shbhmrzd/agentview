package dashboard

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shbhmrzd/agentview/internal/observe"
)

func counter(n int64) *int64 { return &n }
func sample(path string) observe.Event {
	return observe.Event{Agent: "codex", SessionID: path, Kind: "message.user", Source: observe.Source{Path: path, Generation: 1}, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), ObservationPhase: "update", Content: "hello", ContentCaptured: true}
}

func TestNewActivityRequiresAppendedConversationRecord(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			s := NewStore(false)
			e := sample("session")
			e.Agent = agent
			e.ObservedAt = "2026-11-13T08:00:00Z"
			e.SourceModifiedAt = e.ObservedAt
			assertUpdate := func(want string) {
				t.Helper()
				item, _, _ := s.Session(sourceID(agent, e.Source.Path))
				if item.LastUpdateAt != want {
					t.Fatalf("%s/%s: last update %q, want %q", e.ObservationPhase, e.Kind, item.LastUpdateAt, want)
				}
			}
			for _, phase := range []string{"history", "discovered_history", "replacement_history", ""} {
				e.ObservationPhase = phase
				s.Apply(e)
				assertUpdate("")
			}
			e.ObservationPhase = "update"
			for _, kind := range []string{"source.ready", "record.ignored", "record.unsupported", "session.started", "usage.snapshot"} {
				e.Kind = kind
				s.Apply(e)
				assertUpdate("")
			}
			for i, kind := range []string{"message.user", "message.assistant", "tool.requested", "tool.result", "turn.started", "turn.ended"} {
				e.Kind = kind
				e.ObservedAt = fmt.Sprintf("2026-11-13T08:00:%02dZ", i+1)
				s.Apply(e)
				assertUpdate(e.ObservedAt)
			}
			last := e.ObservedAt
			for _, timestamp := range []string{"2020-01-01T00:00:00Z", "", "invalid"} {
				e.ObservedAt = timestamp
				s.Apply(e)
				assertUpdate(last)
			}
			e.Source.Generation++
			e.ObservationPhase = "replacement_history"
			e.ObservedAt = "2026-11-13T08:10:00Z"
			s.Apply(e)
			assertUpdate("")
		})
	}
}

func TestStoreHistoryActivityIsolationAndBounds(t *testing.T) {
	s := NewStore(true)
	e := sample("old")
	e.Kind = "source.ready"
	e.ObservationPhase = ""
	e.SourceModifiedAt = "2020-01-01T00:00:00Z"
	s.Apply(e)
	history := sample("old")
	history.ObservationPhase = "history"
	s.Apply(history)
	old, events, _ := s.Session(sourceID("codex", "old"))
	if old.LiveCount != 0 || len(events) != 0 || old.LastActivity != "2020-01-01T00:00:00Z" || old.Metrics.Messages != 1 {
		t.Fatalf("history counted live: %+v", old)
	}
	for i := 0; i < maxLiveEvents+100; i++ {
		e := sample(fmt.Sprint(i % 20))
		e.Content = strings.Repeat("x", maxContentBytes+100)
		s.Apply(e)
	}
	retained := 0
	for _, item := range s.Snapshot().Sessions {
		_, events, _ := s.Session(item.ID)
		retained += len(events)
		for _, e := range events {
			if len(e.Content) > maxContentBytes || !e.Clipped {
				t.Fatal("content bound missing")
			}
		}
		if len(events) > maxSessionEvents {
			t.Fatal("per-source bound missing")
		}
	}
	if retained > maxLiveEvents {
		t.Fatalf("global retention: %d", retained)
	}
	e = sample("0")
	e.Source.Generation = 2
	e.SessionID = "replacement"
	s.Apply(e)
	item, events, _ := s.Session(sourceID("codex", "0"))
	if len(events) != 1 || item.Metrics.Messages != 1 || item.SessionID != "replacement" {
		t.Fatal("generation mixed")
	}
}

func TestMetricsSnapshotsDedupAndToolTiming(t *testing.T) {
	m := Metrics{}
	e := sample("s")
	e.Kind = "usage.snapshot"
	e.UsageBasis = "codex_thread_cumulative"
	e.Usage = &observe.TokenUsage{Total: counter(110)}
	m.apply(e)
	m.apply(e)
	if *m.Usage.Total != 110 {
		t.Fatal("summed cumulative snapshots")
	}
	e.UsageBasis = "codex_token_count"
	e.Usage = &observe.TokenUsage{Total: counter(999)}
	m.apply(e)
	if *m.Usage.Total != 110 {
		t.Fatal("fallback replaced authoritative thread usage")
	}
	claude := Metrics{}
	e.UsageBasis = "claude_message"
	e.UsageID = "m1"
	e.Usage = &observe.TokenUsage{Input: counter(10), Output: counter(5), Cached: counter(20), CacheWrite: counter(3)}
	claude.apply(e)
	claude.apply(e)
	if *claude.Usage.Total != 38 {
		t.Fatal("double-counted Claude message")
	}
	e.Usage = &observe.TokenUsage{Input: counter(10), Output: counter(8), Cached: counter(20), CacheWrite: counter(3)}
	claude.apply(e)
	if *claude.Usage.Total != 41 {
		t.Fatal("streamed usage update not replaced")
	}
	tracker := observe.NewCallTracker(nil)
	defer tracker.Close()
	e = sample("s")
	e.Kind = "tool.requested"
	e.ToolName = "shell"
	e.ToolCallID = "c"
	e.OccurredAt = "2026-01-01T00:00:00Z"
	tracker.Apply(&e)
	m.apply(e)
	e.Kind = "tool.result"
	e.OccurredAt = "2026-01-01T00:00:02Z"
	b := true
	e.IsError = &b
	tracker.Apply(&e)
	ms := m.apply(e)
	if ms == nil || *ms != 2000 || m.ReportedErrors != 1 || m.Tools["shell"].Timed != 1 {
		t.Fatal("call/result pairing failed")
	}
	tracker.Apply(&e)
	if m.apply(e) != nil {
		t.Fatal("paired duplicate result twice")
	}
}

func TestMetadataOnlyStoreAndSnapshotOwnership(t *testing.T) {
	s := NewStore(false)
	e := sample("s")
	e.AgentContext.Model = "model"
	s.Apply(e)
	_, events, _ := s.Session(sourceID("codex", "s"))
	if events[0].Content != "" || events[0].Captured {
		t.Fatal("content leak")
	}
	e.Kind = "tool.requested"
	e.ToolName = "shell"
	s.Apply(e)
	copy, _, _ := s.Session(sourceID("codex", "s"))
	copy.Metrics.Tools["shell"] = ToolMetric{Calls: 999}
	item, _, _ := s.Session(sourceID("codex", "s"))
	if item.Metrics.Tools["shell"].Calls != 1 {
		t.Fatal("snapshot mutated store")
	}
}
