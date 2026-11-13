//go:build integration && (darwin || linux)

package integration_test

import (
	"bytes"
	"fmt"
	"strings"
	"syscall"
	"testing"
)

const reasoningCanary = "ROVO_REASONING_MUST_STAY_PRIVATE_93e8"

type snapshotPart struct {
	kind    string
	content string
}

// rovoSnapshot is a deliberately small native-shaped fixture. Message positions
// describe this revision only; they are not native message identities.
func rovoSnapshot(session, assistant string, user bool) ([]byte, []snapshotPart) {
	messages := []any{}
	expected := []snapshotPart{}
	if user {
		content := payloadCanary + " user\x1b[2J"
		messages = append(messages, map[string]any{
			"kind": "request", "parts": []any{map[string]any{
				"part_kind": "user-prompt", "content": content, "timestamp": "2026-11-13T00:00:00Z",
			}},
		})
		expected = append(expected, snapshotPart{"message.user", content})
	}
	content := payloadCanary + " " + assistant
	messages = append(messages, map[string]any{
		"kind": "response", "timestamp": "2026-11-13T00:00:01Z", "parts": []any{
			map[string]any{"part_kind": "text", "content": content},
			map[string]any{"part_kind": "thinking", "content": reasoningCanary},
		},
	})
	expected = append(expected, snapshotPart{"message.assistant", content}, snapshotPart{"record.ignored", ""})
	return jsonLine(map[string]any{"id": session, "message_history": messages}), expected
}

func assertSnapshotProvenance(t *testing.T, e event, path, session, revision string, generation int64) {
	t.Helper()
	if e.Agent != "rovo" || e.SessionID != session || e.Source.Path != path || e.Source.Generation != generation || e.Source.Revision != revision {
		t.Fatalf("incorrect snapshot identity or revision: %s", e.Raw)
	}
	sessionKey := ""
	if session != "" {
		sessionKey = digest([]byte("rovo\x00local\x00" + session))
	}
	if e.SessionKey != sessionKey {
		t.Fatalf("snapshot lost provider-scoped native session identity: %s", e.Raw)
	}
	if e.CaptureMode != "snapshot_observation" || e.Source.Kind != "snapshot" || e.NativeID != "" || e.MessageID != "" {
		t.Fatalf("snapshot invented native message IDs or append semantics: %s", e.Raw)
	}
	if strings.Contains(string(e.Raw), reasoningCanary) || bytes.Contains(e.Raw, []byte{0x1b}) {
		t.Fatalf("snapshot leaked reasoning or raw terminal controls: %q", e.Raw)
	}
}

// An atomic rename can race an already-open read. Only the documented transient
// diagnostic is tolerated, never an unexpected projection or schema error.
func nextSnapshot(t *testing.T, v *viewer, kind, path, session, revision string, generation int64) event {
	t.Helper()
	for transient := 0; transient < 3; transient++ {
		e := v.nextEvent(t)
		if e.Kind == kind {
			return e
		}
		if e.Kind != "snapshot.unstable" {
			t.Fatalf("expected %s, received %s: %s", kind, e.Kind, e.Raw)
		}
		// Identity may change in the pending revision, so an unstable read
		// still carries the last accepted session rather than the new one.
		assertSnapshotProvenance(t, e, path, e.SessionID, "", generation)
		if e.ContentCaptured || e.Content != "" || strings.Contains(string(e.Raw), payloadCanary) {
			t.Fatalf("transient diagnostic leaked snapshot content: %s", e.Raw)
		}
	}
	t.Fatalf("too many transient diagnostics before %s (accepted revision %s)", kind, revision)
	return event{}
}

func readSnapshotProjection(t *testing.T, v *viewer, path, session string, document []byte, parts []snapshotPart, generation int64, content bool) {
	t.Helper()
	revision := digest(document)
	boundary := nextSnapshot(t, v, "snapshot.updated", path, session, revision, generation-1)
	assertSnapshotProvenance(t, boundary, path, session, revision, generation)
	if boundary.Source.ItemIndex != nil || boundary.ContentCaptured || boundary.Content != "" {
		t.Fatalf("replacement boundary unexpectedly contains a message: %s", boundary.Raw)
	}
	seen := map[string]bool{boundary.ID: true}
	for index, part := range parts {
		e := v.next(t, part.kind)
		assertSnapshotProvenance(t, e, path, session, revision, generation)
		if e.Source.ItemIndex == nil || *e.Source.ItemIndex != index || seen[e.ID] {
			t.Fatalf("snapshot parts lost revision-local provenance: %s", e.Raw)
		}
		seen[e.ID] = true
		captured := content && part.content != ""
		if e.ContentCaptured != captured || strings.Contains(string(e.Raw), payloadCanary) != captured {
			t.Fatalf("snapshot content capture policy violated (enabled=%v): %s", content, e.Raw)
		}
		if captured && e.Content != part.content || !captured && e.Content != "" {
			t.Fatalf("snapshot projected incorrect content: %s", e.Raw)
		}
	}
}

