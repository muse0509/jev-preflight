package hook

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecode(t *testing.T) {
	dir := t.TempDir()
	base := map[string]any{"session_id": "session", "prompt_id": "prompt", "cwd": dir, "scratchpad_dir": dir, "hook_event_name": "Stop", "stop_hook_active": false, "unknown": map[string]any{"additive": true}, "prompt": "PRIVATE_PROMPT", "last_assistant_message": "PRIVATE_ASSISTANT", "transcript_path": "DO_NOT_READ", "session_crons": []map[string]string{{"prompt": "PRIVATE_CRON_PROMPT", "schedule": "PRIVATE_CRON_SCHEDULE", "future_field": "PRIVATE_CRON_BODY"}}}
	b, _ := json.Marshal(base)
	in, err := Decode(bytes.NewReader(b), "Stop")
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := json.Marshal(in)
	if strings.Contains(string(stored), "PRIVATE") || strings.Contains(string(stored), "DO_NOT_READ") {
		t.Fatal("content retained")
	}
	if len(in.SessionCrons) != 1 || !in.HasBackgroundWork() {
		t.Fatal("scheduled wakeup was not recognized")
	}
	for _, field := range []string{"session_id", "prompt_id", "cwd", "hook_event_name", "stop_hook_active"} {
		copy := make(map[string]any)
		for k, v := range base {
			copy[k] = v
		}
		delete(copy, field)
		b, _ := json.Marshal(copy)
		if _, err := Decode(bytes.NewReader(b), "Stop"); err == nil {
			t.Errorf("accepted missing %s", field)
		}
	}
	for _, field := range []string{"session_id", "prompt_id", "cwd", "scratchpad_dir", "hook_event_name", "stop_hook_active", "background_tasks", "session_crons"} {
		copy := make(map[string]any)
		for k, v := range base {
			copy[k] = v
		}
		copy[field] = 17
		b, _ := json.Marshal(copy)
		if _, err := Decode(bytes.NewReader(b), "Stop"); err == nil {
			t.Errorf("accepted invalid %s", field)
		}
	}
	for _, suffix := range []string{"{}", " trailing"} {
		if _, err := Decode(strings.NewReader(string(b)+suffix), "Stop"); err == nil {
			t.Fatal("trailing data accepted")
		}
	}
	base["cwd"] = filepath.Join("relative", "path")
	b, _ = json.Marshal(base)
	if _, err := Decode(bytes.NewReader(b), "Stop"); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestBackgroundAndOutput(t *testing.T) {
	if (Input{SessionCrons: []struct{}{}}).HasBackgroundWork() {
		t.Fatal("empty cron list must allow evaluation")
	}
	for _, status := range []string{"running", "pending", "future_status", ""} {
		if !(Input{BackgroundTasks: []BackgroundTask{{Status: status}}}).HasBackgroundWork() {
			t.Fatalf("not active: %s", status)
		}
	}
	if (Input{BackgroundTasks: []BackgroundTask{{Status: "completed"}}}).HasBackgroundWork() {
		t.Fatal("completed active")
	}
	var out bytes.Buffer
	if err := (Output{}).Write(&out); err != nil || out.Len() != 0 {
		t.Fatal("no-op output")
	}
	if err := (Output{Specific: &SpecificOutput{HookEventName: "Stop", AdditionalContext: "Inspect evidence."}}).Write(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "additionalContext") || strings.Contains(out.String(), "decision") {
		t.Fatal("incorrect feedback protocol")
	}
}
