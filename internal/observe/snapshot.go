package observe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SnapshotReader reads complete replacement documents, never appended actions.
// Revision hashes identify observations, not native message/session identities.
// It retains bounded state (hashes and metadata), not previous transcript text.
type SnapshotReader struct {
	path           string
	limit          int
	revision       string
	generation     int64
	lastDiagnostic string
	frozen, read   bool
	validate       func([]byte) bool
}

func NewSnapshotReader(path string, limit int) (*SnapshotReader, error) {
	if path == "" {
		return nil, fmt.Errorf("an explicit source file is required")
	}
	if limit < 1 || limit > MaxRecordBytes {
		return nil, fmt.Errorf("max record bytes must be between 1 and %d", MaxRecordBytes)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return &SnapshotReader{path: abs, limit: limit}, nil
}

func (r *SnapshotReader) Location() Source {
	return Source{Kind: "snapshot", Path: r.path, Generation: r.generation, Revision: r.revision}
}
func (r *SnapshotReader) Freeze() { r.frozen = true }

func (r *SnapshotReader) diagnostic(code, signature string) Batch {
	b := Batch{Generation: r.generation}
	key := code + ":" + signature
	if key != r.lastDiagnostic {
		source := r.Location()
		source.Revision = ""
		b.Records = []Record{{Source: source, Diagnostic: code}}
	}
	r.lastDiagnostic = key
	return b
}

func (r *SnapshotReader) ReadBatch() (Batch, error) {
	b := Batch{Generation: r.generation}
	if r.frozen && r.read {
		return b, nil
	}
	f, err := openSource(r.path)
	if err != nil {
		return b, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return b, err
	}
	if !before.Mode().IsRegular() {
		return b, fmt.Errorf("source must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(r.limit)+1))
	if err != nil {
		return b, err
	}
	after, err := f.Stat()
	if err != nil {
		return b, err
	}
	current, err := os.Lstat(r.path)
	if err != nil {
		return b, err
	}
	r.read = true
	if !os.SameFile(before, current) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return r.diagnostic("snapshot.unstable", ""), nil
	}
	digest := sha256.Sum256(data)
	revision := hex.EncodeToString(digest[:])
	if len(data) > r.limit {
		return r.diagnostic("snapshot.oversized", revision), nil
	}
	if !json.Valid(data) {
		return r.diagnostic("snapshot.invalid", revision), nil
	}
	if r.validate != nil && !r.validate(data) {
		return r.diagnostic("snapshot.unsupported", revision), nil
	}
	recovered := r.lastDiagnostic != ""
	r.lastDiagnostic = ""
	if revision == r.revision {
		if recovered {
			b.Records = []Record{{Source: r.Location(), Diagnostic: "snapshot.recovered"}}
		}
		return b, nil
	}
	r.revision = revision
	r.generation++
	b.Generation = r.generation
	b.Records = []Record{{Source: r.Location(), Data: data}}
	return b, nil
}
