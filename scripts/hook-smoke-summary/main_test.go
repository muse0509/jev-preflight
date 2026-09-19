package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

type record = map[string]any

func hookRecords(event, id, stdout string) []record {
	return []record{
		{"type": "system", "subtype": "hook_started", "hook_event": event, "hook_id": id, "hook_name": "fixture-hook"},
		{"type": "system", "subtype": "hook_response", "hook_event": event, "hook_id": id, "hook_name": "fixture-hook", "exit_code": 0, "outcome": "success", "stdout": stdout},
	}
}

func toolRecords(name, id string) []record {
	return []record{
		{"type": "assistant", "parent_tool_use_id": nil, "message": record{"content": []record{{"type": "tool_use", "name": name, "id": id, "input": record{"file_path": "check.go", "old_string": "private fixture", "new_string": "synthetic fixture"}}}}},
		{"type": "user", "parent_tool_use_id": nil, "message": record{"content": []record{{"type": "tool_result", "tool_use_id": id, "content": "private fixture output", "is_error": false}}}},
	}
}

func prefixRecords() []record {
	records := []record{{"type": "system", "subtype": "init", "plugins": []record{{"name": "jev-preflight", "path": "/private/fixture"}}}}
	records = append(records, hookRecords("UserPromptSubmit", "submit", "")...)
	records = append(records, toolRecords("Read", "read")...)
	records = append(records, toolRecords("Edit", "edit")...)
	return records
}

func successRecord() record {
	return record{"type": "result", "subtype": "success", "is_error": false, "result": "private assistant response", "session_id": "private session"}
}

func noKeyRecords() []record {
	records := prefixRecords()
	records = append(records, hookRecords("Stop", "stop", `{"systemMessage":"jev-preflight: skipped (api_key); Claude may finish."}`)...)
	return append(records, successRecord())
}

func lines(t *testing.T, records []record) string {
	t.Helper()
	var out bytes.Buffer
	for _, r := range records {
		if json.NewEncoder(&out).Encode(r) != nil {
			t.Fatal("fixture encoding failed")
		}
	}
	return out.String()
}

func checkRun(t *testing.T, input string, keyMode bool, want int, marker string) string {
	t.Helper()
	var out bytes.Buffer
	if got := run(strings.NewReader(input), &out, keyMode); got != want || !strings.Contains(out.String(), marker) {
		t.Fatal("incorrect summary status")
	}
	for _, private := range []string{"private", "fixture-hook", "synthetic fixture", "check.go", "secret-marker"} {
		if strings.Contains(out.String(), private) {
			t.Fatal("summary exposed input content")
		}
	}
	if !strings.Contains(out.String(), "jev_requests=UNOBSERVED baseline=UNOBSERVED git_cleanup=UNOBSERVED stop_hook_active=UNOBSERVED hook_attribution=UNOBSERVED") {
		t.Fatal("summary overstated observation scope")
	}
	return out.String()
}

func TestNoKeyLifecycleEvidence(t *testing.T) {
	input := lines(t, noKeyRecords())
	out := checkRun(t, input, false, 0, "no_key_fail_open=PASS")
	for _, expected := range []string{"plugin_loaded=true", "read_calls=1 read_successes=1 edit_calls=1 edit_successes=1", "user_prompt_submit_started=1 user_prompt_submit_completed=1 stop_started=1 stop_completed=1 continuation_outputs=0 api_key_notices=1", "claude_result_success=true"} {
		if !strings.Contains(out, expected) {
			t.Fatal("required observation missing")
		}
	}
}

func TestMissingEvidenceNeverPasses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func([]record) []record
	}{
		{"empty", func([]record) []record { return nil }},
		{"only result", func([]record) []record { return []record{successRecord()} }},
		{"plugin absent", func(r []record) []record { r[0]["plugins"] = []record{}; return r }},
		{"unrelated plugin", func(r []record) []record { r[0]["plugins"] = []record{{"name": "other"}}; return r }},
		{"extra plugin", func(r []record) []record {
			r[0]["plugins"] = []record{{"name": "jev-preflight"}, {"name": "other"}}
			return r
		}},
		{"extra init", func(r []record) []record { return append([]record{r[0]}, r...) }},
		{"missing result", func(r []record) []record { return r[:len(r)-1] }},
		{"missing stdout", func(r []record) []record { delete(r[len(r)-2], "stdout"); return r }},
		{"stderr notice only", func(r []record) []record {
			r[len(r)-2]["stderr"] = r[len(r)-2]["stdout"]
			r[len(r)-2]["stdout"] = ""
			return r
		}},
		{"combined output only", func(r []record) []record {
			r[len(r)-2]["output"] = r[len(r)-2]["stdout"]
			r[len(r)-2]["stdout"] = ""
			return r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if run(strings.NewReader(lines(t, tc.modify(noKeyRecords()))), &out, false) == 0 || strings.Contains(out.String(), "=PASS") {
				t.Fatal("incomplete evidence passed")
			}
		})
	}
	checkRun(t, "", false, 1, "no_key_fail_open=SKIP")
}

func TestHookCorrelationAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func([]record)
	}{
		{"id mismatch", func(r []record) { r[len(r)-2]["hook_id"] = "other" }},
		{"name mismatch", func(r []record) { r[len(r)-2]["hook_name"] = "other" }},
		{"event mismatch", func(r []record) { r[len(r)-2]["hook_event"] = "UserPromptSubmit" }},
		{"missing exit", func(r []record) { delete(r[len(r)-2], "exit_code") }},
		{"hook error", func(r []record) { r[len(r)-2]["outcome"] = "error" }},
		{"nonzero exit", func(r []record) { r[len(r)-2]["exit_code"] = 1 }},
		{"result error", func(r []record) { r[len(r)-1]["is_error"] = true }},
		{"plugin error", func(r []record) { r[0]["plugin_errors"] = []record{{"message": "secret-marker"}} }},
		{"tool result error", func(r []record) {
			r[6]["message"] = record{"content": []record{{"type": "tool_result", "tool_use_id": "edit", "is_error": true, "content": "secret-marker"}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := noKeyRecords()
			tc.modify(r)
			var out bytes.Buffer
			if run(strings.NewReader(lines(t, r)), &out, false) == 0 || strings.Contains(out.String(), "=PASS") || strings.Contains(out.String(), "secret-marker") {
				t.Fatal("invalid evidence accepted or exposed")
			}
		})
	}
}

func TestContinuationRequiresFollowingStop(t *testing.T) {
	feedback := `{"hookSpecificOutput":{"hookEventName":"Stop","additionalContext":"Investigate these changed-code risk axes once:\nprivate synthetic feedback. Change code only when you find evidence; otherwise state that no supporting evidence was found and finish."}}`
	for _, tc := range []struct {
		name    string
		outputs []string
		marker  string
		code    int
	}{
		{"none", []string{""}, "continuation_check=NOT_EXERCISED", 1},
		{"one without followup", []string{feedback}, "continuation_check=SKIP", 1},
		{"one with followup", []string{feedback, ""}, "continuation_check=PASS", 0},
		{"followup before continuation", []string{"", feedback}, "continuation_check=SKIP", 1},
		{"two continuations", []string{feedback, feedback, ""}, "continuation_check=FAIL", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := prefixRecords()
			for i, output := range tc.outputs {
				r = append(r, hookRecords("Stop", strings.Repeat("s", i+1), output)...)
			}
			r = append(r, successRecord())
			checkRun(t, lines(t, r), true, tc.code, tc.marker)
		})
	}
	r := prefixRecords()
	r = append(r, hookRecords("Stop", "one", feedback)...)
	r = append(r, successRecord())
	checkRun(t, lines(t, r), false, 1, "no_key_fail_open=FAIL")
	// Completion after feedback is insufficient when this Stop started earlier.
	r = prefixRecords()
	first, second := hookRecords("Stop", "first", feedback), hookRecords("Stop", "second", "")
	r = append(r, first[0], second[0], first[1], second[1], successRecord())
	checkRun(t, lines(t, r), true, 1, "continuation_check=SKIP")
	// Unrelated context must not count as Jev feedback.
	r = prefixRecords()
	r = append(r, hookRecords("Stop", "first", `{"hookSpecificOutput":{"hookEventName":"Stop","additionalContext":"private unrelated feedback"}}`)...)
	r = append(r, hookRecords("Stop", "second", "")...)
	r = append(r, successRecord())
	checkRun(t, lines(t, r), true, 1, "continuation_check=SKIP")
}

func TestMalformedAndSensitiveInputIsDiscarded(t *testing.T) {
	for _, input := range []string{
		"secret-marker", `null`, `[]`, `{}`, `{"type":123}`, `{"type":"result","subtype":"success"}`,
		`{"type":"result","type":"system"}`, `{"type":"system","nested":{"private":1,"private":2}}`,
		`{"type":"system"} {"type":"system"}`, "\n", strings.Repeat("[", 66) + strings.Repeat("]", 66),
		lines(t, noKeyRecords()) + "secret-marker",
	} {
		checkRun(t, input, false, 1, "stream=FAIL")
	}
	r := noKeyRecords()
	r[len(r)-2]["stdout"] = `{"systemMessage":"secret-marker","systemMessage":"jev-preflight: skipped (api_key); Claude may finish."}`
	checkRun(t, lines(t, r), false, 1, "stream=FAIL")
	r = noKeyRecords()
	r[3]["parent_tool_use_id"] = "private subagent"
	r[4]["parent_tool_use_id"] = "private subagent"
	checkRun(t, lines(t, r), false, 1, "no_key_fail_open=SKIP")
}

func TestExactInputLimits(t *testing.T) {
	line := `{"type":"system","padding":"` + strings.Repeat("x", 64) + `"}`
	if _, err := readSummary(strings.NewReader(line), len(line), len(line)); err != nil {
		t.Fatal("exact input bound rejected")
	}
	if _, err := readSummary(strings.NewReader(line+" "), len(line), len(line)+1); !errors.Is(err, errLimit) {
		t.Fatal("line overflow accepted")
	}
	input := line + "\n" + line + "\n"
	if _, err := readSummary(strings.NewReader(input), len(line), len(input)); err != nil {
		t.Fatal("exact total bound rejected")
	}
	if _, err := readSummary(strings.NewReader(input+" "), len(line), len(input)); !errors.Is(err, errLimit) {
		t.Fatal("total overflow accepted")
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("secret-marker") }

func TestReadErrorIsSanitized(t *testing.T) {
	var out bytes.Buffer
	if run(io.MultiReader(strings.NewReader(lines(t, noKeyRecords())), brokenReader{}), &out, false) == 0 || strings.Contains(out.String(), "secret-marker") || strings.Contains(out.String(), "=PASS") {
		t.Fatal("read failure leaked or accepted partial evidence")
	}
}
