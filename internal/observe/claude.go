package observe

import "encoding/json"

type claudeDecoder struct{ decoderState }

func (n *claudeDecoder) Decode(r Record, base Event) []Event {
	var row struct {
		Type       string          `json:"type"`
		Timestamp  string          `json:"timestamp"`
		SessionID  string          `json:"sessionId"`
		Cwd        string          `json:"cwd"`
		UUID       string          `json:"uuid"`
		ParentUUID string          `json:"parentUuid"`
		Message    json.RawMessage `json:"message"`
		AgentID    string          `json:"agentId"`
		Effort     string          `json:"effort"`
	}
	if json.Unmarshal(r.Data, &row) != nil || row.Type == "" {
		return []Event{invalid(base)}
	}
	base.NativeType = row.Type
	setTimestamp(&base, row.Timestamp)
	if row.SessionID != "" {
		n.sessionID = row.SessionID
	}
	if row.Cwd != "" {
		n.workingDirectory = row.Cwd
	}
	base.SessionID = n.sessionID
	if row.AgentID != "" {
		n.debug.AgentID = row.AgentID
		base.NativeAgentID = row.AgentID
	}
	if row.Effort != "" {
		n.debug.Effort = row.Effort
	}
	base.NativeID, base.ParentID = row.UUID, row.ParentUUID
	if row.Type == "user" || row.Type == "assistant" {
		var message struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage *struct {
				Input  *int64 `json:"input_tokens"`
				Cached *int64 `json:"cache_read_input_tokens"`
				Write  *int64 `json:"cache_creation_input_tokens"`
				Output *int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(row.Message, &message)
		if message.Model != "" {
			n.debug.Model = message.Model
		}
		events := n.message(base, row.Message, row.Type)
		if row.Type == "assistant" && message.Usage != nil && message.ID != "" {
			u := message.Usage
			usage := &TokenUsage{Input: u.Input, Cached: u.Cached, CacheWrite: u.Write, Output: u.Output}
			if usage.valid() {
				e := base
				e.Kind, e.Usage, e.UsageBasis, e.UsageID = "usage.snapshot", usage, "claude_message", message.ID
				events = append(events, e)
			}
		}
		return events
	}
	return []Event{unsupported(base)}
}
