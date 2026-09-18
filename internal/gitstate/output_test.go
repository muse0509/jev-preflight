package gitstate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muse0509/jev-preflight/internal/testrepo"
)

// TestOutputProcess is a portable child process fixture for the real runner.
func TestOutputProcess(t *testing.T) {
	args := os.Args
	if len(args) < 4 || args[len(args)-3] != "--" {
		return
	}
	mode, value := args[len(args)-2], args[len(args)-1]
	switch mode {
	case "output", "overflow":
		n, err := strconv.Atoi(value)
		if err != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("x", n))
		if mode == "overflow" {
			time.Sleep(time.Hour)
		}
	case "cancel":
		if err := os.WriteFile(value, []byte("ready"), 0600); err != nil {
			os.Exit(2)
		}
		time.Sleep(time.Hour)
	case "error":
		_, _ = fmt.Fprint(os.Stderr, value)
		os.Exit(42)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func outputProcess(ctx context.Context, mode, value string) *exec.Cmd {
	return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOutputProcess$", "--", mode, value)
}

func TestBoundedOutputBoundary(t *testing.T) {
	const limit = 1024
	for _, size := range []int{limit - 1, limit, limit + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			mode := "output"
			if size > limit {
				mode = "overflow"
			}
			cmd := outputProcess(ctx, mode, strconv.Itoa(size))
			start := time.Now()
			out, err := boundedOutput(ctx, cmd, limit)
			if size > limit {
				if !errors.Is(err, ErrOutputTooLarge) || out != nil {
					t.Fatal("limit + 1 did not discard the entire output with the size sentinel")
				}
				if time.Since(start) >= 5*time.Second {
					t.Fatal("overflow did not terminate the child promptly")
				}
			} else if err != nil || len(out) != size {
				t.Fatal("output at or below the limit was not preserved")
			}
			if cmd.ProcessState == nil || !errors.Is(cmd.Process.Kill(), os.ErrProcessDone) {
				t.Fatal("child process was not reaped")
			}
		})
	}
}

func TestCanceledOutputReapsChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := outputProcess(ctx, "cancel", ready)
	done := make(chan error, 1)
	go func() {
		out, err := boundedOutput(ctx, cmd, 1024)
		if out != nil {
			done <- errors.New("cancellation returned partial output")
			return
		}
		done <- err
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			cancel()
			<-done
			t.Fatal("child did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation was not returned")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled child did not terminate")
	}
	if cmd.ProcessState == nil || !errors.Is(cmd.Process.Kill(), os.ErrProcessDone) {
		t.Fatal("canceled child was not reaped")
	}
}

func TestCommandErrorsDiscardPrivateDiagnostics(t *testing.T) {
	marker := "PRIVATE_DIFF_BODY_AND_FILENAME"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, cmd := range []*exec.Cmd{
		outputProcess(ctx, "error", marker),
		exec.CommandContext(ctx, filepath.Join(t.TempDir(), marker)),
	} {
		out, err := boundedOutput(ctx, cmd, 1024)
		if out != nil || err == nil || err.Error() != "Git operation failed" {
			t.Fatal("command failure was not sanitized")
		}
		var exit *commandError
		if !errors.As(err, &exit) {
			t.Fatal("command exit status is unavailable")
		}
	}
}

func TestGitPatchRawLimitBoundary(t *testing.T) {
	f := testrepo.New(t)
	f.Write("sample.go", "package sample\n")
	r := resolve(t, f)
	private := t.TempDir()
	baseline := snapshot(t, r, private)
	f.Write("sample.go", "package sample\nvar Changed = true\n")
	current := snapshot(t, r, private)
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--src-prefix=a/", "--dst-prefix=b/", baseline, current, "--"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	expected, err := r.command(ctx, private, nil, args...)
	if err != nil || len(expected) == 0 {
		t.Fatal("could not prepare a bounded patch fixture")
	}
	for _, limit := range []int{len(expected), len(expected) - 1} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir=" + r.GitDir, "--work-tree=" + r.Root}, args...)...)
		cmd.Env = append(os.Environ(), r.privateEnv(private)...)
		out, err := boundedOutput(ctx, cmd, limit)
		if limit < len(expected) {
			if !errors.Is(err, ErrOutputTooLarge) || out != nil {
				t.Fatal("raw Git patch one byte over the limit was not rejected")
			}
		} else if err != nil || string(out) != string(expected) {
			t.Fatal("raw Git patch at the limit changed")
		}
	}
}
