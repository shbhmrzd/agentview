//go:build integration && (darwin || linux)

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const operationDeadline = 5 * time.Second
const payloadCanary = "INTEGRATION_PRIVATE_PAYLOAD_7d2a"

var agentviewBinary string

// TestMain builds the actual application exactly once. Child invocations run a
// deterministic fixture writer, not a coding agent or a model-backed harness.
func TestMain(m *testing.M) {
	if os.Getenv("AGENTVIEW_FIXTURE_WRITER") == "1" {
		os.Exit(runFixtureWriter())
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	buildDir, err := os.MkdirTemp("", "agentview-integration-build-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	agentviewBinary = filepath.Join(buildDir, "agentview")
	args := []string{"build"}
	if os.Getenv("AGENTVIEW_QA_RACE") == "1" {
		args = append(args, "-race")
	}
	args = append(args, "-o", agentviewBinary, "./cmd/agentview")
	buildContext, cancelBuild := context.WithTimeout(context.Background(), time.Minute)
	build := exec.CommandContext(buildContext, "go", args...)
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		cancelBuild()
		fmt.Fprintf(os.Stderr, "build integration binary: %v\n%s", err, out)
		_ = os.RemoveAll(buildDir)
		os.Exit(1)
	}
	cancelBuild()
	code := m.Run()
	_ = os.RemoveAll(buildDir)
	os.Exit(code)
}

type event struct {
	Kind            string          `json:"kind"`
	Agent           string          `json:"agent"`
	SessionID       string          `json:"session_id"`
	SessionKey      string          `json:"session_key"`
	NativeID        string          `json:"native_id"`
	MessageID       string          `json:"message_id"`
	ToolCallID      string          `json:"tool_call_id"`
	IsError         *bool           `json:"is_error"`
	ExecutionState  string          `json:"execution_state"`
	CaptureMode     string          `json:"capture_mode"`
	ContentCaptured bool            `json:"content_captured"`
	Content         string          `json:"content"`
	ID              string          `json:"occurrence_id"`
	Source          source          `json:"source"`
	Raw             json.RawMessage `json:"-"`
}

type source struct {
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	Generation int64  `json:"generation"`
	Revision   string `json:"revision"`
	Offset     int64  `json:"offset"`
	EndOffset  int64  `json:"end_offset"`
	ItemIndex  *int   `json:"item_index"`
}

type viewer struct {
	cmd      *exec.Cmd
	events   chan event
	done     chan struct{}
	err      error
	stderr   *os.File
	stopRead chan struct{}
}

func startViewer(t *testing.T, agent, path string, includeContent bool, extraArgs ...string) *viewer {
	t.Helper()
	args := []string{"follow", "--agent", agent, "--file", path, "--poll", "100ms"}
	args = append(args, extraArgs...)
	if includeContent {
		args = append(args, "--include-content")
	}
	v := &viewer{cmd: exec.Command(agentviewBinary, args...), events: make(chan event, 128), done: make(chan struct{}), stopRead: make(chan struct{})}
	var err error
	v.stderr, err = os.Create(filepath.Join(t.TempDir(), "viewer.stderr"))
	if err != nil {
		t.Fatal(err)
	}
	v.cmd.Stderr = v.stderr
	stdout, err := v.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := v.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		decoder := json.NewDecoder(stdout)
		for {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				if !errors.Is(err, io.EOF) {
					v.err = fmt.Errorf("decode viewer JSON: %w", err)
				}
				break
			}
			var e event
			if err := json.Unmarshal(raw, &e); err != nil {
				v.err = err
				break
			}
			e.Raw = raw
			select {
			case v.events <- e:
			case <-v.stopRead:
				_ = stdout.Close()
			}
		}
		if err := v.cmd.Wait(); err != nil && v.err == nil {
			v.err = err
		}
		close(v.events)
		close(v.done)
	}()
	t.Cleanup(func() {
		select {
		case <-v.done:
		default:
			_ = v.cmd.Process.Kill()
			close(v.stopRead)
			select {
			case <-v.done:
			case <-time.After(operationDeadline):
				t.Error("viewer cleanup did not finish")
			}
		}
		_ = v.stderr.Close()
	})
	return v
}

func (v *viewer) next(t *testing.T, kind string) event {
	t.Helper()
	e := v.nextEvent(t)
	if e.Kind != kind {
		t.Fatalf("expected %s, received %s: %s", kind, e.Kind, e.Raw)
	}
	return e
}

