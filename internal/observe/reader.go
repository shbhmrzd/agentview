// Package observe reads explicitly selected agent transcripts without controlling
// the agent or writing to its files. File history does not establish liveness.
package observe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	DefaultMaxRecordBytes = 1024 * 1024
	DefaultBatchBytes     = 4 * 1024 * 1024
	MaxRecordBytes        = 16 * 1024 * 1024
	anchorBytes           = 256
	maxRecordsPerBatch    = 1024
)

// Source identifies a physical occurrence, not a canonical conversation node.
// Generation is local to this attachment and is not a durable restart cursor.
type Source struct {
	Kind       string `json:"kind,omitempty"`
	Path       string `json:"path"`
	Generation int64  `json:"generation"`
	Offset     int64  `json:"offset"`
	EndOffset  int64  `json:"end_offset"`
	Revision   string `json:"revision,omitempty"`
	ItemIndex  *int   `json:"item_index,omitempty"`
}

// Snapshot positions have revisions and part indices, not invented byte ranges.
// Keep the Go fields for existing JSONL consumers while discriminating on wire.
func (s Source) MarshalJSON() ([]byte, error) {
	if s.Kind == "snapshot" {
		return json.Marshal(struct {
			Kind       string `json:"kind"`
			Path       string `json:"path"`
			Generation int64  `json:"generation"`
			Revision   string `json:"revision,omitempty"`
			ItemIndex  *int   `json:"item_index,omitempty"`
		}{s.Kind, s.Path, s.Generation, s.Revision, s.ItemIndex})
	}
	type plain Source
	return json.Marshal(plain(s))
}

type Record struct {
	Source     Source
	Data       []byte
	Diagnostic string
}

type Batch struct {
	Records      []Record
	HasMore      bool
	PendingBytes int
	Discarding   bool
	Generation   int64
}

type Reader struct {
	path          string
	maxRecord     int
	batchBytes    int
	identity      os.FileInfo
	generation    int64
	offset        int64
	lineStart     int64
	pending       []byte
	discarding    bool
	prefix        []byte
	anchor        []byte
	snapshot      bool
	snapshotLimit int64
}

