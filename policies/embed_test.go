package policies

import (
	"encoding/json"
	"testing"
)

func TestEmbeddedDocument(t *testing.T) {
	var document struct {
		SchemaVersion int               `json:"schemaVersion"`
		Questions     []json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal([]byte(Default), &document); err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != 1 || len(document.Questions) != 8 {
		t.Fatal("embedded policy contract changed")
	}
}
