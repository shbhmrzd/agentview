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

		ToolUseResult json.RawMessage `json:"toolUseResult"`
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
	switch row.Type {
	case "custom-title":
		var row struct {
			CustomTitle string `json:"customTitle"`
		}
		if json.Unmarshal(r.Data, &row) != nil {
			return []Event{invalid(base)}
		}
		base.Kind = "session.title"
		if n.includeContent {
			base.SessionTitle = row.CustomTitle
		}
		n.content(&base, row.CustomTitle)
		return []Event{base}
	case "cost-state":
		var row struct {
			CostUSD             *float64 `json:"totalCostUSD"`
			APIMS               *int64   `json:"totalAPIDuration"`
			APIWithoutRetriesMS *int64   `json:"totalAPIDurationWithoutRetries"`
			ToolMS              *int64   `json:"totalToolDuration"`
		}
		if json.Unmarshal(r.Data, &row) != nil {
			return []Event{invalid(base)}
		}
		base.Kind = "usage.reported"
		base.Stats = &ReportedStats{CostUSD: row.CostUSD, APIMS: row.APIMS, APIWithoutRetriesMS: row.APIWithoutRetriesMS, ToolMS: row.ToolMS}
		base.Detail = "Provider-reported cumulative estimate and durations; not an invoice or per-tool timing."
		return []Event{base}
	case "system":
		var row struct {
			Subtype string          `json:"subtype"`
			Content json.RawMessage `json:"content"`
			Error   json.RawMessage `json:"error"`
		}
		if json.Unmarshal(r.Data, &row) != nil {
			return []Event{invalid(base)}
		}
		base.NativeType += ":" + row.Subtype
		switch row.Subtype {
		case "compact_boundary":
			base.Kind = "context.compacted"
			n.content(&base, rawText(row.Content))
		case "stop_hook_summary":
			var row struct {
				HookErrors []json.RawMessage `json:"hookErrors"`
				Prevented  bool              `json:"preventedContinuation"`
			}
			if json.Unmarshal(r.Data, &row) != nil {
				return []Event{invalid(base)}
			}
			base.Kind = "hook.summary"
			failed := len(row.HookErrors) > 0
			if row.HookErrors != nil {
				base.IsError = &failed
			}
			base.Outcome = flagOutcome(base.IsError)
			base.Outcome.Basis = "native_hook_errors"
			base.Detail = "Recorded stop hook summary"
			if row.Prevented {
				base.Detail += "; continuation prevented"
			}
			if failed {
				b, _ := json.Marshal(row.HookErrors)
				n.content(&base, string(b))
			}
		case "api_error":
			base.Kind = "api.error"
			failed := true
			base.IsError = &failed
			base.Outcome = flagOutcome(&failed)
			base.Outcome.Basis = "native_api_error_record"
			n.content(&base, rawText(row.Error))
		default:
			return []Event{unsupported(base)}
		}
		return []Event{base}
	}
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
		// This optional field also contains strings, arrays and provider-specific
		// objects. Its shape must never invalidate the message or error evidence.
		var result struct {
			AgentID string `json:"agentId"`
		}
		if json.Unmarshal(row.ToolUseResult, &result) != nil {
			result.AgentID = ""
		}
		for i := range events {
			if events[i].Kind == "tool.result" && result.AgentID != "" {
				events[i].RelatedAgentIDs = []string{result.AgentID}
			}
		}
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
