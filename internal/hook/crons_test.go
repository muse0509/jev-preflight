package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionCronsPreserveBaselineWithoutRetainingBodies(t *testing.T) {
	h := newHarness(t, 200, true)
	if out := h.run("UserPromptSubmit", "p", false, false); out != (Output{}) {
		t.Fatal("baseline submission failed")
	}
	store := h.store("p")
	before, err := os.ReadFile(filepath.Join(store.Dir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	h.repo.Write("new.go", "package fresh\nvar Changed = true\n")
	const marker = "PRIVATE_CRON_PROMPT_AND_SCHEDULE"
	cronStop := func(active, waiting bool) Output {
		t.Helper()
		var input map[string]any
		if err := json.NewDecoder(h.input("Stop", "p", active, false)).Decode(&input); err != nil {
			t.Fatal(err)
		}
		input["session_crons"] = []map[string]any{}
		if waiting {
			input["session_crons"] = []map[string]any{{"id": "cron-1", "prompt": marker, "schedule": marker, "recurring": true, "future_body": marker}}
		}
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		return h.runner.Run(context.Background(), bytes.NewReader(body), "Stop")
	}
	assertPrivate := func(out Output) {
		t.Helper()
		encoded, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{string(encoded), h.stderr.String(), h.request} {
			if strings.Contains(value, marker) {
				t.Fatal("cron body escaped into output or request")
			}
		}
		if err := filepath.WalkDir(h.scratch, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || filepath.Ext(path) != ".json" {
				return err
			}
			body, err := os.ReadFile(path)
			if err == nil && bytes.Contains(body, []byte(marker)) {
				t.Fatal("cron body persisted")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, active := range []bool{false, true} {
		out := cronStop(active, true)
		if out != (Output{}) || h.calls.Load() != 0 || h.stderr.Len() != 0 {
			t.Fatal("cron wait must be quiet and make zero API requests")
		}
		assertPrivate(out)
		after, err := os.ReadFile(filepath.Join(store.Dir(), "state.json"))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("cron wait changed baseline or state")
		}
	}
	out := cronStop(false, false)
	if out.Specific == nil || h.calls.Load() != 1 || !strings.Contains(h.request, "var Changed = true") {
		t.Fatal("clearing crons must evaluate the pending diff once")
	}
	assertPrivate(out)
	state, err := store.Load()
	if err != nil || state.ContinuationCount != 1 {
		t.Fatal("continuation guard was not saved")
	}
	// Waiting again cannot reset the count, even if the diff changes afterwards.
	if out := cronStop(false, true); out != (Output{}) {
		t.Fatal("cron wait after continuation was not quiet")
	}
	h.repo.Write("new.go", "package fresh\nvar Changed = false\n")
	out = cronStop(false, false)
	if out != (Output{}) || h.calls.Load() != 1 {
		t.Fatal("cron wait reset the one-continuation guard")
	}
	assertPrivate(out)
	if _, err := os.Stat(store.Dir()); !os.IsNotExist(err) {
		t.Fatal("final Stop did not clean its private data")
	}
}

func TestClearedCronsRespectStopHookActive(t *testing.T) {
	h := newHarness(t, 200, true)
	h.run("UserPromptSubmit", "p", false, false)
	h.repo.Write("new.go", "package changed\n")
	var input map[string]any
	if err := json.NewDecoder(h.input("Stop", "p", true, false)).Decode(&input); err != nil {
		t.Fatal(err)
	}
	input["session_crons"] = []struct{}{}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	out := h.runner.Run(context.Background(), bytes.NewReader(body), "Stop")
	if out != (Output{}) || h.calls.Load() != 0 {
		t.Fatal("active Stop hook must not evaluate or continue")
	}
	h.assertClean("p")
}
