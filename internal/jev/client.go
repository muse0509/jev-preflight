// Package jev implements the bounded, single-request TypeSafe router client.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"

	"github.com/muse0509/jev-preflight/internal/policy"
)

// Endpoint is fixed for production; repository config cannot override it.
const Endpoint = "https://api.typesafe.ai/v1/systemone"

const maxResponseBytes = 64 << 10

type State struct {
	Format string   `json:"format"`
	Files  []string `json:"files"`
	Diff   string   `json:"diff"`
}

type request struct {
	State     State                      `json:"state"`
	Model     string                     `json:"model"`
	Questions map[string]policy.Question `json:"questions"`
}

type answer struct {
	Type string   `json:"type"`
	Noul *float64 `json:"noul"`
}

type response struct {
	Answers map[string]answer `json:"answers"`
}

// Error exposes only a stable failure class, never request or response bodies.
type Error struct {
	Class string
}

func (e *Error) Error() string { return "TypeSafe request failed: " + e.Class }

type Client struct {
	endpoint string
	http     *http.Client
}

// New allows endpoint and transport injection for fake-server tests.
func New(endpoint string, client *http.Client) *Client {
	if endpoint == "" {
		endpoint = Endpoint
	}
	if client == nil {
		client = &http.Client{}
	}
	copy := *client
	// Never forward a diff or credentials to a redirected destination.
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{endpoint: endpoint, http: &copy}
}

// Evaluate performs one request with no application retries.
func (c *Client) Evaluate(ctx context.Context, key string, state State, questions map[string]policy.Question) (map[string]float64, error) {
	if key == "" {
		return nil, &Error{Class: "missing_key"}
	}
	if len(questions) == 0 || state.Format != "unified_diff" {
		return nil, &Error{Class: "invalid_request"}
	}
	data, err := json.Marshal(request{State: state, Model: "jev-latest", Questions: questions})
	if err != nil {
		return nil, &Error{Class: "invalid_request"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, &Error{Class: "invalid_request"}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		class := "network"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			class = "timeout"
		}
		return nil, &Error{Class: class}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{Class: "http_" + strconv.Itoa(resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		class := "response_read"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			class = "timeout"
		}
		return nil, &Error{Class: class}
	}
	if len(body) > maxResponseBytes {
		return nil, &Error{Class: "response_too_large"}
	}
	var result response
	d := json.NewDecoder(bytes.NewReader(body))
	if err := d.Decode(&result); err != nil {
		return nil, &Error{Class: "invalid_response"}
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, &Error{Class: "invalid_response"}
	}
	scores := make(map[string]float64, len(questions))
	for id := range questions {
		a, ok := result.Answers[id]
		if !ok || a.Type != "noul" || a.Noul == nil || math.IsNaN(*a.Noul) || math.IsInf(*a.Noul, 0) || *a.Noul < 0 || *a.Noul > 1 {
			return nil, &Error{Class: "invalid_response"}
		}
		scores[id] = *a.Noul
	}
	return scores, nil
}
