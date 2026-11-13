package observe_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shbhmrzd/agentview/internal/observe"
)

func writeSource(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func readAllAvailable(t *testing.T, r *observe.Reader) []observe.Record {
	t.Helper()
	var records []observe.Record
	for attempts := 0; attempts < 1000; attempts++ {
		batch, err := r.ReadBatch()
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, batch.Records...)
		if !batch.HasMore {
			return records
		}
	}
	t.Fatal("reader did not exhaust bounded fixture")
	return nil
}

func TestExternalCopyTruncateRegrow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.jsonl")
	old := []byte("{\"type\":\"old\",\"value\":\"" + strings.Repeat("a", 400) + "\"}\n")
	writeSource(t, path, old)
	r, err := observe.NewReader(path, 4096, 73)
	if err != nil {
		t.Fatal(err)
	}
	first := readAllAvailable(t, r)
	if len(first) != 1 || !bytes.Equal(first[0].Data, old[:len(old)-1]) {
		t.Fatalf("initial read: %+v", first)
	}
	infoBefore, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// os.WriteFile truncates the same inode. Its new size exceeds the prior
	// cursor, exercising the case that size-only tailers silently miss.
	replacement := []byte("{\"type\":\"new\",\"value\":\"" + strings.Repeat("b", 700) + "\"}\n")
	writeSource(t, path, replacement)
	infoAfter, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(infoBefore, infoAfter) {
		t.Fatal("fixture did not preserve source inode")
	}
	next := readAllAvailable(t, r)
	if len(next) != 2 || next[0].Diagnostic != "source.rewritten" || next[0].Source.Generation != 2 {
		t.Fatalf("rewrite must emit a generation diagnostic before replacement data: %+v", next)
	}
	if !bytes.Equal(next[1].Data, replacement[:len(replacement)-1]) || next[1].Source.Offset != 0 {
		t.Fatalf("replacement history was skipped or mixed: %+v", next[1])
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(unchanged, replacement) {
		t.Fatal("observation modified source")
	}
}

func TestExternalPartialOversizeMalformedRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.jsonl")
	valid := []byte("{\"type\":\"user\",\"sessionId\":\"s\",\"message\":{\"content\":\"ok\"}}")
	writeSource(t, path, valid[:15])
	r, err := observe.NewReader(path, 100, 17)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAllAvailable(t, r); len(got) != 0 {
		t.Fatalf("partial record emitted early: %+v", got)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	remaining := append(append([]byte{}, valid[15:]...), '\n')
	remaining = append(remaining, []byte("{malformed}\n"+strings.Repeat("x", 180)+"\n")...)
	remaining = append(remaining, valid...)
	remaining = append(remaining, '\n')
	if _, err = f.Write(remaining); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	records := readAllAvailable(t, r)
	if len(records) != 4 || records[2].Diagnostic != "record.oversized" {
		t.Fatalf("reader did not recover at record boundaries: %+v", records)
	}
	n, err := observe.NewNormalizer("claude", false)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, record := range records {
		for _, event := range n.Normalize(record, time.Unix(0, 0)) {
			kinds = append(kinds, event.Kind)
		}
	}
	if strings.Join(kinds, ",") != "message.user,record.invalid,record.oversized,message.user" {
		t.Fatalf("later valid record must survive malformed and oversized input: %v", kinds)
	}
	if got := readAllAvailable(t, r); len(got) != 0 {
		t.Fatalf("idle poll duplicated records: %+v", got)
	}
}

func TestExternalMetadataAndReasoningBoundaries(t *testing.T) {
	const secret = "PRIVATE_PAYLOAD_CANARY_5d38"
	const reasoning = "REASONING_CANARY_23ef"
	data := []byte(`{"type":"assistant","sessionId":"s","message":{"content":[{"type":"text","text":"` + secret + `"},{"type":"tool_use","id":"call","name":"Bash","input":{"command":"` + secret + `"}},{"type":"thinking","thinking":"` + reasoning + `"}]}}`)
	for _, includeContent := range []bool{false, true} {
		n, err := observe.NewNormalizer("claude", includeContent)
		if err != nil {
			t.Fatal(err)
		}
		events := n.Normalize(observe.Record{Source: observe.Source{Path: "/synthetic", Generation: 1}, Data: data}, time.Unix(0, 0))
		out, err := json.Marshal(events)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(out, []byte(reasoning)) {
			t.Fatal("reasoning content was exposed")
		}
		if bytes.Contains(out, []byte(secret)) != includeContent {
			t.Fatalf("content opt-in not enforced: include=%v, %s", includeContent, out)
		}
		for _, event := range events {
			if event.ExecutionState != "unknown" {
				t.Fatalf("file observation inferred execution state: %+v", event)
			}
		}
	}
}

