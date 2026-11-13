package observe

import (
	"encoding/json"
	"strings"
)

type Coordination struct {
	Operation string `json:"operation"`
	Target    string `json:"target,omitempty"`
}

// A coordination tool request is an attempted operation, not proof of delivery.
// Do not interpret arbitrary shell/JavaScript text as an agent message.
func coordination(name, arguments string) *Coordination {
	parts := strings.Split(name, ".")
	name = parts[len(parts)-1]
	switch name {
	case "spawn_agent", "send_message", "send_input", "followup_task", "wait_agent", "wait", "interrupt_agent", "close_agent", "SendMessage", "Agent", "Task":
	default:
		return nil
	}
	var args struct {
		Target    string   `json:"target"`
		ID        string   `json:"id"`
		Recipient string   `json:"recipient"`
		To        string   `json:"to"`
		IDs       []string `json:"ids"`
	}
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return nil
	}
	// functions.wait waits for an execution cell, not an agent. Only the
	// native agent-wait shape carries a list of agent IDs.
	if name == "wait" && len(args.IDs) == 0 {
		return nil
	}
	target := args.Target
	if target == "" {
		target = args.ID
	}
	if target == "" {
		target = args.Recipient
	}
	if target == "" {
		target = args.To
	}
	if target == "" && len(args.IDs) == 1 {
		target = args.IDs[0]
	}
	return &Coordination{Operation: name, Target: target}
}

// AgentContext contains recorded configuration and relationship identifiers,
// never prompts, hidden reasoning, credentials, or inferred process state.
type AgentContext struct {
	HistoryStartOrdinal *uint64 `json:"history_start_ordinal,omitempty"`
	Model               string  `json:"model,omitempty"`
	Effort              string  `json:"effort,omitempty"`
	Name                string  `json:"name,omitempty"`
	Role                string  `json:"role,omitempty"`
	AgentID             string  `json:"agent_id,omitempty"`
	AgentPath           string  `json:"agent_path,omitempty"`
	ParentThreadID      string  `json:"parent_thread_id,omitempty"`
	ForkedFromID        string  `json:"forked_from_id,omitempty"`
	ContextWindow       *int64  `json:"context_window,omitempty"`
}

// Nil counters mean unavailable, not zero. Input token semantics are native:
// Codex input includes cache reads; Claude input excludes cache reads/writes.
// Consumers must not add cached counts to a Codex input count again.
type TokenUsage struct {
	Input      *int64 `json:"input_tokens,omitempty"`
	Cached     *int64 `json:"cached_input_tokens,omitempty"`
	CacheWrite *int64 `json:"cache_write_input_tokens,omitempty"`
	Output     *int64 `json:"output_tokens,omitempty"`
	Reasoning  *int64 `json:"reasoning_output_tokens,omitempty"`
	Total      *int64 `json:"total_tokens,omitempty"`
}

func (u *TokenUsage) valid() bool {
	if u == nil {
		return false
	}
	any := false
	for _, n := range []*int64{u.Input, u.Cached, u.CacheWrite, u.Output, u.Reasoning, u.Total} {
		if n != nil {
			if *n < 0 {
				return false
			}
			any = true
		}
	}
	return any
}
