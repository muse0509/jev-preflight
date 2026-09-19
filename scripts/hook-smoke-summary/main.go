// Command hook-smoke-summary reduces Claude JSONL to non-content observations.
// Wire fields follow the official SDK's message_parser.py and HookEventMessage:
// https://github.com/anthropics/claude-agent-sdk-python/tree/main/src/claude_agent_sdk
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxLineBytes = 1 << 20
const maxTotalBytes = 8 << 20

var errInvalid = errors.New("invalid stream")
var errLimit = errors.New("stream limit exceeded")

type event struct {
	Type      string  `json:"type"`
	Subtype   string  `json:"subtype"`
	HookID    string  `json:"hook_id"`
	HookName  string  `json:"hook_name"`
	HookEvent string  `json:"hook_event"`
	Outcome   string  `json:"outcome"`
	ExitCode  *int    `json:"exit_code"`
	Stdout    string  `json:"stdout"`
	IsError   *bool   `json:"is_error"`
	ParentID  *string `json:"parent_tool_use_id"`
	Plugins   []struct {
		Name string `json:"name"`
	} `json:"plugins"`
	PluginErrors []json.RawMessage `json:"plugin_errors"`
	Message      *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type block struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
}

type toolCall struct {
	name string
	done bool
}

type hookCall struct {
	event    string
	name     string
	done     bool
	followup bool
}

type summary struct {
	pluginLoaded                                         bool
	inits, pluginEntries                                 int
	readCalls, readSuccess, editCalls, editSuccess       int
	upsStarted, upsCompleted, stopStarted, stopCompleted int
	continuations, keyNotices, results, failures         int
	followupStops                                        int
	jevFeedback                                          int
	feedbackHook                                         string
	resultSuccess                                        bool
	tools                                                map[string]toolCall
	hooks                                                map[string]hookCall
}

func validID(id string) bool { return id != "" && len(id) <= 1024 }

func (s *summary) consume(e event) error {
	if e.Type == "" || s.results != 0 {
		return errInvalid
	}
	switch e.Type {
	case "system":
		switch e.Subtype {
		case "init":
			s.inits++
			s.pluginEntries += len(e.Plugins)
			for _, plugin := range e.Plugins {
				if plugin.Name == "jev-preflight" || plugin.Name == "jev-preflight@inline" || plugin.Name == "jev-preflight@jev-preflight" {
					s.pluginLoaded = true
				}
			}
			s.failures += len(e.PluginErrors)
		case "hook_started", "hook_response":
			return s.hook(e)
		}
	case "assistant", "user":
		if e.Message == nil || len(e.Message.Content) == 0 {
			return errInvalid
		}
		if e.ParentID != nil {
			return nil // Subagent activity cannot satisfy a main-session check.
		}
		content := bytes.TrimSpace(e.Message.Content)
		if e.Type == "user" && len(content) > 0 && content[0] == '"' {
			return nil // Replayed prompt text is deliberately not decoded.
		}
		var blocks []block
		if json.Unmarshal(content, &blocks) != nil || blocks == nil {
			return errInvalid
		}
		for _, b := range blocks {
			if e.Type == "assistant" && b.Type == "tool_use" && (b.Name == "Read" || b.Name == "Edit") {
				if !validID(b.ID) {
					return errInvalid
				}
				if _, exists := s.tools[b.ID]; exists {
					return errInvalid
				}
				s.tools[b.ID] = toolCall{name: b.Name}
				if s.upsCompleted != 1 {
					s.failures++
				}
				if b.Name == "Read" {
					s.readCalls++
				} else {
					s.editCalls++
				}
			}
			if e.Type == "user" && b.Type == "tool_result" {
				call, found := s.tools[b.ToolUseID]
				if !found {
					continue
				}
				if call.done {
					return errInvalid
				}
				call.done = true
				s.tools[b.ToolUseID] = call
				if b.IsError {
					s.failures++
					continue
				}
				if call.name == "Read" {
					s.readSuccess++
				} else {
					s.editSuccess++
				}
			}
		}
	case "result":
		if e.IsError == nil || e.Subtype == "" || s.results != 0 {
			return errInvalid
		}
		s.results++
		if s.stopCompleted == 0 {
			s.failures++
		}
		s.resultSuccess = e.Subtype == "success" && !*e.IsError
		if !s.resultSuccess {
			s.failures++
		}
	}
	return nil
}

