package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestNativeCompletionRequiresSemanticSuccess(t *testing.T) {
	for _, tc := range []struct {
		agent, data string
		ok          bool
	}{
		{"codex", `{"type":"turn.completed"}`, true},
		{"codex", `{"type":"thread.started"}`, false},
		{"codex", "{\"type\":\"turn.completed\"}\n{\"type\":\"turn.failed\"}", false},
		{"claude", `{"type":"result","subtype":"success","is_error":false,"session_id":"session"}`, true},
		{"claude", `{"type":"result","subtype":"error_max_turns","is_error":true,"session_id":"session"}`, false},
		{"claude", `{"type":"result","subtype":"success","session_id":"other"}`, false},
	} {
		path := filepath.Join(t.TempDir(), "stdout.jsonl")
		if err := os.WriteFile(path, []byte(tc.data+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := nativeCompletion(path, tc.agent, "session"); (err == nil) != tc.ok {
			t.Fatalf("%s completion expected ok=%v: %v", tc.agent, tc.ok, err)
		}
	}
}

func TestShutdownHelper(t *testing.T) {
	if os.Getenv("AGENTVIEW_SHUTDOWN_HELPER") != "1" {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	fmt.Println("ready")
	<-ch
	signal.Stop(ch)
}

func TestViewerExitValidation(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, graceful := range []bool{true, false} {
		t.Run(fmt.Sprintf("graceful=%v", graceful), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := ownedCommand(ctx, exe, "-test.run=^TestShutdownHelper$")
			cmd.Env = append(os.Environ(), "AGENTVIEW_SHUTDOWN_HELPER=1")
			cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
			pipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(pipe)
			line, err := reader.ReadString('\n')
			if err != nil || line != "ready\n" {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatalf("readiness: %q %v", line, err)
			}
			if graceful {
				cancel()
			} else if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			err = viewerExitError(cmd, cmd.Wait())
			if graceful && err != nil {
				t.Fatalf("graceful zero exit rejected: %v", err)
			}
			if !graceful && err == nil {
				t.Fatal("external kill accepted as successful shutdown")
			}
		})
	}
}

// Keep the compatibility gate honest: transport echoes, another session's
// output, and the wrong provider must not satisfy an assistant observation.
func TestMarkerGateRejectsFalseEvidence(t *testing.T) {
	r := &report{Agent: "codex", SessionID: "session", Source: "source.jsonl", Events: map[string]int{}}
	events := make(chan observed, 4)
	for _, e := range []observed{
		{Kind: "message.user", Agent: "codex", SessionID: "session", Content: "marker", ExecutionState: "unknown"},
		{Kind: "message.assistant", Agent: "claude", SessionID: "session", Content: "marker", ExecutionState: "unknown"},
		{Kind: "message.assistant", Agent: "codex", SessionID: "different", Content: "marker", ExecutionState: "unknown"},
		{Kind: "message.assistant", Agent: "codex", SessionID: "session", Content: "seed", ExecutionState: "unknown"},
	} {
		events <- e
	}
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := awaitMarker(context.Background(), events, process, r, "marker", 20*time.Millisecond); err == nil {
		t.Fatal("false evidence satisfied the native compatibility gate")
	}
	events <- observed{Kind: "message.assistant", Agent: "codex", SessionID: "session", Content: "marker", ExecutionState: "unknown", Source: position{Path: r.Source, Generation: 1, Offset: 10, EndOffset: 20}}
	if err := awaitMarker(context.Background(), events, process, r, "marker", time.Second); err != nil {
		t.Fatal(err)
	}
	r.SeedObserved = true
	events <- observed{Kind: "message.assistant", Agent: "codex", SessionID: "session", Content: "live", ExecutionState: "unknown", Source: position{Path: r.Source, Generation: 1, Offset: 10, EndOffset: 20}}
	if err := awaitMarker(context.Background(), events, process, r, "live", time.Second); err == nil {
		t.Fatal("replayed seed position satisfied live observation")
	}
}

func TestMetadataGate(t *testing.T) {
	base := observed{Kind: "message.assistant", Agent: "codex", SessionID: "session", ExecutionState: "unknown"}
	encode := func(e observed) []byte {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		return append(append(append([]byte{}, b...), '\n'), b...)
	}
	if err := checkMetadata(encode(base), "codex", "session", "secret"); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name   string
		change func(*observed)
	}{
		{"wrong agent", func(e *observed) { e.Agent = "claude" }},
		{"wrong session", func(e *observed) { e.SessionID = "different" }},
		{"captured", func(e *observed) { e.Captured = true }},
		{"leaked marker", func(e *observed) { e.Content = "secret" }},
		{"inferred state", func(e *observed) { e.ExecutionState = "running" }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			e := base
			mutation.change(&e)
			if err := checkMetadata(encode(e), "codex", "session", "secret"); err == nil {
				t.Fatal("false metadata evidence passed")
			}
		})
	}
}
