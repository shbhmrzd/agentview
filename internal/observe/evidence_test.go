package observe

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNativeShapeEvidenceAndPrivacy(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		for _, content := range []bool{false, true} {
			t.Run(agent+map[bool]string{true: "/content", false: "/metadata"}[content], func(t *testing.T) {
				f, err := os.Open("../../testdata/native-shapes/" + agent + ".jsonl")
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				n, _ := NewNormalizer(agent, content)
				scanner := bufio.NewScanner(f)
				counts := map[string]int{}
				failures := 0
				children := 0
				cost := false
				for scanner.Scan() {
					for _, e := range n.Normalize(Record{Data: scanner.Bytes(), Source: Source{Generation: 1}}, time.Now()) {
						counts[e.Kind]++
						if e.Outcome != nil && e.Outcome.Status == "failure" {
							failures++
						}
						children += len(e.RelatedAgentIDs) + len(e.RelatedAgentPaths)
						cost = cost || e.Stats != nil
						b, _ := json.Marshal(e)
						if !content && strings.Contains(string(b), "PRIVATE_FIXTURE") {
							t.Fatalf("content leak: %s", b)
						}
						if strings.Contains(string(b), "MUST_NOT_REPLAY") {
							t.Fatal("replayed compacted history")
						}
					}
				}
				if err := scanner.Err(); err != nil {
					t.Fatal(err)
				}
				if counts["record.unsupported"] != 0 || counts["record.invalid"] != 0 || counts["context.compacted"] != 1 || children != 1 {
					t.Fatalf("missing semantics: %+v children=%d", counts, children)
				}
				if agent == "codex" && (failures != 1 || counts["agent.message"] != 1 || counts["agent.communication"] != 1) {
					t.Fatalf("codex evidence: %+v failures=%d", counts, failures)
				}
				if agent == "claude" && (failures != 3 || !cost) {
					t.Fatalf("claude failure/cost evidence missing")
				}
			})
		}
	}
}

func TestExecutionOutcomeDoesNotSearchArbitraryOutput(t *testing.T) {
	for _, tc := range []struct{ tool, raw, status string }{
		{"exec_command", `{"exit_code":2,"output":"failed"}`, "failure"},
		{"exec_command", `{"exit_code":0,"output":"Exit code 2"}`, "success"},
		{"Read", `{"exit_code":2}`, "unknown"},
		{"exec_command", `"Output:\nExit code 2"`, "unknown"},
		{"exec_command", `"Wall time: 1 second\nProcess exited with code 0\nFinal output:\nProcess exited with code 2"`, "success"},
		{"exec", `[{"type":"input_text","text":"{\"exit_code\":2,\"output\":\"x\"}"}]`, "unknown"},
		{"exec_command", `{"session_id":42,"output":"still running"}`, "unknown"},
	} {
		if got := executionOutcome(tc.tool, json.RawMessage(tc.raw)); got.Status != tc.status {
			t.Errorf("%s: %+v want %s", tc.raw, got, tc.status)
		}
	}
}
