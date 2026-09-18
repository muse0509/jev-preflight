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
			bin := filepath.Join(base, "bin")
			write := func(name, body string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			launcher := filepath.Join(root, "scripts", "run")
			write(launcher, string(source))
			write(filepath.Join(bin, "uname"), "#!/bin/bash\nif [[ \"$1\" == -s ]]; then printf '%s' \"$TEST_SYSTEM\"; else printf '%s' \"$TEST_ARCH\"; fi\n")
			dev := filepath.Join(root, ".tmp", "runtime", tt.target, "jev-preflight"+tt.suffix)
			release := filepath.Join(root, "scripts", "runtime", tt.target, "jev-preflight"+tt.suffix)
			invoke := func(system, arch string) (string, string, error) {
				cmd := exec.Command(bash, launcher, "hook", "stop", "space ; $(literal)")
				cmd.Env = append(os.Environ(), "CLAUDE_PLUGIN_ROOT="+root, "TEST_SYSTEM="+system, "TEST_ARCH="+arch, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				var stderr strings.Builder
				cmd.Stderr = &stderr
				b, err := cmd.Output()
				return string(b), stderr.String(), err
			}
			if out, diag, err := invoke(tt.system, tt.arch); err != nil || out != "" || !strings.Contains(diag, "unavailable") {
				t.Fatalf("missing runtime: %q %q %v", out, diag, err)
			}
			write(dev, "#!/bin/bash\nprintf 'dev:%s|%s|%s' \"$1\" \"$2\" \"$3\"\n")
			if out, _, err := invoke(tt.system, tt.arch); err != nil || out != "dev:hook|stop|space ; $(literal)" {
				t.Fatalf("fallback: %q %v", out, err)
			}
			write(release, "#!/bin/bash\nprintf 'release:%s|%s|%s' \"$1\" \"$2\" \"$3\"\n")
			if out, _, err := invoke(tt.system, tt.arch); err != nil || out != "release:hook|stop|space ; $(literal)" {
				t.Fatalf("release: %q %v", out, err)
			}
			for _, unsupported := range [][2]string{{"FreeBSD", "x86_64"}, {"Linux", "riscv64"}} {
				if out, diag, err := invoke(unsupported[0], unsupported[1]); err != nil || out != "" || !strings.Contains(diag, "unsupported") {
					t.Fatalf("unsupported: %q %q %v", out, diag, err)
				}
			}
		})
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
		t.Skip("Git for Windows Bash unavailable; WSL is not a supported launcher runtime")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash unavailable; launcher requires Bash")
	}
	return bash
}
