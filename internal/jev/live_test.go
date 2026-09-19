package jev

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muse0509/jev-preflight/internal/policy"
)

const liveModelsEndpoint = "https://api.typesafe.ai/v1/models"
const liveBudget = 30 * time.Second

type liveFailure string

func (failure liveFailure) Error() string { return string(failure) }

type liveCounts struct {
	get, post int
	modelsOK  bool
}

type liveMetadata struct {
	Model *string `json:"model"`
	Usage *struct {
		Input  *int64 `json:"input_tokens"`
		Output *int64 `json:"output_tokens"`
	} `json:"usage"`
	Answers map[string]json.RawMessage `json:"answers"`
}

type liveResult struct {
	scores   map[string]float64
	metadata liveMetadata
}

// This test cannot call the network in an ordinary, untagged test run.
func TestLiveTypeSafe(t *testing.T) {
	counts := new(liveCounts)
	defer func() {
		t.Logf("requests GET=%d POST=%d total=%d", counts.get, counts.post, counts.get+counts.post)
	}()
	if !liveTestsBuilt {
		t.Skip(liveSkipReason(false, "", ""))
	}
	optIn := os.Getenv("JEV_PREFLIGHT_LIVE_TEST")
	if optIn != "1" {
		t.Skip(liveSkipReason(true, optIn, ""))
	}
	key := strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY"))
	if reason := liveSkipReason(true, optIn, key); reason != "" {
		t.Skip(reason)
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveBudget)
	defer cancel()
	transport := newLiveTransport()
	defer transport.CloseIdleConnections()
	start := time.Now()
	result, err := runLiveProbe(ctx, key, transport, counts, time.Now, liveWait)
	if counts.modelsOK {
		t.Log("credential check: GET /v1/models HTTP 200; jev-latest available")
	}
	if err != nil {
		t.Fatalf("live validation failed: %s", err)
	}
	t.Logf("model=%s input_tokens=%d output_tokens=%d elapsed_ms=%d",
		safeLiveModel(*result.metadata.Model, key), *result.metadata.Usage.Input,
		*result.metadata.Usage.Output, time.Since(start).Milliseconds())
	ids := make([]string, 0, len(result.scores))
	for id := range result.scores {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		t.Logf("axis=%s noul=%.6f", id, result.scores[id])
	}
}

func newLiveTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// HTTP/2 can replay requests below our counter. Use fresh HTTP/1 connections
	// so only the explicit, counted 429/529 retry can submit another request.
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	transport.DisableKeepAlives = true
	transport.MaxResponseHeaderBytes = 16 << 10
	return transport
}

func liveSkipReason(built bool, optIn, key string) string {
	if !built {
		return "live validation disabled: build tag jev_live is required"
	}
	if optIn != "1" {
		return "live validation disabled: JEV_PREFLIGHT_LIVE_TEST=1 is required"
	}
	if strings.TrimSpace(key) == "" {
		return "live validation blocked: TYPESAFE_API_KEY is unavailable"
	}
	return ""
}

// Capture is test-only, bounded, and retained only in memory for metadata checks.
type liveCapture struct {
	next       http.RoundTripper
	counts     *liveCounts
	postBody   []byte
	postStatus int
	retryAfter string
}

func (capture *liveCapture) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Context().Err() != nil {
		return nil, liveFailure("request_cancelled")
	}
	if request.URL == nil || request.URL.User != nil {
		return nil, liveFailure("endpoint_rejected")
	}
	switch {
	case request.Method == http.MethodGet && request.URL.String() == liveModelsEndpoint:
		if capture.counts.get != 0 {
			return nil, liveFailure("request_limit")
		}
		capture.counts.get++
	case request.Method == http.MethodPost && request.URL.String() == Endpoint:
		if capture.counts.get != 1 || capture.counts.post >= 2 {
			return nil, liveFailure("request_limit")
		}
		capture.counts.post++
		capture.postBody, capture.postStatus, capture.retryAfter = nil, 0, ""
	default:
		return nil, liveFailure("endpoint_rejected")
	}
	response, err := capture.next.RoundTrip(request)
	if err != nil {
		return nil, liveFailure("network")
	}
	if response == nil || response.Body == nil {
		return nil, liveFailure("invalid_response")
	}
	if request.Method == http.MethodPost {
		capture.postStatus = response.StatusCode
		capture.retryAfter = response.Header.Get("Retry-After")
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		response.Body = io.NopCloser(strings.NewReader(""))
		return response, nil
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, liveFailure("response_read")
	}
	if len(body) > maxResponseBytes {
		return nil, liveFailure("response_too_large")
	}
	if request.Method == http.MethodPost {
		capture.postBody = body
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}

