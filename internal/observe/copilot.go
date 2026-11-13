package observe

import "encoding/json"

type copilotDecoder struct{ decoderState }

// Disk event envelope, not the CLI's --output-format transport stream.
// See docs/provider-format-evidence.md for the pinned upstream contract.
func (n *copilotDecoder) Decode(r Record, base Event) []Event {
	var row struct {
		ID         string          `json:"id"`
		Type       string          `json:"type"`
		Timestamp  string          `json:"timestamp"`
		ParentID   *string         `json:"parentId"`
		Data       json.RawMessage `json:"data"`
		AgentID    string          `json:"agentId"`
		Checkpoint *bool           `json:"checkpoint"`
	}
	if json.Unmarshal(r.Data, &row) != nil || row.Type == "" || len(row.Data) == 0 {
		return []Event{invalid(base)}
	}
	base.NativeType, base.NativeID = row.Type, row.ID
	base.NativeAgentID, base.CheckpointReplay = row.AgentID, row.Checkpoint
	if row.ParentID != nil {
		base.ParentID = *row.ParentID
	}
	setTimestamp(&base, row.Timestamp)
	var data struct {
		SessionID    string          `json:"sessionId"`
		Content      *string         `json:"content"`
		MessageID    string          `json:"messageId"`
		TurnID       string          `json:"turnId"`
		ToolCallID   string          `json:"toolCallId"`
		ToolName     string          `json:"toolName"`
		Arguments    json.RawMessage `json:"arguments"`
		Success      *bool           `json:"success"`
		ToolRequests []struct {
			ID        string          `json:"toolCallId"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"toolRequests"`
		Result *struct {
			Content string `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(row.Data, &data) != nil {
		return []Event{invalid(base)}
	}
	base.TurnID, base.MessageID = data.TurnID, data.MessageID
	switch row.Type {
	case "session.start":
		if data.SessionID == "" {
			return []Event{invalid(base)}
		}
		n.sessionID, base.SessionID = data.SessionID, data.SessionID
		base.Kind = "session.metadata"
	case "user.message":
		if data.Content == nil {
			return []Event{invalid(base)}
		}
		base.Kind = "message.user"
		n.content(&base, *data.Content)
	case "assistant.message":
		var out []Event
		if data.Content != nil && (*data.Content != "" || len(data.ToolRequests) == 0) {
			base.Kind = "message.assistant"
			n.content(&base, *data.Content)
			out = append(out, base)
		}
		for _, request := range data.ToolRequests {
			if len(out) >= 255 {
				e := base
				e.Kind = "record.excess_blocks"
				e.Content = ""
				e.ContentCaptured = false
				e.Detail = "Only the first 255 message/tool blocks are projected; remaining blocks omitted."
				return append(out, e)
			}
			e := base
			e.Content = ""
			e.ContentCaptured = false
			if request.ID == "" || request.Name == "" {
				out = append(out, invalid(e))
				continue
			}
			e.Kind, e.ToolCallID, e.ToolName = "tool.requested", request.ID, request.Name
			n.content(&e, rawText(request.Arguments))
			out = append(out, e)
		}
		if len(out) == 0 {
			return []Event{ignored(base)}
		}
		return out
	case "tool.execution_start":
		if data.ToolCallID == "" || data.ToolName == "" {
			return []Event{invalid(base)}
		}
		base.Kind, base.ToolCallID, base.ToolName = "tool.started", data.ToolCallID, data.ToolName
		n.content(&base, rawText(data.Arguments))
	case "tool.execution_complete":
		if data.ToolCallID == "" || data.Success == nil {
			return []Event{invalid(base)}
		}
		base.Kind, base.ToolCallID = "tool.result", data.ToolCallID
		failed := !*data.Success
		base.IsError = &failed
		text := ""
		if data.Result != nil {
			text = data.Result.Content
		}
		if data.Error != nil {
			if text != "" {
				text += "\n"
			}
			text += data.Error.Message
		}
		n.content(&base, text)
	case "assistant.turn_start":
		base.Kind = "turn.started"
	case "assistant.turn_end":
		base.Kind = "turn.ended"
	case "assistant.reasoning", "assistant.reasoning_delta", "assistant.message_delta", "session.usage_info":
		base = ignored(base)
	default:
		base = unsupported(base)
	}
	return []Event{base}
}
