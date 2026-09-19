package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func smokeScript(t *testing.T) (string, string) {
	t.Helper()
	fixtureBash(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("fixture Git command unavailable")
	}
	root := filepath.Join(t.TempDir(), "fixture space ' $(not-executed)")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile("prepare-hook-smoke")
	if err != nil {
		t.Fatal(err)
	}
	write("scripts/prepare-hook-smoke", string(body))
	write("source/.claude-plugin/plugin.json", `{"name":"jev-preflight","defaultEnabled":false}`)
	write("source/hooks/hooks.json", `{"hooks":{}}`)
	write("source/scripts/run", "fixture launcher\n")
	write("source/scripts/runtime/fixture", "fixture binary\n")
	return root, filepath.Join(root, "scripts", "prepare-hook-smoke")
}

func fixtureCommand(t *testing.T, script string, wantSuccess bool, args ...string) string {
	t.Helper()
	argv := []string{filepath.ToSlash(script)}
	for _, arg := range args {
		argv = append(argv, filepath.ToSlash(arg))
	}
	command := fixtureBashCommand(t, argv...)
	output, err := command.CombinedOutput()
	if (err == nil) != wantSuccess {
		t.Fatalf("fixture command success=%v, wanted %v: %s", err == nil, wantSuccess, output)
	}
	return strings.TrimSpace(string(output))
}

func fixtureBash(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		// WSL cannot exercise the native Windows paths and Git used by these tests.
		var candidates []string
		if git, err := exec.LookPath("git"); err == nil {
			base := filepath.Dir(git)
			for i := 0; i < 3; i++ {
				candidates = append(candidates, filepath.Join(base, "bin", "bash.exe"), filepath.Join(base, "usr", "bin", "bash.exe"))
				base = filepath.Dir(base)
			}
		}
		candidates = append(candidates, filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe"))
		for _, path := range candidates {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path
			}
		}
		t.Fatal("Git for Windows Bash unavailable; fixture tests require Git Bash")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal("fixture tests require Bash")
	}
	return bash
}

func fixtureBashCommand(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	command := exec.Command(fixtureBash(t), append([]string{"--noprofile", "--norc"}, args...)...)
	command.Env = append(os.Environ(), "BASH_ENV=")
	return command
}

func nativeFixturePath(t *testing.T, path string) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return path
	}
	// The script's pwd prints /c/...; Go needs a native drive path for file checks.
	output, err := fixtureBashCommand(t, "-c", `cygpath -m -- "$1"`, "fixture-path", path).Output()
	if err != nil {
		t.Fatal("Git Bash fixture path conversion failed")
	}
	return filepath.Clean(filepath.FromSlash(strings.TrimSpace(string(output))))
}

func TestSmokeFixtureLifecycleAndMetadata(t *testing.T) {
	root, script := smokeScript(t)
	// Hostile inherited Git settings must not redirect the fixture commands.
	outside := filepath.Join(root, "untouched")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", outside)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(outside, "index"))
	// Git Bash must not need automatic /c/... argument conversion for native Git.
	t.Setenv("MSYS_NO_PATHCONV", "1")
	t.Setenv("MSYS2_ARG_CONV_EXCL", "*")
	fixture := nativeFixturePath(t, fixtureCommand(t, script, true, filepath.Join(root, "source")))
	relative, err := filepath.Rel(filepath.Join(root, ".tmp"), fixture)
	if err != nil || filepath.Dir(relative) != "." || !strings.HasPrefix(relative, "hook-smoke.") {
		t.Fatal("fixture escaped private output directory")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatal("inherited Git paths were mutated")
	}
	fixtureCommand(t, script, false, "--verify", fixture)
	if err := os.WriteFile(filepath.Join(fixture, "repo", "check.go"), []byte("package synthetic\nfunc allowed(owner, actor string) bool { return true }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureCommand(t, script, true, "--verify", fixture)
	if err := os.WriteFile(filepath.Join(fixture, "repo", ".git", "unexpected"), []byte("changed metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureCommand(t, script, false, "--verify", fixture)
	fixtureCommand(t, script, true, "--cleanup", fixture)
	if _, err := os.Stat(fixture); !os.IsNotExist(err) {
		t.Fatal("fixture cleanup failed")
	}
	if _, err := os.Stat(filepath.Join(root, "source", "scripts", "run")); err != nil {
		t.Fatal("cleanup removed the source plugin")
	}
}

func TestSmokeFixtureRejectsUnsafeInputs(t *testing.T) {
	root, script := smokeScript(t)
	source := filepath.Join(root, "source")
	fixtureCommand(t, script, false, "--cleanup", source)
	fixtureCommand(t, script, false, "--verify", source)
	if err := os.Symlink(filepath.Join(source, "scripts", "run"), filepath.Join(source, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := fixtureBashCommand(t, "-c", `test -L "$1"`, "fixture-symlink", filepath.ToSlash(filepath.Join(source, "linked"))).Run(); err != nil {
		t.Fatal("Bash did not recognize the fixture symlink")
	}
	fixtureCommand(t, script, false, source)
	if _, err := os.Stat(filepath.Join(root, ".tmp")); !os.IsNotExist(err) {
		t.Fatal("unsafe source produced a fixture")
	}
	if err := os.Remove(filepath.Join(source, "linked")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".tmp")); err != nil {
		t.Fatal(err)
	}
	fixtureCommand(t, script, false, source)
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatal("symlinked output directory was modified")
	}
}
