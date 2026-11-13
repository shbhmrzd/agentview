// native-smoke is an opt-in compatibility harness, not part of AgentView.
// It owns only the disposable CLI processes it starts. Routine CI never runs it.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type report struct {
	Agent           string         `json:"agent"`
	Version         string         `json:"version"`
	ModelOverride   string         `json:"model_override,omitempty"`
	Outcome         string         `json:"outcome"`
	Stage           string         `json:"stage"`
	Error           string         `json:"error,omitempty"`
	SessionID       string         `json:"session_id,omitempty"`
	Source          string         `json:"source,omitempty"`
	SeedEndOffset   int64          `json:"seed_end_offset,omitempty"`
	LiveOffset      int64          `json:"post_attach_offset,omitempty"`
	Generation      int64          `json:"source_generation,omitempty"`
	SeedObserved    bool           `json:"seed_assistant_observed"`
	LiveObserved    bool           `json:"post_attach_assistant_observed"`
	MetadataOnly    bool           `json:"metadata_only_verified"`
	SourceUnchanged bool           `json:"source_unchanged_during_inspect"`
	Events          map[string]int `json:"observed_event_kinds,omitempty"`
	StartedAt       time.Time      `json:"started_at"`
	FinishedAt      time.Time      `json:"finished_at"`
}

func main() {
	agent := flag.String("agent", "", "required: claude or codex; starts paid native model calls")
	binary := flag.String("agentview", "bin/agentview", "built AgentView binary")
	output := flag.String("output", "test-results", "private artifact parent directory")
	deadline := flag.Duration("timeout", 90*time.Second, "deadline for each native turn")
	model := flag.String("model", "", "optional native CLI model override; default uses the CLI's model")
	flag.Parse()
	if (*agent != "claude" && *agent != "codex") || *deadline <= 0 || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "use --agent claude|codex and a positive --timeout; this opts into native model calls")
		os.Exit(2)
	}
	absBinary, err := filepath.Abs(*binary)
	if err != nil {
		fatal(err)
	}
	absOutput, err := filepath.Abs(*output)
	if err != nil {
		fatal(err)
	}
	if err = os.MkdirAll(absOutput, 0700); err != nil {
		fatal(err)
	}
	runDir, err := os.MkdirTemp(absOutput, "native-"+*agent+"-")
	if err != nil {
		fatal(err)
	}
	r := report{Agent: *agent, ModelOverride: *model, Outcome: "failed", StartedAt: time.Now().UTC(), Events: map[string]int{}}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	err = run(ctx, &r, absBinary, runDir, *deadline)
	r.FinishedAt = time.Now().UTC()
	if err != nil {
		r.Error = err.Error()
	} else {
		r.Outcome = "passed"
	}
	data, marshalErr := json.MarshalIndent(r, "", "  ")
	if marshalErr != nil {
		fatal(marshalErr)
	}
	if writeErr := os.WriteFile(filepath.Join(runDir, "report.json"), append(data, '\n'), 0600); writeErr != nil {
		fatal(writeErr)
	}
	fmt.Printf("Native %s smoke: %s (%s)\nReport: %s\n", *agent, r.Outcome, r.Stage, filepath.Join(runDir, "report.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "Inspect this run's private stderr files; failure is not a native compatibility pass.")
		os.Exit(1)
	}
}

func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }

func run(ctx context.Context, r *report, binary, dir string, deadline time.Duration) (runErr error) {
	r.Stage = "preflight"
	if _, err := os.Stat(binary); err != nil {
		return err
	}
	cli, err := exec.LookPath(r.Agent)
	if err != nil {
		return err
	}
	vctx, vcancel := context.WithTimeout(ctx, 10*time.Second)
	version, err := exec.CommandContext(vctx, cli, "--version").Output()
	vcancel()
	if err != nil {
		return fmt.Errorf("native version: %w", err)
	}
	r.Version = strings.TrimSpace(string(version))
	cwd := filepath.Join(dir, "workspace")
	if err = os.Mkdir(cwd, 0700); err != nil {
		return err
	}
	id, err := uuid()
	if err != nil {
		return err
	}
	seedMarker := "AGENTVIEW_SEED_" + strings.ReplaceAll(id, "-", "")
	liveMarker := "AGENTVIEW_LIVE_" + strings.ReplaceAll(id, "-", "")
	if r.Agent == "claude" {
		r.SessionID = id
	}
	r.Stage = "seed_native_session"
	fmt.Printf("Starting disposable %s session (%s); artifacts in %s\n", r.Agent, r.Version, dir)
	if err = nativeTurn(ctx, cli, r.Agent, r.ModelOverride, cwd, dir, "seed", r.SessionID, seedMarker, false, deadline); err != nil {
		return err
	}
	if r.Agent == "codex" {
		r.SessionID, err = codexThread(filepath.Join(dir, "seed.stdout.jsonl"))
		if err != nil {
			return err
		}
	}
	r.Stage = "locate_native_transcript"
	r.Source, err = findSource(r.Agent, r.SessionID)
	if err != nil {
		return err
	}
	// stdout from native --json/stream-json is not a persisted transcript.
	// Only the exact new session's native JSONL file is passed to AgentView.
	r.Stage = "attach_existing_history"
	viewerCtx, viewerCancel := context.WithCancel(ctx)
	defer viewerCancel()
	viewer := ownedCommand(viewerCtx, binary, "follow", "--agent", r.Agent, "--file", r.Source, "--include-content", "--poll", "100ms")
	// AgentView handles SIGTERM and should exit successfully. An external kill
	// or a forced timeout must not be mistaken for successful viewer shutdown.
	viewer.Cancel = func() error { return viewer.Process.Signal(syscall.SIGTERM) }
	stderr, err := privateFile(filepath.Join(dir, "viewer.stderr.txt"))
	if err != nil {
		return err
	}
	defer stderr.Close()
	viewer.Stderr = stderr
	pipe, err := viewer.StdoutPipe()
	if err != nil {
		return err
	}
	eventsFile, err := privateFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return err
	}
	defer eventsFile.Close()
	if err = viewer.Start(); err != nil {
		return err
	}
	events := make(chan observed, 256)
	scanDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(pipe)
		scanner.Buffer(make([]byte, 4096), 20<<20)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			if _, e := eventsFile.Write(append(line, '\n')); e != nil {
				scanDone <- e
				return
			}
			var event observed
			if e := json.Unmarshal(line, &event); e != nil {
				scanDone <- e
				return
			}
			select {
			case events <- event:
			case <-viewerCtx.Done():
				scanDone <- viewerCtx.Err()
				return
			}
		}
		scanDone <- scanner.Err()
	}()
	defer func() {
		viewerCancel()
		scanErr := <-scanDone
		waitErr := viewer.Wait()
		if runErr != nil {
			return
		}
		if scanErr != nil && !errors.Is(scanErr, context.Canceled) {
			r.Stage = "viewer_shutdown"
			runErr = fmt.Errorf("capture viewer output: %w", scanErr)
			return
		}
		if waitErr = viewerExitError(viewer, waitErr); waitErr != nil {
			r.Stage = "viewer_shutdown"
			runErr = fmt.Errorf("viewer exit: %w", waitErr)
		}
	}()
	if err = awaitMarker(ctx, events, viewer.Process, r, seedMarker, 15*time.Second); err != nil {
		return err
	}
	r.SeedObserved = true
	r.Stage = "resume_while_attached"
	fmt.Printf("Attached to new session %s; starting a second turn while follow is active\n", r.SessionID)
	if err = nativeTurn(ctx, cli, r.Agent, r.ModelOverride, cwd, dir, "resume", r.SessionID, liveMarker, true, deadline); err != nil {
		return err
	}
	if err = awaitMarker(ctx, events, viewer.Process, r, liveMarker, 15*time.Second); err != nil {
		return err
	}
	r.LiveObserved = true
	// Native writers have finished. Check read-only inspection and metadata mode
	// with a stable source, since hashes necessarily change during native writes.
	r.Stage = "metadata_and_source_integrity"
	before, modeBefore, err := fingerprint(r.Source)
	if err != nil {
		return err
	}
	inspectCtx, inspectCancel := context.WithTimeout(ctx, 15*time.Second)
	defer inspectCancel()
	inspection := ownedCommand(inspectCtx, binary, "inspect", "--agent", r.Agent, "--file", r.Source)
	metadata, err := inspection.Output()
	if err != nil {
		return fmt.Errorf("metadata inspection: %w", err)
	}
	if err = checkMetadata(metadata, r.Agent, r.SessionID, seedMarker, liveMarker); err != nil {
		return err
	}
	r.MetadataOnly = true
	if err = os.WriteFile(filepath.Join(dir, "metadata.jsonl"), metadata, 0600); err != nil {
		return err
	}
	after, modeAfter, err := fingerprint(r.Source)
	if err != nil {
		return err
	}
	if before != after || modeBefore != modeAfter {
		return errors.New("native source changed during read-only inspection")
	}
	r.SourceUnchanged = true
	r.Stage = "complete"
	return nil
}