func runLiveProbe(ctx context.Context, key string, next http.RoundTripper, counts *liveCounts, now func() time.Time, wait func(context.Context, time.Duration) error) (liveResult, error) {
	if strings.TrimSpace(key) == "" {
		return liveResult{}, liveFailure("missing_key")
	}
	if _, ok := ctx.Deadline(); !ok {
		return liveResult{}, liveFailure("missing_deadline")
	}
	ctx, cancel := context.WithTimeout(ctx, liveBudget)
	defer cancel()
	if ctx.Err() != nil {
		return liveResult{}, liveFailure("request_cancelled")
	}
	capture := &liveCapture{next: next, counts: counts}
	client := &http.Client{Transport: capture, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, liveModelsEndpoint, nil)
	if err != nil {
		return liveResult{}, liveFailure("models_request")
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return liveResult{}, liveFailure("models_network")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return liveResult{}, liveFailure(fmt.Sprintf("models_http_%d", response.StatusCode))
	}
	var listing struct {
		Models []struct{ Name string } `json:"models"`
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes || json.Unmarshal(body, &listing) != nil {
		return liveResult{}, liveFailure("models_invalid")
	}
	available := false
	for _, model := range listing.Models {
		available = available || model.Name == "jev-latest"
	}
	if !available {
		return liveResult{}, liveFailure("jev_unavailable")
	}
	counts.modelsOK = true
	pol, err := policy.Load()
	if err != nil {
		return liveResult{}, liveFailure("policy_invalid")
	}
	state := State{Format: "unified_diff", Files: []string{"synthetic/check.go"}, Diff: "diff --git a/synthetic/check.go b/synthetic/check.go\n--- a/synthetic/check.go\n+++ b/synthetic/check.go\n@@ -1,4 +1,4 @@\n package synthetic\n func allowed(owner, actor string) bool {\n-\treturn owner == actor\n+\treturn true\n }\n"}
	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil {
			return liveResult{}, liveFailure("evaluation_cancelled")
		}
		scores, err := New(Endpoint, client).Evaluate(ctx, key, state, pol.Questions)
		if err == nil {
			metadata, err := parseLiveMetadata(capture.postBody)
			if err != nil || len(scores) != 8 {
				return liveResult{}, liveFailure("metadata_invalid")
			}
			return liveResult{scores: scores, metadata: metadata}, nil
		}
		if attempt != 0 || (capture.postStatus != 429 && capture.postStatus != 529) {
			var failure *Error
			if errors.As(err, &failure) {
				return liveResult{}, liveFailure("evaluation_" + failure.Class)
			}
			return liveResult{}, liveFailure("evaluation_failed")
		}
		deadline, _ := ctx.Deadline()
		delay, ok := liveRetryDelay(capture.retryAfter, now(), deadline)
		if !ok {
			return liveResult{}, liveFailure("retry_unavailable")
		}
		if err := wait(ctx, delay); err != nil {
			return liveResult{}, liveFailure("retry_cancelled")
		}
	}
	return liveResult{}, liveFailure("evaluation_failed")
}

func parseLiveMetadata(body []byte) (liveMetadata, error) {
	var metadata liveMetadata
	if json.Unmarshal(body, &metadata) != nil || metadata.Model == nil || strings.TrimSpace(*metadata.Model) == "" || metadata.Usage == nil || metadata.Usage.Input == nil || metadata.Usage.Output == nil || *metadata.Usage.Input < 0 || *metadata.Usage.Output < 0 || len(metadata.Answers) != 8 {
		return liveMetadata{}, liveFailure("metadata_invalid")
	}
	return metadata, nil
}

func liveRetryDelay(value string, now, deadline time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	var delay time.Duration
	seconds, err := strconv.ParseUint(value, 10, 64)
	if err == nil {
		if seconds > uint64(liveBudget/time.Second) {
			return 0, false
		}
		delay = time.Duration(seconds) * time.Second
	} else {
		date, err := http.ParseTime(value)
		if err != nil {
			return 0, false
		}
		delay = date.Sub(now)
		if delay < 0 {
			delay = 0
		}
	}
	return delay, deadline.After(now.Add(delay))
}

func liveWait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return liveFailure("retry_cancelled")
	case <-timer.C:
		return nil
	}
}

var liveModelPattern = regexp.MustCompile(`^jev-(latest|preview|[0-9]{1,4}(\.[0-9]{1,4}){1,3})$`)

func safeLiveModel(model, key string) string {
	if len(model) > 64 || (key != "" && strings.Contains(model, key)) || !liveModelPattern.MatchString(model) {
		return "[unrecognized-model]"
	}
	return model
}
