package observe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxDirectorySources = 10000
	maxDirectoryEntries = 100000
	maxDirectoryDepth   = 64
)

// DirectoryOptions controls an ephemeral, explicitly rooted observation run.
// History controls initial output, not which files may be read for context.
type DirectoryOptions struct {
	Agent, Root, History string
	Inspect              bool
	Normalizer           NormalizerOptions
	MaxRecordBytes       int
}

type directorySource struct {
	reader                          *Reader
	normalizer                      *Normalizer
	info                            os.FileInfo
	boundary                        int64
	generation, replacementBoundary int64
	initial                         bool
	dirty, more, ready, failed      bool
	done                            bool
	seen                            uint64
}

// Directory owns independent reader/decoder state and schedules one bounded
// batch per source per round. No model processes or durable storage are owned.
type Directory struct {
	options      DirectoryOptions
	root         string
	sources      map[string]*directorySource
	order        []*directorySource
	scanID       uint64
	scanProblem  string
	inspectError bool
	maxSources   int
	maxEntries   int
}

func NewDirectory(options DirectoryOptions) (*Directory, error) {
	if options.Agent != "claude" && options.Agent != "codex" {
		return nil, fmt.Errorf("directory observation currently supports claude and codex")
	}
	if options.Root == "" {
		return nil, fmt.Errorf("an explicit directory is required")
	}
	if options.History != "none" && options.History != "all" {
		return nil, fmt.Errorf("history must be none or all")
	}
	if options.Inspect && options.History != "all" {
		return nil, fmt.Errorf("directory inspection requires history all")
	}
	if options.MaxRecordBytes < 1 || options.MaxRecordBytes > MaxRecordBytes {
		return nil, fmt.Errorf("max record bytes must be between 1 and %d", MaxRecordBytes)
	}
	root, err := filepath.Abs(options.Root)
	if err != nil {
		return nil, err
	}
	return &Directory{options: options, root: root, sources: make(map[string]*directorySource), maxSources: maxDirectorySources, maxEntries: maxDirectoryEntries}, nil
}

// Run validates the root, inventories it once, then reconciles it every poll.
// Inspect freezes each source at discovery and never extends its initial set.
func (d *Directory) Run(ctx context.Context, poll time.Duration, emit func(Event) error) error {
	if poll < 100*time.Millisecond {
		return fmt.Errorf("poll must be at least 100ms")
	}
	root, err := filepath.EvalSymlinks(d.root)
	if err != nil {
		return fmt.Errorf("cannot resolve directory: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source root must be a directory")
	}
	d.root = root
	if err := d.scan(ctx, true, emit); err != nil {
		return err
	}
	if err := d.notice("directory.ready", "Initial scan finished; source.ready marks each initialized source. Check directory diagnostics for incomplete coverage. Checkpoints are not persisted.", emit); err != nil {
		return err
	}
	nextScan := time.Now().Add(poll)
	for {
		if ctx.Err() != nil {
			return nil
		}
		if !d.options.Inspect && !time.Now().Before(nextScan) {
			if err := d.scan(ctx, false, emit); err != nil {
				return err
			}
			nextScan = time.Now().Add(poll)
		}
		more, err := d.readRound(ctx, emit)
		if err != nil {
			return err
		}
		if d.options.Inspect && !more {
			if d.inspectError {
				return errors.New("directory inspection incomplete; see source and directory diagnostics")
			}
			return nil
		}
		if more {
			continue
		}
		timer := time.NewTimer(max(time.Until(nextScan), time.Millisecond))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (d *Directory) candidate(name string) bool {
	if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".jsonl") {
		return false
	}
	return d.options.Agent == "claude" || strings.HasPrefix(name, "rollout-")
}

// safePath rechecks every descendant component before opening. Configured roots
// are trusted local trees: this rejects observed symlinks, not adversarial
// concurrent ancestor swaps (it is not a descriptor-relative sandbox).
func (d *Directory) safePath(path string) error {
	rel, err := filepath.Rel(d.root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("source outside selected root")
	}
	current := d.root
	parts := []string{}
	if rel != "." {
		parts = strings.Split(rel, string(filepath.Separator))
	}
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			current = filepath.Join(current, parts[i])
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink within selected root")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return errors.New("source ancestor is not a directory")
		}
	}
	return nil
}

func (d *Directory) scan(ctx context.Context, initial bool, emit func(Event) error) error {
	d.scanID++
	entries := 0
	problem := ""
	var outputErr error
	var walk func(string, int)
	walk = func(path string, depth int) {
		if ctx.Err() != nil || outputErr != nil {
			return
		}
		if entries >= d.maxEntries || depth > maxDirectoryDepth {
			problem = "directory.limit"
			return
		}
		if err := d.safePath(path); err != nil {
			problem = "directory.unavailable"
			return
		}
		f, err := openDirectory(path)
		if err != nil {
			problem = "directory.unavailable"
			return
		}
		defer f.Close()
		for {
			if ctx.Err() != nil || outputErr != nil {
				return
			}
			page, readErr := f.ReadDir(128)
			for _, entry := range page {
				entries++
				if entries > d.maxEntries {
					problem = "directory.limit"
					return
				}
				child := filepath.Join(path, entry.Name())
				if entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				if entry.IsDir() {
					if !strings.HasPrefix(entry.Name(), ".") {
						walk(child, depth+1)
					}
					continue
				}
				if !d.candidate(entry.Name()) {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					problem = "directory.unavailable"
					continue
				}
				if !info.Mode().IsRegular() {
					continue
				}
				if s, ok := d.sources[child]; ok {
					s.seen = d.scanID
					s.dirty = s.dirty || s.failed || !os.SameFile(s.info, info) || s.info.Size() != info.Size() || !s.info.ModTime().Equal(info.ModTime())
					s.info = info
					continue
				}
				if len(d.sources) >= d.maxSources {
					problem = "directory.limit"
					continue
				}
				s, err := d.add(child, info, initial)
				if err != nil {
					outputErr = err
					return
				}
				if err := d.sourceNotice(s, "source.discovered", "Candidate source discovered; this does not establish process liveness or a newly created session.", emit); err != nil {
					outputErr = err
					return
				}
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					problem = "directory.unavailable"
				}
				return
			}
		}
	}
	walk(d.root, 0)
	if outputErr != nil {
		return outputErr
	}
	if ctx.Err() != nil {
		return nil
	}
	for _, s := range d.order {
		if s.seen != d.scanID {
			s.dirty = true
		}
	}
	if problem != "" {
		d.inspectError = true
	}
	if problem != d.scanProblem {
		if problem == "" {
			if err := d.notice("directory.recovered", "Directory scan coverage recovered.", emit); err != nil {
				return err
			}
		} else {
			if err := d.notice(problem, "Discovery coverage is incomplete: unreadable paths or scan/source/depth limits. Existing registered sources continue to be observed.", emit); err != nil {
				return err
			}
		}
		d.scanProblem = problem
	}
	return nil
}

