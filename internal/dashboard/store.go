// Package dashboard provides an ephemeral, read-only browser view of local
// session sources. Source activity is never interpreted as process liveness.
package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shbhmrzd/agentview/internal/observe"
)

const (
	maxLiveEvents    = 2000
	maxSessionEvents = 120
	maxContentBytes  = 8192
)

type Entry struct {
	CorrelationStatus string                 `json:"correlation_status,omitempty"`
	RelatedAgentPaths []string               `json:"related_agent_paths,omitempty"`
	PartialRecord     bool                   `json:"partial_record,omitempty"`
	Outcome           *observe.Outcome       `json:"outcome,omitempty"`
	Communication     *observe.Communication `json:"communication,omitempty"`
	RelatedAgentIDs   []string               `json:"related_agent_ids,omitempty"`
	Detail            string                 `json:"detail,omitempty"`
	Request           *Entry                 `json:"request,omitempty"`
	PrecedingCalls    []string               `json:"preceding_calls,omitempty"`
	Gap               string                 `json:"gap,omitempty"`
	ID                string                 `json:"id"`
	NativeID          string                 `json:"native_id,omitempty"`
	TurnID            string                 `json:"turn_id,omitempty"`
	TimeStatus        string                 `json:"time_status"`
	ObservedAt        string                 `json:"observed_at"`
	Coordination      *observe.Coordination  `json:"coordination,omitempty"`
	DurationMS        *int64                 `json:"duration_ms,omitempty"`
	Source            observe.Source         `json:"source"`
	NativeType        string                 `json:"native_type"`
	Sequence          uint64                 `json:"sequence"`
	Kind              string                 `json:"kind"`
	Time              string                 `json:"time"`
	Tool              string                 `json:"tool,omitempty"`
	CallID            string                 `json:"call_id,omitempty"`
	Content           string                 `json:"content,omitempty"`
	Captured          bool                   `json:"captured"`
	Clipped           bool                   `json:"clipped,omitempty"`
	Phase             string                 `json:"phase,omitempty"`
	Error             bool                   `json:"error,omitempty"`
}

type Session struct {
	DelegationsOmitted uint64               `json:"delegations_omitted,omitempty"`
	Delegations        []Delegation         `json:"delegations,omitempty"`
	ReaderID           string               `json:"reader_id,omitempty"`
	Overview           *AgentOverview       `json:"overview,omitempty"`
	GroupID            string               `json:"group_id,omitempty"`
	Context            observe.AgentContext `json:"context"`
	Metrics            Metrics              `json:"metrics"`
	ParentEvidence     string               `json:"parent_evidence,omitempty"`
	ID                 string               `json:"id"`
	Agent              string               `json:"agent"`
	SessionID          string               `json:"session_id"`
	Title              string               `json:"title,omitempty"`
	Project            string               `json:"project"`
	Directory          string               `json:"directory"`
	Path               string               `json:"path"`
	Status             string               `json:"status"`
	LastActivity       string               `json:"last_activity"`
	LastUpdateAt       string               `json:"last_update_at,omitempty"`
	LiveCount          uint64               `json:"live_count"`
	Diagnostics        uint64               `json:"diagnostics"`
	Generation         int64                `json:"generation"`
	events             []Entry
	overview           AgentOverview
}

