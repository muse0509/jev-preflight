package hook

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muse0509/jev-preflight/internal/gitstate"
)

func TestRawDiffLimitSkipsWholeTurnAndCleansUp(t *testing.T) {
	h := newHarness(t, 200, true)
	if out := h.run("UserPromptSubmit", "raw-limit", false, false); out != (Output{}) {
		t.Fatal("baseline setup failed")
	}
	marker := "private-raw-diff-" + strings.Repeat("q", 24)
	filename := "private-filename-" + strings.Repeat("q", 12) + ".go"
	// Redaction would shrink this below maxDiffBytes. The raw limit must still
	// reject it first, rather than reading and then sending the canonical result.
	h.repo.Write(filename, "package sample\nvar token = \""+marker+strings.Repeat("x", gitstate.RawOutputLimit)+"\"\n")
	out := h.run("Stop", "raw-limit", false, false)
	if h.calls.Load() != 0 || out.Specific != nil {
		t.Fatal("oversized raw diff was sent or continued")
	}
	if out.SystemMessage != "jev-preflight: skipped: diff too large; Claude may finish." {
		t.Fatal("raw limit did not use the existing too-large notice")
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{marker, filename, "package sample", "PRIVATE_PROMPT_MARKER", "PRIVATE_ASSISTANT_MARKER"} {
		if strings.Contains(string(encoded), forbidden) || strings.Contains(h.stderr.String(), forbidden) || strings.Contains(h.request, forbidden) {
			t.Fatal("raw-limit failure exposed private data")
		}
	}
	h.assertClean("raw-limit")
	if err := filepath.WalkDir(h.scratch, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), marker) || strings.Contains(string(body), filename) || strings.Contains(string(body), "package sample") {
			t.Error("durable state contains raw diff material")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if out := h.run("Stop", "raw-limit", false, false); out != (Output{}) || h.calls.Load() != 0 {
		t.Fatal("duplicate Stop after raw overflow did not finish quietly")
	}
	h.assertClean("raw-limit")
}

func TestNormalRawDiffStillEvaluatesOnce(t *testing.T) {
	h := newHarness(t, 200, true)
	h.run("UserPromptSubmit", "below-limit", false, false)
	h.repo.Write("bounded.go", "package sample\nvar Enabled = true\n")
	if out := h.run("Stop", "below-limit", false, false); out.Specific == nil || h.calls.Load() != 1 {
		t.Fatal("normal diff was not evaluated")
	}
	if out := h.run("Stop", "below-limit", false, false); out.Specific != nil || h.calls.Load() != 1 {
		t.Fatal("normal diff violated the one-continuation guard")
	}
	h.assertClean("below-limit")
}