func viewerExitError(cmd *exec.Cmd, err error) error {
	// CommandContext returns context.Canceled even when its Cancel function
	// successfully asks a cooperative child to exit zero. Require the actual
	// successful process status; a signal or nonzero exit must still fail.
	if errors.Is(err, context.Canceled) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		return nil
	}
	return err
}

type observed struct {
	Kind           string   `json:"kind"`
	Agent          string   `json:"agent"`
	SessionID      string   `json:"session_id"`
	Content        string   `json:"content"`
	Captured       bool     `json:"content_captured"`
	ExecutionState string   `json:"execution_state"`
	Source         position `json:"source"`
}

type position struct {
	Path       string `json:"path"`
	Generation int64  `json:"generation"`
	Offset     int64  `json:"offset"`
	EndOffset  int64  `json:"end_offset"`
}

func awaitMarker(ctx context.Context, events <-chan observed, process *os.Process, r *report, marker string, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("AgentView did not emit assistant marker for session %s before deadline", r.SessionID)
		case e := <-events:
			r.Events[e.Kind]++
			if strings.HasPrefix(e.Kind, "source.") {
				return fmt.Errorf("source changed during native smoke: %s", e.Kind)
			}
			if e.ExecutionState != "unknown" {
				return errors.New("AgentView inferred execution state")
			}
			if e.Kind == "message.assistant" && e.Agent == r.Agent && e.SessionID == r.SessionID && e.Content == marker {
				if e.Source.Path != r.Source || e.Source.Generation < 1 || e.Source.Offset < 0 || e.Source.EndOffset <= e.Source.Offset {
					return errors.New("assistant marker lacks valid source provenance")
				}
				if r.SeedObserved {
					if e.Source.Generation != r.Generation || e.Source.Offset < r.SeedEndOffset {
						return errors.New("live marker did not advance beyond seed source position")
					}
					r.LiveOffset = e.Source.Offset
				} else {
					r.SeedEndOffset, r.Generation = e.Source.EndOffset, e.Source.Generation
				}
				if err := process.Signal(syscall.Signal(0)); err != nil {
					return fmt.Errorf("viewer exited before assertion: %w", err)
				}
				return nil
			}
		}
	}
}

func nativeTurn(ctx context.Context, cli, agent, model, cwd, dir, phase, session, marker string, resume bool, timeout time.Duration) error {
	turnCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := ownedCommand(turnCtx, cli, nativeArgs(agent, model, session, marker, resume)...)
	cmd.Dir = cwd
	out, err := privateFile(filepath.Join(dir, phase+".stdout.jsonl"))
	if err != nil {
		return err
	}
	defer out.Close()
	errOut, err := privateFile(filepath.Join(dir, phase+".stderr.txt"))
	if err != nil {
		return err
	}
	defer errOut.Close()
	cmd.Stdout, cmd.Stderr = &limitedWriter{w: out, remaining: 8 << 20}, &limitedWriter{w: errOut, remaining: 8 << 20}
	if err = cmd.Run(); err != nil {
		if turnCtx.Err() != nil {
			return fmt.Errorf("native %s: %w", phase, turnCtx.Err())
		}
		return fmt.Errorf("native %s: %w", phase, err)
	}
	return nativeCompletion(filepath.Join(dir, phase+".stdout.jsonl"), agent, session)
}

