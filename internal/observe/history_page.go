package observe

import (
	"context"
	"fmt"
	"time"
)

// HistoryPager preserves decoder context across bounded pages. Its reader ID
// namespaces generation counters independently of a live observer. A frozen
// first-open boundary excludes later appends; detected rewrites invalidate it.
type HistoryPager struct {
	reader     *Reader
	normalizer *Normalizer
	guard      Directory
	pending    []Event
	done       bool
	readAt     time.Time
}

func NewHistoryPager(root, path, agent string, options NormalizerOptions, maxRecord int) (*HistoryPager, error) {
	guard := Directory{root: root}
	if err := guard.safePath(path); err != nil {
		return nil, err
	}
	r, err := NewReader(path, maxRecord, DefaultBatchBytes)
	if err != nil {
		return nil, err
	}
	r.Freeze()
	if _, err := r.captureBoundary(); err != nil {
		return nil, err
	}
	n, err := NewNormalizerWithOptions(agent, options)
	if err != nil {
		return nil, err
	}
	return &HistoryPager{reader: r, normalizer: n, guard: guard, readAt: time.Now()}, nil
}

func (p *HistoryPager) ReadAt() time.Time { return p.readAt }
func (p *HistoryPager) Source() Source    { return p.reader.Location() }

// Next returns every normalized record in order, including diagnostics. A page
// can contain only diagnostics: more=true always means continue, not EOF.
func (p *HistoryPager) Next(ctx context.Context, count int) ([]Event, bool, error) {
	if count < 1 || count > 1000 {
		return nil, false, fmt.Errorf("invalid history page size")
	}
	if err := p.guard.safePath(p.reader.path); err != nil {
		return nil, false, err
	}
	out := []Event{}
	bytes := 0
	for len(out) < count && bytes < 2*1024*1024 {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if len(p.pending) == 0 {
			if p.done {
				break
			}
			batch, err := p.reader.ReadBatch()
			if err != nil {
				return nil, false, err
			}
			p.done = !batch.HasMore
			for _, record := range batch.Records {
				if record.Diagnostic == "source.changed_during_read" {
					return nil, false, fmt.Errorf("source changed during history read; refresh")
				}
				for _, e := range p.normalizer.Normalize(record, p.readAt) {
					e.ObservationPhase = "history"
					p.pending = append(p.pending, e)
				}
			}
			if p.done && batch.PendingBytes > 0 {
				p.pending = append(p.pending, p.normalizer.Normalize(Record{Source: p.reader.Location(), Diagnostic: "record.incomplete"}, p.readAt)...)
			}
			// Bound work even when a batch ends inside a large record.
			if len(p.pending) == 0 {
				break
			}
		}
		if len(p.pending) > 0 {
			e := p.pending[0]
			p.pending[0] = Event{}
			p.pending = p.pending[1:]
			out = append(out, e)
			bytes += len(e.Content)
		}
	}
	return out, !p.done || len(p.pending) > 0, nil
}

func (p *HistoryPager) Close() { p.normalizer.Close(); p.pending = nil; p.done = true }
