package testrepo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsolatedFixture(t *testing.T) {
	r := New(t)
	r.Write("dir/file.go", "package example\n")
	r.Git("add", "--all")
	r.Git("commit", "--quiet", "-m", "fixture")
	if got := r.Git("status", "--porcelain"); got != "" {
		t.Fatalf("unexpected status: %q", got)
	}
	if !strings.HasPrefix(os.Getenv("HOME"), filepath.Dir(r.Dir)) || os.Getenv("GIT_CONFIG_GLOBAL") != os.DevNull {
		t.Fatal("fixture does not isolate Git configuration")
	}
}
