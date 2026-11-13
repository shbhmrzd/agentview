package observe

import (
	"bytes"
	"encoding/json"
	"errors"
)

type rovoDecoder struct{ decoderState }

// Native session_context.json wraps Pydantic AI request/response messages.
// It is a current snapshot, not an immutable stream of execution events.
type rovoSnapshot struct {
	ID       string        `json:"id"`
	Messages []rovoMessage `json:"message_history"`
}
type rovoMessage struct {
	Kind      string     `json:"kind"`
	Timestamp string     `json:"timestamp"`
	Parts     []rovoPart `json:"parts"`
}
type rovoPart struct {
	Kind       string          `json:"part_kind"`
	Timestamp  string          `json:"timestamp"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	ToolName   string          `json:"tool_name"`
	Args       json.RawMessage `json:"args"`
	Outcome    *string         `json:"outcome"`
}

func parseRovo(data []byte) (rovoSnapshot, error) {
	var snapshot rovoSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return snapshot, err
	}
	if snapshot.ID == "" || snapshot.Messages == nil {
		return snapshot, errors.New("missing native identity or message history")
	}
	for _, msg := range snapshot.Messages {
		if (msg.Kind != "request" && msg.Kind != "response") || msg.Parts == nil {
			return snapshot, errors.New("unsupported message envelope")
		}
		for _, p := range msg.Parts {
			if p.Kind == "" {
				return snapshot, errors.New("missing part kind")
			}
			switch p.Kind {
			case "user-prompt", "system-prompt", "tool-return":
				if msg.Kind != "request" {
					return snapshot, errors.New("part is not a request part")
				}
			case "text", "thinking", "tool-call":
				if msg.Kind != "response" {
					return snapshot, errors.New("part is not a response part")
				}
			}
			if p.Kind == "user-prompt" {
				content := bytes.TrimSpace(p.Content)
				if len(content) == 0 || (content[0] != '"' && content[0] != '[') {
					return snapshot, errors.New("invalid user content")
				}
			}
			if p.Kind == "tool-call" || p.Kind == "tool-return" {
				if p.ToolCallID == "" || p.ToolName == "" {
					return snapshot, errors.New("missing tool identity")
				}
			}
			if p.Kind == "tool-return" {
				if len(p.Content) == 0 {
					return snapshot, errors.New("missing tool result")
				}
				if p.Outcome != nil && *p.Outcome != "success" && *p.Outcome != "failed" && *p.Outcome != "denied" {
					return snapshot, errors.New("invalid tool outcome")
				}
			}
			if p.Kind == "text" {
				var text string
				if len(p.Content) == 0 || bytes.Equal(p.Content, []byte("null")) || json.Unmarshal(p.Content, &text) != nil {
					return snapshot, errors.New("invalid text content")
				}
			}
		}
	}
	return snapshot, nil
}

func validRovo(data []byte) bool { _, err := parseRovo(data); return err == nil }

func (n *rovoDecoder) Decode(r Record, base Event) []Event {
	snapshot, err := parseRovo(r.Data)
	if err != nil {
		return []Event{invalid(base)}
	}
	n.sessionID, base.SessionID = snapshot.ID, snapshot.ID
	base.NativeType, base.Kind = "session_context", "snapshot.updated"
	base.Detail = "Replace the prior snapshot projection with this revision. Its contents are history, not newly executed actions."
	out := []Event{base}
	index := 0
	for _, message := range snapshot.Messages {
		for _, part := range message.Parts {
			e := base
			e.Detail = ""
			if len(out) >= 255 {
				e.Kind = "record.excess_blocks"
				e.Detail = "Only the first 254 snapshot parts are projected; remaining parts omitted."
				return append(out, e)
			}
			item := index
			index++
			e.Source.ItemIndex = &item
			e.NativeType = message.Kind + ":" + part.Kind
			setTimestamp(&e, message.Timestamp)
			if part.Timestamp != "" {
				e.OccurredAt = ""
				e.TimeStatus = "missing"
				setTimestamp(&e, part.Timestamp)
			}
			switch part.Kind {
			case "user-prompt":
				if message.Kind != "request" {
					e = invalid(e)
					break
				}
				var text string
				if json.Unmarshal(part.Content, &text) == nil && !bytes.Equal(part.Content, []byte("null")) {
					e.Kind = "message.user"
					n.content(&e, text)
				} else {
					e = unsupported(e)
				} // Multimodal parts require a separate allowlist.
			case "text":
				if message.Kind != "response" {
					e = invalid(e)
					break
				}
				e.Kind = "message.assistant"
				n.content(&e, rawText(part.Content))
			case "tool-call":
				if message.Kind != "response" || part.ToolCallID == "" || part.ToolName == "" {
					e = invalid(e)
					break
				}
				e.Kind, e.ToolCallID, e.ToolName = "tool.requested", part.ToolCallID, part.ToolName
				n.content(&e, rawText(part.Args))
			case "tool-return":
				if message.Kind != "request" || part.ToolCallID == "" || part.ToolName == "" {
					e = invalid(e)
					break
				}
				e.Kind, e.ToolCallID, e.ToolName = "tool.result", part.ToolCallID, part.ToolName
				if part.Outcome != nil {
					failed := *part.Outcome != "success"
					e.IsError = &failed
				}
				n.content(&e, rawText(part.Content))
			case "system-prompt", "thinking":
				e = ignored(e)
			default:
				e = unsupported(e)
			}
			out = append(out, e)
		}
	}
	return out
}
