package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCLI(t *testing.T) {
	for _, tt := range []struct {
		args   []string
		code   int
		output string
	}{
		{[]string{"version"}, 0, "jev-preflight 0.1.0\n"},
		{nil, 2, ""}, {[]string{"hook", "other"}, 2, ""},
		{[]string{"hook", "stop"}, 0, ""},
	} {
		var out, err bytes.Buffer
		if got := run(tt.args, strings.NewReader("{}"), &out, &err); got != tt.code || out.String() != tt.output {
			t.Fatalf("args %v: code %d, output %q", tt.args, got, out.String())
		}
	}
}
