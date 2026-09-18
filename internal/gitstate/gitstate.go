// Package gitstate snapshots working trees without writing to their Git metadata.
package gitstate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RawOutputLimit bounds each Git command's stdout before parsing or redaction.
// The separate maxDiffBytes setting limits the canonical JSON sent to the API.
const RawOutputLimit = 4 << 20

// ErrOutputTooLarge means the entire Git result was discarded, never truncated.
var ErrOutputTooLarge = errors.New("Git output exceeds raw limit")

// Repo contains canonical paths discovered from the working directory.
type Repo struct {
	Root, GitDir, CommonDir, Index, Objects string
}

// Change describes one tree-to-tree change. Binary changes never contain a body.
type Change struct {
	Path, OldPath, Status, Patch string
	Binary                       bool
}

// Resolve ignores inherited repository-routing variables and locates a worktree.
func Resolve(ctx context.Context, cwd string) (*Repo, error) {
	location, err := canonical(cwd)
	if err != nil {
		return nil, errors.New("invalid working directory")
	}
	r := new(Repo)
	queries := []struct {
		dest *string
		args []string
	}{
		{&r.Root, []string{"--show-toplevel"}},
		{&r.GitDir, []string{"--git-dir"}},
		{&r.CommonDir, []string{"--git-common-dir"}},
		{&r.Index, []string{"--git-path", "index"}},
		{&r.Objects, []string{"--git-path", "objects"}},
	}
	for _, query := range queries {
		out, err := run(ctx, location, nil, nil, append([]string{"rev-parse", "--path-format=absolute"}, query.args...)...)
		if err != nil {
			return nil, errors.New("working directory is not a usable Git repository")
		}
		path := strings.TrimSuffix(string(out), "\n")
		*query.dest, err = canonical(path)
		if err != nil {
			return nil, errors.New("invalid Git repository path")
		}
	}
	if !contains(r.Root, location) {
		return nil, errors.New("working directory escapes repository")
	}
	return r, nil
}

// Snapshot stores a private index and object database in privateDir. The directory
// must be outside both the worktree and real Git metadata, including symlink aliases.
func (r *Repo) Snapshot(ctx context.Context, privateDir string) (string, error) {
	dir, err := r.privatePath(privateDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0700); err != nil {
		return "", errors.New("cannot create private snapshot")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return "", errors.New("cannot secure private snapshot")
	}
	index := filepath.Join(dir, "index")
	if _, err := os.Lstat(index); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(r.Index); err == nil {
			if err := copyFile(r.Index, index); err != nil {
				return "", errors.New("cannot copy Git index")
			}
		} else if errors.Is(err, os.ErrNotExist) {
			args := []string{"read-tree", "--empty"}
			if head, err := r.command(ctx, dir, nil, "rev-parse", "--verify", "HEAD^{tree}"); err == nil {
				args = []string{"read-tree", strings.TrimSpace(string(head))}
			}
			if _, err := r.command(ctx, dir, nil, args...); err != nil {
				return "", err
			}
		} else {
			return "", errors.New("cannot read Git index")
		}
	} else if err != nil {
		return "", errors.New("cannot read private index")
	}
	// Repeat conversion safely after an interrupted earlier snapshot too.
	if err := r.expandIndex(ctx, dir); err != nil {
		return "", err
	}
	filters, err := r.filterOverrides(ctx)
	if err != nil {
		return "", err
	}
	if _, err := r.writeCommand(ctx, dir, filters, "add", "-A", "--", "."); err != nil {
		return "", err
	}
	out, err := r.writeCommand(ctx, dir, filters, "write-tree", "--missing-ok")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(out))
	if !objectID(tree) {
		return "", errors.New("invalid private tree")
	}
	return tree, nil
}

// expandIndex prevents Git from even refreshing the real shared index's mtime.
// Git first searches its Git directory for sharedindex files, so conversion must
// use a separate Git directory, not merely GIT_INDEX_FILE.
func (r *Repo) expandIndex(ctx context.Context, dir string) error {
	gitDir := filepath.Join(dir, "index-git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs"), 0700); err != nil {
		return errors.New("cannot prepare private index")
	}
	defer os.RemoveAll(gitDir)
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/private\n"), 0600); err != nil {
		return errors.New("cannot prepare private index")
	}
	format, err := r.command(ctx, dir, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(format)) == "sha256" {
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\nrepositoryformatversion = 1\n[extensions]\nobjectformat = sha256\n"), 0600); err != nil {
			return errors.New("cannot prepare private index")
		}
	}
	entries, err := os.ReadDir(r.GitDir)
	if err != nil {
		return errors.New("cannot inspect shared indexes")
	}
	for _, entry := range entries {
		if suffix, ok := strings.CutPrefix(entry.Name(), "sharedindex."); ok && objectID(suffix) {
			if err := copyFile(filepath.Join(r.GitDir, entry.Name()), filepath.Join(gitDir, entry.Name())); err != nil {
				return errors.New("cannot copy shared index")
			}
		}
	}
	_, err = run(ctx, r.Root, r.privateEnv(dir), []string{"--git-dir=" + gitDir, "--work-tree=" + r.Root}, "update-index", "--no-split-index")
	return err
}

