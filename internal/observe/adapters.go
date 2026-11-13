package observe

import (
	"fmt"
	"sort"
)

// AdapterInfo is descriptive capability coverage, not a process-state probe.
// Adding a provider requires a registered decoder, source reader, and tests.
type AdapterInfo struct {
	Name         string       `json:"name"`
	Version      string       `json:"version"`
	InputFormat  string       `json:"input_format"`
	Capabilities Capabilities `json:"capabilities"`
}

type Capabilities struct {
	History         string `json:"history"`
	Updates         string `json:"updates"`
	ToolActivity    string `json:"tool_activity"`
	Subagents       string `json:"subagents"`
	ExecutionState  string `json:"execution_state"`
	SessionIdentity string `json:"session_identity"`
}

type decoder interface {
	Decode(Record, Event) []Event
	Context() (string, string)
	Reset()
}

type decoderState struct {
	includeContent            bool
	sessionID, groupSessionID string
}

func state(options NormalizerOptions) decoderState {
	return decoderState{includeContent: options.IncludeContent}
}
func (s *decoderState) Reset()                    { s.sessionID, s.groupSessionID = "", "" }
func (s *decoderState) Context() (string, string) { return s.sessionID, s.groupSessionID }

type registration struct {
	info             AdapterInfo
	newDecoder       func(NormalizerOptions) decoder
	validateSnapshot func([]byte) bool
}

func jsonlInfo(name string) AdapterInfo {
	return AdapterInfo{Name: name, Version: "0.1.0", InputFormat: "jsonl", Capabilities: Capabilities{
		History: "supported", Updates: "append_polling", ToolActivity: "partial", Subagents: "unavailable", ExecutionState: "unknown", SessionIdentity: "native_when_present",
	}}
}

// Compile-time registry: no dynamic loading or global mutable registration API.
var adapters = map[string]registration{
	"claude":  {info: jsonlInfo("claude"), newDecoder: func(o NormalizerOptions) decoder { return &claudeDecoder{state(o)} }},
	"codex":   {info: jsonlInfo("codex"), newDecoder: func(o NormalizerOptions) decoder { return &codexDecoder{state(o)} }},
	"copilot": {info: jsonlInfo("copilot"), newDecoder: func(o NormalizerOptions) decoder { return &copilotDecoder{state(o)} }},
	"rovo": {info: AdapterInfo{Name: "rovo", Version: "0.1.0", InputFormat: "snapshot", Capabilities: Capabilities{
		History: "partial", Updates: "replacement_snapshots", ToolActivity: "partial", Subagents: "unavailable", ExecutionState: "unknown", SessionIdentity: "native_when_present",
	}}, newDecoder: func(o NormalizerOptions) decoder { return &rovoDecoder{state(o)} }, validateSnapshot: validRovo},
}

func Adapters() []AdapterInfo {
	result := make([]AdapterInfo, 0, len(adapters))
	for _, adapter := range adapters {
		result = append(result, adapter.info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func LookupAdapter(name string) (AdapterInfo, bool) {
	a, ok := adapters[name]
	return a.info, ok
}

// SourceReader separates acquisition from provider-specific decoding. Cursors
// belong to a reader: byte ranges for JSONL, revision hashes for snapshots.
type SourceReader interface {
	ReadBatch() (Batch, error)
	Location() Source
	Freeze()
}

func NewSourceReader(info AdapterInfo, path string, limit int) (SourceReader, error) {
	switch info.InputFormat {
	case "jsonl":
		return NewReader(path, limit, DefaultBatchBytes)
	case "snapshot":
		r, err := NewSnapshotReader(path, limit)
		if err != nil {
			return nil, err
		}
		r.validate = adapters[info.Name].validateSnapshot
		return r, nil
	default:
		return nil, fmt.Errorf("unsupported input format %q", info.InputFormat)
	}
}
