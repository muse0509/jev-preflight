package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muse0509/jev-preflight/internal/policy"
)

func questions(t *testing.T) map[string]policy.Question {
	t.Helper()
	p, err := policy.Load()
	if err != nil {
		t.Fatal(err)
	}
	return p.Questions
}

func validResponse(q map[string]policy.Question) string {
	answers := make(map[string]answer, len(q))
	for id := range q {
		n := 0.91
		answers[id] = answer{Type: "noul", Noul: &n}
	}
	b, _ := json.Marshal(response{Answers: answers})
	return string(b)
}

func testState() State {
	return State{Format: "unified_diff", Files: []string{"a.go"}, Diff: "+value := 42\n"}
}

func TestRequestAndResponse(t *testing.T) {
	q := questions(t)
	key := "unit-" + strings.Repeat("x", 20)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer "+key || r.Header.Get("Content-Type") != "application/json" {
			t.Error("incorrect request contract")
		}
		var body request
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "jev-latest" || !reflect.DeepEqual(body.State, testState()) || !reflect.DeepEqual(body.Questions, q) {
			t.Error("incorrect request body")
		}
		io.WriteString(w, validResponse(q))
	}))
	defer srv.Close()
	scores, err := New(srv.URL+"/v1/systemone", srv.Client()).Evaluate(context.Background(), key, testState(), q)
	if err != nil || len(scores) != 8 || calls.Load() != 1 {
		t.Fatalf("scores=%v, err=%v, calls=%d", scores, err, calls.Load())
	}
	for _, score := range scores {
		if score != 0.91 {
			t.Fatal("incorrect response score")
		}
	}
}

func TestFailuresAreBoundedAndSafe(t *testing.T) {
	q := questions(t)
	valid := validResponse(q)
	leak := "private-body-" + strings.Repeat("z", 20)
	for _, tt := range []struct {
		name, body, class string
		status            int
	}{
		{"unauthorized", leak, "http_401", 401},
		{"forbidden", leak, "http_403", 403},
		{"limited", leak, "http_429", 429},
		{"server", leak, "http_500", 500},
		{"malformed", leak, "invalid_response", 200},
		{"array", `[]`, "invalid_response", 200},
		{"null", `null`, "invalid_response", 200},
		{"missing", `{"answers":{}}`, "invalid_response", 200},
		{"partial", `{"answers":{"behavior_regression":{"type":"noul","noul":0.9}}}`, "invalid_response", 200},
		{"type", strings.Replace(valid, `"type":"noul"`, `"type":"text"`, 1), "invalid_response", 200},
		{"missing type", strings.Replace(valid, `"type":"noul",`, ``, 1), "invalid_response", 200},
		{"high", strings.Replace(valid, `0.91`, `1.01`, 1), "invalid_response", 200},
		{"low", strings.Replace(valid, `0.91`, `-0.01`, 1), "invalid_response", 200},
		{"null score", strings.Replace(valid, `0.91`, `null`, 1), "invalid_response", 200},
		{"nan string", strings.Replace(valid, `0.91`, `"NaN"`, 1), "invalid_response", 200},
		{"nan literal", strings.Replace(valid, `0.91`, `NaN`, 1), "invalid_response", 200},
		{"infinite", strings.Replace(valid, `0.91`, `1e999`, 1), "invalid_response", 200},
		{"trailing", valid + `{}`, "invalid_response", 200},
		{"oversized", strings.Repeat(" ", maxResponseBytes+1), "response_too_large", 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			_, err := New(srv.URL, srv.Client()).Evaluate(context.Background(), "test-value", testState(), q)
			var failure *Error
			if !errors.As(err, &failure) || failure.Class != tt.class || calls.Load() != 1 {
				t.Fatalf("class=%v, calls=%d", err, calls.Load())
			}
			if strings.Contains(err.Error(), leak) || strings.Contains(err.Error(), "test-value") {
				t.Fatal("error leaked sensitive data")
			}
		})
	}
}

func TestTimeout(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
	}))
	defer func() { close(release); srv.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := New(srv.URL, srv.Client()).Evaluate(ctx, "test-value", testState(), questions(t))
	var failure *Error
	if !errors.As(err, &failure) || failure.Class != "timeout" || calls.Load() != 1 {
		t.Fatalf("timeout result=%v, calls=%d", err, calls.Load())
	}
}

func TestRedirectDoesNotForward(t *testing.T) {
	var leaked atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { leaked.Add(1) }))
	defer destination.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	_, err := New(srv.URL, srv.Client()).Evaluate(context.Background(), "test-value", testState(), questions(t))
	var failure *Error
	if !errors.As(err, &failure) || failure.Class != "http_307" || leaked.Load() != 0 {
		t.Fatalf("redirect result=%v, forwarded=%d", err, leaked.Load())
	}
}

type errorTransport struct{ err error }

func (rt errorTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, rt.err }

func TestNetworkFailureIsSanitized(t *testing.T) {
	q := questions(t)
	for _, err := range []error{
		&net.DNSError{Err: "test DNS error", Name: "private-host"},
		fmt.Errorf("transport contains %s", "private-request-body"),
	} {
		client := New("http://fake.invalid", &http.Client{Transport: errorTransport{err}})
		_, got := client.Evaluate(context.Background(), "test-value", testState(), q)
		if got == nil || got.Error() != "TypeSafe request failed: network" {
			t.Fatalf("unsafe network error: %v", got)
		}
	}
}

func TestAdditiveResponseFieldsAndBoundaryScores(t *testing.T) {
	q := questions(t)
	for _, score := range []string{"0", "1"} {
		body := strings.ReplaceAll(validResponse(q), "0.91", score)
		body = strings.Replace(body, `{"answers":`, `{"metadata":{"future":true},"answers":`, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) }))
		_, err := New(srv.URL, srv.Client()).Evaluate(context.Background(), "test-value", testState(), q)
		srv.Close()
		if err != nil {
			t.Fatalf("valid additive response rejected: %v", err)
		}
	}
}
