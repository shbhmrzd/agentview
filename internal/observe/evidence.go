package observe

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Outcome describes reported tool/process evidence, never task correctness.
type Outcome struct {
	Status   string `json:"status"`
	Basis    string `json:"basis"`
	ExitCode *int   `json:"exit_code,omitempty"`
}
type Communication struct {
	Author      string `json:"author,omitempty"`
	Recipient   string `json:"recipient,omitempty"`
	TriggerTurn *bool  `json:"trigger_turn,omitempty"`
	Evidence    string `json:"evidence"`
}
type ReportedStats struct {
	CostUSD             *float64 `json:"estimated_cost_usd,omitempty"`
	APIMS               *int64   `json:"api_duration_ms,omitempty"`
	APIWithoutRetriesMS *int64   `json:"api_without_retries_ms,omitempty"`
	ToolMS              *int64   `json:"tool_duration_ms,omitempty"`
}

func flagOutcome(flag *bool) *Outcome {
	if flag == nil {
		return &Outcome{Status: "unknown", Basis: "No supported outcome evidence"}
	}
	status := "success"
	if *flag {
		status = "failure"
	}
	return &Outcome{Status: status, Basis: "native_is_error"}
}

var exitHeader = regexp.MustCompile(`(?m)^(?:Process exited with code |Exit code: ?|Exit code )(-?[0-9]+)\r?$`)

// Explicit IDs returned by a spawn operation establish identity, not completion.
func spawnedIDs(tool string, raw json.RawMessage) []string {
	name := tool[strings.LastIndex(tool, ".")+1:]
	if name != "spawn_agent" && name != "Agent" && name != "Task" {
		return nil
	}
	var object struct {
		ID      string `json:"agent_id"`
		CamelID string `json:"agentId"`
	}
	if json.Unmarshal([]byte(rawText(raw)), &object) != nil {
		return nil
	}
	if object.ID != "" {
		return []string{object.ID}
	}
	if object.CamelID != "" {
		return []string{object.CamelID}
	}
	return nil
}

// Task paths are scoped names, not global agent IDs. Consumers must match an
// exact path among the spawning parent's recorded children, never globally.
func spawnedPaths(tool string, raw json.RawMessage) []string {
	if tool[strings.LastIndex(tool, ".")+1:] != "spawn_agent" {
		return nil
	}
	var object struct {
		TaskName string `json:"task_name"`
	}
	if json.Unmarshal([]byte(rawText(raw)), &object) == nil && strings.HasPrefix(object.TaskName, "/") {
		return []string{object.TaskName}
	}
	return nil
}

// Execution outcomes describe the named tool's native envelope. In particular,
// exec's printed values belong to nested calls and are not the script outcome.
func executionOutcome(tool string, raw json.RawMessage) *Outcome {
	name := tool[strings.LastIndex(tool, ".")+1:]
	// Only the execution-cell wait shares the exec script envelope.
	// Agent-coordination wait tools do not report a script outcome.
	if tool == "wait" || tool == "functions.wait" {
		name = "exec"
	}
	if name != "exec_command" && name != "write_stdin" && name != "exec" && name != "apply_patch" {
		return flagOutcome(nil)
	}
	var inspect func(json.RawMessage, int) *Outcome
	inspect = func(data json.RawMessage, depth int) *Outcome {
		if depth > 4 {
			return flagOutcome(nil)
		}
		var text string
		if json.Unmarshal(data, &text) == nil {
			text = strings.TrimSpace(text)
			if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
				return inspect(json.RawMessage(text), depth+1)
			}
			if name == "exec" {
				banner, _, _ := strings.Cut(text, "\n")
				switch strings.TrimSpace(banner) {
				case "Script completed":
					return &Outcome{Status: "success", Basis: "native_script_completed; nested calls may have different outcomes"}
				case "Script failed":
					return &Outcome{Status: "failure", Basis: "native_script_failed; not overall task outcome"}
				}
				return flagOutcome(nil)
			}
			if !(strings.HasPrefix(text, "Chunk ID:") || strings.HasPrefix(text, "Wall time:") || strings.HasPrefix(text, "Process exited with code ") || strings.HasPrefix(text, "Exit code:") || strings.HasPrefix(text, "Exit code ")) {
				return flagOutcome(nil)
			}
			for _, delimiter := range []string{"\nOutput:", "\nFinal output:"} {
				if i := strings.Index(text, delimiter); i >= 0 {
					text = text[:i]
				}
			}
			if match := exitHeader.FindStringSubmatch(text); len(match) > 0 {
				if code, err := strconv.Atoi(match[1]); err == nil {
					return processOutcome(code)
				}
			}
			return flagOutcome(nil)
		}
		var array []json.RawMessage
		if json.Unmarshal(data, &array) == nil {
			// The first content block is the native envelope. Later blocks may be
			// arbitrary text printed by an exec script, including nested JSON.
			if len(array) > 0 {
				return inspect(array[0], depth+1)
			}
			return flagOutcome(nil)
		}
		var object struct {
			ExitCode *int `json:"exit_code"`
			Metadata struct {
				ExitCode *int `json:"exit_code"`
			} `json:"metadata"`
			Content json.RawMessage `json:"content"`
			Text    string          `json:"text"`
			Type    string          `json:"type"`
		}
		if json.Unmarshal(data, &object) != nil {
			return flagOutcome(nil)
		}
		if name != "exec" {
			if object.ExitCode != nil {
				return processOutcome(*object.ExitCode)
			}
			if name == "apply_patch" && object.Metadata.ExitCode != nil {
				return processOutcome(*object.Metadata.ExitCode)
			}
		}
		if len(object.Content) > 0 {
			return inspect(object.Content, depth+1)
		}
		if object.Type == "text" || object.Type == "input_text" {
			b, _ := json.Marshal(object.Text)
			return inspect(b, depth+1)
		}
		return flagOutcome(nil)
	}
	return inspect(raw, 0)
}

func processOutcome(code int) *Outcome {
	status := "success"
	if code != 0 {
		status = "failure"
	}
	return &Outcome{Status: status, Basis: "recorded_process_exit; not overall task outcome", ExitCode: &code}
}