func NewReader(path string, maxRecord, batchBytes int) (*Reader, error) {
	if path == "" {
		return nil, fmt.Errorf("an explicit source file is required")
	}
	if maxRecord < 1 || maxRecord > MaxRecordBytes {
		return nil, fmt.Errorf("max record bytes must be between 1 and %d", MaxRecordBytes)
	}
	if batchBytes < 1 || batchBytes > 64*1024*1024 {
		return nil, fmt.Errorf("batch bytes must be between 1 and 67108864")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return &Reader{path: abs, maxRecord: maxRecord, batchBytes: batchBytes, generation: 1}, nil
}

func (r *Reader) Location() Source {
	return Source{Kind: "jsonl", Path: r.path, Generation: r.generation, Offset: r.lineStart, EndOffset: r.offset}
}

// Freeze limits inspection to the byte size at the first successful open.
// Call before ReadBatch. Changes between reads cannot form an atomic snapshot.
func (r *Reader) Freeze() { r.snapshot = true; r.snapshotLimit = -1 }

// ReadBatch consumes at most batchBytes newly available bytes. Only newline-
// terminated records are returned. The reader must be used by one goroutine.
// Missing/inaccessible sources return errors and preserve the existing cursor.
func (r *Reader) ReadBatch() (Batch, error) {
	b := Batch{Generation: r.generation}
	f, err := openSource(r.path)
	if err != nil {
		return b, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return b, err
	}
	if !info.Mode().IsRegular() {
		return b, fmt.Errorf("source must be a regular file")
	}
	changed := ""
	if r.identity != nil {
		switch {
		case !os.SameFile(r.identity, info):
			changed = "source.replaced"
		case info.Size() < r.offset || info.Size() < r.identity.Size():
			changed = "source.truncated"
		default:
			matches, err := matchesAt(f, r.prefix, 0)
			if err != nil {
				return b, err
			}
			if matches {
				matches, err = matchesAt(f, r.anchor, r.offset-int64(len(r.anchor)))
			}
			if err != nil {
				return b, err
			}
			if !matches {
				changed = "source.rewritten"
			}
		}
	}
	if changed != "" {
		r.generation++
		r.offset, r.lineStart = 0, 0
		r.pending, r.prefix, r.anchor = nil, nil, nil
		r.discarding = false
		b.Records = append(b.Records, Record{Source: r.Location(), Diagnostic: changed})
		if r.snapshot {
			return b, fmt.Errorf("source changed during inspection; retry against a stable copy")
		}
	}
	r.identity = info
	limit := info.Size()
	if r.snapshot {
		if r.snapshotLimit < 0 {
			r.snapshotLimit = limit
		}
		if limit > r.snapshotLimit {
			limit = r.snapshotLimit
		}
	}
	remaining := limit - r.offset
	if remaining > int64(r.batchBytes) {
		remaining = int64(r.batchBytes)
	}
	buf := make([]byte, 32*1024)
	for remaining > 0 {
		want := int64(len(buf))
		if remaining < want {
			want = remaining
		}
		n, readErr := f.ReadAt(buf[:want], r.offset)
		consumed := 0
		if n > 0 {
			for _, c := range buf[:n] {
				consumed++
				r.offset++
				if c == '\n' {
					if !r.discarding && len(r.pending) > 0 {
						line := bytes.TrimSuffix(r.pending, []byte{'\r'})
						b.Records = append(b.Records, Record{Source: r.Location(), Data: bytes.Clone(line)})
					}
					r.pending = r.pending[:0]
					r.discarding = false
					r.lineStart = r.offset
				} else if !r.discarding {
					if len(r.pending) == r.maxRecord {
						r.pending = nil
						r.discarding = true
						b.Records = append(b.Records, Record{Source: r.Location(), Diagnostic: "record.oversized"})
					} else {
						r.pending = append(r.pending, c)
					}
				}
				if len(b.Records) >= maxRecordsPerBatch {
					break
				}
			}
			r.remember(buf[:consumed])
			remaining -= int64(consumed)
		}
		if readErr != nil {
			// A source truncated during this batch is not an atomic snapshot.
			// Preserve returned observations, report uncertainty, revalidate next poll.
			if readErr == io.EOF {
				b.Records = append(b.Records, Record{Source: r.Location(), Diagnostic: "source.changed_during_read"})
				break
			}
			return b, readErr
		}
		if n == 0 {
			return b, io.ErrNoProgress
		}
		if len(b.Records) >= maxRecordsPerBatch {
			break
		}
	}
	b.HasMore = limit > r.offset
	b.PendingBytes = len(r.pending)
	b.Discarding = r.discarding
	b.Generation = r.generation
	return b, nil
}

func matchesAt(f *os.File, expected []byte, offset int64) (bool, error) {
	if len(expected) == 0 {
		return true, nil
	}
	actual := make([]byte, len(expected))
	n, err := f.ReadAt(actual, offset)
	if err != nil && err != io.EOF {
		return false, err
	}
	return n == len(expected) && bytes.Equal(expected, actual), nil
}

func (r *Reader) remember(data []byte) {
	if len(r.prefix) < anchorBytes {
		n := anchorBytes - len(r.prefix)
		if n > len(data) {
			n = len(data)
		}
		r.prefix = append(r.prefix, data[:n]...)
	}
	if len(data) >= anchorBytes {
		r.anchor = append(r.anchor[:0], data[len(data)-anchorBytes:]...)
		return
	}
	r.anchor = append(r.anchor, data...)
	if len(r.anchor) > anchorBytes {
		r.anchor = bytes.Clone(r.anchor[len(r.anchor)-anchorBytes:])
	}
}
