package observe

import "encoding/json"

type codexDecoder struct{ decoderState }

func (n *codexDecoder) Decode(r Record, base Event) []Event {
	var row struct {
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		Ordinal   *uint64         `json:"ordinal"`
		Payload   json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(r.Data, &row) != nil || row.Type == "" {
		return []Event{invalid(base)}
	}
	base.NativeType, base.SourceOrdinal = row.Type, row.Ordinal
	setTimestamp(&base, row.Timestamp)
	if n.inherited(row.Ordinal) {
		return []Event{inheritedRecord(base)}
	}

	return n.payload(base, row.Type, row.Payload)
}

func (n *codexDecoder) payload(base Event, typ string, raw json.RawMessage) []Event {
	var payload struct {
		Type           string          `json:"type"`
		ID             string          `json:"id"`
		SessionID      string          `json:"session_id"`
		Cwd            string          `json:"cwd"`
		ParentThreadID string          `json:"parent_thread_id"`
		ForkedFromID   string          `json:"forked_from_id"`
		TurnID         string          `json:"turn_id"`
		CallID         string          `json:"call_id"`
		Name           string          `json:"name"`
		Arguments      json.RawMessage `json:"arguments"`
		Input          json.RawMessage `json:"input"`
		Output         json.RawMessage `json:"output"`
		IsError        *bool           `json:"is_error"`
		Author         string          `json:"author"`
		Recipient      string          `json:"recipient"`
		TriggerTurn    *bool           `json:"trigger_turn"`
		Message        string          `json:"message"`
		Source         json.RawMessage `json:"source"`
		AgentNickname  string          `json:"agent_nickname"`
		AgentRole      string          `json:"agent_role"`
		AgentPath      string          `json:"agent_path"`
		Model          string          `json:"model"`
		Effort         string          `json:"effort"`
		ResponseID     string          `json:"response_id"`
		ThreadID       string          `json:"thread_id"`
		HistoryStart   *uint64         `json:"subagent_history_start_ordinal"`
		ThreadUsage    *TokenUsage     `json:"thread_token_usage"`
		Info           *struct {
			Total         *TokenUsage `json:"total_token_usage"`
			ContextWindow *int64      `json:"model_context_window"`
		} `json:"info"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &payload) != nil {
		return []Event{invalid(base)}
	}
	base.NativeType = typ + ":" + payload.Type
	base.NativeID = payload.ID
	base.TurnID = payload.TurnID
	switch typ {
	case "session_meta":
		if payload.ID == "" {
			return []Event{invalid(base)}
		}
		// The first metadata record owns this source generation. Forked native
		// transcripts can later replay parent metadata; it must not replace the
		// child's identity, ancestry, or configuration.
		if n.sessionID != "" {
			base = ignored(base)
			base.Detail = "Additional session metadata omitted; source owner identity is retained."
			return []Event{base}
		}
		n.sessionID, n.groupSessionID = payload.ID, payload.SessionID
		n.debug.HistoryStartOrdinal = payload.HistoryStart
		n.workingDirectory = payload.Cwd
		n.debug.ParentThreadID, n.debug.ForkedFromID = payload.ParentThreadID, payload.ForkedFromID
		n.debug.Name, n.debug.Role, n.debug.AgentPath = payload.AgentNickname, payload.AgentRole, payload.AgentPath
		var source struct {
			Subagent struct {
				ThreadSpawn struct {
					Parent string `json:"parent_thread_id"`
					Name   string `json:"agent_nickname"`
					Role   string `json:"agent_role"`
					Path   string `json:"agent_path"`
				} `json:"thread_spawn"`
			} `json:"subagent"`
		}
		if json.Unmarshal(payload.Source, &source) == nil {
			spawn := source.Subagent.ThreadSpawn
			if n.debug.ParentThreadID == "" {
				n.debug.ParentThreadID = spawn.Parent
			}
			if n.debug.Name == "" {
				n.debug.Name = spawn.Name
			}
			if n.debug.Role == "" {
				n.debug.Role = spawn.Role
			}
			if n.debug.AgentPath == "" {
				n.debug.AgentPath = spawn.Path
			}
		}
		base.SessionID, base.GroupSessionID = n.sessionID, n.groupSessionID
		base.ParentThreadID, base.ForkedFromID = payload.ParentThreadID, payload.ForkedFromID
		base.Kind = "session.metadata"
	case "response_item":
		switch payload.Type {
		case "message":
			return n.message(base, raw, "")
		case "function_call", "custom_tool_call":
			base.Kind = "tool.requested"
			base.ToolCallID, base.ToolName = payload.CallID, payload.Name

			if payload.Type == "function_call" {
				base.Coordination = coordination(payload.Name, rawText(payload.Arguments))
				n.content(&base, rawText(payload.Arguments))
			} else {
				base.Coordination = coordination(payload.Name, rawText(payload.Input))
				n.content(&base, rawText(payload.Input))
			}
		case "function_call_output", "custom_tool_call_output":
			base.Kind = "tool.result"
			base.ToolCallID = payload.CallID
			base.nativeOutput = payload.Output
			base.IsError = payload.IsError

			n.content(&base, rawText(payload.Output))
		case "agent_message":
			events := n.message(base, raw, "assistant")
			for i := range events {
				if events[i].Kind == "message.assistant" {
					events[i].Kind = "agent.message"
					events[i].Communication = &Communication{Author: payload.Author, Recipient: payload.Recipient, Evidence: "recorded agent message; receipt and processing not established"}
				}
			}
			return events
		case "reasoning":
			base = ignored(base)
		default:
			base = unsupported(base)
		}
	case "event_msg":
		switch payload.Type {
		case "token_count":
			if payload.Info == nil || !payload.Info.Total.valid() {
				return []Event{ignored(base)}
			}
			n.debug.ContextWindow = payload.Info.ContextWindow
			base.Kind, base.Usage, base.UsageBasis = "usage.snapshot", payload.Info.Total, "codex_token_count"
		case "item_completed", "item_started":
			base = ignored(base)
		case "task_started", "turn_started":
			base.Kind = "turn.started"
		case "task_complete", "turn_complete":
			base.Kind = "turn.ended"
		case "turn_aborted":
			base.Kind = "turn.interrupted"
		case "agent_message", "user_message", "agent_reasoning", "agent_reasoning_raw_content":
			// Transcript messages use response_item as their canonical source;
			// event_msg copies do not become duplicate conversation messages.
			base = ignored(base)
		default:
			base = unsupported(base)
		}
	case "inter_agent_communication_metadata":
		base.Kind = "agent.communication"
		base.Communication = &Communication{TriggerTurn: payload.TriggerTurn, Evidence: "standalone native metadata; not counted as another message or delivery acknowledgement"}
	case "compacted":
		base.Kind = "context.compacted"
		base.Detail = "Recorded compaction boundary. Replacement history is not replayed as new conversation."
		n.content(&base, payload.Message)
	case "turn_context":
		n.debug.Model, n.debug.Effort = payload.Model, payload.Effort
		base.Kind = "session.context"
	case "token_usage_record":
		if payload.ThreadID != "" && payload.ThreadID != n.sessionID {
			return []Event{ignored(base)}
		}
		if !payload.ThreadUsage.valid() {
			return []Event{ignored(base)}
		}
		base.Kind, base.Usage, base.UsageBasis, base.UsageID = "usage.snapshot", payload.ThreadUsage, "codex_thread_cumulative", payload.ResponseID
	default:
		base = unsupported(base)
	}
	return []Event{base}
}

func (n *codexDecoder) inherited(ordinal *uint64) bool {
	return n.sessionID != "" && n.debug.HistoryStartOrdinal != nil && ordinal != nil && *ordinal < *n.debug.HistoryStartOrdinal
}
func inheritedRecord(base Event) Event {
	base = ignored(base)
	base.Inherited = true
	base.Detail = "Inherited parent context precedes this subagent's recorded history boundary; omitted from agent activity."
	return base
}
