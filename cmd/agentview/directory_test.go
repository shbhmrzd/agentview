package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryArgumentsAndInspection(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		args       []string
		code       int
		diagnostic string
	}{
		{[]string{"follow", "--agent", "codex"}, 2, "exactly one"},
		{[]string{"inspect", "--agent", "codex", "--dir", root, "--format", "bad"}, 2, "format must be"},
		{[]string{"follow", "--agent", "codex", "--file", "x", "--dir", root}, 2, "exactly one"},
		{[]string{"follow", "--agent", "rovo", "--dir", root}, 2, "claude and codex"},
		{[]string{"follow", "--agent", "codex", "--dir", root, "--history", "recent"}, 2, "none or all"},
		{[]string{"inspect", "--agent", "codex", "--dir", root, "--history", "none"}, 2, "requires history all"},
		{[]string{"follow", "--agent", "codex", "--file", "x", "--history", "none"}, 2, "only supported with --dir"},
		{[]string{"inspect", "--agent", "claude", "--dir", filepath.Join(root, "absent")}, 1, "cannot observe directory"},
	} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), test.args, &out, &errOut); code != test.code || !strings.Contains(errOut.String(), test.diagnostic) {
			t.Fatalf("%v: code=%d stderr=%s", test.args, code, errOut.String())
		}
	}
	const canary = "DIRECTORY_PRIVATE_PAYLOAD"
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","sessionId":"session","message":{"content":"`+canary+`"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, include := range []bool{false, true} {
		args := []string{"inspect", "--agent", "claude", "--dir", root}
		if include {
			args = append(args, "--include-content")
		}
		var out, errOut bytes.Buffer
		if code := run(context.Background(), args, &out, &errOut); code != 0 {
			t.Fatalf("inspection: %d %s", code, errOut.String())
		}
		if strings.Contains(out.String(), canary) != include || !strings.Contains(out.String(), `"kind":"message.user"`) {
			t.Fatalf("capture/history default: %s", out.String())
		}
	}
}