type Root struct {
	Agent  string `json:"agent"`
	Path   string `json:"path"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type Snapshot struct {
	Sessions       []Session         `json:"sessions"`
	Roots          []Root            `json:"roots"`
	Diagnostics    map[string]uint64 `json:"diagnostics"`
	IncludeContent bool              `json:"include_content"`
	StartedAt      string            `json:"started_at"`
}

type Store struct {
	delegationOrder []struct{ id, resultID string }
	mu              sync.RWMutex
	sessions        map[string]*Session
	roots           map[string]Root
	diagnostics     map[string]uint64
	order           []struct {
		id  string
		seq uint64
	}
	sequence uint64
	content  bool
	started  string
}

func NewStore(content bool) *Store {
	return &Store{sessions: make(map[string]*Session), roots: make(map[string]Root), diagnostics: make(map[string]uint64), content: content, started: time.Now().UTC().Format(time.RFC3339Nano)}
}

func sourceID(agent, path string) string {
	h := sha256.Sum256([]byte(agent + "\x00" + path))
	return hex.EncodeToString(h[:16])
}

func (s *Store) SetRoot(root Root) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roots[sourceID(root.Agent, root.Path)] = root
}

func displayEvent(e observe.Event) (Entry, bool) {
	if !strings.HasPrefix(e.Kind, "message.") && !strings.HasPrefix(e.Kind, "tool.") && !strings.HasPrefix(e.Kind, "turn.") && !strings.HasPrefix(e.Kind, "agent.") && e.Kind != "context.compacted" && e.Kind != "hook.summary" && e.Kind != "api.error" {
		return Entry{}, false
	}
	content := e.Content
	clipped := len(content) > maxContentBytes || e.ContentClipped
	if len(content) > maxContentBytes {
		content = content[:maxContentBytes]
		for !utf8.ValidString(content) {
			content = content[:len(content)-1]
		}
	}
	entry := Entry{RelatedAgentPaths: e.RelatedAgentPaths, PartialRecord: e.PartialRecord, Outcome: e.Outcome, Communication: e.Communication, RelatedAgentIDs: e.RelatedAgentIDs, Detail: e.Detail, ID: e.ID, NativeID: e.NativeID, TurnID: e.TurnID, TimeStatus: e.TimeStatus, ObservedAt: e.ObservedAt, Kind: e.Kind, Time: e.OccurredAt, Tool: e.ToolName, CallID: e.ToolCallID, Content: strings.Clone(content), Captured: e.ContentCaptured, Clipped: clipped, Phase: e.ObservationPhase, Error: (e.IsError != nil && *e.IsError) || (e.Outcome != nil && e.Outcome.Status == "failure"), Source: e.Source, NativeType: e.NativeType, Coordination: e.Coordination, PrecedingCalls: e.PrecedingCalls, Gap: e.EvidenceGap}
	if e.Call != nil {
		entry.CorrelationStatus = e.Call.Status
		entry.Gap = strings.TrimSpace(entry.Gap + " " + e.Call.Detail)
		entry.DurationMS = e.Call.DurationMS
		if e.Call.Request != nil {
			request, _ := displayEvent(*e.Call.Request)
			entry.Request = &request
		}
	}
	return entry, true
}

func (s *Store) Apply(e observe.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.HasPrefix(e.Kind, "directory.") {
		key := sourceID(e.Agent, e.Source.Path)
		if e.Kind == "directory.ready" && s.roots[key].Status == "degraded" {
			return // Only an explicit successful rescan establishes recovery.
		}
		status := "watching"
		if e.Kind != "directory.ready" && e.Kind != "directory.recovered" {
			status = "degraded"
			s.diagnostics[e.Kind]++
		}
		s.roots[key] = Root{Agent: e.Agent, Path: e.Source.Path, Status: status, Detail: e.Detail}
		return
	}
	if e.Source.Path == "" {
		return
	}
	id := sourceID(e.Agent, e.Source.Path)
	item := s.sessions[id]
	if item == nil {
		// Directory discovery is already bounded; keep a defensive UI bound too.
		if len(s.sessions) >= 20000 {
			s.diagnostics["dashboard.session_limit"]++
			return
		}
		item = &Session{ID: id, Agent: e.Agent, Path: e.Source.Path, Project: filepath.Base(filepath.Dir(e.Source.Path)), Status: "indexing", Generation: e.Source.Generation}
		s.sessions[id] = item
	}
	if e.Source.Generation > item.Generation {
		item.Generation = e.Source.Generation
		item.SessionID, item.Directory = "", ""
		item.Title = ""
		item.GroupID, item.ParentEvidence = "", ""
		item.Project = filepath.Base(filepath.Dir(e.Source.Path))
		item.events = nil
		item.Metrics = Metrics{}
		item.Delegations = nil
		item.DelegationsOmitted = 0
		item.overview = AgentOverview{}
		item.LastUpdateAt = ""
	}
	if e.SessionID != "" {
		item.SessionID = e.SessionID
	}
	item.ReaderID = e.Source.ReaderID
	if e.GroupSessionID != "" {
		item.GroupID = e.GroupSessionID
	}
	item.Context = e.AgentContext
	if item.Context.ParentThreadID != "" {
		item.ParentEvidence = "native parent_thread_id"
	}
	// Claude stores subagent transcripts beneath the owning session directory.
	// Keep this layout-derived edge distinct from a native parent identifier.
	if e.Agent == "claude" && filepath.Base(filepath.Dir(e.Source.Path)) == "subagents" {
		item.Context.ParentThreadID = filepath.Base(filepath.Dir(filepath.Dir(e.Source.Path)))
		item.ParentEvidence = "Claude transcript directory layout"
	}
	duration := item.Metrics.apply(e)
	if s.content {
		item.overview.apply(e)
		if e.SessionTitle != "" {
			item.Title = requestTitle(e.SessionTitle)
		}
		if item.Title == "" && e.Kind == "message.user" && e.ContentCaptured && !e.Inherited {
			item.Title = requestTitle(e.Content)
		}
	}
	if e.WorkingDirectory != "" {
		item.Directory = e.WorkingDirectory
		item.Project = filepath.Base(e.WorkingDirectory)
	}
	when := e.SourceModifiedAt
	if e.ObservationPhase == "update" || e.ObservationPhase == "discovered_history" || e.ObservationPhase == "replacement_history" {
		when = e.ObservedAt
	}
	if parsed, err := time.Parse(time.RFC3339Nano, when); err == nil {
		old, _ := time.Parse(time.RFC3339Nano, item.LastActivity)
		if parsed.After(old) {
			item.LastActivity = when
		}
	}
	switch e.Kind {
	case "source.ready", "source.available":
		item.Status = "watching"
	case "source.unavailable":
		item.Status = "unavailable"
	}
	if e.Kind == "record.ignored" {
		return
	}
	if strings.HasPrefix(e.Kind, "record.") || e.Kind == "source.unavailable" || e.Kind == "source.replaced" || e.Kind == "source.truncated" || e.Kind == "source.rewritten" {
		key := e.Kind
		if e.Kind == "record.unsupported" {
			key += ":" + e.NativeType
		}
		if _, ok := s.diagnostics[key]; !ok && len(s.diagnostics) >= 32 {
			key = "other"
		}
		s.diagnostics[key]++
		item.Diagnostics++
	}
	entry, ok := displayEvent(e)
	if !ok {
		return
	}
	if (len(entry.RelatedAgentIDs) > 0 || len(entry.RelatedAgentPaths) > 0) && entry.Kind == "tool.result" {
		item.Delegations = append(item.Delegations, Delegation{Result: entry})
		if len(item.Delegations) > 100 {
			item.Delegations = append([]Delegation(nil), item.Delegations[1:]...)
			item.DelegationsOmitted++
		}
		s.delegationOrder = append(s.delegationOrder, struct{ id, resultID string }{item.ID, entry.ID})
		if len(s.delegationOrder) > 1024 {
			old := s.delegationOrder[0]
			s.delegationOrder = s.delegationOrder[1:]
			if source := s.sessions[old.id]; source != nil && len(source.Delegations) > 0 && source.Delegations[0].Result.ID == old.resultID {
				source.Delegations[0] = Delegation{}
				source.Delegations = source.Delegations[1:]
				source.DelegationsOmitted++
			}
		}
	}
	if e.ObservationPhase == "history" {
		return
	}
	// Only newly appended conversation/tool/turn records establish observed
	// activity. File mtime, startup replay and new-file backfills do not.
	if e.ObservationPhase == "update" {
		if observed, err := time.Parse(time.RFC3339Nano, e.ObservedAt); err == nil {
			previous, _ := time.Parse(time.RFC3339Nano, item.LastUpdateAt)
			if observed.After(previous) {
				item.LastUpdateAt = e.ObservedAt
			}
		}
	}
	entry.DurationMS = duration
	// Enforce metadata-only mode at the view boundary too.
	if !s.content {
		entry.Content, entry.Captured = "", false
	}
	s.sequence++
	entry.Sequence = s.sequence
	item.LiveCount++
	item.events = append(item.events, entry)
	if len(item.events) > maxSessionEvents {
		item.events[0] = Entry{}
		item.events = item.events[1:]
	}
	s.order = append(s.order, struct {
		id  string
		seq uint64
	}{id, entry.Sequence})
	if len(s.order) > maxLiveEvents {
		old := s.order[0]
		s.order = s.order[1:]
		if previous := s.sessions[old.id]; previous != nil && len(previous.events) > 0 && previous.events[0].Sequence <= old.seq {
			previous.events[0] = Entry{}
			previous.events = previous.events[1:]
		}
	}
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := Snapshot{Sessions: make([]Session, 0, len(s.sessions)), Roots: make([]Root, 0, len(s.roots)), Diagnostics: make(map[string]uint64), IncludeContent: s.content, StartedAt: s.started}
	for _, item := range s.sessions {
		copy := *item
		copy.Metrics = item.Metrics.summary()
		copy.events = nil
		copy.Delegations = nil
		result.Sessions = append(result.Sessions, copy)
	}
	for _, root := range s.roots {
		result.Roots = append(result.Roots, root)
	}
	for key, count := range s.diagnostics {
		result.Diagnostics[key] = count
	}
	sort.Slice(result.Sessions, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, result.Sessions[i].LastActivity)
		b, _ := time.Parse(time.RFC3339Nano, result.Sessions[j].LastActivity)
		if a.Equal(b) {
			return result.Sessions[i].ID < result.Sessions[j].ID
		}
		return a.After(b)
	})
	sort.Slice(result.Roots, func(i, j int) bool { return result.Roots[i].Agent < result.Roots[j].Agent })
	return result
}

func (s *Store) Session(id string) (Session, []Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.sessions[id]
	if !ok {
		return Session{}, nil, false
	}
	copy := *item
	copy.Metrics = item.Metrics.copy()
	copy.events = nil
	copy.Delegations = nil
	return copy, append([]Entry{}, item.events...), true
}
