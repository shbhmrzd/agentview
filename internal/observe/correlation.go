package observe

import (
	"crypto/sha256"
	"strings"
	"sync"
	"time"
)

const DefaultEvidenceBudget = 32 * 1024 * 1024
const maxPendingCalls = 128
const maxSeenCalls = 65536
const identityCharge = 128 // Conservative map/bucket charge per SHA-256 key.

// EvidenceBudget bounds retained correlation state, including identity history.
// It does not claim to measure the Go heap, source registry or response buffers.
type EvidenceBudget struct {
	mu          sync.Mutex
	used, limit int64
}

func NewEvidenceBudget(limit int64) *EvidenceBudget { return &EvidenceBudget{limit: limit} }
func (b *EvidenceBudget) reserve(n int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.limit-b.used {
		return false
	}
	b.used += n
	return true
}
func (b *EvidenceBudget) release(n int64) { b.mu.Lock(); defer b.mu.Unlock(); b.used -= n }
func (b *EvidenceBudget) Used() int64     { b.mu.Lock(); defer b.mu.Unlock(); return b.used }

type CallEvidence struct {
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
	Request    *Event `json:"request,omitempty"`
	DurationMS *int64 `json:"duration_ms,omitempty"`
}
type pendingCall struct {
	request Event
	charge  int64
}

// CallTracker is the sole authority for request/result matching in a reader.
// Fingerprints survive completion and eviction. At identity-budget exhaustion
// it stops accepting new identities until reset, rather than forgetting that
// an ID may have been reused. Hash collisions conservatively reject pairing.
type CallTracker struct {
	budget       *EvidenceBudget
	seen         map[[32]byte]string
	pending      map[[32]byte]pendingCall
	order        [][32]byte
	exhausted    bool
	reserved     int64
	results      []string
	resultCharge int64
	followupGap  bool
}

func NewCallTracker(budget *EvidenceBudget) *CallTracker {
	if budget == nil {
		budget = NewEvidenceBudget(DefaultEvidenceBudget)
	}
	return &CallTracker{budget: budget}
}
func (c *CallTracker) Close() {
	c.budget.release(c.reserved)
	*c = CallTracker{budget: c.budget}
}
func (c *CallTracker) reserve(n int64) bool {
	if !c.budget.reserve(n) {
		return false
	}
	c.reserved += n
	return true
}
func (c *CallTracker) release(n int64) { c.reserved -= n; c.budget.release(n) }
func (c *CallTracker) remove(key [32]byte) {
	if pending, ok := c.pending[key]; ok {
		c.release(pending.charge)
		delete(c.pending, key)
	}
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}
func (c *CallTracker) remember(key [32]byte) bool {
	if c.exhausted || len(c.seen) >= maxSeenCalls || !c.reserve(identityCharge) {
		c.exhausted = true
		return false
	}
	if c.seen == nil {
		c.seen = map[[32]byte]string{}
		c.pending = map[[32]byte]pendingCall{}
	}
	c.seen[key] = "seen"
	return true
}
func (c *CallTracker) Apply(e *Event) {
	if e.Inherited {
		return
	}
	if e.Kind == "message.assistant" {
		e.PrecedingCalls = c.results
		c.results = nil
		c.release(c.resultCharge)
		c.resultCharge = 0
		if c.followupGap {
			e.EvidenceGap = "Some preceding results omitted by the correlation budget"
			c.followupGap = false
		}
		return
	}
	if e.Kind != "tool.requested" && e.Kind != "tool.result" {
		return
	}
	ev := &CallEvidence{Status: "missing", Detail: "Matching request unavailable in this source/read"}
	e.Call = ev
	if e.ToolCallID == "" {
		ev.Detail = "Native call ID unavailable"
		return
	}
	key := sha256.Sum256([]byte(e.ToolCallID))
	previous, seen := c.seen[key]
	if !seen && !c.remember(key) {
		ev.Status = "capacity"
		if e.Kind == "tool.result" {
			c.followupGap = true
		}
		ev.Detail = "Correlation identity budget exhausted; no new pairs inferred in this reader"
		return
	}
	if e.Kind == "tool.requested" {
		if seen {
			c.seen[key] = "ambiguous"
			c.remove(key)
			ev.Status = "ambiguous"
			ev.Detail = "Reused call ID in this source/read; no request selected"
			return
		}
		if len(c.order) == maxPendingCalls {
			old := c.order[0]
			c.remove(old)
			c.seen[old] = "evicted"
			ev.Detail = "Oldest unmatched request evicted from the pairing cache"
		}
		// Only these fields are retained. In particular, never copy raw native
		// output, context objects or a slice into the full payload allocation.
		request := Event{Kind: e.Kind, ID: e.ID, ToolCallID: e.ToolCallID, ToolName: e.ToolName, Source: e.Source, OccurredAt: e.OccurredAt, ObservedAt: e.ObservedAt, TimeStatus: e.TimeStatus, NativeType: e.NativeType, ContentCaptured: e.ContentCaptured}
		metadataBytes := len(request.ID) + len(request.ToolCallID) + len(request.ToolName) + len(request.Source.Path) + len(request.Source.ReaderID) + len(request.OccurredAt) + len(request.ObservedAt) + len(request.TimeStatus) + len(request.NativeType)
		if metadataBytes > 16384 {
			c.seen[key] = "evicted"
			ev.Status = "evicted"
			ev.Detail = "Request metadata exceeds the correlation limit"
			return
		}
		request.ContentClipped = len(e.Content) > 1024
		excerpt := e.Content[:min(len(e.Content), 1024)]
		request.Content = strings.Clone(strings.ToValidUTF8(excerpt, ""))
		charge := int64(2048 + metadataBytes + len(request.Content))
		if !c.reserve(charge) {
			c.seen[key] = "evicted"
			ev.Status = "evicted"
			ev.Detail = "Request omitted by the shared correlation byte budget"
			return
		}
		c.pending[key] = pendingCall{request, charge}
		c.order = append(c.order, key)
		c.seen[key] = "pending"
		ev.Status = "pending"
		if ev.Detail == "Matching request unavailable in this source/read" {
			ev.Detail = ""
		}
		return
	}
	switch {
	case previous == "ambiguous":
		ev.Status = "ambiguous"
		ev.Detail = "Reused call ID in this source/read; no request selected"
	case previous == "evicted":
		ev.Status = "evicted"
		ev.Detail = "Matching request evicted from the bounded pairing cache"
	case previous == "completed":
		ev.Status = "duplicate_result"
		ev.Detail = "Repeated result ID; no new pair inferred"
	default:
		if pending, ok := c.pending[key]; ok {
			request := pending.request
			ev.Status = "paired"
			ev.Detail = ""
			ev.Request = &request
			e.ToolName = request.ToolName
			at, aerr := time.Parse(time.RFC3339Nano, request.OccurredAt)
			end, berr := time.Parse(time.RFC3339Nano, e.OccurredAt)
			if aerr == nil && berr == nil && !end.Before(at) {
				ms := end.Sub(at).Milliseconds()
				ev.DurationMS = &ms
			}
			c.seen[key] = "completed"
		} else if !seen {
			c.seen[key] = "completed"
		}
	}
	c.remove(key)
	// This is physical chronology, not acknowledgement or a task outcome.
	charge := int64(len(e.ToolCallID) + 64)
	if len(c.results) < 128 && len(e.ToolCallID) <= 4096 && c.reserve(charge) {
		c.results = append(c.results, strings.Clone(e.ToolCallID))
		c.resultCharge += charge
	} else {
		c.followupGap = true
	}
}
