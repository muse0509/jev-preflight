package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaults(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil || !reflect.DeepEqual(c, Defaults()) {
		t.Fatalf("default config: %#v, %v", c, err)
	}
}

func TestConfig(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		valid      bool
	}{
		{"empty", `{}`, true},
		{"override", `{"mode":"report","riskThreshold":0.5,"timeoutMs":100,"maxDiffBytes":200,"exclude":["dist/**","*.min.js","src/exact.go"]}`, true},
		{"off", `{"mode":"off"}`, true},
		{"zero threshold", `{"riskThreshold":0}`, true},
		{"unknown", `{"endpoint":"invalid"}`, false},
		{"trailing", `{} {}`, false},
		{"null", `null`, false},
		{"null field", `{"mode":null}`, false},
		{"array", `[]`, false},
		{"mode", `{"mode":"auto"}`, false},
		{"threshold low", `{"riskThreshold":-0.1}`, false},
		{"threshold high", `{"riskThreshold":1.1}`, false},
		{"threshold string", `{"riskThreshold":"NaN"}`, false},
		{"timeout low", `{"timeoutMs":0}`, false},
		{"timeout high", `{"timeoutMs":60001}`, false},
		{"bytes low", `{"maxDiffBytes":-1}`, false},
		{"bytes high", `{"maxDiffBytes":1048577}`, false},
		{"parent pattern", `{"exclude":["../private"]}`, false},
		{"absolute pattern", `{"exclude":["/private"]}`, false},
		{"backslash pattern", `{"exclude":["dir\\file"]}`, false},
		{"invalid glob", `{"exclude":["["]}`, false},
		{"recursive middle", `{"exclude":["a/**/b"]}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, ".jev-preflight.json"), []byte(tt.body), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(root)
			if (err == nil) != tt.valid {
				t.Fatalf("Load valid=%v: %v", tt.valid, err)
			}
			if tt.name == "override" && (c.Mode != "report" || c.RiskThreshold != 0.5 || c.TimeoutMS != 100 || c.MaxDiffBytes != 200 || len(c.Exclude) != 3) {
				t.Fatalf("override not applied: %#v", c)
			}
		})
	}
}

func TestKeyPrecedence(t *testing.T) {
	for _, tt := range []struct{ plugin, fallback, want string }{
		{"plugin-value", "fallback-value", "plugin-value"},
		{"", "fallback-value", "fallback-value"},
		{"  ", " fallback-value ", "fallback-value"},
		{"", "", ""},
	} {
		got := APIKey(func(name string) string {
			if name == "CLAUDE_PLUGIN_OPTION_TYPESAFE_API_KEY" {
				return tt.plugin
			}
			return tt.fallback
		})
		if got != tt.want {
			t.Fatal("unexpected key source")
		}
	}
}