func readSnapshotDiagnostic(t *testing.T, v *viewer, kind, path, session, revision string, generation int64) {
	t.Helper()
	e := nextSnapshot(t, v, kind, path, session, revision, generation)
	assertSnapshotProvenance(t, e, path, session, revision, generation)
	if e.ContentCaptured || e.Content != "" || strings.Contains(string(e.Raw), payloadCanary) {
		t.Fatalf("snapshot diagnostic leaked content: %s", e.Raw)
	}
}

func TestFixtureProcessesSnapshotReplacement(t *testing.T) {
	for _, content := range []bool{false, true} {
		t.Run(fmt.Sprintf("rovo/content=%t", content), func(t *testing.T) {
			w := startFixtureWriter(t, "snapshot.json")
			session := "rovo-integration-session"
			initial, initialParts := rovoSnapshot(session, "assistant-v1", true)
			w.send(t, "snapshot", initial)
			v := startViewer(t, "rovo", w.path, content)
			readSnapshotProjection(t, v, w.path, session, initial, initialParts, 1, content)
			w.assertUnchanged(t)

			// Gate a truncate/rebuild at its empty intermediate state. This
			// makes the read/write interleaving explicit instead of assuming
			// the observer misses the truncate-to-write scheduling window.
			rebuilt, rebuiltParts := rovoSnapshot(session, "assistant-v2", true)
			w.send(t, "rewrite", nil)
			readSnapshotDiagnostic(t, v, "snapshot.invalid", w.path, session, "", 1)
			w.send(t, "append", rebuilt)
			// Rebuild emits the full projection, including existing user text,
			// with one replacement boundary and a new byte revision.
			readSnapshotProjection(t, v, w.path, session, rebuilt, rebuiltParts, 2, content)

			// A stable partial write is invalid, not an empty history. Completing
			// the prior revision recovers without another replacement boundary.
			split := len(rebuilt) / 2
			w.send(t, "rewrite", nil)
			readSnapshotDiagnostic(t, v, "snapshot.invalid", w.path, session, "", 2)
			w.send(t, "append", rebuilt[:split])
			readSnapshotDiagnostic(t, v, "snapshot.invalid", w.path, session, "", 2)
			w.send(t, "append", rebuilt[split:])
			readSnapshotDiagnostic(t, v, "snapshot.recovered", w.path, session, digest(rebuilt), 2)

			// JSON validity alone must not commit an unsupported provider shape.
			unsupported := jsonLine(map[string]any{"id": session, "message_history": payloadCanary})
			w.send(t, "snapshot", unsupported)
			readSnapshotDiagnostic(t, v, "snapshot.unsupported", w.path, session, "", 2)
			w.send(t, "snapshot", rebuilt)
			readSnapshotDiagnostic(t, v, "snapshot.recovered", w.path, session, digest(rebuilt), 2)

			// Recovery from a missing path is an observation handshake: after
			// seeing unavailable, expose identical bytes through a different
			// inode. The successful read must emit available without replaying.
			w.send(t, "hide", nil)
			readSnapshotDiagnostic(t, v, "source.unavailable", w.path, session, digest(rebuilt), 2)
			w.archives[w.path+".hidden"] = bytes.Clone(rebuilt)
			w.send(t, "snapshot", rebuilt)
			readSnapshotDiagnostic(t, v, "source.available", w.path, session, digest(rebuilt), 2)
			w.assertUnchanged(t)

			// Pruning removes the user part from the replacement projection;
			// the following boundary also proves that no old parts were replayed.
			pruned, prunedParts := rovoSnapshot(session, "assistant-v2", false)
			w.send(t, "snapshot", pruned)
			readSnapshotProjection(t, v, w.path, session, pruned, prunedParts, 3, content)
			nextSession := "rovo-integration-new-session"
			changedIdentity, identityParts := rovoSnapshot(nextSession, "assistant-v2", false)
			w.send(t, "snapshot", changedIdentity)
			readSnapshotProjection(t, v, w.path, nextSession, changedIdentity, identityParts, 4, content)
			// An explicitly empty supported history is a replacement that clears
			// the projection, unlike an empty/truncated source document.
			emptyHistory := jsonLine(map[string]any{"id": nextSession, "message_history": []any{}})
			w.send(t, "snapshot", emptyHistory)
			readSnapshotProjection(t, v, w.path, nextSession, emptyHistory, nil, 5, content)
			w.assertUnchanged(t)

			v.stop(t, syscall.SIGTERM)
			continued, _ := rovoSnapshot(nextSession, "assistant-after-viewer-exit", false)
			w.send(t, "snapshot", continued)
			w.send(t, "ping", nil)
			w.assertUnchanged(t)
		})
	}
}
