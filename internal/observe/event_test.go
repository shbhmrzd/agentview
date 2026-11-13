package observe

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func fixtureEvents(t *testing.T, agent string, content bool) []Event {
	t.Helper()
	info, _ := LookupAdapter(agent)
	suffix := ".synthetic.jsonl"
	if info.InputFormat == "snapshot" {
		suffix = ".synthetic.json"
	}
	r, err := NewSourceReader(info, "../../testdata/"+agent+suffix, DefaultMaxRecordBytes)
	if err != nil {
		t.Fatal(err)
	}
	n, err := NewNormalizer(agent, content)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	for {
		batch, err := r.ReadBatch()
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range batch.Records {
			events = append(events, n.Normalize(record, time.Unix(0, 0))...)
		}
		if !batch.HasMore {
			break
		}
	}
	return events
}

func TestFixturesOmitContentByDefault(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "copilot", "rovo"} {
		t.Run(agent, func(t *testing.T) {
			events := fixtureEvents(t, agent, false)
			if len(events) < 5 {
				t.Fatal("expected meaningful projected events")
			}
			seen := map[string]bool{}
			for _, e := range events {
				if e.Content != "" || e.ContentCaptured {
					t.Errorf("content leaked: %+v", e)
				}
				if e.ExecutionState != "unknown" {
					t.Errorf("inferred execution state: %s", e.ExecutionState)
				}
				if seen[e.ID] {
					t.Fatal("occurrence identity collision")
				}
				seen[e.ID] = true
				if e.SessionID == "" {
					t.Errorf("missing known session identity: %+v", e)
				}
			}
		})
	}
}

func TestContentExcludesReasoningAndClassifiesToolResults(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "copilot", "rovo"} {
		events := fixtureEvents(t, agent, true)
		data, err := json.Marshal(events)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "SYNTHETIC_REASONING") || strings.Contains(string(data), "SYNTHETIC_ENCRYPTED") {
			t.Fatal("reasoning leaked")
		}
		results, requests, users := 0, 0, 0
		for _, e := range events {
			switch e.Kind {
			case "tool.result":
				results++
				if !strings.Contains(e.Content, "FAIL") {
					t.Fatal("missing selected tool output")
				}
			case "tool.requested":
				requests++
			case "message.user":
				users++
			}
			if agent == "codex" && (e.SessionID != "codex-thread-example" || e.GroupSessionID != "codex-group-example") {
				t.Fatal("conflated thread and group identity")
			}
		}
		if results != 1 || requests != 1 || users != 1 {
			t.Fatalf("%s: results=%d requests=%d users=%d", agent, results, requests, users)
		}
	}
}

func TestInvalidAndUnknownShapesDoNotExposeRawContent(t *testing.T) {
	cases := []string{
		`{"type":"user","message":{"content":null}}`,
		`{"type":"user","message":{"content":{"secret":"CANARY"}}}`,
		`{"type":"new_type","secret":"CANARY"}`,
		`{"type":"user","message":`,
	}
	n, _ := NewNormalizer("claude", true)
	for _, raw := range cases {
		events := n.Normalize(Record{Source: Source{Generation: 1}, Data: []byte(raw)}, time.Time{})
		if len(events) != 1 || !strings.HasPrefix(events[0].Kind, "record.") {
			t.Fatalf("unexpected interpretation: %+v", events)
		}
		encoded, _ := json.Marshal(events)
		if strings.Contains(string(encoded), "CANARY") {
			t.Fatal("raw unknown payload exposed")
		}
	}
}

func TestGenerationResetsSessionContext(t *testing.T) {
	n, _ := NewNormalizer("codex", false)
	n.Normalize(Record{Source: Source{Generation: 1}, Data: []byte(`{"type":"session_meta","payload":{"id":"old"}}`)}, time.Time{})
	e := n.Normalize(Record{Source: Source{Generation: 2}, Data: []byte(`{"type":"event_msg","payload":{"type":"task_started"}}`)}, time.Time{})
	if e[0].SessionID != "" {
		t.Fatal("new generation inherited old session identity")
	}
}

func TestCodexNativeMetadataAndInvalidTime(t *testing.T) {
	n, _ := NewNormalizer("codex", false)
	events := n.Normalize(Record{Source: Source{Generation: 1}, Data: []byte(`{"timestamp":"invalid","ordinal":42,"type":"response_item","payload":{"type":"message","id":"native-1","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`)}, time.Time{})
	if len(events) != 1 {
		t.Fatal("unexpected projection")
	}
	e := events[0]
	if e.NativeID != "native-1" || e.SourceOrdinal == nil || *e.SourceOrdinal != 42 || e.TimeStatus != "invalid" || e.OccurredAt != "" {
		t.Fatalf("metadata lost: %+v", e)
	}
}

func TestMessageProjectionBound(t *testing.T) {
	n, _ := NewNormalizer("claude", false)
	line := `{"type":"assistant","message":{"content":[` + strings.Repeat(`{"type":"text","text":"a"},`, 300) + `{"type":"text","text":"last"}]}}`
	events := n.Normalize(Record{Source: Source{Generation: 1}, Data: []byte(line)}, time.Time{})
	if len(events) != 256 || events[255].Kind != "record.excess_blocks" {
		t.Fatal("unbounded message projection")
	}
}

func FuzzNormalize(f *testing.F) {
	for _, agent := range []string{"claude", "codex", "copilot", "rovo"} {
		suffix := ".synthetic.jsonl"
		if agent == "rovo" {
			suffix = ".synthetic.json"
		}
		data, err := os.ReadFile("../../testdata/" + agent + suffix)
		if err != nil {
			f.Fatal(err)
		}
		if agent == "rovo" {
			f.Add(agent, string(data))
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			f.Add(agent, line)
		}
	}
	f.Fuzz(func(t *testing.T, agent, line string) {
		if len(line) > DefaultMaxRecordBytes {
			t.Skip()
		}
		n, err := NewNormalizer(agent, false)
		if err != nil {
			return
		}
		events := n.Normalize(Record{Source: Source{Generation: 1}, Data: []byte(line)}, time.Time{})
		if len(events) > 256 {
			t.Fatal("event expansion exceeded bound")
		}
		for _, e := range events {
			if e.ContentCaptured || e.Content != "" || e.ExecutionState != "unknown" {
				t.Fatal("unsafe projection")
			}
			if _, err := json.Marshal(e); err != nil {
				t.Fatal(err)
			}
		}
	})
}
