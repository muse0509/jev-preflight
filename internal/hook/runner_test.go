package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	turnDiff "github.com/muse0509/jev-preflight/internal/diff"
	"github.com/muse0509/jev-preflight/internal/gitstate"
	"github.com/muse0509/jev-preflight/internal/jev"
	"github.com/muse0509/jev-preflight/internal/policy"
	"github.com/muse0509/jev-preflight/internal/session"
	"github.com/muse0509/jev-preflight/internal/testrepo"
)

type harness struct {
	t       *testing.T
	repo    *testrepo.Repo
	scratch string
	runner  Runner
	calls   atomic.Int32
	request string
	stderr  bytes.Buffer
}

func newHarness(t *testing.T, status int, risky bool) *harness {
	t.Helper()
	h := &harness{t: t, repo: testrepo.New(t), scratch: t.TempDir()}
	h.repo.Write("main.go", "package main\nfunc main() {}\n")
	h.repo.Git("add", "-A")
	h.repo.Git("commit", "-qm", "fixture")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.calls.Add(1)
		var body struct {
			State jev.State `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error("request decode failed")
		}
		h.request = body.State.Diff
		w.WriteHeader(status)
		if status != 200 {
			_, _ = w.Write([]byte("sensitive response must not be exposed"))
			return
		}
		p, err := policy.Load()
		if err != nil {
			t.Error(err)
			return
		}
		answers := make(map[string]any)
		for id := range p.Questions {
			score := 0.1
			if risky {
				score = 0.95
			}
			answers[id] = map[string]any{"type": "noul", "noul": score}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-latest", "answers": answers, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	}))
	t.Cleanup(server.Close)
	h.runner = Runner{Client: jev.New(server.URL, server.Client()), Getenv: func(key string) string {
		if key == "TYPESAFE_API_KEY" {
			return "test-" + strings.Repeat("x", 24)
		}
		return ""
	}, Stderr: &h.stderr}
	return h
}

func (h *harness) input(event, prompt string, active bool, background bool) *bytes.Reader {
	h.t.Helper()
	in := map[string]any{"session_id": "test-session", "prompt_id": prompt, "cwd": h.repo.Dir, "scratchpad_dir": h.scratch, "hook_event_name": event, "stop_hook_active": active, "prompt": "PRIVATE_PROMPT_MARKER", "last_assistant_message": "PRIVATE_ASSISTANT_MARKER"}
	if background {
		in["background_tasks"] = []map[string]string{{"status": "running", "command": "PRIVATE_BACKGROUND_COMMAND"}}
	}
	b, err := json.Marshal(in)
	if err != nil {
		h.t.Fatal(err)
	}
	return bytes.NewReader(b)
}

func (h *harness) run(event, prompt string, active, background bool) Output {
	h.t.Helper()
	return h.runner.Run(context.Background(), h.input(event, prompt, active, background), event)
}

func (h *harness) store(prompt string) *session.Store {
	h.t.Helper()
	r, err := gitstate.Resolve(context.Background(), h.repo.Dir)
	if err != nil {
		h.t.Fatal(err)
	}
	s, err := session.Open(session.Options{RepositoryRoot: r.Root, SessionID: "test-session", PromptID: prompt, ScratchpadDir: h.scratch})
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *harness) assertClean(prompt string) {
	h.t.Helper()
	s := h.store(prompt)
	if _, err := s.Load(); !os.IsNotExist(err) {
		h.t.Fatalf("state not cleaned: %v", err)
	}
	entries, err := os.ReadDir(s.SnapshotPath())
	if err != nil {
		h.t.Fatal(err)
	}
	if len(entries) != 0 {
		h.t.Fatal("private snapshot not cleaned")
	}
	if err := s.Cleanup(); err != nil {
		h.t.Fatal(err)
	}
}

func TestRiskyTurnContinuesExactlyOnce(t *testing.T) {
	h := newHarness(t, 200, true)
	h.repo.Write("staged.go", "package staged\n")
	h.repo.Git("add", "staged.go")
	h.repo.Write("old.go", "package preexisting\n")
	h.repo.Write("main.go", "package main\n// preexisting edit\nfunc main() {}\n")
	if out := h.run("UserPromptSubmit", "p", false, false); out != (Output{}) {
		t.Fatalf("submit: %+v", out)
	}
	h.repo.Write("new.go", "package fresh\nvar Enabled = true\n")
	out := h.run("Stop", "p", false, false)
	if out.Specific == nil || out.Specific.HookEventName != "Stop" || !strings.Contains(out.Specific.AdditionalContext, "not proof of defects") {
		t.Fatalf("missing risk feedback: %+v, %s", out, h.stderr.String())
	}
	if h.calls.Load() != 1 {
		t.Fatal("expected exactly one API call")
	}
	if strings.Contains(h.request, "preexisting") || strings.Contains(h.request, "package staged") || !strings.Contains(h.request, "package fresh") {
		t.Fatal("baseline changes leaked or turn change missing")
	}
	s := h.store("p")
	st, err := s.Load()
	if err != nil || st.ContinuationCount != 1 || st.LastDiffHash == "" {
		t.Fatalf("guard not persisted: %+v %v", st, err)
	}
	data, _ := json.Marshal(st)
	for _, forbidden := range []string{"PRIVATE", "package fresh", "test-", "sensitive response"} {
		if strings.Contains(string(data), forbidden) || strings.Contains(out.Specific.AdditionalContext, forbidden) || strings.Contains(h.stderr.String(), forbidden) {
			t.Fatal("private content escaped")
		}
	}
	// Even a changed diff and false upstream signal cannot override our count.
	h.repo.Write("new.go", "package fresh\nvar Enabled = false\n")
	if out := h.run("Stop", "p", false, false); out.Specific != nil {
		t.Fatal("second continuation")
	}
	if h.calls.Load() != 1 {
		t.Fatal("second API request")
	}
	h.assertClean("p")
	if out := h.run("Stop", "p", false, false); out.Specific != nil || h.calls.Load() != 1 {
		t.Fatal("late duplicate Stop evaluated")
	}
}

func TestNoopAndExcludedChangesNeverCallAPI(t *testing.T) {
	for _, kind := range []string{"empty", "excluded", "off"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t, 200, true)
			if kind == "off" {
				h.repo.Write(".jev-preflight.json", `{"mode":"off"}`)
			}
			h.run("UserPromptSubmit", "p", false, false)
			if kind == "excluded" {
				h.repo.Write("README.md", "documentation\n")
				h.repo.Write("dist/generated.js", "artifact\n")
				h.repo.Write("go.sum", "lock content\n")
			}
			if kind == "off" {
				h.repo.Write("new.go", "package new\n")
			}
			if out := h.run("Stop", "p", false, false); out.Specific != nil || h.calls.Load() != 0 {
				t.Fatal("no-op made request or continued")
			}
			h.assertClean("p")
		})
	}
}

func TestAllowModesAndFailuresCleanUp(t *testing.T) {
	for _, kind := range []string{"low", "report", "401", "403", "429", "500", "key", "config", "too-large", "active"} {
		t.Run(kind, func(t *testing.T) {
			status := 200
			if kind == "401" {
				status = 401
			}
			if kind == "403" {
				status = 403
			}
			if kind == "429" {
				status = 429
			}
			if kind == "500" {
				status = 500
			}
			h := newHarness(t, status, kind != "low")
			if kind == "report" {
				h.repo.Write(".jev-preflight.json", `{"mode":"report"}`)
			}
			if kind == "too-large" {
				h.repo.Write(".jev-preflight.json", `{"maxDiffBytes":10}`)
			}
			h.run("UserPromptSubmit", "p", false, false)
			h.repo.Write("new.go", "package changed\n")
			if kind == "config" {
				h.repo.Write(".jev-preflight.json", `{"unknown":true}`)
			}
			if kind == "key" {
				h.runner.Getenv = func(string) string { return "" }
			}
			out := h.run("Stop", "p", kind == "active", false)
			if out.Specific != nil {
				t.Fatal("allow path continued")
			}
			if strings.Contains(out.SystemMessage, "sensitive response") || strings.Contains(h.stderr.String(), "sensitive response") {
				t.Fatal("API body exposed")
			}
			if kind == "report" && out.SystemMessage == "" {
				t.Fatal("missing report")
			}
			if kind == "too-large" && !strings.Contains(out.SystemMessage, "skipped: diff too large") {
				t.Fatal("missing size notice")
			}
			want := int32(1)
			if kind == "active" || kind == "key" || kind == "config" || kind == "too-large" {
				want = 0
			}
			if h.calls.Load() != want {
				t.Fatalf("requests %d, want %d", h.calls.Load(), want)
			}
			h.assertClean("p")
		})
	}
}

func TestBackgroundPreservesBaselineAndDuplicateSubmit(t *testing.T) {
	h := newHarness(t, 200, true)
	h.run("UserPromptSubmit", "p", false, false)
	before, err := h.store("p").Load()
	if err != nil {
		t.Fatal(err)
	}
	h.repo.Write("new.go", "package fresh\n")
	h.run("UserPromptSubmit", "p", false, false)
	if out := h.run("Stop", "p", false, true); out != (Output{}) || h.calls.Load() != 0 {
		t.Fatal("background Stop was not quiet")
	}
	after, err := h.store("p").Load()
	if err != nil || before.BaselineTree != after.BaselineTree {
		t.Fatal("baseline changed")
	}
	if out := h.run("Stop", "p", false, false); out.Specific == nil {
		t.Fatalf("final Stop not evaluated: %+v", out)
	}
	h.run("Stop", "p", true, false)
	h.assertClean("p")
}

func TestSameHashAndSessionNotices(t *testing.T) {
	h := newHarness(t, 500, true)
	h.run("UserPromptSubmit", "p", false, false)
	h.repo.Write("new.go", "package fresh\n")
	s := h.store("p")
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := gitstate.Resolve(context.Background(), h.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	current, err := repo.Snapshot(context.Background(), s.SnapshotPath())
	if err != nil {
		t.Fatal(err)
	}
	changes, err := repo.Diff(context.Background(), s.SnapshotPath(), st.BaselineTree, current)
	if err != nil {
		t.Fatal(err)
	}
	d, err := turnDiff.Prepare(changes, nil, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEvaluation(d.Hash, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	if out := h.run("Stop", "p", false, false); out.Specific != nil || h.calls.Load() != 0 {
		t.Fatal("same hash evaluated")
	}
	h.assertClean("p")
	for i, p := range []string{"q", "r"} {
		h.run("UserPromptSubmit", p, false, false)
		h.repo.Write(p+".go", "package changed\n")
		out := h.run("Stop", p, false, false)
		if (out.SystemMessage != "") != (i == 0) {
			t.Fatal("notices not deduplicated by session/class")
		}
		h.assertClean(p)
	}
}

func TestConcurrentStopsUseSingleEvaluation(t *testing.T) {
	h := newHarness(t, 200, true)
	h.runner.Stderr = nil
	h.run("UserPromptSubmit", "p", false, false)
	h.repo.Write("new.go", "package fresh\n")
	var wg sync.WaitGroup
	var continued atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if h.run("Stop", "p", false, false).Specific != nil {
				continued.Add(1)
			}
		}()
	}
	wg.Wait()
	if continued.Load() != 1 || h.calls.Load() != 1 {
		t.Fatalf("continuations=%d requests=%d", continued.Load(), h.calls.Load())
	}
	h.run("Stop", "p", true, false)
	h.assertClean("p")
}

func TestSubmissionFailureAndMissingInputAreSafe(t *testing.T) {
	h := newHarness(t, 200, true)
	h.repo.Write(".jev-preflight.json", `{"bad":"PRIVATE_CONFIG_VALUE"}`)
	out := h.run("UserPromptSubmit", "p", false, false)
	if out.Specific != nil || out.SystemMessage == "" {
		t.Fatal("config must fail open with notice")
	}
	h.assertClean("p")
	if err := os.Remove(filepath.Join(h.repo.Dir, ".jev-preflight.json")); err != nil {
		t.Fatal(err)
	}
	if out := h.runner.Run(context.Background(), strings.NewReader(`{"prompt":"PRIVATE_PROMPT"}`), "Stop"); out.Specific != nil {
		t.Fatal("invalid input continued")
	}
	if h.calls.Load() != 0 || strings.Contains(h.stderr.String(), "PRIVATE") {
		t.Fatal("unsafe failure")
	}
}
