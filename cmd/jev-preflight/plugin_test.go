package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPluginContracts(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(path string) []byte {
		t.Helper()
		b, e := os.ReadFile(filepath.Join(root, path))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	var manifest struct {
		Name, Version  string
		DefaultEnabled *bool
		UserConfig     map[string]struct {
			Type      string
			Sensitive bool
		}
	}
	if err := json.Unmarshal(read(".claude-plugin/plugin.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "jev-preflight" || manifest.Version != version || manifest.DefaultEnabled == nil || *manifest.DefaultEnabled || !manifest.UserConfig["typesafe_api_key"].Sensitive {
		t.Fatal("manifest contract mismatch")
	}
	var wiring struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type, Command string
				Args          []string
			}
		}
	}
	if err := json.Unmarshal(read("hooks/hooks.json"), &wiring); err != nil {
		t.Fatal(err)
	}
	for event, arg := range map[string]string{"UserPromptSubmit": "user-prompt-submit", "Stop": "stop"} {
		groups := wiring.Hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatal("hook missing")
		}
		h := groups[0].Hooks[0]
		if h.Type != "command" || h.Command != "bash" || len(h.Args) != 3 || h.Args[0] != "${CLAUDE_PLUGIN_ROOT}/scripts/run" || h.Args[1] != "hook" || h.Args[2] != arg {
			t.Fatal("unsafe hook wiring")
		}
	}
}

func TestLauncherMapping(t *testing.T) {
	bash := launcherBash(t)
	const argument = "space ' \" ; $(printf unexpected)"
	const harness = `
test_system=$1
test_arch=$2
shift 2
uname() {
  case "$1" in
    -s) printf '%s\n' "$test_system" ;;
    -m) printf '%s\n' "$test_arch" ;;
    *) return 1 ;;
  esac
}
source "$1" "${@:2}"
`
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ system, arch, target, suffix string }{
		{"Darwin", "x86_64", "darwin-amd64", ""}, {"Darwin", "arm64", "darwin-arm64", ""},
		{"Linux", "x86_64", "linux-amd64", ""}, {"Linux", "aarch64", "linux-arm64", ""},
		{"MINGW64_NT-10.0", "x86_64", "windows-amd64", ".exe"}, {"MSYS_NT-10.0", "aarch64", "windows-arm64", ".exe"},
	} {
		t.Run(tt.target, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "plugin space ' $(no-execution)")
			launcher := filepath.Join(root, "scripts", "run")
			writeLauncherFile(t, launcher, source)
			dev := filepath.Join(root, ".tmp", "runtime", tt.target, "jev-preflight"+tt.suffix)
			release := filepath.Join(root, "scripts", "runtime", tt.target, "jev-preflight"+tt.suffix)
			invoke := func(system, arch string) (string, string, error) {
				// A sourced function avoids executable lookup and Windows PATH rules.
				cmd := exec.Command(bash, "--noprofile", "--norc", "-c", harness, "launcher-test", system, arch, filepath.ToSlash(launcher), "hook", "stop", argument)
				cmd.Env = append(os.Environ(), "HOME="+base, "BASH_ENV=", "CLAUDE_PLUGIN_ROOT="+filepath.ToSlash(root))
				var stderr strings.Builder
				cmd.Stderr = &stderr
				b, err := cmd.Output()
				return string(b), stderr.String(), err
			}
			if out, diag, err := invoke(tt.system, tt.arch); err != nil || out != "" || !strings.Contains(diag, "unavailable") {
				t.Fatalf("missing runtime: %q %q %v", out, diag, err)
			}
			writeLauncherFile(t, dev, []byte("#!/bin/bash\nprintf 'dev:%s|%s|%s' \"$1\" \"$2\" \"$3\"\n"))
			if out, diag, err := invoke(tt.system, tt.arch); err != nil || out != "dev:hook|stop|"+argument || diag != "" {
				t.Fatalf("fallback: %q %q %v", out, diag, err)
			}
			writeLauncherFile(t, release, []byte("#!/bin/bash\nprintf 'release:%s|%s|%s' \"$1\" \"$2\" \"$3\"\n"))
			if out, diag, err := invoke(tt.system, tt.arch); err != nil || out != "release:hook|stop|"+argument || diag != "" {
				t.Fatalf("release: %q %q %v", out, diag, err)
			}
			for _, unsupported := range [][2]string{{"FreeBSD", "x86_64"}, {"Linux", "riscv64"}} {
				if out, diag, err := invoke(unsupported[0], unsupported[1]); err != nil || out != "" || !strings.Contains(diag, "unsupported") {
					t.Fatalf("unsupported: %q %q %v", out, diag, err)
				}
			}
		})
	}
}

func TestLauncherNativeRuntime(t *testing.T) {
	bash := launcherBash(t)
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run"))
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "plugin space ' $(no-execution)")
	launcher := filepath.Join(root, "scripts", "run")
	writeLauncherFile(t, launcher, source)
	name := "jev-preflight"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := runtime.GOOS + "-" + runtime.GOARCH
	dev := filepath.Join(root, ".tmp", "runtime", target, name)
	release := filepath.Join(root, "scripts", "runtime", target, name)
	args := []string{"hook", "stop", "space ' \" ; $(printf unexpected)"}
	for _, path := range []string{dev, release} {
		writeLauncherFile(t, path, binary)
		cmd := exec.Command(bash, append([]string{"--noprofile", "--norc", launcher, "-test.run=^TestLauncherProcess$", "--"}, args...)...)
		// Keep native paths here to exercise Git Bash launching a Windows .exe.
		cmd.Env = append(os.Environ(), "HOME="+filepath.Dir(root), "BASH_ENV=", "CLAUDE_PLUGIN_ROOT="+root, "JEV_PREFLIGHT_LAUNCHER_HELPER=1")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if err != nil || stderr.Len() != 0 {
			t.Fatalf("native launcher: %q %v", stderr.String(), err)
		}
		var result struct {
			Executable string
			Args       []string
		}
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatalf("native runtime did not return arguments: %v", err)
		}
		wantFile, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		gotFile, err := os.Stat(result.Executable)
		if err != nil || !os.SameFile(wantFile, gotFile) || len(result.Args) != len(args) {
			t.Fatalf("native runtime selection or argument count: %+v", result)
		}
		for i := range args {
			if result.Args[i] != args[i] {
				t.Fatalf("native runtime argument %d changed", i)
			}
		}
	}
}

func TestLauncherProcess(t *testing.T) {
	if os.Getenv("JEV_PREFLIGHT_LAUNCHER_HELPER") != "1" {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	if len(os.Args) < 3 || os.Args[2] != "--" {
		os.Exit(1)
	}
	err = json.NewEncoder(os.Stdout).Encode(struct {
		Executable string
		Args       []string
	}{executable, os.Args[3:]})
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func writeLauncherFile(t *testing.T, name string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, body, 0700); err != nil {
		t.Fatal(err)
	}
}

func launcherBash(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		// WSL's bash.exe is not a Git Bash runtime for native Windows paths.
		var candidates []string
		if git, err := exec.LookPath("git"); err == nil {
			base := filepath.Dir(git)
			for i := 0; i < 3; i++ {
				candidates = append(candidates, filepath.Join(base, "bin", "bash.exe"), filepath.Join(base, "usr", "bin", "bash.exe"))
				base = filepath.Dir(base)
			}
		}
		candidates = append(candidates, filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe"))
		for _, candidate := range candidates {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
		t.Fatal("Git for Windows Bash unavailable; launcher tests require Git Bash, not WSL")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal("Bash unavailable; launcher tests require Bash")
	}
	return bash
}
