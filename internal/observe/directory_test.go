//go:build darwin || linux

package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func dirFixture(agent, session, text string) []byte {
	var value any = map[string]any{"type": "user", "sessionId": session, "message": map[string]any{"content": text}, "timestamp": "2000-01-01T00:00:00Z"}
	if agent == "codex" {
		value = map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}, "timestamp": "2000-01-01T00:00:00Z"}
	}
	data, _ := json.Marshal(value)
	return append(data, '\n')
}

func dirSeed(agent, session string) []byte {
	if agent == "claude" {
		return dirFixture(agent, session, "OLD_PRIVATE_HISTORY")
	}
	data, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": session}})
	return append(append(data, '\n'), dirFixture(agent, session, "OLD_PRIVATE_HISTORY")...)
}

func dirWrite(t *testing.T, path string, data []byte, appendMode bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendMode {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func dirTest(t *testing.T, root, agent, history string, inspect bool) *Directory {
	t.Helper()
	d, err := NewDirectory(DirectoryOptions{Agent: agent, Root: root, History: history, Inspect: inspect, MaxRecordBytes: DefaultMaxRecordBytes, Normalizer: NormalizerOptions{IncludeContent: true, Scope: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func dirCollect(events *[]Event) func(Event) error {
	return func(event Event) error { *events = append(*events, event); return nil }
}

func dirDrain(t *testing.T, d *Directory, events *[]Event) {
	t.Helper()
	for rounds := 0; rounds < 100; rounds++ {
		more, err := d.readRound(context.Background(), dirCollect(events))
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			return
		}
	}
	t.Fatal("directory reader did not finish bounded input")
}

func TestDirectoryNewActivityAndDiscovery(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "yesterday", "rollout-existing.jsonl")
			partial := dirFixture(agent, "existing", "COMPLETED_AFTER_ATTACHMENT")
			dirWrite(t, path, append(dirSeed(agent, "existing"), partial[:len(partial)/2]...), false)
			d := dirTest(t, root, agent, "none", false)
			var events []Event
			if err := d.scan(context.Background(), true, dirCollect(&events)); err != nil {
				t.Fatal(err)
			}
			dirDrain(t, d, &events)
			if len(events) != 2 || events[0].Kind != "source.discovered" || events[1].Kind != "source.ready" || events[1].SessionID != "existing" {
				t.Fatalf("initial inventory/context: %+v", events)
			}
			if cap(d.sources[path].reader.pending) != 0 {
				t.Fatal("idle source retained incomplete payload")
			}
			events = nil
			dirWrite(t, path, partial[len(partial)/2:], true)
			newPath := filepath.Join(root, "new-date", "nested", "rollout-new.jsonl")
			dirWrite(t, newPath, append(dirSeed(agent, "new"), dirFixture(agent, "new", "BEFORE_FIRST_DISCOVERY")...), false)
			if err := d.scan(context.Background(), false, dirCollect(&events)); err != nil {
				t.Fatal(err)
			}
			dirDrain(t, d, &events)
			found := map[string]Event{}
			for _, e := range events {
				if e.Kind == "message.user" {
					found[e.Content] = e
				}
			}
			if len(found) != 3 {
				t.Fatalf("lost/replayed records: %+v", events)
			}
			if e := found["COMPLETED_AFTER_ATTACHMENT"]; e.SessionID != "existing" || e.ObservationPhase != "update" {
				t.Fatalf("partial completion/context: %+v", e)
			}
			if e := found["BEFORE_FIRST_DISCOVERY"]; e.SessionID != "new" || e.ObservationPhase != "discovered_history" {
				t.Fatalf("new file history: %+v", e)
			}
			events = nil
			if err := d.scan(context.Background(), false, dirCollect(&events)); err != nil {
				t.Fatal(err)
			}
			dirDrain(t, d, &events)
			if len(events) != 0 {
				t.Fatalf("unchanged files replayed: %+v", events)
			}
		})
	}
}

func TestDirectoryFrozenInspectionAndSourceIsolation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout-one.jsonl")
	original := append(dirSeed("codex", "first"), []byte("{\"unfinished\":")...)
	dirWrite(t, path, original, false)
	d := dirTest(t, root, "codex", "all", true)
	var events []Event
	if err := d.scan(context.Background(), true, dirCollect(&events)); err != nil {
		t.Fatal(err)
	}
	dirWrite(t, path, []byte("true}\n"), true)
	dirWrite(t, filepath.Join(root, "rollout-two.jsonl"), dirSeed("codex", "late"), false)
	dirDrain(t, d, &events)
	var user, incomplete int
	for _, e := range events {
		if e.Kind == "message.user" {
			user++
			if e.SessionID != "first" || e.ObservationPhase != "history" {
				t.Fatalf("bad history: %+v", e)
			}
		}
		if e.Kind == "record.incomplete" {
			incomplete++
		}
		if e.Kind == "record.unsupported" || e.SessionID == "late" {
			t.Fatalf("inspection grew beyond captured membership/boundaries: %+v", e)
		}
	}
	if user != 1 || incomplete != 1 {
		t.Fatalf("bounded inspection: %+v", events)
	}
	actual, _ := os.ReadFile(path)
	if !bytes.Equal(actual, append(original, []byte("true}\n")...)) {
		t.Fatal("source modified")
	}
}