func TestExternalFrozenInspectionBoundary(t *testing.T) {
	line := []byte("{\"type\":\"user\",\"sessionId\":\"s\",\"message\":{\"content\":\"ok\"}}\n")
	t.Run("later append excluded", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "source.jsonl")
		writeSource(t, path, bytes.Repeat(line, 3))
		r, err := observe.NewReader(path, 1000, 20)
		if err != nil {
			t.Fatal(err)
		}
		r.Freeze()
		first, err := r.ReadBatch()
		if err != nil || !first.HasMore {
			t.Fatalf("fixture did not create multi-batch inspection: %+v, %v", first, err)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(bytes.Repeat(line, 4)); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		records := append(first.Records, readAllAvailable(t, r)...)
		if len(records) != 3 {
			t.Fatalf("inspection included bytes appended after its initial boundary: %d records", len(records))
		}
	})
	t.Run("unread truncation disclosed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "source.jsonl")
		writeSource(t, path, bytes.Repeat(line, 3))
		r, err := observe.NewReader(path, 1000, 20)
		if err != nil {
			t.Fatal(err)
		}
		r.Freeze()
		if _, err := r.ReadBatch(); err != nil {
			t.Fatal(err)
		}
		// The source shrinks, but its new size remains beyond the reader's
		// cursor and both already-observed fingerprints remain unchanged.
		if err := os.Truncate(path, int64(len(line))); err != nil {
			t.Fatal(err)
		}
		batch, err := r.ReadBatch()
		if err == nil {
			t.Fatal("inspection silently accepted loss of unread snapshot bytes")
		}
		if len(batch.Records) == 0 || batch.Records[0].Diagnostic != "source.truncated" {
			t.Fatalf("missing truncation evidence: %+v, %v", batch, err)
		}
	})
}

func TestExternalReplacementDiscardsPartialTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.jsonl")
	initial := []byte("{\"type\":\"user\",\"sessionId\":\"old\",\"message\":{\"content\":\"old\"}}\n{\"unfinished\":")
	writeSource(t, path, initial)
	r, err := observe.NewReader(path, 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	n, err := observe.NewNormalizer("claude", false)
	if err != nil {
		t.Fatal(err)
	}
	first := readAllAvailable(t, r)
	if len(first) != 1 {
		t.Fatalf("partial tail unexpectedly emitted: %+v", first)
	}
	if event := n.Normalize(first[0], time.Unix(0, 0))[0]; event.SessionID != "old" {
		t.Fatalf("fixture old session missing: %+v", event)
	}
	if err := os.Rename(path, path+".rotated"); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("{\"type\":\"user\",\"message\":{\"content\":\"new without known session metadata\"}}\n")
	writeSource(t, path, replacement)
	next := readAllAvailable(t, r)
	if len(next) != 2 || next[0].Diagnostic != "source.replaced" {
		t.Fatalf("replacement not disclosed before new source data: %+v", next)
	}
	for _, record := range next {
		for _, event := range n.Normalize(record, time.Unix(0, 0)) {
			if event.SessionID != "" {
				t.Fatalf("new source inherited stale session identity: %+v", event)
			}
		}
	}
	if !bytes.Equal(next[1].Data, replacement[:len(replacement)-1]) {
		t.Fatal("partial bytes from old source were mixed into replacement")
	}
	oldAfter, err := os.ReadFile(path + ".rotated")
	if err != nil || !bytes.Equal(oldAfter, initial) {
		t.Fatal("reader modified rotated source")
	}
}
