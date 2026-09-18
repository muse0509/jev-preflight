package hook

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/muse0509/jev-preflight/internal/jev"
	"github.com/muse0509/jev-preflight/internal/policy"
)

func completeHookResponse(t *testing.T) string {
	t.Helper()
	p, err := policy.Load()
	if err != nil {
		t.Fatal(err)
	}
	type answer struct {
		Type string  `json:"type"`
		Noul float64 `json:"noul"`
	}
	answers := make(map[string]answer, len(p.Questions))
	for id := range p.Questions {
		answers[id] = answer{Type: "noul", Noul: 0.95}
	}
	b, err := json.Marshal(struct {
		Answers map[string]answer `json:"answers"`
	}{Answers: answers})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAPIValidationFailuresAllowAndClean(t *testing.T) {
	valid := completeHookResponse(t)
	private := "private-body-" + strings.Repeat("z", 24)
	for _, tt := range []struct{ name, body string }{
		{"malformed", private},
		{"missing", `{"answers":{}}`},
		{"partial", `{"answers":{"auth_boundary":{"type":"noul","noul":0.95}}}`},
		{"wrong_type", strings.Replace(valid, `"type":"noul"`, `"type":"text"`, 1)},
		{"out_of_range", strings.Replace(valid, `0.95`, `1.2`, 1)},
		{"nan_equivalent", strings.Replace(valid, `0.95`, `"NaN"`, 1)},
		{"oversized", strings.Repeat(" ", (64<<10)+1)},
		{"trailing", valid + `{}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, http.StatusOK, true)
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				_, _ = io.WriteString(w, tt.body)
			}))
			t.Cleanup(srv.Close)
			h.runner.Client = jev.New(srv.URL, srv.Client())
			if out := h.run("UserPromptSubmit", "p", false, false); out != (Output{}) {
				t.Fatalf("submit failed: %+v", out)
			}
			h.repo.Write("new.go", "package changed\nconst Value = \""+private+"\"\n")
			out := h.run("Stop", "p", false, false)
			if out.Specific != nil || out.SystemMessage == "" || calls.Load() != 1 {
				t.Fatalf("failure did not allow with one notice: %+v; requests=%d", out, calls.Load())
			}
			assertHookFailurePrivacy(t, h, out, private)
			h.assertClean("p")
			if again := h.run("Stop", "p", false, false); again.Specific != nil || calls.Load() != 1 {
				t.Fatal("failure was retried by a duplicate Stop")
			}
		})
	}
}

func TestAPITimeoutAndNetworkFailureAllowAndClean(t *testing.T) {
	for _, kind := range []string{"timeout", "network"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t, http.StatusOK, true)
			h.repo.Write(".jev-preflight.json", `{"timeoutMs":100}`)
			var calls atomic.Int32
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				<-release
			}))
			defer func() { close(release); srv.Close() }()
			h.runner.Client = jev.New(srv.URL, srv.Client())
			if kind == "network" {
				srv.Close()
			}
			h.run("UserPromptSubmit", "p", false, false)
			h.repo.Write("new.go", "package changed\n")
			out := h.run("Stop", "p", false, false)
			if out.Specific != nil || out.SystemMessage == "" {
				t.Fatalf("failure did not allow with notice: %+v", out)
			}
			want := int32(1)
			if kind == "network" {
				want = 0
			}
			if calls.Load() != want {
				t.Fatalf("requests=%d, want=%d", calls.Load(), want)
			}
			h.assertClean("p")
			assertHookFailurePrivacy(t, h, out, srv.URL)
		})
	}
}

func assertHookFailurePrivacy(t *testing.T, h *harness, out Output, private string) {
	t.Helper()
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	contents := string(b) + h.stderr.String()
	if err := filepath.WalkDir(h.scratch, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			contents += string(b)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{private, "PRIVATE_PROMPT_MARKER", "PRIVATE_ASSISTANT_MARKER", "test-" + strings.Repeat("x", 24), "package changed"} {
		if strings.Contains(contents, forbidden) {
			t.Fatal("failure output or durable metadata exposed private content")
		}
	}
}

func TestHookTransmitsSelectedRedactedTurnOnly(t *testing.T) {
	h := newHarness(t, http.StatusOK, false)
	h.repo.Write("preexisting.go", "package preexisting\n")
	h.run("UserPromptSubmit", "p", false, false)
	secret := "fixture-" + strings.Repeat("q", 32)
	h.repo.Write("new.go", "package changed\nvar password = \""+secret+"\"\n")
	h.repo.Write("README.md", "PRIVATE_DOCUMENT_MARKER\n")
	if out := h.run("Stop", "p", false, false); out.Specific != nil || out.SystemMessage != "" || h.calls.Load() != 1 {
		t.Fatalf("normal evaluation failed: %+v", out)
	}
	for _, forbidden := range []string{secret, "package preexisting", "PRIVATE_DOCUMENT_MARKER", "PRIVATE_PROMPT_MARKER", "PRIVATE_ASSISTANT_MARKER"} {
		if strings.Contains(h.request, forbidden) {
			t.Fatal("request exposed excluded content")
		}
	}
	if !strings.Contains(h.request, "[REDACTED:assignment]") || !strings.Contains(h.request, "package changed") {
		t.Fatal("request lost selected change or redaction marker")
	}
	h.assertClean("p")
}