func (v *viewer) nextEvent(t *testing.T) event {
	t.Helper()
	select {
	case e, ok := <-v.events:
		if !ok {
			<-v.done
			t.Fatalf("viewer ended before next event: %v", v.err)
		}
		if e.ExecutionState != "unknown" {
			t.Fatalf("file observer inferred execution state: %s", e.Raw)
		}
		if e.ID == "" || e.Source.Path == "" {
			t.Fatalf("event lost occurrence identity/provenance: %s", e.Raw)
		}
		return e
	case <-time.After(operationDeadline):
		t.Fatal("deadline waiting for next viewer event")
		return event{}
	}
}

func (v *viewer) stop(t *testing.T, signal os.Signal) {
	t.Helper()
	if err := v.cmd.Process.Signal(signal); err != nil {
		t.Fatal(err)
	}
	select {
	case <-v.done:
		if v.err != nil {
			t.Fatalf("viewer shutdown: %v", v.err)
		}
		if len(v.events) != 0 {
			t.Fatalf("viewer emitted unexpected or duplicate records: %d unconsumed", len(v.events))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("viewer did not shut down within two seconds")
	}
}

func assertEvent(t *testing.T, e event, agent, sessionID, nativeID, path string, generation int64, includeContent bool) {
	t.Helper()
	if e.Agent != agent || e.SessionID != sessionID || e.NativeID != nativeID || e.Source.Path != path || e.Source.Generation != generation {
		t.Fatalf("incorrect identity or provenance: %s", e.Raw)
	}
	if e.CaptureMode != "file_observation" || e.Source.Kind != "jsonl" {
		t.Fatalf("append-log event lost its source semantics: %s", e.Raw)
	}
	if e.ContentCaptured != includeContent || strings.Contains(string(e.Raw), payloadCanary) != includeContent {
		t.Fatalf("content capture policy violated (enabled=%v): %s", includeContent, e.Raw)
	}
	if bytes.Contains(e.Raw, []byte{0x1b}) {
		t.Fatalf("raw terminal escape in JSON output: %q", e.Raw)
	}
}

func seed(agent, session string) []byte {
	if agent == "claude" {
		return jsonLine(map[string]any{"type": "user", "uuid": "native-seed", "sessionId": session, "timestamp": "2026-11-13T00:00:00Z", "message": map[string]any{"role": "user", "content": payloadCanary + "\x1b[2J"}})
	}
	if agent == "copilot" {
		metadata := copilotLine("session.start", "native-session", "", map[string]any{"sessionId": session})
		return append(metadata, copilotLine("user.message", "native-seed", "native-session", map[string]any{"content": payloadCanary + "\x1b[2J"})...)
	}
	meta := jsonLine(map[string]any{"type": "session_meta", "ordinal": 0, "timestamp": "2026-11-13T00:00:00Z", "payload": map[string]any{"id": session, "session_id": "group-" + session, "source": "cli"}})
	return append(meta, jsonLine(map[string]any{"type": "response_item", "ordinal": 1, "timestamp": "2026-11-13T00:00:01Z", "payload": map[string]any{"type": "message", "id": "native-seed", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": payloadCanary + "\x1b[2J"}}}})...)
}

func tool(agent, session string, result bool) []byte {
	if agent == "claude" {
		role, id := "assistant", "native-tool"
		block := map[string]any{"type": "tool_use", "id": "call-1", "name": "Bash", "input": map[string]any{"command": payloadCanary}}
		if result {
			role, id = "user", "native-result"
			block = map[string]any{"type": "tool_result", "tool_use_id": "call-1", "is_error": true, "content": payloadCanary}
		}
		return jsonLine(map[string]any{"type": role, "uuid": id, "parentUuid": "native-seed", "sessionId": session, "timestamp": "2026-11-13T00:00:02Z", "message": map[string]any{"role": role, "content": []any{block}}})
	}
	if agent == "copilot" {
		if result {
			return copilotLine("tool.execution_complete", "native-result", "native-tool", map[string]any{
				"toolCallId": "call-1", "success": false,
				"result": map[string]any{"content": payloadCanary, "detailedContent": "COPILOT_UNSELECTED_DETAIL_CANARY"},
			})
		}
		return copilotLine("assistant.message", "native-tool", "native-seed", map[string]any{
			"messageId": "message-tool", "content": "",
			"toolRequests": []any{map[string]any{"toolCallId": "call-1", "name": "Bash", "arguments": map[string]any{"command": payloadCanary}}},
		})
	}
	payload := map[string]any{"type": "function_call", "id": "native-tool", "call_id": "call-1", "name": "exec_command", "arguments": string(jsonLine(map[string]any{"cmd": payloadCanary}))}
	if result {
		payload = map[string]any{"type": "function_call_output", "id": "native-result", "call_id": "call-1", "output": payloadCanary}
	}
	return jsonLine(map[string]any{"type": "response_item", "ordinal": 2, "timestamp": "2026-11-13T00:00:02Z", "payload": payload})
}

func copilotLine(kind, id, parent string, data any) []byte {
	var parentID any
	if parent != "" {
		parentID = parent
	}
	return jsonLine(map[string]any{"type": kind, "id": id, "parentId": parentID, "timestamp": "2026-11-13T00:00:02Z", "data": data})
}

func jsonLine(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err) // The test constructs only JSON-compatible fixture values.
	}
	return append(data, '\n')
}

func readSeed(t *testing.T, v *viewer, agent, session, path string, generation int64, content bool) {
	t.Helper()
	if agent == "codex" || agent == "copilot" {
		e := v.next(t, "session.metadata")
		metadataID := session
		if agent == "copilot" {
			metadataID = "native-session"
		}
		if e.SessionID != session || e.NativeID != metadataID || e.Source.Generation != generation {
			t.Fatalf("%s session metadata missing: %s", agent, e.Raw)
		}
	}
	assertEvent(t, v.next(t, "message.user"), agent, session, "native-seed", path, generation, content)
}

func TestFixtureProcessesFollowAndCapturePolicy(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "copilot"} {
		for _, content := range []bool{false, true} {
			name := agent + "/metadata-start-before-writes"
			if content {
				name = agent + "/content-attach-to-existing-history"
			}
			t.Run(name, func(t *testing.T) {
				w := startFixtureWriter(t)
				session := "integration-" + agent
				if content {
					w.send(t, "append", seed(agent, session))
				}
				v := startViewer(t, agent, w.path, content)
				if !content {
					w.send(t, "append", seed(agent, session))
				}
				readSeed(t, v, agent, session, w.path, 1, content)
				w.assertUnchanged(t)
				w.send(t, "append", tool(agent, session, false))
				request := v.next(t, "tool.requested")
				assertEvent(t, request, agent, session, "native-tool", w.path, 1, content)
				if request.ToolCallID != "call-1" {
					t.Fatalf("native call identity missing: %s", request.Raw)
				}
				if agent == "copilot" {
					if request.MessageID != "message-tool" {
						t.Fatalf("Copilot message identity was conflated with its envelope ID: %s", request.Raw)
					}
					w.send(t, "append", copilotLine("tool.execution_start", "native-execution", "native-tool", map[string]any{"toolCallId": "call-1", "toolName": "Bash", "arguments": map[string]any{"command": payloadCanary}}))
					started := v.next(t, "tool.started")
					assertEvent(t, started, agent, session, "native-execution", w.path, 1, content)
					if started.ToolCallID != request.ToolCallID {
						t.Fatal("Copilot execution lost request correlation")
					}
				}
				// A second real process sees a stable unterminated record and
				// reports it incomplete. No sleep-only timing assertion is used.
				result := tool(agent, session, true)
				split := len(result) / 2
				w.send(t, "append", result[:split])
				assertIncompleteInspection(t, agent, w.path)
				w.send(t, "append", result[split:len(result)-1])
				assertIncompleteInspection(t, agent, w.path)
				w.send(t, "append", []byte{'\n'})
				completed := v.next(t, "tool.result")
				assertEvent(t, completed, agent, session, "native-result", w.path, 1, content)
				if completed.ToolCallID != request.ToolCallID {
					t.Fatal("result lost request correlation")
				}
				if agent != "codex" && (completed.IsError == nil || !*completed.IsError) {
					t.Fatalf("explicit failed tool result was not preserved: %s", completed.Raw)
				}
				if strings.Contains(string(completed.Raw), "COPILOT_UNSELECTED_DETAIL_CANARY") {
					t.Fatal("unselected Copilot detailed result leaked into normalized content")
				}
				w.assertUnchanged(t)
				v.stop(t, os.Interrupt)
				// The actual producer process, rather than a newly opened parent
				// descriptor, must still accept and perform writes after shutdown.
				w.send(t, "append", tool(agent, session, false))
				w.send(t, "ping", nil)
				w.assertUnchanged(t)
			})
		}
	}
}

func assertIncompleteInspection(t *testing.T, agent, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), operationDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, agentviewBinary, "inspect", "--agent", agent, "--file", path)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("inspect partial source: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	var last event
	for {
		var e event
		if err := dec.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		if e.Kind == "tool.result" || e.Kind == "record.invalid" {
			t.Fatalf("unterminated record was parsed prematurely: %s", out)
		}
		last = e
	}
	if last.Kind != "record.incomplete" {
		t.Fatalf("missing incomplete-record evidence: %s", out)
	}
}

func TestFixtureProcessesRotationAndAvailabilityRecovery(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "copilot"} {
		t.Run(agent, func(t *testing.T) {
			w := startFixtureWriter(t)
			w.send(t, "append", seed(agent, "old-session"))
			v := startViewer(t, agent, w.path, false)
			readSeed(t, v, agent, "old-session", w.path, 1, false)
			w.send(t, "append", []byte(`{"unfinished":`))
			assertIncompleteInspection(t, agent, w.path)
			w.send(t, "rotate", seed(agent, "new-session"))
			change := v.next(t, "source.replaced")
			if change.Source.Generation != 2 || change.SessionID != "" {
				t.Fatalf("rotation retained stale identity: %s", change.Raw)
			}
			readSeed(t, v, agent, "new-session", w.path, 2, false)
			w.assertUnchanged(t)
			w.send(t, "hide", nil)
			v.next(t, "source.unavailable")
			w.send(t, "show", nil)
			v.next(t, "source.available")
			w.send(t, "append", tool(agent, "new-session", false))
			assertEvent(t, v.next(t, "tool.requested"), agent, "new-session", "native-tool", w.path, 2, false)
			v.stop(t, syscall.SIGTERM)
			w.send(t, "append", tool(agent, "new-session", true))
			w.send(t, "ping", nil)
			w.assertUnchanged(t)
		})
	}
}

type writerCommand struct {
	Sequence  int    `json:"sequence"`
	Operation string `json:"operation"`
	Data      []byte `json:"data,omitempty"`
}

type writerReply struct {
	Sequence int    `json:"sequence"`
	PID      int    `json:"pid"`
	Size     int64  `json:"size"`
	Digest   string `json:"digest"`
	Error    string `json:"error,omitempty"`
	Retired  string `json:"retired,omitempty"`
}

type fixtureWriter struct {
	cmd      *exec.Cmd
	input    *json.Encoder
	output   *json.Decoder
	stdin    io.WriteCloser
	path     string
	pid      int
	sequence int
	expected []byte
	archives map[string][]byte
}

func startFixtureWriter(t *testing.T, filename ...string) *fixtureWriter {
	t.Helper()
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "transcript.jsonl"
	if len(filename) != 0 {
		name = filename[0]
	}
	w := &fixtureWriter{path: filepath.Join(t.TempDir(), name), archives: make(map[string][]byte)}
	w.cmd = exec.Command(testBinary, "-test.run=^$")
	w.cmd.Env = append(os.Environ(), "AGENTVIEW_FIXTURE_WRITER=1", "AGENTVIEW_FIXTURE_PATH="+w.path)
	w.cmd.Stderr = os.Stderr
	w.stdin, err = w.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := w.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	w.input, w.output = json.NewEncoder(w.stdin), json.NewDecoder(stdout)
	if err := w.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = w.stdin.Close()
		done := make(chan error, 1)
		go func() { done <- w.cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("fixture writer shutdown: %v", err)
			}
		case <-time.After(operationDeadline):
			_ = w.cmd.Process.Kill()
			<-done
			t.Error("fixture writer did not shut down after command pipe closed")
		}
	})
	ready := w.reply(t)
	if ready.Sequence != 0 || ready.Error != "" || ready.PID != w.cmd.Process.Pid {
		t.Fatalf("fixture writer startup failed: %+v", ready)
	}
	w.pid = ready.PID
	return w
}

