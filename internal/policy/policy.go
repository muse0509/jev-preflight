// Package policy validates the embedded questions and selects investigation priorities.
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/muse0509/jev-preflight/policies"
)

var expectedIDs = []string{
	"behavior_regression", "auth_boundary", "input_validation", "data_integrity",
	"error_handling", "compatibility", "lifecycle", "regression_tests",
}

// Criteria fixes the meaning of both outcomes.
type Criteria struct {
	True  string `json:"true"`
	False string `json:"false"`
}

// Question is sent directly in the TypeSafe questions object.
type Question struct {
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Criteria     Criteria `json:"criteria"`
}

type Policy struct {
	SchemaVersion int
	Questions     map[string]Question
}

// Load validates the embedded policy on startup.
func Load() (Policy, error) {
	return parse(policies.Default)
}

func parse(source string) (Policy, error) {
	var document struct {
		SchemaVersion int `json:"schemaVersion"`
		Questions     []struct {
			ID string `json:"id"`
			Question
		} `json:"questions"`
	}
	d := json.NewDecoder(strings.NewReader(source))
	d.DisallowUnknownFields()
	if err := d.Decode(&document); err != nil {
		return Policy{}, errors.New("invalid embedded policy JSON")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Policy{}, errors.New("trailing embedded policy JSON")
	}
	if document.SchemaVersion != 1 || len(document.Questions) != len(expectedIDs) {
		return Policy{}, errors.New("invalid policy version or question count")
	}
	p := Policy{SchemaVersion: document.SchemaVersion, Questions: make(map[string]Question, len(expectedIDs))}
	for _, item := range document.Questions {
		known := false
		for _, expected := range expectedIDs {
			known = known || expected == item.ID
		}
		if _, duplicate := p.Questions[item.ID]; !known || duplicate {
			return Policy{}, errors.New("invalid or duplicate policy question ID")
		}
		if item.Type != "noul" || strings.TrimSpace(item.Instructions) == "" || strings.TrimSpace(item.Criteria.True) == "" || strings.TrimSpace(item.Criteria.False) == "" {
			return Policy{}, errors.New("invalid policy question")
		}
		p.Questions[item.ID] = item.Question
	}
	return p, nil
}

type Risk struct {
	ID          string
	Probability float64
}

// Select returns at most three axes, sorted by probability then ID.
func Select(scores map[string]float64, threshold float64) []Risk {
	var risks []Risk
	for _, id := range expectedIDs {
		score, found := scores[id]
		if found && !math.IsNaN(score) && !math.IsInf(score, 0) && score >= threshold && score >= 0 && score <= 1 {
			risks = append(risks, Risk{ID: id, Probability: score})
		}
	}
	sort.Slice(risks, func(i, j int) bool {
		if risks[i].Probability == risks[j].Probability {
			return risks[i].ID < risks[j].ID
		}
		return risks[i].Probability > risks[j].Probability
	})
	if len(risks) > 3 {
		risks = risks[:3]
	}
	return risks
}

// Feedback gives compact investigation guidance without a generated review.
func Feedback(risks []Risk, files []string) string {
	var b strings.Builder
	b.WriteString("Investigate these changed-code risk axes once:\n")
	for _, risk := range risks {
		fmt.Fprintf(&b, "- %s: yes probability %.3f; check this risk in the changed code.\n", risk.ID, risk.Probability)
	}
	paths := append([]string(nil), files...)
	sort.Strings(paths)
	b.WriteString("Files: ")
	for i, file := range paths {
		if i != 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Quote(file))
	}
	b.WriteString(".\nJev scores are investigation priorities, not proof of defects. Inspect the actual diff, surrounding code, and tests. Change code only when you find evidence; otherwise state that no supporting evidence was found and finish.")
	return b.String()
}
