package gitstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/muse0509/jev-preflight/internal/testrepo"
)

type invariant struct {
	index  []byte
	status string
	refs   string
	files  map[string]string
}

func capture(t *testing.T, fixture *testrepo.Repo, repo *Repo) invariant {
	t.Helper()
	// Capture status first: Git itself may freshen a shared index while reading it.
	status := fixture.Git("-c", "core.fsmonitor=false", "status", "--porcelain=v2", "-z", "--untracked-files=all")
	index, err := os.ReadFile(repo.Index)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return invariant{index, status, fixture.Git("for-each-ref", "--format=%(refname) %(objectname)"), inventory(t, repo.CommonDir)}
}

func inventory(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		files[rel] = fmt.Sprintf("%x:%s:%d", sha256.Sum256(body), info.Mode(), info.ModTime().UnixNano())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func unchanged(t *testing.T, fixture *testrepo.Repo, repo *Repo, before invariant) {
	t.Helper()
	// Compare all Git metadata before running another status command.
	if after := inventory(t, repo.CommonDir); !reflect.DeepEqual(before.files, after) {
		for name, value := range before.files {
			if after[name] != value {
				t.Errorf("real Git metadata changed: %s", name)
			}
		}
		for name := range after {
			if _, ok := before.files[name]; !ok {
				t.Errorf("unexpected real Git metadata: %s", name)
			}
		}
	}
	index, _ := os.ReadFile(repo.Index)
	if !bytes.Equal(index, before.index) {
		t.Error("real index bytes changed")
	}
	if got := fixture.Git("for-each-ref", "--format=%(refname) %(objectname)"); got != before.refs {
		t.Error("real refs changed")
	}
	if got := fixture.Git("-c", "core.fsmonitor=false", "status", "--porcelain=v2", "-z", "--untracked-files=all"); got != before.status {
		t.Error("real working-tree status changed")
	}
}

func resolve(t *testing.T, fixture *testrepo.Repo) *Repo {
	t.Helper()
	r, err := Resolve(context.Background(), fixture.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func snapshot(t *testing.T, r *Repo, dir string) string {
	t.Helper()
	id, err := r.Snapshot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func diff(t *testing.T, r *Repo, dir, base, current string) []Change {
	t.Helper()
	changes, err := r.Diff(context.Background(), dir, base, current)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

func TestTurnDiffAndRealRepositoryInvariants(t *testing.T) {
	f := testrepo.New(t)
	for _, path := range []string{"staged.go", "unstaged.go", "changed.go", "deleted.go", "old-name.go"} {
		f.Write(path, "package sample\n\n// unique file: "+path+"\nvar Before = 1\n")
	}
	f.Write(".gitignore", "ignored.go\n")
	f.Git("add", "-A")
	f.Git("commit", "--quiet", "-m", "fixture")
	f.Write("staged.go", "package sample\nvar PreexistingStaged = 2\n")
	f.Git("add", "--", "staged.go")
	f.Write("unstaged.go", "package sample\nvar PreexistingUnstaged = 3\n")
	f.Write("untracked.go", "package sample\nvar PreexistingUntracked = 4\n")
	f.Write("ignored.go", "private ignored material\n")
	r := resolve(t, f)
	private := t.TempDir()
	before := capture(t, f, r)
	baseline := snapshot(t, r, private)
	unchanged(t, f, r, before)
	if got := diff(t, r, private, baseline, snapshot(t, r, private)); len(got) != 0 {
		t.Fatal("unchanged working state produced diff")
	}
	f.Write("changed.go", "package sample\nvar NewlyChanged = 99\n")
	f.Write("added.go", "package sample\nvar NewFile = 1\n")
	if err := os.Remove(filepath.Join(f.Dir, "deleted.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.Dir, "old-name.go"), filepath.Join(f.Dir, "renamed.go")); err != nil {
		t.Fatal(err)
	}
	before = capture(t, f, r)
	current := snapshot(t, r, private)
	changes := diff(t, r, private, baseline, current)
	unchanged(t, f, r, before)
	want := []string{"added.go", "changed.go", "deleted.go", "renamed.go"}
	var paths []string
	for _, change := range changes {
		paths = append(paths, change.Path)
		if change.Path == "renamed.go" && (change.OldPath != "old-name.go" || change.Status != "R100") {
			t.Errorf("rename not preserved: %+v", change)
		}
		if strings.Contains(change.Patch, "Preexisting") || strings.Contains(change.Patch, "private ignored") {
			t.Error("baseline-only content entered turn diff")
		}
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("files = %q, want %q", paths, want)
	}
	cmd := exec.Command("git", "-C", f.Dir, "cat-file", "-e", current)
	if err := cmd.Run(); err == nil {
		t.Fatal("private tree is reachable from real object database")
	}
	if info, err := os.Stat(private); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
		t.Fatal("private snapshot directory is not protected")
	}
}

func TestUnbornAndMissingIndex(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			f := testrepo.New(t)
			f.Write("file.go", "package sample\n")
			if committed {
				f.Git("add", "-A")
				f.Git("commit", "--quiet", "-m", "fixture")
				if err := os.Remove(filepath.Join(f.Dir, ".git", "index")); err != nil {
					t.Fatal(err)
				}
			}
			r := resolve(t, f)
			private := t.TempDir()
			before := capture(t, f, r)
			base := snapshot(t, r, private)
			unchanged(t, f, r, before)
			f.Write("file.go", "package sample\nvar After = true\n")
			before = capture(t, f, r)
			changes := diff(t, r, private, base, snapshot(t, r, private))
			unchanged(t, f, r, before)
			if len(changes) != 1 || !strings.Contains(changes[0].Patch, "+var After = true") {
				t.Fatal("unborn/missing-index diff lost working change")
			}
		})
	}
}

func TestLinkedWorktreeAndSplitIndex(t *testing.T) {
	f := testrepo.New(t)
	f.Write("file.go", "package sample\n")
	f.Git("add", "-A")
	f.Git("commit", "--quiet", "-m", "fixture")
	linked := filepath.Join(t.TempDir(), "linked worktree")
	f.Git("worktree", "add", "--quiet", "-b", "linked", linked)
	f.Dir = linked
	f.Git("update-index", "--split-index")
	r := resolve(t, f)
	if r.GitDir == r.CommonDir {
		t.Fatal("linked worktree Git directory was not resolved separately")
	}
	private := t.TempDir()
	before := capture(t, f, r)
	base := snapshot(t, r, private)
	unchanged(t, f, r, before)
	f.Write("file.go", "package sample\nvar LinkedChange = 1\n")
	before = capture(t, f, r)
	changes := diff(t, r, private, base, snapshot(t, r, private))
	unchanged(t, f, r, before)
	if len(changes) != 1 {
		t.Fatal("linked worktree change not found")
	}
}

func TestInterruptedSplitIndexCopyResumesWithoutRealWrites(t *testing.T) {
	f := testrepo.New(t)
	f.Write("file.go", "package sample\n")
	f.Git("add", "-A")
	f.Git("commit", "--quiet", "-m", "fixture")
	f.Git("update-index", "--split-index")
	r := resolve(t, f)
	private := t.TempDir()
	// Reproduce a previous invocation interrupted immediately after its copy.
	if err := copyFile(r.Index, filepath.Join(private, "index")); err != nil {
		t.Fatal(err)
	}
	before := capture(t, f, r)
	snapshot(t, r, private)
	unchanged(t, f, r, before)
}

func TestSpecialFilenamesAndBinaryMetadata(t *testing.T) {
	f := testrepo.New(t)
	r := resolve(t, f)
	private := t.TempDir()
	base := snapshot(t, r, private)
	names := []string{"space name.go", "-leading.go", "semi;colon.go", "$(echo unsafe).go", "single'quote.go", "double\"quote.go", "new\nline.go", "tab\tname.go"}
	var supported []string
	for _, name := range names {
		if runtime.GOOS == "windows" && strings.ContainsAny(name, "\"\n\t") {
			t.Logf("SKIP filename %q: Windows forbids this character", name)
			continue
		}
		f.Write(name, "package sample\nvar Value = 1\n")
		supported = append(supported, name)
	}
	f.Write("image.dat", "\x00\x01binary payload\x02")
	before := capture(t, f, r)
	changes := diff(t, r, private, base, snapshot(t, r, private))
	unchanged(t, f, r, before)
	if len(changes) != len(supported)+1 {
		t.Fatalf("got %d changes, want %d", len(changes), len(supported)+1)
	}
	for _, change := range changes {
		if change.Path == "image.dat" {
			if !change.Binary || change.Patch != "" {
				t.Fatal("binary body escaped metadata-only handling")
			}
		} else if !strings.Contains(change.Patch, "+var Value = 1") {
			t.Errorf("special filename patch missing: %q", change.Path)
		}
	}
}

func TestFiltersAndHooksNeverExecute(t *testing.T) {
	f := testrepo.New(t)
	f.Write("file.go", "package sample\n")
	f.Git("add", "-A")
	f.Git("commit", "--quiet", "-m", "fixture")
	f.Write(".gitattributes", "*.go filter=trap diff=trap\n")
	marker := filepath.Join(f.Dir, "FILTER_EXECUTED")
	// The marker is relative so this shell fixture is portable to Git for Windows.
	command := "echo unexpected > FILTER_EXECUTED; cat"
	f.Git("config", "filter.trap.clean", command)
	f.Git("config", "filter.trap.process", "echo unexpected > FILTER_EXECUTED; exit 1")
	f.Git("config", "filter.trap.required", "true")
	f.Git("config", "diff.trap.textconv", command)
	f.Git("config", "diff.trap.command", command)
	f.Git("config", "core.fsmonitor", "echo unexpected > FILTER_EXECUTED")
	hooks := filepath.Join(f.Dir, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "post-index-change"), []byte("#!/bin/sh\necho unexpected > FILTER_EXECUTED\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r := resolve(t, f)
	private := t.TempDir()
	// Avoid fixture status: it would intentionally invoke the configured filters.
	before := inventory(t, r.CommonDir)
	base := snapshot(t, r, private)
	f.Write("file.go", "package sample\nvar Changed = true\n")
	changes := diff(t, r, private, base, snapshot(t, r, private))
	if len(changes) != 1 || !strings.Contains(changes[0].Patch, "+var Changed = true") {
		t.Fatal("neutralized filter lost raw source change")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("configured Git command executed")
	}
	if !reflect.DeepEqual(before, inventory(t, r.CommonDir)) {
		t.Fatal("filter snapshot modified real Git metadata")
	}
}

func TestUnsafeSnapshotAndInvalidInputs(t *testing.T) {
	f := testrepo.New(t)
	r := resolve(t, f)
	for _, dir := range []string{f.Dir, filepath.Join(f.Dir, "private"), r.GitDir, filepath.Dir(f.Dir), "relative"} {
		if _, err := r.Snapshot(context.Background(), dir); err == nil {
			t.Errorf("accepted unsafe snapshot path %q", dir)
		}
	}
	private := t.TempDir()
	if _, err := r.Diff(context.Background(), private, "--output=unsafe", strings.Repeat("0", 40)); err == nil {
		t.Fatal("accepted invalid tree id")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Snapshot(ctx, private); err == nil {
		t.Fatal("canceled snapshot succeeded")
	}
	if _, err := Resolve(context.Background(), t.TempDir()); err == nil {
		t.Fatal("resolved a non-Git directory")
	}
}

func TestSnapshotSymlinkContainment(t *testing.T) {
	f := testrepo.New(t)
	r := resolve(t, f)
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(f.Dir, link); err != nil {
		t.Skipf("symlink capability unavailable: %v", err)
	}
	if _, err := r.Snapshot(context.Background(), filepath.Join(link, "private")); err == nil {
		t.Fatal("snapshot escaped containment through repository symlink")
	}
	private := t.TempDir()
	if err := os.Symlink(r.Objects, filepath.Join(private, "objects")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Snapshot(context.Background(), private); err == nil {
		t.Fatal("snapshot followed private object symlink")
	}
}

func TestInheritedGitRoutingCannotRedirectSnapshot(t *testing.T) {
	f := testrepo.New(t)
	f.Write("source.go", "package sample\n")
	other := t.TempDir()
	t.Setenv("GIT_DIR", other)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, "index"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(other, "objects"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_TRACE", filepath.Join(other, "trace"))
	r := resolve(t, f)
	wantRoot, err := filepath.EvalSymlinks(f.Dir)
	if err != nil || r.Root != wantRoot {
		t.Fatal("inherited Git routing was honored")
	}
	snapshot(t, r, t.TempDir())
	entries, err := os.ReadDir(other)
	if err != nil || len(entries) != 0 {
		t.Fatal("inherited Git environment created output")
	}
}

func TestPrivateAlternatePathWithSeparators(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows forbids colon and quote in directory names")
	}
	f := testrepo.New(t)
	f.Write("file.go", "package sample\n")
	f.Git("add", "-A")
	f.Git("commit", "--quiet", "-m", "fixture")
	renamed := f.Dir + ":;\"quoted\npath"
	if err := os.Rename(f.Dir, renamed); err != nil {
		t.Fatal(err)
	}
	f.Dir = renamed
	r := resolve(t, f)
	private := t.TempDir()
	base := snapshot(t, r, private)
	f.Write("file.go", "package sample\nvar Added = 1\n")
	if len(diff(t, r, private, base, snapshot(t, r, private))) != 1 {
		t.Fatal("quoted alternate path lost change")
	}
}

func TestIndependentPrivateSnapshots(t *testing.T) {
	f := testrepo.New(t)
	f.Write("source.go", "package sample\n")
	r := resolve(t, f)
	one, two := t.TempDir(), t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	errs := make(chan error, 2)
	for _, dir := range []string{one, two} {
		go func(dir string) {
			_, err := r.Snapshot(ctx, dir)
			errs <- err
		}(dir)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{one, two} {
		if _, err := os.Stat(filepath.Join(dir, "index")); err != nil {
			t.Fatal("private sessions collided")
		}
	}
}