func (s *summary) hook(e event) error {
	if e.HookEvent != "UserPromptSubmit" && e.HookEvent != "Stop" {
		return nil
	}
	if !validID(e.HookID) || e.HookName == "" {
		return errInvalid
	}
	if e.Subtype == "hook_started" {
		if _, exists := s.hooks[e.HookID]; exists {
			return errInvalid
		}
		s.hooks[e.HookID] = hookCall{event: e.HookEvent, name: e.HookName, followup: e.HookEvent == "Stop" && s.jevFeedback == 1 && e.HookName == s.feedbackHook}
		if e.HookEvent == "Stop" {
			s.stopStarted++
		} else {
			s.upsStarted++
		}
		return nil
	}
	call, found := s.hooks[e.HookID]
	if !found || call.done || call.event != e.HookEvent || call.name != e.HookName {
		return errInvalid
	}
	call.done = true
	s.hooks[e.HookID] = call
	if e.Outcome != "success" || e.ExitCode == nil || *e.ExitCode != 0 {
		s.failures++
		return nil
	}
	if e.HookEvent == "Stop" {
		s.stopCompleted++
		if s.editSuccess == 0 {
			s.failures++
		}
		if call.followup {
			s.followupStops++
		}
	} else {
		s.upsCompleted++
	}
	if strings.TrimSpace(e.Stdout) == "" {
		return nil
	}
	// Only structured hook stdout carries protocol output. Never infer a notice
	// from arbitrary assistant text, hook progress, stderr, or combined output.
	var out struct {
		SystemMessage string `json:"systemMessage"`
		Specific      *struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if validJSON([]byte(e.Stdout)) != nil || json.Unmarshal([]byte(e.Stdout), &out) != nil {
		return errInvalid
	}
	if e.HookEvent == "Stop" {
		if out.SystemMessage == "jev-preflight: skipped (api_key); Claude may finish." {
			s.keyNotices++
		}
		if out.Specific != nil && out.Specific.Event == "Stop" && out.Specific.Context != "" {
			s.continuations++
			if strings.HasPrefix(out.Specific.Context, "Investigate these changed-code risk axes once:\n") && strings.HasSuffix(out.Specific.Context, "Change code only when you find evidence; otherwise state that no supporting evidence was found and finish.") {
				s.jevFeedback++
				s.feedbackHook = e.HookName
			}
		}
	}
	return nil
}

// Reject duplicate keys as well as malformed JSON; ambiguous evidence never passes.
func validJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return errInvalid
		}
		token, err := d.Token()
		if err != nil {
			return errInvalid
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]bool)
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return errInvalid
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errInvalid
				}
				seen[name] = true
				if value(depth+1) != nil {
					return errInvalid
				}
			}
		case '[':
			for d.More() {
				if value(depth+1) != nil {
					return errInvalid
				}
			}
		default:
			return errInvalid
		}
		_, err = d.Token()
		if err != nil {
			return errInvalid
		}
		return nil
	}
	if value(0) != nil {
		return errInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return errInvalid
	}
	return nil
}

func readSummary(r io.Reader, lineLimit, totalLimit int) (summary, error) {
	s := summary{tools: make(map[string]toolCall), hooks: make(map[string]hookCall)}
	limited := &io.LimitedReader{R: r, N: int64(totalLimit) + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), lineLimit+2)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) > lineLimit || limited.N == 0 {
			return summary{}, errLimit
		}
		if validJSON(line) != nil {
			return summary{}, errInvalid
		}
		var e event
		if json.Unmarshal(line, &e) != nil || s.consume(e) != nil {
			return summary{}, errInvalid
		}
	}
	if scanner.Err() != nil || limited.N == 0 {
		return summary{}, errLimit
	}
	return s, nil
}

func run(r io.Reader, w io.Writer, withKey bool) int {
	s, err := readSummary(r, maxLineBytes, maxTotalBytes)
	if err != nil {
		fmt.Fprintln(w, "stream=FAIL validation=UNVERIFIED")
		writeLimits(w)
		return 1
	}
	fmt.Fprintf(w, "plugin_loaded=%t read_calls=%d read_successes=%d edit_calls=%d edit_successes=%d\n", s.pluginLoaded, s.readCalls, s.readSuccess, s.editCalls, s.editSuccess)
	fmt.Fprintf(w, "user_prompt_submit_started=%d user_prompt_submit_completed=%d stop_started=%d stop_completed=%d continuation_outputs=%d api_key_notices=%d jev_feedback_outputs=%d\n", s.upsStarted, s.upsCompleted, s.stopStarted, s.stopCompleted, s.continuations, s.keyNotices, s.jevFeedback)
	fmt.Fprintf(w, "claude_result_success=%t\n", s.resultSuccess)
	common := s.failures == 0 && s.inits == 1 && s.pluginEntries == 1 && s.pluginLoaded && s.readSuccess > 0 && s.editSuccess > 0 && s.upsStarted == 1 && s.upsCompleted == 1 && s.results == 1 && s.resultSuccess
	verdict := "SKIP"
	if withKey {
		if s.continuations == 0 {
			verdict = "NOT_EXERCISED"
		}
		if common && s.continuations == 1 && s.jevFeedback == 1 && s.followupStops > 0 && s.stopCompleted >= 2 && s.stopCompleted == s.stopStarted && s.keyNotices == 0 {
			verdict = "PASS"
		}
		if s.failures > 0 || s.continuations > 1 || s.keyNotices > 0 {
			verdict = "FAIL"
		}
		fmt.Fprintf(w, "continuation_check=%s\n", verdict)
	} else {
		if common && s.continuations == 0 && s.stopStarted == 1 && s.stopCompleted == 1 && s.keyNotices == 1 {
			verdict = "PASS"
		}
		if s.failures > 0 || s.continuations > 0 {
			verdict = "FAIL"
		}
		fmt.Fprintf(w, "no_key_fail_open=%s\n", verdict)
	}
	writeLimits(w)
	if verdict == "PASS" {
		return 0
	}
	return 1
}

func writeLimits(w io.Writer) {
	fmt.Fprintln(w, "jev_requests=UNOBSERVED baseline=UNOBSERVED git_cleanup=UNOBSERVED stop_hook_active=UNOBSERVED hook_attribution=UNOBSERVED")
}

func main() {
	withKey := len(os.Args) == 2 && os.Args[1] == "--with-key"
	if len(os.Args) != 1 && !withKey {
		fmt.Fprintln(os.Stdout, "usage=FAIL")
		os.Exit(1)
	}
	os.Exit(run(os.Stdin, os.Stdout, withKey))
}
