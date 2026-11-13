//go:build darwin || linux

package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func buildExternalCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agentview")
	args := []string{"build"}
	if os.Getenv("AGENTVIEW_QA_RACE") == "1" {
		args = append(args, "-race")
	}
	args = append(args, "-o", bin, ".")
	cmd := exec.Command("go", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	return bin
}

func TestExternalCLIContentAndInputExit(t *testing.T) {
	bin := buildExternalCLI(t)
	path := filepath.Join(t.TempDir(), "source.jsonl")
	const canary = "PRIVATE_CANARY_8c1f"
	data := []byte(`{"type":"user","sessionId":"s","message":{"content":"` + canary + `\u001b[2J\nline"}}` + "\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, include := range []bool{false, true} {
		args := []string{"inspect", "--agent", "claude", "--file", path}
		if include {
			args = append(args, "--include-content")
		}
		cmd := exec.Command(bin, args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("CLI inspect failed: %v, %s", err, stderr.String())
		}
		if bytes.Contains(stdout.Bytes(), []byte(canary)) != include {
			t.Fatalf("CLI capture boundary failed: include=%v: %s", include, stdout.String())
		}
		if bytes.Contains(stdout.Bytes(), []byte{0x1b}) || bytes.Contains(stderr.Bytes(), []byte(canary)) {
			t.Fatal("raw terminal escape or private diagnostic escaped output boundary")
		}
		var event map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &event); err != nil {
			t.Fatalf("stdout was not one JSON event: %v\n%s", err, stdout.String())
		}
		if event["execution_state"] != "unknown" {
			t.Fatalf("CLI inferred process state: %+v", event)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, data) {
		t.Fatal("CLI modified source bytes")
	}
	cmd := exec.Command(bin, "inspect", "--agent", "claude", "--file", path+".missing")
	if err := cmd.Run(); err == nil {
		t.Fatal("missing source returned success")
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("missing-source exit status: %v", err)
	}
	cmd = exec.Command(bin, "inspect", "--agent", "unknown", "--file", path)
	if err := cmd.Run(); err == nil {
		t.Fatal("invalid agent returned success")
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		t.Fatalf("invalid-agent exit status: %v", err)
	}
}

func TestExternalCLIFollowCancellationAndSourceIsolation(t *testing.T) {
	bin := buildExternalCLI(t)
	path := filepath.Join(t.TempDir(), "source.jsonl")
	data := []byte(`{"type":"user","sessionId":"s","message":{"content":"hello"}}` + "\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "follow", "--agent", "claude", "--file", path, "--poll", "100ms")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	first := make(chan error, 1)
	go func() {
		var event map[string]any
		first <- json.NewDecoder(stdout).Decode(&event)
	}()
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follower did not emit initial record")
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("interrupt should close only observer cleanly: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follower did not cancel promptly")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("follower changed source content")
	}
	// A separate producer descriptor remains usable after observer exit.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("{}\n"); err != nil {
		t.Fatalf("source writer could not continue: %v", err)
	}
}

func TestExternalCLIRejectsFIFOAndSymlinkWithoutHanging(t *testing.T) {
	bin := buildExternalCLI(t)
	root := t.TempDir()
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(root, "regular.jsonl")
	if err := os.WriteFile(regular, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.jsonl")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, link, root} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cmd := exec.CommandContext(ctx, bin, "inspect", "--agent", "claude", "--file", path)
		output, err := cmd.CombinedOutput()
		deadline := ctx.Err()
		cancel()
		if deadline != nil {
			t.Fatalf("opening special path hung: %s", path)
		}
		if err == nil || !strings.Contains(string(output), "cannot read source") {
			t.Fatalf("special path was not rejected: %s, %v, %s", path, err, output)
		}
	}
}

func TestExternalCLICancelsWithUnreadOutputPipe(t *testing.T) {
	bin := buildExternalCLI(t)
	path := filepath.Join(t.TempDir(), "source.jsonl")
	line := []byte(`{"type":"user","sessionId":"s","message":{"content":"hello"}}` + "\n")
	if err := os.WriteFile(path, bytes.Repeat(line, 5000), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "follow", "--agent", "claude", "--file", path, "--poll", "100ms")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	// Consume the first event to prove startup, then deliberately stop reading.
	first := make(chan error, 1)
	go func() {
		var event map[string]any
		first <- json.NewDecoder(stdout).Decode(&event)
	}()
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follower did not start")
	}
	// Several thousand metadata events exceed the OS pipe capacity.
	time.Sleep(200 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled observer returned failure: %v", err)
		}
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("observer ignored cancellation while blocked on an unread output pipe")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, bytes.Repeat(line, 5000)) {
		t.Fatal("observer changed source while cancelling blocked output")
	}
}
