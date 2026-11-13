package observe

import (
	"bytes"
	"encoding/json"
)

func (n *decoderState) message(base Event, raw json.RawMessage, role string) []Event {
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &msg) != nil || len(msg.Content) == 0 {
		return []Event{invalid(base)}
	}
	if bytes.Equal(bytes.TrimSpace(msg.Content), []byte("null")) {
		return []Event{invalid(base)}
	}
	if role == "" {
		role = msg.Role
	}
	if role != "user" && role != "assistant" {
		return []Event{ignored(base)}
	}
	var text string
	if json.Unmarshal(msg.Content, &text) == nil {
		base.Kind = "message." + role
		n.content(&base, text)
		return []Event{base}
	}
	type blockType struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		ToolUseID string          `json:"tool_use_id"`
		Input     json.RawMessage `json:"input"`
		Content   json.RawMessage `json:"content"`
		IsError   *bool           `json:"is_error"`
	}
	dec := json.NewDecoder(bytes.NewReader(msg.Content))
	opening, err := dec.Token()
	if err != nil || opening != json.Delim('[') {
		return []Event{invalid(base)}
	}
	out := make([]Event, 0, 4)
	for dec.More() {
		if len(out) >= 255 {
			e := base
			e.Kind = "record.excess_blocks"
			e.Detail = "Only the first 255 message blocks are projected; remaining blocks omitted."
			return append(out, e)
		}
		var block blockType
		if err := dec.Decode(&block); err != nil {
			return []Event{invalid(base)}
		}
		e := base
		switch block.Type {
		case "text", "input_text", "output_text":
			e.Kind = "message." + role
			n.content(&e, block.Text)
		case "tool_use":
			e.Kind = "tool.requested"
			e.ToolCallID, e.ToolName = block.ID, block.Name
			n.content(&e, rawText(block.Input))
		case "tool_result":
			e.Kind = "tool.result"
			e.ToolCallID, e.IsError = block.ToolUseID, block.IsError
			n.content(&e, rawText(block.Content))
		case "thinking", "redacted_thinking", "reasoning":
			e = ignored(e)
		default:
			e = unsupported(e)
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return []Event{invalid(base)}
	}
	return out
}

func (n *decoderState) content(e *Event, text string) {
	if n.includeContent {
		e.Content = text
		e.ContentCaptured = true
	}
}
