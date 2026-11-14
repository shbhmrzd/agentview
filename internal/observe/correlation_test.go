package observe

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestCallIdentitySurvivesEvictionCompletionAndOrphanResults(t *testing.T) {
	for _, prior := range []string{"pending", "ambiguous", "completed", "orphan"} {
		t.Run(prior, func(t *testing.T) {
			c := NewCallTracker(nil)
			defer c.Close()
			apply := func(kind, id string) Event {
				e := Event{Kind: kind, ToolCallID: id, ToolName: "exec_command", Content: kind}
				c.Apply(&e)
				return e
			}
			if prior != "orphan" {
				apply("tool.requested", "reused")
			}
			if prior == "ambiguous" {
				apply("tool.requested", "reused")
			}
			if prior == "completed" || prior == "orphan" {
				apply("tool.result", "reused")
			}
			for i := 0; i < maxPendingCalls+1; i++ {
				apply("tool.requested", fmt.Sprint(i))
			}
			apply("tool.requested", "reused")
			late := apply("tool.result", "reused")
			if late.Call.Status != "ambiguous" || late.Call.Request != nil || late.Call.DurationMS != nil {
				t.Fatalf("false pairing: %+v", late.Call)
			}
			recent := apply("tool.result", fmt.Sprint(maxPendingCalls))
			if recent.Call.Status != "paired" {
				t.Fatal("recent unambiguous pair lost")
			}
			duplicate := apply("tool.result", fmt.Sprint(maxPendingCalls))
			if duplicate.Call.Status != "duplicate_result" || duplicate.Call.Request != nil {
				t.Fatal("duplicate paired")
			}
		})
	}
}

func TestRequestExcerptOwnsItsAllocation(t *testing.T) {
	c := NewCallTracker(nil)
	defer c.Close()
	request := Event{Kind: "tool.requested", ToolCallID: "large", Content: strings.Repeat("x", 2*1024*1024)}
	c.Apply(&request)
	result := Event{Kind: "tool.result", ToolCallID: "large"}
	c.Apply(&result)
	stored := result.Call.Request
	if stored == nil || len(stored.Content) != 1024 || !stored.ContentClipped {
		t.Fatal("excerpt incorrect")
	}
	if unsafe.StringData(stored.Content) == unsafe.StringData(request.Content) {
		t.Fatal("excerpt retains entire input allocation")
	}
}

func TestSharedEvidenceBudgetExhaustionIsStickyUntilReset(t *testing.T) {
	budget := NewEvidenceBudget(12000)
	trackers := []*CallTracker{}
	for i := 0; i < 80; i++ {
		c := NewCallTracker(budget)
		trackers = append(trackers, c)
		e := Event{Kind: "tool.requested", ToolCallID: fmt.Sprint(i), Content: strings.Repeat("x", 2048)}
		c.Apply(&e)
		if budget.Used() > 12000 {
			t.Fatal("shared budget exceeded")
		}
	}
	exhausted := trackers[len(trackers)-1]
	for _, c := range trackers[:len(trackers)-1] {
		c.Close()
	}
	e := Event{Kind: "tool.requested", ToolCallID: "new"}
	exhausted.Apply(&e)
	if e.Call.Status != "capacity" {
		t.Fatal("forgotten IDs became eligible after capacity recovered")
	}
	exhausted.Close()
	if budget.Used() != 0 {
		t.Fatal("close did not release reservations", budget.Used())
	}
	exhausted.Apply(&e)
	if e.Call.Status != "pending" {
		t.Fatal("new reader did not reset capacity")
	}
	exhausted.Close()
}

func TestGenerationResetSeparatesCallIdentities(t *testing.T) {
	n, _ := NewNormalizer("codex", true)
	defer n.Close()
	call := []byte(`{"type":"response_item","payload":{"type":"function_call","call_id":"same","name":"exec_command","arguments":"input"}}`)
	result := []byte(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"same","output":"{\"exit_code\":2}"}}`)
	for _, generation := range []int{1, 2} {
		e := n.Normalize(Record{Source: Source{Generation: int64(generation)}, Data: call}, time.Now())[0]
		if e.Call.Status != "pending" {
			t.Fatal(e.Call)
		}
		out := n.Normalize(Record{Source: Source{Generation: int64(generation)}, Data: result}, time.Now())[0]
		if out.Call.Status != "paired" || out.Outcome.Status != "failure" {
			t.Fatalf("%+v", out)
		}
	}
}

// Stateful seed sequences exercise ownership before and after the size cutoff.
func FuzzCompactionOwnership(f *testing.F) {
	f.Add(uint16(9), uint16(10), uint16(1024), uint16(512))
	f.Add(uint16(10), uint16(10), uint16(100), uint16(512))
	f.Fuzz(func(t *testing.T, ordinal, boundary, size, cutoff uint16) {
		n, _ := NewNormalizer("codex", true)
		defer n.Close()
		meta := fmt.Sprintf(`{"type":"session_meta","payload":{"id":"child","parent_thread_id":"parent","subagent_history_start_ordinal":%d}}`, boundary)
		n.Normalize(Record{Data: []byte(meta)}, time.Now())
		raw := []byte(fmt.Sprintf(`{"ordinal":%d,"type":"compacted","payload":{"message":%q}}`, ordinal, strings.Repeat("x", int(size)%8192)))
		record := Record{Data: raw}
		if len(raw) > int(cutoff)%8192+256 {
			record = Record{Diagnostic: "record.oversized", Header: readPartialHeader(raw[:min(len(raw), 16384)])}
		}
		events := n.Normalize(record, time.Now())
		markers := 0
		for _, e := range events {
			if e.Kind == "context.compacted" {
				markers++
			}
		}
		want := 1
		if ordinal < boundary {
			want = 0
		}
		if markers != want {
			t.Fatalf("ordinal=%d boundary=%d: %+v", ordinal, boundary, events)
		}
	})
}

// Run explicitly with -run '^$' -bench '^BenchmarkSharedCorrelationRetention$'
// -benchtime=1x. This isolates retained correlation state, not total server RSS.
func BenchmarkSharedCorrelationRetention(b *testing.B) {
	for n := 0; n < b.N; n++ {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		budget := NewEvidenceBudget(64 * 1024 * 1024)
		trackers := make([]*CallTracker, 256)
		payload := strings.Repeat("x", 2*1024*1024)
		for i := range trackers {
			c := NewCallTracker(budget)
			trackers[i] = c
			for j := 0; j < 128; j++ {
				e := Event{Kind: "tool.requested", ToolCallID: fmt.Sprint(j), ToolName: "exec_command", Content: payload}
				c.Apply(&e)
			}
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		b.ReportMetric(float64(budget.Used()), "charged-B")
		if after.HeapAlloc > before.HeapAlloc {
			b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc), "retained-heap-B")
		}
		runtime.KeepAlive(trackers)
		for _, c := range trackers {
			c.Close()
		}
		if budget.Used() != 0 {
			b.Fatal("reservations leaked")
		}
	}
}
