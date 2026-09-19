// Package testrepo creates isolated Git fixtures under a test's temporary directory.
package testrepo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a disposable repository. New changes process environment with t.Setenv,
// so callers must not run their test with t.Parallel.
type Repo struct {
	Dir string
	t   testing.TB
}

// New shields fixture commands and production code from the user's Git settings.
func New(t testing.TB) *Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is unavailable")
	}
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			t.Setenv(key, "")
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_OPTIONAL_LOCKS", "0")
	r := &Repo{Dir: filepath.Join(base, "repo"), t: t}
	if err := os.Mkdir(r.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	r.Git("init", "--quiet", "--initial-branch=main", "--template=")
	r.Git("config", "user.name", "jev-preflight test")
	r.Git("config", "user.email", "test@example.invalid")
	r.Git("config", "core.autocrlf", "false")
	r.Git("config", "commit.gpgSign", "false")
	return r
}

// Git runs an explicit-argument fixture command with optional writes disabled.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.Dir}, args...)...)
	// Empty inherited GIT_* variables can still have special meanings to Git.
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "GIT_") && value == "" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("fixture Git %s failed: %v: %s", args[0], err, out)
	}
	return string(out)
}

// Write creates a regular file inside the fixture working tree.
func (r *Repo) Write(path, content string) {
	r.t.Helper()
	full := filepath.Join(r.Dir, filepath.FromSlash(path))
	rel, err := filepath.Rel(r.Dir, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		r.t.Fatal("fixture path escapes repository")
	}
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0600); err != nil {
		r.t.Fatal(err)
	}
}