func TestDirectoryReplacementMissingAndSymlinkRecovery(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "project", "rollout-a.jsonl")
	dirWrite(t, path, dirSeed("codex", "before"), false)
	dirWrite(t, filepath.Join(outside, "rollout-a.jsonl"), dirSeed("codex", "OUTSIDE_SECRET"), false)
	if err := os.Symlink(outside, filepath.Join(root, "linked-directory")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "rollout-a.jsonl"), filepath.Join(root, "rollout-link.jsonl")); err != nil {
		t.Fatal(err)
	}
	d := dirTest(t, root, "codex", "none", false)
	var events []Event
	step := func(initial bool) {
		if err := d.scan(context.Background(), initial, dirCollect(&events)); err != nil {
			t.Fatal(err)
		}
		dirDrain(t, d, &events)
	}
	step(true)
	if len(d.sources) != 1 {
		t.Fatal("discovery followed a symlink")
	}
	events = nil
	if err := os.Rename(filepath.Dir(path), filepath.Join(root, "held")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	step(false)
	foundUnavailable := false
	for _, e := range events {
		if e.Source.Path == path && e.Kind == "source.unavailable" {
			foundUnavailable = true
		}
		if e.SessionID == "OUTSIDE_SECRET" {
			t.Fatal("followed swapped ancestor symlink")
		}
	}
	if !foundUnavailable {
		t.Fatal("missing source not reported")
	}
	if err := os.Remove(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	dirWrite(t, path, dirSeed("codex", "replacement"), false)
	events = nil
	step(false)
	var replacement bool
	for _, e := range events {
		if e.Kind == "message.user" && e.Source.Path == path {
			replacement = e.SessionID == "replacement" && e.ObservationPhase == "replacement_history"
		}
	}
	if !replacement {
		t.Fatalf("replacement lost identity/history distinction: %+v", events)
	}
	events = nil
	dirWrite(t, path, dirFixture("codex", "", "AFTER_REPLACEMENT"), true)
	step(false)
	if len(events) != 1 || events[0].SessionID != "replacement" || events[0].ObservationPhase != "update" {
		t.Fatalf("replacement follow: %+v", events)
	}
}

func TestDirectoryFairSchedulingAndVisibleLimits(t *testing.T) {
	root := t.TempDir()
	dirWrite(t, filepath.Join(root, "rollout-big.jsonl"), bytes.Repeat(dirFixture("claude", "big", "history"), 3000), false)
	dirWrite(t, filepath.Join(root, "rollout-small.jsonl"), dirSeed("claude", "small"), false)
	d := dirTest(t, root, "claude", "all", false)
	var events []Event
	if err := d.scan(context.Background(), true, dirCollect(&events)); err != nil {
		t.Fatal(err)
	}
	more, err := d.readRound(context.Background(), dirCollect(&events))
	if err != nil || !more {
		t.Fatalf("expected large backlog: %v", err)
	}
	var small bool
	for _, e := range events {
		if e.SessionID == "small" && e.Kind == "message.user" {
			small = true
		}
	}
	if !small {
		t.Fatal("large history starved another source")
	}
	d = dirTest(t, root, "claude", "all", true)
	d.maxSources = 1
	events = nil
	if err := d.scan(context.Background(), true, dirCollect(&events)); err != nil {
		t.Fatal(err)
	}
	var limit bool
	for _, e := range events {
		if e.Kind == "directory.limit" {
			limit = true
		}
	}
	if !limit || !d.inspectError {
		t.Fatal("source limit silently hid coverage")
	}
}

func TestDirectoryLargePartialTailMakesProgress(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout-large.jsonl")
	line := dirFixture("claude", "large", strings.Repeat("x", DefaultBatchBytes+10))
	dirWrite(t, path, line[:len(line)-1], false)
	d := dirTest(t, root, "claude", "none", false)
	d.options.MaxRecordBytes = 8 * 1024 * 1024
	var events []Event
	if err := d.scan(context.Background(), true, dirCollect(&events)); err != nil {
		t.Fatal(err)
	}
	dirDrain(t, d, &events)
	if cap(d.sources[path].reader.pending) != 0 {
		t.Fatal("large idle partial retained in registry")
	}
	dirWrite(t, path, []byte("\n"), true)
	events = nil
	if err := d.scan(context.Background(), false, dirCollect(&events)); err != nil {
		t.Fatal(err)
	}
	dirDrain(t, d, &events)
	if len(events) != 1 || events[0].Content != strings.Repeat("x", DefaultBatchBytes+10) {
		t.Fatal("large incomplete record failed to complete")
	}
}

func TestDirectoryRunEmptyCancellationAndMetadata(t *testing.T) {
	root := t.TempDir()
	d := dirTest(t, root, "codex", "none", false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []Event
	err := d.Run(ctx, 100*time.Millisecond, func(e Event) error {
		events = append(events, e)
		if e.Kind == "directory.ready" {
			cancel()
		}
		return nil
	})
	if err != nil || len(events) != 1 || events[0].ExecutionState != "unknown" {
		t.Fatalf("empty root/cancellation: %v %+v", err, events)
	}
}
