package observe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdapterConformance(t *testing.T) {
	for _, info := range Adapters() {
		t.Run(info.Name, func(t *testing.T) {
			for _, capture := range []bool{false, true} {
				events := fixtureEvents(t, info.Name, capture)
				for _, e := range events {
					if e.Agent != info.Name || e.AdapterVersion == "" || e.Scope != "local" || e.SessionKey == "" || e.Source.Kind != info.InputFormat {
						t.Fatalf("missing contract metadata: %+v", e)
					}
					if info.InputFormat == "snapshot" && (e.CaptureMode != "snapshot_observation" || e.Source.Revision == "") {
						t.Fatalf("snapshot presented as append activity: %+v", e)
					}
					if info.InputFormat == "snapshot" && e.NativeID != "" {
						t.Fatal("invented native record identity from an array position")
					}
				}
			}
			for _, raw := range []string{"{", "null", `{"unexpected":"SECRET_UNKNOWN"}`} {
				n, _ := NewNormalizer(info.Name, true)
				e := n.Normalize(Record{Source: Source{Generation: 1}, Data: []byte(raw)}, time.Time{})
				encoded, _ := json.Marshal(e)
				if strings.Contains(string(encoded), "SECRET_UNKNOWN") || len(e) == 0 || !strings.HasPrefix(e[0].Kind, "record.") {
					t.Fatalf("bad unsupported handling: %s", encoded)
				}
			}
		})
	}
	// Descriptors are copied, so consumers cannot mutate registry capabilities.
	list := Adapters()
	list[0].Capabilities.History = "changed"
	again, _ := LookupAdapter(list[0].Name)
	if again.Capabilities.History == "changed" {
		t.Fatal("registry descriptor mutated")
	}
}

func TestSessionNamespaceAndIndependentDecoderState(t *testing.T) {
	r := Record{Source: Source{Path: "selected", Generation: 1}, Data: []byte(`{"type":"session.start","id":"native","data":{"sessionId":"same"}}`)}
	a, _ := NewNormalizerWithOptions("copilot", NormalizerOptions{Scope: "host-a"})
	b, _ := NewNormalizerWithOptions("copilot", NormalizerOptions{Scope: "host-b"})
	ea, eb := a.Normalize(r, time.Time{})[0], b.Normalize(r, time.Time{})[0]
	if ea.SessionKey == eb.SessionKey || ea.SessionID != eb.SessionID {
		t.Fatal("session namespace collision")
	}
	if ea.ID == eb.ID {
		t.Fatal("cross-environment occurrence identity collision")
	}
	if ea.ID != a.Normalize(r, time.Time{})[0].ID {
		t.Fatal("unstable occurrence for identical physical record")
	}
	other, _ := NewNormalizer("copilot", false)
	message := Record{Source: r.Source, Data: []byte(`{"type":"user.message","data":{"content":"hi"}}`)}
	if other.Normalize(message, time.Time{})[0].SessionID != "" {
		t.Fatal("shared mutable decoder state")
	}
	message.Source.Generation++
	if a.Normalize(message, time.Time{})[0].SessionID != "" {
		t.Fatal("replacement source inherited native identity")
	}
}

func TestCopilotRetainsCheckpointAndNativeAgentProvenance(t *testing.T) {
	n, _ := NewNormalizer("copilot", false)
	events := n.Normalize(Record{Source: Source{Generation: 1}, Data: []byte(`{"id":"event","type":"tool.execution_start","agentId":"native-child","checkpoint":true,"data":{"toolCallId":"call","toolName":"shell"}}`)}, time.Time{})
	if len(events) != 1 || events[0].CheckpointReplay == nil || !*events[0].CheckpointReplay || events[0].NativeAgentID != "native-child" || events[0].ExecutionState != "unknown" {
		t.Fatalf("lost native provenance: %+v", events)
	}
}

