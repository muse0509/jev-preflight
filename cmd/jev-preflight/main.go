package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/muse0509/jev-preflight/internal/hook"
)

const version = "0.1.0"

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintln(stdout, "jev-preflight "+version)
		return 0
	}
	if len(args) != 2 || args[0] != "hook" || (args[1] != "user-prompt-submit" && args[1] != "stop") {
		fmt.Fprintln(stderr, "Usage: jev-preflight version | hook {user-prompt-submit|stop}")
		return 2
	}
	event := "Stop"
	if args[1] == "user-prompt-submit" {
		event = "UserPromptSubmit"
	}
	output := (hook.Runner{Stderr: stderr}).Run(context.Background(), stdin, event)
	if output.Write(stdout) != nil {
		fmt.Fprintln(stderr, "jev-preflight: unable to write hook output")
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
