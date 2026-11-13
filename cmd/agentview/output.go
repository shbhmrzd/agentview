package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shbhmrzd/agentview/internal/observe"
)

// JSON remains the default for pipes/files. Interactive output is a bounded,
// human-readable projection; it never writes raw transcript terminal controls.
func outputFormat(out io.Writer, requested string) (string, error) {
	if requested != "auto" && requested != "text" && requested != "jsonl" {
		return "", fmt.Errorf("format must be auto, text, or jsonl")
	}
	if requested != "auto" {
		return requested, nil
	}
	if file, ok := out.(*os.File); ok {
		if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return "text", nil
		}
	}
	return "jsonl", nil
}

type eventOutput struct {
	out              io.Writer
	encoder          *json.Encoder
	mode, history    string
	pending          map[string]bool
	scanned, summary bool
	oversized        int
	unsupported      map[string]bool
}

func newEventOutput(out io.Writer, format, mode, history string) *eventOutput {
	w := &eventOutput{out: out, mode: mode, history: history, pending: make(map[string]bool), unsupported: make(map[string]bool)}
	if format == "jsonl" {
		w.encoder = json.NewEncoder(out)
	}
	return w
}

func (w *eventOutput) write(e observe.Event) error {
	if w.encoder != nil {
		return w.encoder.Encode(e)
	}
	switch e.Kind {
	case "source.discovered":
		if !w.scanned {
			w.pending[e.Source.Path] = true
			return nil
		}
		return w.line("Discovered %s session file %s", e.Agent, displayText(e.Source.Path, 220))
	case "directory.ready":
		w.scanned = true
		action := "Watching"
		if w.mode == "inspect" {
			action = "Inspecting"
		}
		if err := w.line("%s %s directory %s (%d existing files).", action, e.Agent, displayText(e.Source.Path, 220), len(w.pending)); err != nil {
			return err
		}
		return w.initialized()
	case "source.ready":
		if w.pending[e.Source.Path] {
			delete(w.pending, e.Source.Path)
			return w.initialized()
		}
		return w.line("Following %s/%s", e.Agent, displayIdentity(e.SessionID))
	case "record.ignored":
		return nil
	case "record.oversized":
		if e.ObservationPhase == "history" && w.history == "none" && !w.summary {
			w.oversized++
			return nil
		}
	case "record.unsupported":
		if w.unsupported[e.NativeType] {
			return nil
		}
		if len(w.unsupported) >= 32 {
			if !w.unsupported[""] {
				w.unsupported[""] = true
				return w.line("Further unsupported event types hidden; use --format jsonl to inspect all diagnostics.")
			}
			return nil
		}
		w.unsupported[e.NativeType] = true
		return w.line("Notice: unsupported %s event type %s; repeats hidden. Use --format jsonl for details.", e.Agent, displayText(e.NativeType, 160))
	case "source.unavailable":
		delete(w.pending, e.Source.Path)
		if err := w.line("Warning: source unavailable %s", displayText(e.Source.Path, 220)); err != nil {
			return err
		}
		return w.initialized()
	}
	stamp := ""
	if observed, err := time.Parse(time.RFC3339Nano, e.ObservedAt); err == nil {
		stamp = observed.Local().Format("15:04:05") + " "
	}
	label := e.Kind
	if e.ObservationPhase != "" && e.ObservationPhase != "update" {
		label += " [" + e.ObservationPhase + "]"
	}
	detail := e.Detail
	if e.ContentCaptured {
		detail = e.Content
	}
	if e.ToolName != "" {
		detail = e.ToolName + " " + detail
	}
	if detail == "" {
		detail = "(metadata only; use --include-content for messages and tool payloads)"
	}
	return w.line("%s%s/%s %s %s", stamp, e.Agent, displayIdentity(e.SessionID), label, displayText(detail, 500))
}

func (w *eventOutput) initialized() error {
	if !w.scanned || len(w.pending) != 0 || w.summary {
		return nil
	}
	w.summary = true
	text := "Initial sources processed."
	if w.mode == "follow" {
		text += " Watching for new activity; checkpoints are not persisted."
	}
	if w.oversized > 0 {
		text += fmt.Sprintf(" Skipped %d oversized historical records; use --history all --format jsonl to inspect diagnostics.", w.oversized)
	}
	return w.line("%s", text)
}

func (w *eventOutput) line(format string, args ...any) error {
	_, err := fmt.Fprintf(w.out, format+"\n", args...)
	return err
}

func displayIdentity(id string) string {
	if id == "" {
		return "unknown-session"
	}
	runes := []rune(id)
	if len(runes) > 20 {
		id = string(runes[:8]) + "…" + string(runes[len(runes)-4:])
	}
	return strings.Trim(strconv.QuoteToASCII(id), "\"")
}

func displayText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return strconv.QuoteToASCII(string(runes[:limit])) + "… [truncated; --format jsonl for full output]"
	}
	return strconv.QuoteToASCII(text)
}
