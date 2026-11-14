package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/shbhmrzd/agentview/internal/observe"
)

// Export uses an allowlist projection, not regex replacement inside private
// payloads. IDs remain correlatable within one export but not across exports.
type exportRedactor struct{ salt string }

func (r exportRedactor) alias(value string) string {
	if value == "" {
		return ""
	}
	h := sha256.Sum256([]byte(r.salt + "\x00" + value))
	return "ref-" + hex.EncodeToString(h[:12])
}
func (r exportRedactor) event(e observe.Event) map[string]any {
	out := map[string]any{"type": "event", "provider": e.Agent, "kind": e.Kind, "time": e.OccurredAt, "time_status": e.TimeStatus, "id": r.alias(e.ID), "source": r.alias(e.Source.Path), "reader": r.alias(e.Source.ReaderID), "generation": e.Source.Generation, "offset": e.Source.Offset, "end_offset": e.Source.EndOffset, "call_id": r.alias(e.ToolCallID), "session_id": r.alias(e.SessionID), "turn_id": r.alias(e.TurnID), "content_omitted": true}
	if e.Call != nil {
		out["correlation"] = map[string]any{"status": e.Call.Status, "duration_ms": e.Call.DurationMS}
		if e.Call.Request != nil {
			out["request_id"] = r.alias(e.Call.Request.ID)
		}
	}
	if len(e.PrecedingCalls) > 0 {
		refs := []string{}
		for _, id := range e.PrecedingCalls {
			refs = append(refs, r.alias(id))
		}
		out["preceding_calls"] = refs
	}
	if e.Outcome != nil {
		out["outcome"] = e.Outcome
	}
	if e.PartialRecord {
		out["partial_record"] = true
	}
	if len(e.RelatedAgentPaths) > 0 {
		paths := []string{}
		for _, path := range e.RelatedAgentPaths {
			paths = append(paths, r.alias(e.SessionID+"\x00"+path))
		}
		out["related_agent_paths"] = paths
	}
	if e.Stats != nil {
		out["reported_stats"] = e.Stats
	}
	if e.Usage != nil {
		out["usage"] = e.Usage
	}
	if e.Communication != nil {
		out["communication"] = map[string]any{"author": r.alias(e.Communication.Author), "recipient": r.alias(e.Communication.Recipient), "trigger_turn": e.Communication.TriggerTurn}
	}
	if len(e.RelatedAgentIDs) > 0 {
		ids := []string{}
		for _, id := range e.RelatedAgentIDs {
			ids = append(ids, r.alias(id))
		}
		out["related_agents"] = ids
	}
	name := e.ToolName[strings.LastIndex(e.ToolName, ".")+1:]
	switch name {
	case "exec_command", "write_stdin", "exec", "Agent", "Task", "spawn_agent", "send_message", "send_input", "Read", "Write", "Edit", "Bash":
		out["tool"] = name
	default:
		out["tool"] = r.alias(e.ToolName)
	}
	return out
}

func (s *Server) export(w http.ResponseWriter, r *http.Request, session Session) {
	select {
	case s.historySlots <- struct{}{}:
		defer func() { <-s.historySlots }()
	default:
		http.Error(w, "Reader busy; retry shortly", 429)
		return
	}
	var root string
	for _, candidate := range s.options.Roots {
		if candidate.Agent == session.Agent {
			root = candidate.Path
			break
		}
	}
	pager, err := observe.NewHistoryPager(root, session.Path, session.Agent, observe.NormalizerOptions{EvidenceBudget: s.evidenceBudget}, s.options.MaxRecordBytes)
	if err != nil {
		http.Error(w, "Could not open source for export", 503)
		return
	}
	defer pager.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="agentview-evidence.jsonl"`)
	enc := json.NewEncoder(w)
	redactor := exportRedactor{salt: rand.Text()}
	if enc.Encode(map[string]any{"type": "manifest", "schema_version": 1, "scope": "selected source only", "redaction": "All prompts, payloads, titles, raw diagnostics and paths omitted; identifiers aliased per export. Provider, timestamps, usage, process outcomes and byte positions retained.", "coverage": "Frozen first-open boundary; maximum 100000 records or 15 seconds. A complete footer is required; no footer means incomplete."}) != nil {
		return
	}
	count := 0
	complete := false
	reason := ""
	for count < 100000 {
		events, more, readErr := pager.Next(ctx, min(200, 100000-count))
		if readErr != nil {
			reason = "Source changed, became unavailable, or read timed out"
			break
		}
		for _, e := range events {
			if enc.Encode(redactor.event(e)) != nil {
				return
			}
			count++
		}
		if !more {
			complete = true
			break
		}
	}
	if !complete && reason == "" {
		reason = "Export record limit reached"
	}
	_ = enc.Encode(map[string]any{"type": "coverage", "complete": complete, "records": count, "reason": reason})
}
