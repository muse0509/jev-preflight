package diff

import (
	"errors"
	"strings"
	"testing"

	"github.com/muse0509/jev-preflight/internal/gitstate"
)

func TestPathPolicy(t *testing.T) {
	for _, name := range []string{"main.go", "package.json", "go.mod", "Cargo.toml", "requirements.txt", "requirements-dev.txt", "requirements/base.txt", "CMakeLists.txt", ".github/workflows/security.yml", "db/migrations/001.sql", "pkg/main_test.go", "docs/example.go", "assets/data.bin"} {
		if !Included(name, nil) {
			t.Errorf("excluded %q", name)
		}
	}
	for _, name := range []string{"README.md", "doc/a.rst", "picture.png", "dist/main.js", "pkg/vendor/x.go", "build/x", "node_modules/pkg/a.js", "go.sum", "Cargo.lock", "a.min.js", "types.generated.go", "a.pb.go", "../outside.go", "/absolute.go"} {
		if Included(name, nil) {
			t.Errorf("included %q", name)
		}
	}
	for _, tt := range []struct {
		pattern, file string
		want          bool
	}{
		{"secret.go", "secret.go", true}, {"secret.go", "pkg/secret.go", false},
		{"*.go", "main.go", true}, {"*.go", "pkg/main.go", false},
		{"src/**", "src/deep/main.go", true}, {"src/**", "src2/main.go", false},
		{"pkg/*/**", "pkg/one/deep/main.go", true},
	} {
		if got := Match(tt.pattern, tt.file); got != tt.want {
			t.Errorf("Match(%q,%q)=%v", tt.pattern, tt.file, got)
		}
	}
}

func TestPrepareDeterministicAndRedacted(t *testing.T) {
	secret := strings.Repeat("s", 28)
	changes := []gitstate.Change{{Path: "b.go", Patch: "+password = \"" + secret + "\"\r\n"}, {Path: "a.go", Patch: "+return true\n"}, {Path: "README.md", Patch: "ignored"}}
	one, err := Prepare(changes, nil, 65536)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Prepare([]gitstate.Change{changes[2], changes[1], changes[0]}, nil, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if one.Hash == "" || one.Hash != two.Hash || one.State.Diff != two.State.Diff {
		t.Fatal("unstable normalization")
	}
	if len(one.State.Files) != 2 || one.State.Files[0] != "a.go" || strings.Contains(one.State.Diff, secret) || strings.Contains(one.State.Diff, "\r") {
		t.Fatal("selection/redaction failed")
	}
}

func TestBinaryAndLimits(t *testing.T) {
	got, err := Prepare([]gitstate.Change{{Path: "data.bin", OldPath: "old.bin", Status: "R100", Binary: true, Patch: "binary body must not appear"}}, nil, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.State.Diff, "Binary change:") || strings.Contains(got.State.Diff, "must not appear") {
		t.Fatal("binary body exposed")
	}
	if _, err := Prepare([]gitstate.Change{{Path: "a.go", Patch: strings.Repeat("x", 200)}}, nil, 100); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("size: %v", err)
	}
	for _, changes := range [][]gitstate.Change{nil, {{Path: "README.md", Patch: "docs"}}, {{Path: "main.go", Patch: ""}}, {{Path: "new.go", OldPath: "vendor/old.go", Patch: "excluded rename"}}} {
		r, err := Prepare(changes, nil, 65536)
		if err != nil || len(r.State.Files) != 0 || r.Hash != "" {
			t.Fatal("empty diff must be no-op")
		}
	}
	// Redaction happens before the size check.
	r, err := Prepare([]gitstate.Change{{Path: "a.go", Patch: "+token=\"" + strings.Repeat("s", 2000) + "\""}}, nil, 256)
	if err != nil || r.Hash == "" {
		t.Fatalf("post-redaction size: %v", err)
	}
}
