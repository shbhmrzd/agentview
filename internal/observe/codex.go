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
	return n.payload(base, row.Type, row.Payload)
}

func (n *codexDecoder) payload(base Event, typ string, raw json.RawMessage) []Event {
	var payload struct {
		Type           string          `json:"type"`
		ID             string          `json:"id"`
		SessionID      string          `json:"session_id"`
		ParentThreadID string          `json:"parent_thread_id"`
		ForkedFromID   string          `json:"forked_from_id"`
		TurnID         string          `json:"turn_id"`
		CallID         string          `json:"call_id"`
		Name           string          `json:"name"`
		Arguments      json.RawMessage `json:"arguments"`
		Input          json.RawMessage `json:"input"`
		Output         json.RawMessage `json:"output"`
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
		n.sessionID, n.groupSessionID = payload.ID, payload.SessionID
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
				n.content(&base, rawText(payload.Arguments))
			} else {
				n.content(&base, rawText(payload.Input))
			}
		case "function_call_output", "custom_tool_call_output":
			base.Kind = "tool.result"
			base.ToolCallID = payload.CallID
			n.content(&base, rawText(payload.Output))
		case "reasoning":
			base = ignored(base)
		default:
			base = unsupported(base)
		}
	case "event_msg":
		switch payload.Type {
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
	case "turn_context":
		base = ignored(base)
	default:
		base = unsupported(base)
	}
	return []Event{base}
}
