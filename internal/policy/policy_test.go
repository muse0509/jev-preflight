package policy

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/muse0509/jev-preflight/policies"
)

func TestDefaultPolicy(t *testing.T) {
	p, err := Load()
	if err != nil || p.SchemaVersion != 1 || len(p.Questions) != 8 {
		t.Fatalf("default policy: %#v, %v", p, err)
	}
	for _, id := range expectedIDs {
		if q := p.Questions[id]; q.Type != "noul" || q.Instructions == "" {
			t.Errorf("missing valid question %s", id)
		}
	}
}

func TestRejectInvalidPolicy(t *testing.T) {
	var original struct {
		SchemaVersion int                          `json:"schemaVersion"`
		Questions     []map[string]json.RawMessage `json:"questions"`
	}
	for _, name := range []string{"version", "missing", "duplicate", "unknown", "type", "instructions", "criteria"} {
		t.Run(name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(policies.Default), &original); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "version":
				original.SchemaVersion++
			case "missing":
				original.Questions = original.Questions[:7]
			case "duplicate":
				original.Questions[0]["id"] = original.Questions[1]["id"]
			case "unknown":
				original.Questions[0]["id"] = json.RawMessage(`"unknown"`)
			case "type":
				original.Questions[0]["type"] = json.RawMessage(`"text"`)
			case "instructions":
				original.Questions[0]["instructions"] = json.RawMessage(`" "`)
			case "criteria":
				original.Questions[0]["criteria"] = json.RawMessage(`{"true":"","false":"no"}`)
			}
			b, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parse(string(b)); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
	for _, invalid := range []string{policies.Default + `{}`, `{`, `null`} {
		if _, err := parse(invalid); err == nil {
			t.Fatal("invalid policy JSON accepted")
		}
	}
}

func TestStableTopThree(t *testing.T) {
	scores := map[string]float64{"lifecycle": 0.9, "compatibility": 0.9, "auth_boundary": 0.99, "data_integrity": 0.9, "behavior_regression": 0.84, "regression_tests": math.NaN(), "unknown": 1}
	want := []Risk{{"auth_boundary", 0.99}, {"compatibility", 0.9}, {"data_integrity", 0.9}}
	for i := 0; i < 20; i++ {
		if got := Select(scores, 0.85); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
	if got := Select(map[string]float64{"auth_boundary": 0.85}, 0.85); len(got) != 1 {
		t.Fatal("threshold should be inclusive")
	}
}

func TestFeedback(t *testing.T) {
	f := Feedback([]Risk{{"auth_boundary", 0.91}}, []string{"z.go", "line\nbreak.go", "a.go"})
	for _, part := range []string{"auth_boundary", "yes probability 0.910", `"a.go", "line\nbreak.go", "z.go"`, "not proof of defects", "surrounding code, and tests", "no supporting evidence was found and finish"} {
		if !strings.Contains(f, part) {
			t.Errorf("feedback missing %q", part)
		}
	}
}
