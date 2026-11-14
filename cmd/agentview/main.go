package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shbhmrzd/agentview/internal/observe"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		// The HTTP server has its own bounded shutdown and must finish cancelling
		// readers before the process exits. The pipe fallback is CLI-only.
		if len(os.Args) > 1 && os.Args[1] == "serve" {
			return
		}
		// A stalled pipe consumer must not swallow termination indefinitely.
		// There is no durable store to flush in M0. This exits only AgentView.
		_ = os.Stdout.SetWriteDeadline(time.Now())
		go os.Stdout.Close()
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-done:
			return
		case <-timer.C:
			os.Exit(0)
		}
	}()
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	close(done)
	if ctx.Err() != nil {
		code = 0
	}
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Fprintln(out, "AgentView local session observer\nUsage: agentview serve [--agent all|claude|codex] [--include-content]\n       agentview serve --agent <claude|codex> --dir <root> [--listen 127.0.0.1:7433]\n       agentview <inspect|follow> --agent <adapter> --file <source> [--include-content]\n       agentview <inspect|follow> --agent <claude|codex> --dir <root> [--history none|all]\n       agentview adapters\nServe opens a local browser dashboard; use the printed URL. It discovers default Claude/Codex roots unless --dir is set.\nDirectory follow defaults to history none; inspect defaults to all.\nCLI output: text in terminals, JSONL in pipes (--format text|jsonl). Process state stays unknown. No durable store or restart checkpoints yet.")
		return 0
	}
	mode := args[0]
	if mode == "version" || mode == "--version" {
		fmt.Fprintln(out, "agentview", version)
		return 0
	}
	if mode == "serve" {
		return runServe(ctx, args[1:], out, errOut)
	}
	if mode == "adapters" {
		if len(args) != 1 {
			fmt.Fprintln(errOut, "adapters takes no arguments")
			return 2
		}
		if err := json.NewEncoder(out).Encode(observe.Adapters()); err != nil {
			return 1
		}
		return 0
	}
	if mode != "inspect" && mode != "follow" {
		fmt.Fprintln(errOut, "unknown command; use agentview --help")
		return 2
	}
	flags := flag.NewFlagSet(mode, flag.ContinueOnError)
	flags.SetOutput(errOut)
	agent := flags.String("agent", "", "adapter name (required; see agentview adapters)")
	path := flags.String("file", "", "explicit regular source file (required; final-component symlinks rejected)")
	dir := flags.String("dir", "", "session directory to discover recursively (claude/codex; mutually exclusive with --file)")
	history := flags.String("history", "", "directory startup history: none or all (follow defaults to none, inspect to all)")
	format := flags.String("format", "auto", "output: auto (text in terminals, JSONL in pipes), text, or jsonl")
	content := flags.Bool("include-content", false, "include user-facing messages and tool payloads; reasoning remains omitted")
	scope := flags.String("scope", "local", "session identity namespace for this host/account/source environment")
	maxRecord := flags.Int("max-record-bytes", observe.DefaultMaxRecordBytes, "maximum complete JSONL record size")
	poll := flags.Duration("poll", time.Second, "follow polling interval, minimum 100ms")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errOut, "unexpected positional arguments")
		return 2
	}
	if *poll < 100*time.Millisecond {
		fmt.Fprintln(errOut, "poll must be at least 100ms")
		return 2
	}
	if (*path == "") == (*dir == "") {
		fmt.Fprintln(errOut, "provide exactly one of --file or --dir")
		return 2
	}
	selectedFormat, err := outputFormat(out, *format)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	if *dir != "" {
		if *history == "" {
			*history = "none"
			if mode == "inspect" {
				*history = "all"
			}
		}
		directory, err := observe.NewDirectory(observe.DirectoryOptions{
			Agent: *agent, Root: *dir, History: *history, Inspect: mode == "inspect",
			Normalizer: observe.NormalizerOptions{IncludeContent: *content, Scope: *scope}, MaxRecordBytes: *maxRecord,
		})
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 2
		}
		output := newEventOutput(out, selectedFormat, mode, *history)
		if err := directory.Run(ctx, *poll, output.write); err != nil {
			fmt.Fprintf(errOut, "cannot observe directory: %q\n", err.Error())
			return 1
		}
		return 0
	}
	if *history != "" {
		fmt.Fprintln(errOut, "--history is only supported with --dir; file following includes existing history")
		return 2
	}
	norm, err := observe.NewNormalizerWithOptions(*agent, observe.NormalizerOptions{IncludeContent: *content, Scope: *scope})
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	info, _ := observe.LookupAdapter(*agent)
	reader, err := observe.NewSourceReader(info, *path, *maxRecord)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	if mode == "inspect" {
		reader.Freeze()
	}
	output := newEventOutput(out, selectedFormat, mode, "all")
	emit := func(r observe.Record) error {
		for _, event := range norm.Normalize(r, time.Now()) {
			if err := output.write(event); err != nil {
				return err
			}
		}
		return nil
	}
	started := false
	unavailable := false
	for {
		if ctx.Err() != nil {
			return 0
		}
		batch, readErr := reader.ReadBatch()
		for _, record := range batch.Records {
			if err := emit(record); err != nil {
				fmt.Fprintln(errOut, "cannot write output")
				return 1
			}
		}
		if readErr != nil {
			if !started || mode == "inspect" {
				// Quote errors so path control characters cannot alter a terminal.
				fmt.Fprintf(errOut, "cannot read source: %q\n", readErr.Error())
				return 1
			}
			if !unavailable {
				if err := emit(observe.Record{Source: reader.Location(), Diagnostic: "source.unavailable"}); err != nil {
					return 1
				}
			}
			unavailable = true
		} else {
			if unavailable {
				if err := emit(observe.Record{Source: reader.Location(), Diagnostic: "source.available"}); err != nil {
					return 1
				}
			}
			unavailable = false
			started = true
			// inspect follows the bytes present at first successful open. The
			// reader enforces this boundary, including across multiple batches.
			if mode == "inspect" {
				if !batch.HasMore {
					if batch.PendingBytes > 0 {
						if err := emit(observe.Record{Source: reader.Location(), Diagnostic: "record.incomplete"}); err != nil {
							return 1
						}
					}
					return 0
				}
			}
			if batch.HasMore {
				continue
			}
		}
		timer := time.NewTimer(*poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0
		case <-timer.C:
		}
	}
}
