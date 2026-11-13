package observe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Event struct {
	AgentContext     AgentContext  `json:"agent_context,omitempty"`
	Usage            *TokenUsage   `json:"usage,omitempty"`
	UsageBasis       string        `json:"usage_basis,omitempty"`
	UsageID          string        `json:"usage_id,omitempty"`
	Coordination     *Coordination `json:"coordination,omitempty"`
	Inherited        bool          `json:"inherited,omitempty"`
	SchemaVersion    int           `json:"schema_version"`
	ID               string        `json:"occurrence_id"`
	Agent            string        `json:"agent"`
	AdapterVersion   string        `json:"adapter_version"`
	Scope            string        `json:"scope"`
	SessionKey       string        `json:"session_key,omitempty"`
	SessionID        string        `json:"session_id,omitempty"`
	WorkingDirectory string        `json:"working_directory,omitempty"`
	SourceModifiedAt string        `json:"source_modified_at,omitempty"`
	GroupSessionID   string        `json:"group_session_id,omitempty"`
	NativeID         string        `json:"native_id,omitempty"`
	MessageID        string        `json:"message_id,omitempty"`
	NativeAgentID    string        `json:"native_agent_id,omitempty"`
	CheckpointReplay *bool         `json:"checkpoint_replay,omitempty"`
	ParentID         string        `json:"parent_id,omitempty"`
	ParentThreadID   string        `json:"parent_thread_id,omitempty"`
	ForkedFromID     string        `json:"forked_from_id,omitempty"`
	SourceOrdinal    *uint64       `json:"source_ordinal,omitempty"`
	TurnID           string        `json:"turn_id,omitempty"`
	Kind             string        `json:"kind"`
	NativeType       string        `json:"native_type,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
	ToolName         string        `json:"tool_name,omitempty"`
	IsError          *bool         `json:"is_error,omitempty"`
	OccurredAt       string        `json:"occurred_at,omitempty"`
	TimeStatus       string        `json:"time_status"`
	ObservedAt       string        `json:"observed_at"`
	Source           Source        `json:"source"`
	CaptureMode      string        `json:"capture_mode"`
	ObservationPhase string        `json:"observation_phase,omitempty"`
	ExecutionState   string        `json:"execution_state"`
	ContentCaptured  bool          `json:"content_captured"`
	Content          string        `json:"content,omitempty"`
	Detail           string        `json:"detail,omitempty"`
}

// Normalizer owns attachment context; provider decoders own native semantics.
type Normalizer struct {
	info       AdapterInfo
	options    NormalizerOptions
	decoder    decoder
	generation int64
}

type NormalizerOptions struct {
	IncludeContent bool
	Scope          string
}

func NewNormalizer(agent string, includeContent bool) (*Normalizer, error) {
	return NewNormalizerWithOptions(agent, NormalizerOptions{IncludeContent: includeContent})
}

func NewNormalizerWithOptions(agent string, options NormalizerOptions) (*Normalizer, error) {
	registration, ok := adapters[agent]
	if !ok {
		return nil, fmt.Errorf("unknown adapter %q; use agentview adapters", agent)
	}
	if options.Scope == "" {
		options.Scope = "local"
	}
	return &Normalizer{info: registration.info, options: options, decoder: registration.newDecoder(options)}, nil
}

func (n *Normalizer) Normalize(r Record, observedAt time.Time) []Event {
	if n.generation != r.Source.Generation {
		n.decoder.Reset()
		n.generation = r.Source.Generation
	}
	session, group := n.decoder.Context()
	captureMode := "file_observation"
	if n.info.InputFormat == "snapshot" {
		captureMode = "snapshot_observation"
	}
	base := Event{SchemaVersion: 1, Agent: n.info.Name, AdapterVersion: n.info.Version, Scope: n.options.Scope, Source: r.Source, ObservedAt: observedAt.UTC().Format(time.RFC3339Nano), TimeStatus: "missing", CaptureMode: captureMode, ExecutionState: "unknown", SessionID: session, GroupSessionID: group}
	var events []Event
	if r.Diagnostic != "" {
		base.Kind, base.Detail = r.Diagnostic, diagnosticDetail(r.Diagnostic)
		events = []Event{base}
	} else {
		events = n.decoder.Decode(r, base)
	}
	for i := range events {
		events[i].WorkingDirectory = n.decoder.WorkingDirectory()
		events[i].AgentContext = n.decoder.DebugContext()
		if events[i].SessionID != "" {
			sum := sha256.Sum256([]byte(n.info.Name + "\x00" + n.options.Scope + "\x00" + events[i].SessionID))
			events[i].SessionKey = hex.EncodeToString(sum[:])
		}
	}
	return identify(events, r)
}

func setTimestamp(base *Event, value string) {
	if value == "" {
		return
	}
	base.TimeStatus = "invalid"
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		base.OccurredAt = t.UTC().Format(time.RFC3339Nano)
		base.TimeStatus = "source"
	}
}

func rawText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func invalid(e Event) Event {
	e.Kind = "record.invalid"
	e.Detail = "Malformed JSON or unsupported field shape; content omitted."
	return e
}
func unsupported(e Event) Event {
	e.Kind = "record.unsupported"
	e.Detail = "Record type is not interpreted by this adapter; content omitted."
	return e
}
func ignored(e Event) Event {
	e.Kind = "record.ignored"
	e.Detail = "Non-conversation, duplicate representation, or reasoning record intentionally omitted."
	return e
}

func identify(events []Event, r Record) []Event {
	// Hash a potentially large record once, rather than once per message block.
	recordDigest := sha256.Sum256(r.Data)
	for i := range events {
		h := sha256.New()
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00", events[i].Agent, events[i].Scope, r.Source.Path, r.Source.Generation, r.Source.Offset, i)
		h.Write(recordDigest[:])
		h.Write([]byte(events[i].Kind))
		if r.Source.Kind == "snapshot" {
			h.Write([]byte(r.Source.Revision))
		}
		events[i].ID = hex.EncodeToString(h.Sum(nil))
	}
	return events
}

func diagnosticDetail(code string) string {
	switch code {
	case "source.replaced", "source.truncated", "source.rewritten":
		return "Source changed; started a new attachment generation. History may be missing or repeated."
	case "source.changed_during_read":
		return "Source changed while reading; this batch is not an atomic snapshot."
	case "record.oversized":
		return "Record exceeds the configured limit; discarding through the next newline."
	case "record.incomplete":
		return "Final record has no newline; deferred without parsing."
	case "source.unavailable":
		return "Source could not be opened/read; observation is degraded. No execution state is inferred."
	case "snapshot.invalid":
		return "Current document is not complete valid JSON; previous accepted snapshot retained."
	case "snapshot.unsupported":
		return "Current document does not match the supported provider snapshot schema; previous accepted snapshot retained."
	case "snapshot.oversized":
		return "Snapshot exceeds the configured byte limit; previous accepted snapshot retained."
	case "snapshot.unstable":
		return "Snapshot changed during the read; deferred until a stable observation."
	case "snapshot.recovered":
		return "Snapshot is readable again with the same accepted revision."
	default:
		return strings.ReplaceAll(code, ".", " ")
	}
}
