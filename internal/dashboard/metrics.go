package dashboard

import (
	"time"

	"github.com/shbhmrzd/agentview/internal/observe"
)

// Metrics describe records in the currently indexed source generation. They
// are not execution outcomes or cross-source billing totals.
type Metrics struct {
	InheritedRecords uint64                `json:"inherited_records"`
	Messages         uint64                `json:"messages"`
	ToolCalls        uint64                `json:"tool_calls"`
	ToolResults      uint64                `json:"tool_results"`
	ReportedErrors   uint64                `json:"reported_errors"`
	Turns            uint64                `json:"turns"`
	LastTurn         string                `json:"last_turn"`
	Usage            *observe.TokenUsage   `json:"usage,omitempty"`
	UsageBasis       string                `json:"usage_basis,omitempty"`
	UsageAt          string                `json:"usage_at,omitempty"`
	Partial          bool                  `json:"partial"`
	Tools            map[string]ToolMetric `json:"tools"`
	requests         map[string]toolRequest
	usageMessages    map[string]observe.TokenUsage
}

type ToolMetric struct {
	Calls   uint64 `json:"calls"`
	Results uint64 `json:"results"`
	Errors  uint64 `json:"errors"`
	Timed   uint64 `json:"timed"`
	TotalMS int64  `json:"total_ms"`
}
type toolRequest struct {
	name string
	at   time.Time
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
		m.requests = make(map[string]toolRequest)
		m.usageMessages = make(map[string]observe.TokenUsage)
	}
	if e.Kind == "message.user" || e.Kind == "message.assistant" {
		m.Messages++
	}
	switch e.Kind {
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
		if e.ToolCallID != "" {
			if len(m.requests) < 1024 {
				at, _ := time.Parse(time.RFC3339Nano, e.OccurredAt)
				m.requests[e.ToolCallID] = toolRequest{name, at}
			} else {
				m.Partial = true
			}
		}
	}
	if e.Kind == "tool.result" {
		m.ToolResults++
		if e.IsError != nil && *e.IsError {
			m.ReportedErrors++
		}
		request, ok := m.requests[e.ToolCallID]
		if !ok {
			return nil
		}
		delete(m.requests, e.ToolCallID)
		metric := m.Tools[request.name]
		metric.Results++
		if e.IsError != nil && *e.IsError {
			metric.Errors++
		}
		var duration *int64
		at, err := time.Parse(time.RFC3339Nano, e.OccurredAt)
		if err == nil && !request.at.IsZero() && !at.Before(request.at) {
			ms := at.Sub(request.at).Milliseconds()
			duration = &ms
			metric.Timed++
			metric.TotalMS += ms
		}
		m.Tools[request.name] = metric
		return duration
	}
	return nil
}

func (m Metrics) copy() Metrics {
	m.requests, m.usageMessages = nil, nil
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
	m.requests, m.usageMessages, m.Tools = nil, nil, nil
	if m.Usage != nil {
		usage := *m.Usage
		m.Usage = &usage
	}
	return m
}