func (r *Repo) filterOverrides(ctx context.Context) ([]string, error) {
	out, err := r.command(ctx, "", nil, "config", "--null", "--name-only", "--get-regexp", `^filter\..*\.(clean|process|required)$`)
	if err != nil {
		var exit *commandError
		if errors.As(err, &exit) && exit.exitCode == 1 {
			return nil, nil
		}
		return nil, errors.New("cannot inspect Git filters")
	}
	drivers := make(map[string]bool)
	for _, key := range bytes.Split(out, []byte{0}) {
		if len(key) == 0 {
			continue
		}
		value := string(key)
		if dot := strings.LastIndexByte(value, '.'); dot > len("filter.") {
			drivers[value[:dot]] = true
		}
	}
	var names []string
	for driver := range drivers {
		names = append(names, driver)
	}
	sort.Strings(names)
	var args []string
	for _, name := range names {
		args = append(args, "-c", name+".process=", "-c", name+".clean=cat", "-c", name+".required=false")
	}
	return args, nil
}

// Diff compares trees only; the user's staged state and HEAD do not participate.
func (r *Repo) Diff(ctx context.Context, privateDir, baseline, current string) ([]Change, error) {
	dir, err := r.privatePath(privateDir)
	if err != nil {
		return nil, err
	}
	if !objectID(baseline) || !objectID(current) {
		return nil, errors.New("invalid snapshot tree identity")
	}
	base := []string{"diff", "--no-ext-diff", "--no-textconv", "--find-renames=50%", "--no-color", "--no-relative", "--submodule=short", "--diff-algorithm=myers", "--no-indent-heuristic", "-O", os.DevNull}
	invoke := func(format ...string) ([]byte, error) {
		args := append(append([]string{}, base...), format...)
		args = append(args, baseline, current, "--")
		return r.command(ctx, dir, nil, args...)
	}
	names, err := invoke("--name-status", "-z")
	if err != nil {
		return nil, err
	}
	changes, err := parseNames(names)
	if err != nil || len(changes) == 0 {
		return changes, err
	}
	numstat, err := invoke("--numstat", "-z")
	if err != nil {
		return nil, err
	}
	binary, err := parseNumstat(numstat)
	if err != nil {
		return nil, err
	}
	patch, err := invoke("--patch", "--src-prefix=a/", "--dst-prefix=b/", "--line-prefix=", "--output-indicator-new=+", "--output-indicator-old=-", "--output-indicator-context= ", "--unified=3")
	if err != nil {
		return nil, err
	}
	patches := splitPatches(patch)
	if len(changes) != len(patches) {
		return nil, errors.New("inconsistent Git diff")
	}
	for i := range changes {
		if binary[changes[i].Path] {
			changes[i].Binary = true
		} else {
			changes[i].Patch = string(patches[i])
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

func parseNames(data []byte) ([]Change, error) {
	parts := bytes.Split(data, []byte{0})
	var result []Change
	for i := 0; i < len(parts)-1; {
		status := string(parts[i])
		i++
		if len(status) == 0 || i >= len(parts)-1 {
			return nil, errors.New("invalid Git file list")
		}
		change := Change{Status: status, Path: string(parts[i])}
		i++
		if status[0] == 'R' || status[0] == 'C' {
			if i >= len(parts)-1 {
				return nil, errors.New("invalid Git rename")
			}
			change.OldPath, change.Path = change.Path, string(parts[i])
			i++
		}
		if !safeRelative(change.Path) || change.OldPath != "" && !safeRelative(change.OldPath) {
			return nil, errors.New("Git path escapes repository")
		}
		result = append(result, change)
	}
	return result, nil
}

func parseNumstat(data []byte) (map[string]bool, error) {
	parts := bytes.Split(data, []byte{0})
	result := make(map[string]bool)
	for i := 0; i < len(parts)-1; i++ {
		fields := bytes.SplitN(parts[i], []byte{'\t'}, 3)
		if len(fields) != 3 {
			return nil, errors.New("invalid Git numstat")
		}
		name := string(fields[2])
		if name == "" {
			if i+2 >= len(parts)-1 {
				return nil, errors.New("invalid Git rename numstat")
			}
			i += 2
			name = string(parts[i])
		}
		result[name] = bytes.Equal(fields[0], []byte{'-'}) && bytes.Equal(fields[1], []byte{'-'})
	}
	return result, nil
}

func splitPatches(data []byte) [][]byte {
	if len(data) == 0 || !bytes.HasPrefix(data, []byte("diff --git ")) {
		return nil
	}
	var result [][]byte
	for {
		next := bytes.Index(data, []byte("\ndiff --git "))
		if next < 0 {
			return append(result, data)
		}
		result = append(result, data[:next+1])
		data = data[next+1:]
	}
}

func (r *Repo) command(ctx context.Context, dir string, config []string, args ...string) ([]byte, error) {
	flags := []string{"--git-dir=" + r.GitDir, "--work-tree=" + r.Root}
	flags = append(flags, config...)
	var env []string
	if dir != "" {
		env = r.privateEnv(dir)
	}
	return run(ctx, r.Root, env, flags, args...)
}

// Git refreshes mtimes of duplicate objects even in alternates. Only read
// commands receive the real alternate; writes must see the private ODB alone.
// The copied index can reference real blobs, so write-tree uses --missing-ok.
func (r *Repo) writeCommand(ctx context.Context, dir string, config []string, args ...string) ([]byte, error) {
	flags := []string{"--git-dir=" + r.GitDir, "--work-tree=" + r.Root}
	flags = append(flags, config...)
	env := r.privateEnv(dir)
	return run(ctx, r.Root, env[:2], flags, args...)
}

func (r *Repo) privateEnv(dir string) []string {
	return []string{
		"GIT_INDEX_FILE=" + filepath.Join(dir, "index"),
		"GIT_OBJECT_DIRECTORY=" + filepath.Join(dir, "objects"),
		// Git's C-style quoting protects ':' / ';', quotes, and newlines.
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + strconv.Quote(filepath.ToSlash(r.Objects)),
	}
}

func run(ctx context.Context, cwd string, extraEnv, flags []string, args ...string) ([]byte, error) {
	base := []string{"--no-pager", "-c", "core.fsmonitor=false", "-c", "core.splitIndex=false", "-c", "core.untrackedCache=false", "-c", "core.hooksPath=" + os.DevNull, "-c", "credential.helper=", "-c", "credential.interactive=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "core.quotePath=true"}
	base = append(base, flags...)
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Dir = cwd
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "GIT_") && upper != "GIT_CONFIG_NOSYSTEM" && upper != "GIT_CONFIG_GLOBAL" && upper != "GIT_CONFIG_SYSTEM" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "LC_ALL=C")
	cmd.Env = append(cmd.Env, extraEnv...)
	return boundedOutput(ctx, cmd, RawOutputLimit)
}

type commandError struct{ exitCode int }

func (*commandError) Error() string { return "Git operation failed" }

// boundedOutput always waits for the child, including after overflow or cancel.
// Run routes nil stderr to the null device and never captures Git diagnostics.
func boundedOutput(ctx context.Context, cmd *exec.Cmd, limit int) ([]byte, error) {
	output := &limitedOutput{limit: limit, kill: func() { _ = cmd.Process.Kill() }}
	cmd.Stdout = output
	cmd.Stderr = nil
	// A descendant holding the stdout pipe must not prevent child reclamation.
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if output.exceeded {
		return nil, ErrOutputTooLarge
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		return nil, &commandError{exitCode: code}
	}
	return output.data, nil
}

type limitedOutput struct {
	data     []byte
	limit    int
	exceeded bool
	kill     func()
}

func (out *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > out.limit-len(out.data) {
		out.exceeded = true
		out.kill()
		return 0, ErrOutputTooLarge
	}
	out.data = append(out.data, p...)
	return len(p), nil
}

func (r *Repo) privatePath(path string) (string, error) {
	dir, err := canonical(path)
	if err != nil || !filepath.IsAbs(path) {
		return "", errors.New("invalid private snapshot path")
	}
	for _, protected := range []string{r.Root, r.GitDir, r.CommonDir, r.Objects} {
		if contains(protected, dir) || contains(dir, protected) {
			return "", errors.New("private snapshot overlaps repository")
		}
	}
	for _, leaf := range []string{"index", "objects", "index-git"} {
		if info, err := os.Lstat(filepath.Join(dir, leaf)); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("private snapshot contains symlink")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("invalid private snapshot entry")
		}
	}
	return dir, nil
}

func canonical(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return "", err
	}
	parent, err = canonical(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

func contains(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func safeRelative(path string) bool {
	return path != "" && !filepath.IsAbs(path) && path != ".." && !strings.HasPrefix(path, "../") && !strings.Contains(path, "/../") && !strings.ContainsRune(path, 0)
}

func objectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}

func copyFile(source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	return errors.Join(copyErr, closeErr)
}
