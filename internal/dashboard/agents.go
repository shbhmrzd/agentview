package dashboard

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/shbhmrzd/agentview/internal/observe"
)

const maxAgentPreviewBytes = 1024
const maxTreeAgents = 100

// These are excerpts, not inferred tasks or claims of successful completion.
// Keep them out of the inventory response; fetch only for the selected family.
type AgentOverview struct {
	FirstRequest *Entry `json:"first_request,omitempty"`
	LatestReply  *Entry `json:"latest_reply,omitempty"`
}

func (a *AgentOverview) apply(e observe.Event) {
	if e.Inherited || !e.ContentCaptured || strings.TrimSpace(e.Content) == "" {
		return
	}
	if e.Kind != "message.assistant" && (e.Kind != "message.user" || a.FirstRequest != nil) {
		return
	}
	entry, _ := displayEvent(e)
	if len(entry.Content) > maxAgentPreviewBytes {
		entry.Content = entry.Content[:maxAgentPreviewBytes]
		for !utf8.ValidString(entry.Content) {
			entry.Content = entry.Content[:len(entry.Content)-1]
		}
		entry.Clipped = true
	}
	entry.Content = strings.Clone(entry.Content)
	if e.Kind == "message.user" {
		a.FirstRequest = &entry
	} else {
		a.LatestReply = &entry
	}
}

func (a AgentOverview) copy() *AgentOverview {
	copy := a
	if a.FirstRequest != nil {
		entry := *a.FirstRequest
		copy.FirstRequest = &entry
	}
	if a.LatestReply != nil {
		entry := *a.LatestReply
		copy.LatestReply = &entry
	}
	return &copy
}

type AgentTree struct {
	Sessions []Session `json:"sessions"`
	Limited  bool      `json:"limited"`
}

// AgentTree returns a bounded family connected by explicit parent/group IDs.
// The browser builds nesting from parent IDs only: group membership is not a
// parent edge. Native IDs are scoped to their provider and source duplicates
// remain distinct. Cycles and missing parents are handled by the tree view.
func (s *Store) AgentTree(id string) (AgentTree, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := AgentTree{Sessions: []Session{}}
	selected := s.sessions[id]
	if selected == nil {
		return result, false
	}
	// Use the same connected identifiers as the conversation inventory. Each
	// identifier is expanded once, including missing parents and duplicate
	// exports; parent edges remain a separate concern in the browser.
	index := map[string][]*Session{}
	identifiers := func(item *Session) []string {
		return []string{item.SessionID, item.GroupID, item.Context.ParentThreadID}
	}
	for _, item := range s.sessions {
		if item.Agent != selected.Agent {
			continue
		}
		for _, key := range identifiers(item) {
			if key != "" {
				index[key] = append(index[key], item)
			}
		}
	}
	for _, items := range index {
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	}
	queued, expanded := map[string]bool{selected.ID: true}, map[string]bool{}
	queue := []*Session{selected}
	for cursor := 0; cursor < len(queue); cursor++ {
		item := queue[cursor]
		if len(result.Sessions) == maxTreeAgents {
			result.Limited = true
			break
		}
		copy := *item
		copy.events = nil
		copy.Delegations = append([]Delegation(nil), item.Delegations...)
		copy.Metrics = item.Metrics.summary()
		copy.Overview = item.overview.copy()
		result.Sessions = append(result.Sessions, copy)
		for _, key := range identifiers(item) {
			if key == "" || expanded[key] {
				continue
			}
			expanded[key] = true
			for _, related := range index[key] {
				if !queued[related.ID] {
					queued[related.ID] = true
					queue = append(queue, related)
				}
			}
		}
	}

	sort.Slice(result.Sessions, func(i, j int) bool { return result.Sessions[i].ID < result.Sessions[j].ID })
	return result, true
}