func (d *Directory) add(path string, info fs.FileInfo, initial bool) (*directorySource, error) {
	// One batch can span a maximum-sized record even when partial tails rewind.
	r, err := NewReader(path, d.options.MaxRecordBytes, max(DefaultBatchBytes, d.options.MaxRecordBytes+1))
	if err != nil {
		return nil, err
	}
	r.rereadPartial = true
	if d.options.Inspect {
		r.Freeze()
	}
	n, err := NewNormalizerWithOptions(d.options.Agent, d.options.Normalizer)
	if err != nil {
		return nil, err
	}
	s := &directorySource{reader: r, normalizer: n, info: info, boundary: -1, generation: 1, initial: initial, dirty: true, seen: d.scanID}
	if err := d.safePath(path); err == nil {
		if boundary, err := r.captureBoundary(); err == nil {
			s.boundary = boundary
		}
	}
	d.sources[path] = s
	d.order = append(d.order, s)
	return s, nil
}

func (d *Directory) readRound(ctx context.Context, emit func(Event) error) (bool, error) {
	more := false
	for _, s := range d.order {
		if ctx.Err() != nil {
			return false, nil
		}
		if s.done || (!s.dirty && !s.more) {
			continue
		}
		s.dirty, s.more = false, false
		err := d.safePath(s.reader.path)
		if err == nil && s.boundary < 0 {
			s.boundary, err = s.reader.captureBoundary()
			if err != nil {
				s.boundary = -1
			}
			if err == nil && s.initial && d.options.History == "none" {
				if err := d.sourceNotice(s, "source.baseline_delayed", "Starting boundary established after an earlier access failure; earlier records are historical.", emit); err != nil {
					return false, err
				}
			}
		}
		var batch Batch
		if err == nil {
			batch, err = s.reader.ReadBatch()
		}
		if s.reader.generation != s.generation {
			s.generation = s.reader.generation
			s.replacementBoundary = s.reader.identity.Size()
		}
		if err == nil && s.failed {
			if emitErr := d.sourceNotice(s, "source.available", "Source can be read again.", emit); emitErr != nil {
				return false, emitErr
			}
		}
		for _, record := range batch.Records {
			phase := "update"
			if record.Source.Generation == 1 && record.Source.EndOffset <= s.boundary {
				phase = "history"
				if !s.initial {
					phase = "discovered_history"
				}
			}
			if record.Source.Generation > 1 && record.Source.EndOffset <= s.replacementBoundary {
				phase = "replacement_history"
			}
			events := s.normalizer.Normalize(record, time.Now())
			if record.Diagnostic == "" && s.initial && d.options.History == "none" && phase == "history" {
				continue
			}
			for _, event := range events {
				event.ObservationPhase = phase
				if err := emit(event); err != nil {
					return false, err
				}
			}
		}
		if err != nil {
			if !s.failed {
				if err := d.sourceNotice(s, "source.unavailable", "Source could not be read safely; other sources continue. No process state is inferred.", emit); err != nil {
					return false, err
				}
			}
			s.failed = true
			if d.options.Inspect {
				s.done = true
				d.inspectError = true
			}
			continue
		}
		s.failed = false
		if !s.ready && (batch.ReadThrough >= s.boundary || batch.Generation > 1) {
			s.ready = true
			if err := d.sourceNotice(s, "source.ready", "Reader reached its initial boundary. Session identity is included when available; this is not agent readiness.", emit); err != nil {
				return false, err
			}
		}
		s.more = batch.HasMore
		more = more || s.more
		if d.options.Inspect && !s.more {
			s.done = true
			if batch.PendingBytes > 0 {
				if err := d.sourceNotice(s, "record.incomplete", "Final record has no newline and was not parsed.", emit); err != nil {
					return false, err
				}
			}
		}
	}
	return more, nil
}

func (d *Directory) sourceNotice(s *directorySource, kind, detail string, emit func(Event) error) error {
	for _, event := range s.normalizer.Normalize(Record{Source: s.reader.Location(), Diagnostic: kind}, time.Now()) {
		event.Detail = detail
		event.SourceModifiedAt = s.info.ModTime().UTC().Format(time.RFC3339Nano)
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}

func (d *Directory) notice(kind, detail string, emit func(Event) error) error {
	n, _ := NewNormalizerWithOptions(d.options.Agent, d.options.Normalizer)
	for _, event := range n.Normalize(Record{Source: Source{Kind: "directory", Path: d.root, Generation: int64(d.scanID)}, Diagnostic: kind}, time.Now()) {
		event.Detail = detail
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}