func nativeCompletion(path, agent, session string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	completed := false
	for scanner.Scan() {
		var e struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			IsError bool   `json:"is_error"`
			Session string `json:"session_id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return fmt.Errorf("native transport JSON: %w", err)
		}
		if agent == "codex" {
			if e.Type == "turn.failed" {
				return errors.New("native Codex reported turn.failed")
			}
			if e.Type == "turn.completed" {
				completed = true
			}
		} else if e.Type == "result" {
			if e.IsError || e.Subtype != "success" || e.Session != session {
				return errors.New("native Claude result was not success for the expected session")
			}
			completed = true
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !completed {
		return errors.New("native CLI exited without a successful completion event")
	}
	return nil
}

func nativeArgs(agent, model, session, marker string, resume bool) []string {
	prompt := "This is a disposable AgentView observer integration test. Reply with exactly " + marker + " and nothing else. Do not use tools, delegate, read files, run commands, or modify anything."
	if agent == "claude" {
		args := []string{"-p", "--output-format", "stream-json", "--verbose", "--safe-mode", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--tools", "", "--permission-prompts", "none", "--max-turns", "1", "--max-budget-usd", "0.50"}
		if model != "" {
			args = append(args, "--model", model)
		}
		if resume {
			args = append(args, "--resume", session)
		} else {
			args = append(args, "--session-id", session)
		}
		return append(args, prompt)
	}
	args := []string{"exec", "--sandbox", "read-only", "--ignore-user-config", "--ignore-rules", "--json", "--skip-git-repo-check", "--color", "never"}
	if model != "" {
		args = append(args, "--model", model)
	}
	if resume {
		args = append(args, "resume", session)
	}
	return append(args, prompt)
}

func ownedCommand(ctx context.Context, path string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Only kill the process group created by this harness; never an attached agent.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

func privateFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}

type limitedWriter struct {
	mu        sync.Mutex
	w         io.Writer
	remaining int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) > w.remaining {
		return 0, errors.New("native output exceeded 8 MiB limit")
	}
	n, err := w.w.Write(p)
	w.remaining -= n
	return n, err
}

func uuid() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}

func codexThread(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	for scanner.Scan() {
		var event struct {
			Type   string `json:"type"`
			Thread string `json:"thread_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type == "thread.started" && validID(event.Thread) {
			return event.Thread, nil
		}
	}
	if scanner.Err() != nil {
		return "", scanner.Err()
	}
	return "", errors.New("no newly-created thread ID in native stdout")
}

func validID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil
}

func findSource(agent, id string) (string, error) {
	if !validID(id) {
		return "", errors.New("refusing source lookup without an exact native UUID")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := os.Getenv("CODEX_HOME")
	if root == "" {
		root = filepath.Join(home, ".codex")
	}
	subdir := "sessions"
	if agent == "claude" {
		root = os.Getenv("CLAUDE_CONFIG_DIR")
		if root == "" {
			root = filepath.Join(home, ".claude")
		}
		subdir = "projects"
	}
	var matches []string
	err = filepath.WalkDir(filepath.Join(root, subdir), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() && ((agent == "claude" && entry.Name() == id+".jsonl") || (agent == "codex" && strings.HasPrefix(entry.Name(), "rollout-") && strings.HasSuffix(entry.Name(), "-"+id+".jsonl"))) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("native source lookup: %w", err)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected one native JSONL file for new session %s, found %d; this storage mode is not verified", id, len(matches))
	}
	return matches[0], nil
}

func fingerprint(path string) ([32]byte, fs.FileMode, error) {
	var sum [32]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return sum, 0, err
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return sum, 0, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, info.Mode(), nil
}

func checkMetadata(data []byte, agent, session string, markers ...string) error {
	for _, marker := range markers {
		if strings.Contains(string(data), marker) {
			return errors.New("marker leaked into metadata output")
		}
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 4096), 20<<20)
	count := 0
	for scanner.Scan() {
		var e observed
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return err
		}
		if e.Agent != agent || e.Captured || e.Content != "" || e.ExecutionState != "unknown" {
			return errors.New("metadata or execution-state contract violated")
		}
		if e.SessionID == session && e.Kind == "message.assistant" {
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if count < 2 {
		return errors.New("metadata inspection did not contain both native assistant turns")
	}
	return nil
}