func TestSnapshotAcceptanceAndSourceBoundaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "context.json")
	good := []byte(`{"id":"session","message_history":[{"kind":"request","parts":[{"part_kind":"user-prompt","content":"secret"}]}]}`)
	write := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(good)
	info, _ := LookupAdapter("rovo")
	r, err := NewSourceReader(info, path, 512)
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.ReadBatch()
	if err != nil || len(first.Records) != 1 {
		t.Fatalf("first: %+v %v", first, err)
	}
	accepted := r.Location()
	for _, tc := range []struct {
		data []byte
		kind string
	}{
		{[]byte("{"), "snapshot.invalid"},
		{[]byte("null"), "snapshot.unsupported"},
		{[]byte(`{"id":"session","message_history":[{"kind":"response","parts":[{"part_kind":"text","content":null}]}]}`), "snapshot.unsupported"},
		{[]byte(`{"id":"session","message_history":[{"kind":"response","parts":[{"part_kind":"tool-return","tool_name":"bash","tool_call_id":"c","content":"SECRET_BAD"}]}]}`), "snapshot.unsupported"},
		{[]byte(`{"id":"session","message_history":[{"kind":"request","parts":[{"part_kind":"tool-return","tool_name":"bash","tool_call_id":"c","content":"SECRET_BAD","outcome":"new-value"}]}]}`), "snapshot.unsupported"},
		{[]byte(`{"id":"session","message_history":[{"kind":"response","parts":[{"part_kind":"tool-call","args":{}}]}]}`), "snapshot.unsupported"},
		{[]byte(strings.Repeat("x", 513)), "snapshot.oversized"},
	} {
		write(tc.data)
		b, err := r.ReadBatch()
		if err != nil || len(b.Records) != 1 || b.Records[0].Diagnostic != tc.kind {
			t.Fatalf("diagnostic: %+v %v", b, err)
		}
		if r.Location().Revision != accepted.Revision || r.Location().Generation != accepted.Generation {
			t.Fatal("invalid snapshot advanced accepted cursor")
		}
		b, err = r.ReadBatch()
		if err != nil || len(b.Records) != 0 {
			t.Fatal("unchanged diagnostic repeated")
		}
	}
	write(good)
	b, err := r.ReadBatch()
	if err != nil || len(b.Records) != 1 || b.Records[0].Diagnostic != "snapshot.recovered" {
		t.Fatalf("same revision recovery: %+v %v", b, err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	write(good)
	b, err = r.ReadBatch()
	if err != nil || len(b.Records) != 0 {
		t.Fatal("same content on new inode replayed")
	}
	encoded, _ := json.Marshal(accepted)
	if strings.Contains(string(encoded), "offset") || !strings.Contains(string(encoded), "revision") {
		t.Fatalf("snapshot fakes byte positions: %s", encoded)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".old", path); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadBatch(); err == nil {
		t.Fatal("snapshot followed final symlink")
	}
}

func TestRovoProjectionBoundAndReasoningExclusion(t *testing.T) {
	raw := `{"id":"session","message_history":[{"kind":"response","parts":[` + strings.Repeat(`{"part_kind":"thinking","content":"SECRET_THOUGHT"},`, 300) + `{"part_kind":"text","content":"last"}]}]}`
	n, _ := NewNormalizer("rovo", true)
	events := n.Normalize(Record{Source: Source{Kind: "snapshot", Generation: 1, Revision: "rev"}, Data: []byte(raw)}, time.Time{})
	if len(events) != 256 || events[0].Kind != "snapshot.updated" || events[255].Kind != "record.excess_blocks" {
		t.Fatal("snapshot output expansion exceeded contract")
	}
	data, _ := json.Marshal(events)
	if strings.Contains(string(data), "SECRET_THOUGHT") {
		t.Fatal("reasoning leaked")
	}
}

func TestRovoMalformedKnownPartsDoNotReplaceOrCapture(t *testing.T) {
	for _, part := range []string{
		`{"part_kind":"tool-return","tool_name":"bash","tool_call_id":"c","outcome":"invalid","content":"SECRET_BAD"}`,
		`{"part_kind":"text","content":"SECRET_BAD"}`,
		`{"part_kind":"tool-return","content":"SECRET_BAD"}`,
	} {
		n, _ := NewNormalizer("rovo", true)
		raw := `{"id":"session","message_history":[{"kind":"request","parts":[` + part + `]}]}`
		events := n.Normalize(Record{Source: Source{Kind: "snapshot", Generation: 1}, Data: []byte(raw)}, time.Time{})
		encoded, _ := json.Marshal(events)
		if len(events) != 1 || events[0].Kind != "record.invalid" || events[0].ContentCaptured || strings.Contains(string(encoded), "SECRET_BAD") {
			t.Fatalf("invalid snapshot replaced/captured: %s", encoded)
		}
	}
}
