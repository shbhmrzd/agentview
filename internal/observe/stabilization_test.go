package observe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStabilizationCompactionOwnershipIsIndependentOfRecordSize(t *testing.T) {
	for _, ordinal := range []string{`"ordinal":9,`, `"ordinal":10,`, ``} {
		for _, limit := range []int{512, 4096} {
			path := filepath.Join(t.TempDir(), "rollout-child.jsonl")
			data := `{"type":"session_meta","payload":{"id":"child","parent_thread_id":"parent","subagent_history_start_ordinal":10}}` + "\n" +
				`{` + ordinal + `"type":"compacted","payload":{"message":"` + strings.Repeat("x", 1024) + `"}}` + "\n"
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			r, _ := NewReader(path, limit, DefaultBatchBytes)
			n, _ := NewNormalizer("codex", false)
			defer n.Close()
			batch, err := r.ReadBatch()
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, record := range batch.Records {
				for _, event := range n.Normalize(record, time.Now()) {
					counts[event.Kind]++
				}
			}
			t.Logf("ordinal=%s limit=%d counts=%+v", ordinal, limit, counts)
			want := 0
			if ordinal == `"ordinal":10,` || (ordinal == "" && limit == 4096) {
				want = 1
			}
			if counts["context.compacted"] != want {
				t.Errorf("ordinal=%s limit=%d: %+v", ordinal, limit, counts)
			}
			if limit == 512 && counts["record.oversized"] != 1 {
				t.Fatal("lost size diagnostic")
			}
		}
	}
}

func TestStabilizationHistoryNoneSuppressesOversizedHistoricalCompaction(t *testing.T) {
	for _, history := range []string{"none", "all"} {
		root := t.TempDir()
		path := filepath.Join(root, "rollout-main.jsonl")
		line := fmt.Sprintf("{\"ordinal\":1,\"type\":\"compacted\",\"payload\":{\"message\":%q}}\n", strings.Repeat("x", 1024))
		dirWrite(t, path, []byte(line), false)
		d := dirTest(t, root, "codex", history, false)
		d.options.MaxRecordBytes = 512
		var events []Event
		if err := d.scan(context.Background(), true, dirCollect(&events)); err != nil {
			t.Fatal(err)
		}
		dirDrain(t, d, &events)
		markers, diagnostics := 0, 0
		for _, event := range events {
			if event.Kind == "context.compacted" {
				markers++
			}
			if event.Kind == "record.oversized" {
				diagnostics++
			}
		}
		want := 0
		if history == "all" {
			want = 1
		}
		if markers != want || diagnostics != 1 {
			t.Fatalf("history=%s markers=%d diagnostics=%d", history, markers, diagnostics)
		}
		// Appended and newly discovered compactions still appear in either mode.
		dirWrite(t, path, []byte(line), true)
		dirWrite(t, filepath.Join(root, "rollout-new.jsonl"), []byte(line), false)
		events = nil
		if err := d.scan(context.Background(), false, dirCollect(&events)); err != nil {
			t.Fatal(err)
		}
		dirDrain(t, d, &events)
		markers = 0
		for _, event := range events {
			if event.Kind == "context.compacted" {
				markers++
			}
		}
		if markers != 2 {
			t.Fatalf("new compactions lost: %d", markers)
		}
	}
}

func TestStabilizationIrrelevantMetadataCannotEraseToolFailure(t *testing.T) {
	for _, field := range []string{`"totalCostUSD":"unknown"`, `"hookErrors":{}`, `"preventedContinuation":"false"`, `"subtype":{}`, `"customTitle":42`} {
		n, _ := NewNormalizer("claude", false)
		defer n.Close()
		data := `{"type":"user",` + field + `,"message":{"content":[{"type":"tool_result","tool_use_id":"call","is_error":true,"content":"synthetic error"}]}}`
		events := n.Normalize(Record{Data: []byte(data)}, time.Now())
		if len(events) != 1 || events[0].Kind != "tool.result" {
			t.Errorf("irrelevant %s erases result: %s", field, events[0].Kind)
		}
	}
}
