package dashboard

import (
	"github.com/shbhmrzd/agentview/internal/observe"
)

// Metrics describe records in the currently indexed source generation. They
// are not execution outcomes or cross-source billing totals.
type Metrics struct {
	FailureEvidence    uint64                 `json:"failure_evidence"`
	UnsupportedRecords uint64                 `json:"unsupported_records"`
	UnknownOutcomes    uint64                 `json:"unknown_outcomes"`
	ReportedStats      *observe.ReportedStats `json:"reported_stats,omitempty"`
	Compactions        uint64                 `json:"compactions"`
	HookErrors         uint64                 `json:"hook_errors"`
	APIErrors          uint64                 `json:"api_errors"`
	InheritedRecords   uint64                 `json:"inherited_records"`
	Messages           uint64                 `json:"messages"`
	ToolCalls          uint64                 `json:"tool_calls"`
	ToolResults        uint64                 `json:"tool_results"`
	ReportedErrors     uint64                 `json:"reported_errors"`
	Turns              uint64                 `json:"turns"`
	LastTurn           string                 `json:"last_turn"`
	Usage              *observe.TokenUsage    `json:"usage,omitempty"`
	UsageBasis         string                 `json:"usage_basis,omitempty"`
	UsageAt            string                 `json:"usage_at,omitempty"`
	Partial            bool                   `json:"partial"`
	Tools              map[string]ToolMetric  `json:"tools"`
	usageMessages      map[string]observe.TokenUsage
}

type ToolMetric struct {
	Calls   uint64 `json:"calls"`
	Results uint64 `json:"results"`
	Errors  uint64 `json:"errors"`
	Timed   uint64 `json:"timed"`
	TotalMS int64  `json:"total_ms"`
}

func value(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
func addCounter(total, old, next *int64) *int64 {
	if total == nil && old == nil && next == nil {
		return nil
	}
	n := value(total) - value(old) + value(next)
	return &n
}

func (m *Metrics) apply(e observe.Event) *int64 {
	if e.Inherited {
		m.InheritedRecords++
		return nil
	}
	if m.Tools == nil {
		m.Tools = make(map[string]ToolMetric)
		m.usageMessages = make(map[string]observe.TokenUsage)
	}
	if e.Kind == "message.user" || e.Kind == "message.assistant" {
		m.Messages++
	}
	if (e.Kind == "tool.result" || e.Kind == "api.error" || e.Kind == "hook.summary") && ((e.IsError != nil && *e.IsError) || (e.Outcome != nil && e.Outcome.Status == "failure")) {
		m.FailureEvidence++
	}
	if e.Call != nil && e.Call.Status != "paired" && e.Call.Status != "pending" {
		m.Partial = true
	}
	if e.Stats != nil {
		copy := *e.Stats
		m.ReportedStats = &copy
	}
	switch e.Kind {
	case "record.unsupported":
		m.UnsupportedRecords++
	case "context.compacted":
		m.Compactions++
	case "hook.summary":
		if e.IsError != nil && *e.IsError {
			m.HookErrors++
		}
	case "api.error":
		m.APIErrors++
	case "turn.started":
		m.Turns++
		m.LastTurn = "started"
	case "turn.ended":
		m.LastTurn = "ended"
	case "turn.interrupted":
		m.LastTurn = "interrupted"
	case "record.oversized", "record.invalid", "record.excess_blocks":
		m.Partial = true
	}
	if e.Usage != nil {
		if e.UsageBasis == "claude_message" {
			old, exists := m.usageMessages[e.UsageID]
			if !exists && len(m.usageMessages) >= 10000 {
				m.Partial = true
				return nil
			}
			if m.Usage == nil {
				m.Usage = &observe.TokenUsage{}
			}
			u, n := m.Usage, e.Usage
			u.Input = addCounter(u.Input, old.Input, n.Input)
			u.Cached = addCounter(u.Cached, old.Cached, n.Cached)
			u.CacheWrite = addCounter(u.CacheWrite, old.CacheWrite, n.CacheWrite)
			u.Output = addCounter(u.Output, old.Output, n.Output)
			if u.Input != nil && u.Output != nil {
				total := value(u.Input) + value(u.Cached) + value(u.CacheWrite) + value(u.Output)
				u.Total = &total
			}
			m.usageMessages[e.UsageID] = *e.Usage
			m.UsageBasis, m.UsageAt = "claude_message_totals", e.OccurredAt
		} else if e.UsageBasis == "codex_thread_cumulative" || m.UsageBasis != "codex_thread_cumulative" {
			copy := *e.Usage
			m.Usage, m.UsageBasis, m.UsageAt = &copy, e.UsageBasis, e.OccurredAt
		}
	}
	if e.Kind == "tool.requested" {
		m.ToolCalls++
		name := e.ToolName
		if name == "" {
			name = "unnamed"
		}
		if _, ok := m.Tools[name]; !ok && len(m.Tools) >= 64 {
			name = "other"
		}
		metric := m.Tools[name]
		metric.Calls++
		m.Tools[name] = metric

	}
	if e.Kind == "tool.result" {
		m.ToolResults++
		if e.IsError == nil && (e.Outcome == nil || e.Outcome.Status == "unknown") {
			m.UnknownOutcomes++
		}
		if (e.IsError != nil && *e.IsError) || (e.Outcome != nil && e.Outcome.Status == "failure") {
			m.ReportedErrors++
		}
		if e.Call == nil || e.Call.Status != "paired" || e.Call.Request == nil {
			return nil
		}
		name := e.Call.Request.ToolName
		if name == "" {
			name = "unnamed"
		}
		if _, ok := m.Tools[name]; !ok {
			name = "other"
		}
		metric := m.Tools[name]
		metric.Results++
		if (e.IsError != nil && *e.IsError) || (e.Outcome != nil && e.Outcome.Status == "failure") {
			metric.Errors++
		}
		duration := e.Call.DurationMS
		if duration != nil {
			metric.Timed++
			metric.TotalMS += *duration
		}
		m.Tools[name] = metric
		return duration
	}
	return nil
}

func (m Metrics) copy() Metrics {
	m.usageMessages = nil
	if m.Usage != nil {
		usage := *m.Usage
		m.Usage = &usage
	}
	tools := make(map[string]ToolMetric, len(m.Tools))
	for key, metric := range m.Tools {
		tools[key] = metric
	}
	m.Tools = tools
	return m
}

func (m Metrics) summary() Metrics {
	m.usageMessages, m.Tools = nil, nil
	if m.Usage != nil {
		usage := *m.Usage
		m.Usage = &usage
	}
	return m
}
