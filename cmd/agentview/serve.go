package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/shbhmrzd/agentview/internal/dashboard"
	"github.com/shbhmrzd/agentview/internal/observe"
)

func runServe(ctx context.Context, args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(errOut)
	agent := flags.String("agent", "all", "agents: all, claude, or codex")
	dir := flags.String("dir", "", "custom session root (requires --agent claude or codex)")
	listen := flags.String("listen", "127.0.0.1:7433", "loopback IP and port for the dashboard")
	content := flags.Bool("include-content", false, "show message text and expandable tool payloads; reasoning is omitted")
	poll := flags.Duration("poll", time.Second, "directory polling interval, minimum 100ms")
	maxRecord := flags.Int("max-record-bytes", observe.DefaultMaxRecordBytes, "maximum complete JSONL record size")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errOut, "unexpected positional arguments")
		return 2
	}
	if *agent != "all" && *agent != "claude" && *agent != "codex" {
		fmt.Fprintln(errOut, "serve supports all, claude, or codex")
		return 2
	}
	if *dir != "" && *agent == "all" {
		fmt.Fprintln(errOut, "--dir requires --agent claude or codex")
		return 2
	}
	if *poll < 100*time.Millisecond || *maxRecord < 1 || *maxRecord > observe.MaxRecordBytes {
		fmt.Fprintln(errOut, "invalid poll interval or maximum record size")
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	var roots []dashboard.Root
	for _, provider := range []string{"codex", "claude"} {
		if *agent != "all" && *agent != provider {
			continue
		}
		path := *dir
		if path == "" {
			base, child := os.Getenv("CODEX_HOME"), "sessions"
			if provider == "claude" {
				base, child = os.Getenv("CLAUDE_CONFIG_DIR"), "projects"
			}
			if base == "" {
				base = filepath.Join(home, "."+provider)
			}
			path = filepath.Join(base, child)
		}
		roots = append(roots, dashboard.Root{Agent: provider, Path: path})
	}
	if err := dashboard.Serve(ctx, *listen, dashboard.Options{Roots: roots, IncludeContent: *content, Poll: *poll, MaxRecordBytes: *maxRecord}, out); err != nil {
		fmt.Fprintf(errOut, "dashboard: %q\n", err.Error())
		return 1
	}
	return 0
}
