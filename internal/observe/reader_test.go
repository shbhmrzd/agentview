package observe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPartialRecordsAndPhysicalOffsets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.jsonl")
	if err := os.WriteFile(path, []byte("first\nsecond"), 0600); err != nil {
		t.Fatal(err)
	}
	r, _ := NewReader(path, 20, 100)
	b, err := r.ReadBatch()
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Records) != 1 || b.PendingBytes != 6 || b.Records[0].Source.EndOffset != 6 {
		t.Fatalf("bad partial handling %+v", b)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("\n")
	f.Close()
	b, err = r.ReadBatch()
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Records) != 1 || string(b.Records[0].Data) != "second" || b.Records[0].Source.Offset != 6 {
		t.Fatalf("bad completion %+v", b)
	}
	b, err = r.ReadBatch()
	if err != nil || len(b.Records) != 0 {
		t.Fatal("duplicate unchanged data")
	}
}

func TestFreezeStopsAtInitialBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.jsonl")
	os.WriteFile(path, []byte("a\nb\n"), 0600)
	r, _ := NewReader(path, 20, 2)
	r.Freeze()
	b, err := r.ReadBatch()
	if err != nil || !b.HasMore {
		t.Fatal("expected snapshot backlog")
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("c\n")
	f.Close()
	b, err = r.ReadBatch()
	if err != nil {
		t.Fatal(err)
	}
	if b.HasMore || len(b.Records) != 1 || string(b.Records[0].Data) != "b" {
		t.Fatal("inspection extended to appended data")
	}
}

func TestBatchBoundPreservesCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.jsonl")
	os.WriteFile(path, []byte(strings.Repeat("a\n", 2500)), 0600)
	r, _ := NewReader(path, 20, DefaultBatchBytes)
	count := 0
	for {
		b, err := r.ReadBatch()
		if err != nil {
			t.Fatal(err)
		}
		if len(b.Records) > 1024 {
			t.Fatal("record count unbounded")
		}
		for _, record := range b.Records {
			if record.Diagnostic != "" {
				t.Fatalf("false generation change %s", record.Diagnostic)
			}
		}
		count += len(b.Records)
		if !b.HasMore {
			break
		}
	}
	if count != 2500 {
		t.Fatalf("lost or duplicated records: %d", count)
	}
}
