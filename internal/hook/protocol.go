// Package hook implements the Claude Code hook protocol and turn workflow.
package hook

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
)

type Input struct {
	SessionID       string           `json:"session_id"`
	PromptID        string           `json:"prompt_id"`
	CWD             string           `json:"cwd"`
	ScratchpadDir   string           `json:"scratchpad_dir"`
	HookEventName   string           `json:"hook_event_name"`
	StopHookActive  *bool            `json:"stop_hook_active"`
	BackgroundTasks []BackgroundTask `json:"background_tasks"`
	SessionCrons    []struct{}       `json:"session_crons"`
}

type BackgroundTask struct {
	Status string `json:"status"`
}

type Output struct {
	SystemMessage string          `json:"systemMessage,omitempty"`
	Specific      *SpecificOutput `json:"hookSpecificOutput,omitempty"`
}

type SpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// Decode intentionally has no prompt, transcript, assistant message, or cron body fields.
func Decode(r io.Reader, event string) (Input, error) {
	var in Input
	decoder := json.NewDecoder(io.LimitReader(r, (4<<20)+1))
	if err := decoder.Decode(&in); err != nil || decoder.InputOffset() > 4<<20 {
		return Input{}, errors.New("invalid hook input")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Input{}, errors.New("invalid hook input")
	}
	validID := func(s string) bool {
		return strings.TrimSpace(s) != "" && len(s) <= 512 && !strings.ContainsAny(s, "\x00\r\n")
	}
	if !validID(in.SessionID) || !validID(in.PromptID) || !filepath.IsAbs(in.CWD) || strings.ContainsRune(in.CWD, 0) || in.HookEventName != event || (event != "Stop" && event != "UserPromptSubmit") {
		return Input{}, errors.New("invalid hook fields")
	}
	if in.ScratchpadDir != "" && (!filepath.IsAbs(in.ScratchpadDir) || strings.ContainsRune(in.ScratchpadDir, 0)) {
		return Input{}, errors.New("invalid scratchpad path")
	}
	if event == "Stop" && in.StopHookActive == nil {
		return Input{}, errors.New("missing stop loop signal")
	}
	return in, nil
}

func (in Input) HasBackgroundWork() bool {
	if len(in.SessionCrons) != 0 {
		return true
	}
	for _, task := range in.BackgroundTasks {
		switch task.Status {
		case "completed", "failed", "killed", "cancelled", "stopped":
		default:
			return true
		}
	}
	return false
}

func (out Output) Write(w io.Writer) error {
	if out.SystemMessage == "" && out.Specific == nil {
		return nil
	}
	return json.NewEncoder(w).Encode(out)
}