func (w *fixtureWriter) reply(t *testing.T) writerReply {
	t.Helper()
	ch := make(chan writerReply, 1)
	go func() {
		var r writerReply
		if err := w.output.Decode(&r); err != nil {
			r.Error = err.Error()
		}
		ch <- r
	}()
	select {
	case r := <-ch:
		return r
	case <-time.After(operationDeadline):
		t.Fatal("fixture writer acknowledgement deadline exceeded")
		return writerReply{}
	}
}

func (w *fixtureWriter) send(t *testing.T, operation string, data []byte) {
	t.Helper()
	w.sequence++
	if err := w.input.Encode(writerCommand{Sequence: w.sequence, Operation: operation, Data: data}); err != nil {
		t.Fatalf("fixture writer is no longer accepting commands: %v", err)
	}
	r := w.reply(t)
	if r.Error != "" || r.Sequence != w.sequence || r.PID != w.pid {
		t.Fatalf("fixture writer acknowledgement: %+v", r)
	}
	switch operation {
	case "append":
		w.expected = append(w.expected, data...)
	case "rotate":
		w.archives[r.Retired] = bytes.Clone(w.expected)
		w.expected = bytes.Clone(data)
	case "snapshot", "rewrite":
		w.expected = bytes.Clone(data)
	}
	if r.Size != int64(len(w.expected)) || r.Digest != digest(w.expected) {
		t.Fatalf("writer's source differs from exclusively commanded bytes: %+v", r)
	}
}

