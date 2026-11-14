package dashboard

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shbhmrzd/agentview/internal/observe"
)

type historyRead struct {
	mu            sync.Mutex
	retired       atomic.Bool
	id            string
	pager         *observe.HistoryPager
	query         historyQuery
	buffer        []observe.Event
	more, started bool
	scanned       int
	page          int
	last          historyPage
	touched       time.Time // guarded by Server.historyMu
}
type historyQuery struct {
	Text      string `json:"text,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Call      string `json:"call,omitempty"`
	AfterCall string `json:"after_call,omitempty"`
}

func (q historyQuery) active() bool {
	return q.Text != "" || q.Kind != "" && q.Kind != "all" || q.Call != "" || q.AfterCall != ""
}
func (q historyQuery) matches(e observe.Event) bool {
	if q.Call != "" && e.ToolCallID != q.Call {
		return false
	}
	if q.AfterCall != "" {
		found := false
		for _, id := range e.PrecedingCalls {
			if id == q.AfterCall {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	switch q.Kind {
	case "", "all":
	case "failures":
		if !(e.IsError != nil && *e.IsError || e.Outcome != nil && e.Outcome.Status == "failure") {
			return false
		}
	case "coordination":
		if e.Coordination == nil && e.Communication == nil {
			return false
		}
	default:
		if !strings.HasPrefix(e.Kind, q.Kind) {
			return false
		}
	}
	if q.Text == "" {
		return true
	}
	texts := []string{e.Content, e.ToolName, e.ToolCallID, e.Detail}
	if e.Coordination != nil {
		texts = append(texts, e.Coordination.Target)
	}
	if e.Communication != nil {
		texts = append(texts, e.Communication.Author, e.Communication.Recipient)
	}
	for _, text := range texts {
		if strings.Contains(strings.ToLower(text), q.Text) {
			return true
		}
	}
	return false
}

type historyPage struct {
	Events      []Entry        `json:"events"`
	Diagnostics map[string]int `json:"diagnostic_counts"`
	ReadAt      string         `json:"read_at"`
	Next        string         `json:"next_cursor,omitempty"`
	Page        int            `json:"page"`
	Source      observe.Source `json:"source"`
	Coverage    string         `json:"coverage"`
	Query       historyQuery   `json:"query"`
	Searching   bool           `json:"searching"`
	Scanned     int            `json:"scanned"`
}

type apiError struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Recovery string `json:"recovery"`
}

func writeAPIError(w http.ResponseWriter, status int, code, message, recovery string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeJSON(w, struct {
		Error apiError `json:"error"`
	}{apiError{code, message, recovery}})
}
func closeHistory(h *historyRead) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pager.Close()
	h.buffer = nil
	h.last = historyPage{}
}

func (s *Server) history(w http.ResponseWriter, r *http.Request, session Session) {
	select {
	case s.historySlots <- struct{}{}:
		defer func() { <-s.historySlots }()
	default:
		writeAPIError(w, 429, "reader_busy", "History reader busy; retry shortly.", "retry")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	key, pageText, _ := strings.Cut(r.URL.Query().Get("cursor"), ".")
	page := 1
	if key != "" {
		n, err := strconv.Atoi(pageText)
		if err != nil || n < 1 {
			writeAPIError(w, 400, "invalid_cursor", "Invalid history cursor.", "refresh")
			return
		}
		page = n
	}
	query := historyQuery{Text: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("query"))), Kind: r.URL.Query().Get("kind"), Call: r.URL.Query().Get("call"), AfterCall: r.URL.Query().Get("after_call")}
	if len(query.Text)+len(query.Kind)+len(query.Call)+len(query.AfterCall) > 8192 {
		writeAPIError(w, 400, "invalid_query", "Search parameters exceed the supported limit.", "refresh")
		return
	}
	switch query.Kind {
	case "", "all", "message.", "tool.", "turn.", "context.", "coordination", "failures", "tool.requested", "tool.result":
	default:
		writeAPIError(w, 400, "invalid_filter", "Unknown history filter.", "refresh")
		return
	}
	// Never close a pager while another request is using it. Mark it retired
	// under the registry lock; release its budget after taking its own lock.
	retired := []*historyRead{}
	defer func() {
		for _, h := range retired {
			closeHistory(h)
		}
	}()
	s.historyMu.Lock()
	if s.histories == nil {
		s.histories = map[string]*historyRead{}
	}
	for k, h := range s.histories {
		if time.Since(h.touched) > 10*time.Minute {
			delete(s.histories, k)
			h.retired.Store(true)
			retired = append(retired, h)
		}
	}
	h := s.histories[key]
	if key == "" {
		if len(s.histories) >= 8 {
			oldest := ""
			var at time.Time
			for k, v := range s.histories {
				if oldest == "" || v.touched.Before(at) {
					oldest = k
					at = v.touched
				}
			}
			old := s.histories[oldest]
			delete(s.histories, oldest)
			old.retired.Store(true)
			retired = append(retired, old)
		}
		root := ""
		for _, candidate := range s.options.Roots {
			if candidate.Agent == session.Agent {
				root = candidate.Path
				break
			}
		}
		pager, err := observe.NewHistoryPager(root, session.Path, session.Agent, observe.NormalizerOptions{IncludeContent: s.options.IncludeContent, EvidenceBudget: s.evidenceBudget}, s.options.MaxRecordBytes)
		if err != nil {
			s.historyMu.Unlock()
			writeAPIError(w, 503, "source_unavailable", "History source could not be opened. Restore it and refresh history.", "refresh")
			return
		}
		key = rand.Text()
		h = &historyRead{id: session.ID, pager: pager, query: query}
		s.histories[key] = h
	}
	if h == nil || h.id != session.ID {
		s.historyMu.Unlock()
		writeAPIError(w, 409, "cursor_expired", "History cursor expired.", "refresh")
		return
	}
	h.touched = time.Now()
	s.historyMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.retired.Load() {
		writeAPIError(w, 409, "cursor_expired", "History cursor expired.", "refresh")
		return
	}
	if page == h.page {
		writeJSON(w, h.last)
		return
	}
	if page != h.page+1 {
		writeAPIError(w, 409, "cursor_order", "History pages must be read in order.", "refresh")
		return
	}
	result := historyPage{Events: []Entry{}, Diagnostics: map[string]int{}, ReadAt: h.pager.ReadAt().UTC().Format(time.RFC3339Nano), Page: page, Query: h.query, Searching: h.query.active(), Coverage: "Frozen first-open boundary; append-only consistency checks, not an atomic filesystem snapshot. Missing/oversized/unsupported records remain diagnostics. Search preserves source order and never treats a following reply as acknowledgement."}
	limit := 200
	if h.query.active() {
		limit = 5000
	}
	bytes, processed := 0, 0
	started := time.Now()
	for processed < limit && len(result.Events) < 200 && bytes < 2*1024*1024 && (processed == 0 || time.Since(started) < 750*time.Millisecond) {
		if len(h.buffer) == 0 {
			if h.started && !h.more {
				break
			}
			events, more, err := h.pager.Next(ctx, 200)
			if err != nil {
				s.historyMu.Lock()
				if s.histories[key] == h {
					delete(s.histories, key)
				}
				s.historyMu.Unlock()
				h.retired.Store(true)
				h.pager.Close()
				h.buffer = nil
				writeAPIError(w, 409, "history_invalidated", "History changed or became unreadable.", "refresh")
				return
			}
			h.buffer, h.more, h.started = events, more, true
			if len(h.buffer) == 0 {
				break
			}
		}
		e := h.buffer[0]
		h.buffer[0] = observe.Event{}
		h.buffer = h.buffer[1:]
		processed++
		h.scanned++
		if entry, ok := displayEvent(e); ok {
			if !h.query.matches(e) {
				continue
			}
			entry.Content, entry.Clipped = e.Content, e.ContentClipped
			result.Events = append(result.Events, entry)
			bytes += len(entry.Content)
		} else if e.Kind != "record.ignored" && strings.HasPrefix(e.Kind, "record.") {
			result.Diagnostics[e.Kind+":"+e.NativeType]++
		}
	}
	result.Source = h.pager.Source()
	result.Scanned = h.scanned
	if h.more || len(h.buffer) > 0 {
		result.Next = fmt.Sprintf("%s.%d", key, page+1)
	} else {
		h.pager.Close()
	}
	h.page, h.last = page, result
	writeJSON(w, result)
}