func (w *fixtureWriter) assertUnchanged(t *testing.T) {
	t.Helper()
	paths := make(map[string][]byte, len(w.archives)+1)
	paths[w.path] = w.expected
	for path, data := range w.archives {
		paths[path] = data
	}
	for path, expected := range paths {
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("observer modified source %s: %v", path, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("observer changed source permissions: %s, %v", path, err)
		}
	}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func runFixtureWriter() int {
	path := os.Getenv("AGENTVIEW_FIXTURE_PATH")
	if path == "" {
		return 2
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()
	input, output := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	ack := func(sequence int, retired string, operationErr error) error {
		r := writerReply{Sequence: sequence, PID: os.Getpid(), Retired: retired}
		if operationErr == nil {
			operationErr = f.Sync()
		}
		if operationErr == nil {
			info, err := f.Stat()
			operationErr = err
			if err == nil {
				r.Size = info.Size()
				hash := sha256.New()
				_, operationErr = io.Copy(hash, io.NewSectionReader(f, 0, info.Size()))
				r.Digest = hex.EncodeToString(hash.Sum(nil))
			}
		}
		if operationErr != nil {
			r.Error = operationErr.Error()
		}
		return output.Encode(r)
	}
	if err := ack(0, "", nil); err != nil {
		return 2
	}
	for {
		var command writerCommand
		if err := input.Decode(&command); err != nil {
			if errors.Is(err, io.EOF) {
				return 0
			}
			return 2
		}
		var operationErr error
		retired := ""
		switch command.Operation {
		case "append":
			_, operationErr = f.Write(command.Data)
		case "rewrite":
			operationErr = f.Truncate(0)
			if operationErr == nil {
				_, operationErr = f.Write(command.Data)
			}
		case "snapshot":
			staged := fmt.Sprintf("%s.staged-%d", path, command.Sequence)
			var next *os.File
			next, operationErr = os.OpenFile(staged, os.O_CREATE|os.O_EXCL|os.O_RDWR|os.O_APPEND, 0600)
			if operationErr == nil {
				_, operationErr = next.Write(command.Data)
			}
			if operationErr == nil {
				operationErr = next.Sync()
			}
			if operationErr == nil {
				operationErr = os.Rename(staged, path)
			}
			if operationErr == nil {
				operationErr = f.Close()
				f = next
			} else if next != nil {
				_ = next.Close()
				_ = os.Remove(staged)
			}
		case "rotate":
			retired = fmt.Sprintf("%s.retired-%d", path, command.Sequence)
			operationErr = f.Close()
			if operationErr == nil {
				operationErr = os.Rename(path, retired)
			}
			if operationErr == nil {
				f, operationErr = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR|os.O_APPEND, 0600)
			}
			if operationErr == nil {
				_, operationErr = f.Write(command.Data)
			}
		case "hide":
			operationErr = os.Rename(path, path+".hidden")
		case "show":
			operationErr = os.Rename(path+".hidden", path)
		case "ping":
		default:
			operationErr = fmt.Errorf("unsupported fixture operation")
		}
		if err := ack(command.Sequence, retired, operationErr); err != nil {
			return 2
		}
		if operationErr != nil {
			return 2
		}
	}
}
