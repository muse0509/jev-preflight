package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type liveFakeTransport func(*http.Request) (*http.Response, error)

func (fake liveFakeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return fake(request)
}

func liveFakeResponse(status int, body, retryAfter string) *http.Response {
	header := make(http.Header)
	if retryAfter != "" {
		header.Set("Retry-After", retryAfter)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func liveValidBody(t *testing.T) string {
	t.Helper()
	return strings.TrimSuffix(validResponse(questions(t)), "}") + `,"model":"jev-1.13.0","usage":{"input_tokens":100,"output_tokens":16}}`
}

func liveFakeProbe(t *testing.T, next http.RoundTripper, wait func(context.Context, time.Duration) error) (liveResult, *liveCounts, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), liveBudget)
	defer cancel()
	counts := new(liveCounts)
	result, err := runLiveProbe(ctx, "fixture-"+strings.Repeat("x", 20), next, counts, time.Now, wait)
	return result, counts, err
}

func TestLiveHarnessGates(t *testing.T) {
	for _, tt := range []struct {
		name, optIn, key string
		built, allow     bool
	}{
		{"tag missing", "1", "fixture-value", false, false},
		{"opt-in missing", "", "fixture-value", true, false},
		{"opt-in not exact", "true", "fixture-value", true, false},
		{"key missing", "1", "", true, false},
		{"key whitespace", "1", "  ", true, false},
		{"explicit opt-in", "1", "fixture-value", true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reason := liveSkipReason(tt.built, tt.optIn, tt.key)
			if (reason == "") != tt.allow || strings.Contains(reason, "fixture-value") {
				t.Fatal("incorrect or unsafe live gate")
			}
		})
	}
	if !liveTestsBuilt && liveSkipReason(liveTestsBuilt, "1", "fixture-value") == "" {
		t.Fatal("untagged build permitted live access with inherited environment")
	}
}

func TestLiveHarnessTransportUsesFreshHTTP1WithoutRetries(t *testing.T) {
	var mu sync.Mutex
	var protocols []int
	var connections []string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		protocols = append(protocols, r.ProtoMajor)
		connections = append(connections, r.RemoteAddr)
		mu.Unlock()
		switch r.URL.Path {
		case "/close":
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("expected an HTTP/1 connection")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			connection, _, err := hijacker.Hijack()
			if err != nil {
				t.Error("cannot close synthetic connection")
				return
			}
			_ = connection.Close()
		case "/rate-limit":
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	transport := newLiveTransport()
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for _, path := range []string{"/ok", "/ok", "/rate-limit", "/close"} {
		request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader("synthetic"))
		if err != nil {
			t.Fatal("cannot create local transport request")
		}
		response, err := client.Do(request)
		if path == "/close" {
			if err == nil {
				_ = response.Body.Close()
				t.Fatal("closed connection unexpectedly succeeded")
			}
			continue
		}
		if err != nil {
			t.Fatal("local transport request failed")
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		wantStatus := http.StatusOK
		if path == "/rate-limit" {
			wantStatus = http.StatusTooManyRequests
		}
		if response.ProtoMajor != 1 || response.StatusCode != wantStatus {
			t.Fatal("transport negotiated HTTP/2 or changed the response")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(protocols) != 4 {
		t.Fatal("transport resent an HTTP request")
	}
	seen := make(map[string]bool)
	for i, protocol := range protocols {
		if protocol != 1 || seen[connections[i]] {
			t.Fatal("transport used HTTP/2 or reused a connection")
		}
		seen[connections[i]] = true
	}
}

func TestLiveHarnessSuccessUsesProductionClient(t *testing.T) {
	valid := liveValidBody(t)
	key := "fixture-" + strings.Repeat("x", 20)
	expectedQuestions := questions(t)
	const expectedDiff = "diff --git a/synthetic/check.go b/synthetic/check.go\n--- a/synthetic/check.go\n+++ b/synthetic/check.go\n@@ -1,4 +1,4 @@\n package synthetic\n func allowed(owner, actor string) bool {\n-\treturn owner == actor\n+\treturn true\n }\n"
	next := liveFakeTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Fatal("missing credential header")
		}
		if r.Method == http.MethodGet {
			return liveFakeResponse(200, `{"models":[{"name":"jev-latest","description":"fixture","release_date":"2026-01-01"}]}`, ""), nil
		}
		var body request
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "jev-latest" || body.State.Format != "unified_diff" || len(body.State.Files) != 1 || body.State.Files[0] != "synthetic/check.go" || body.State.Diff != expectedDiff || !reflect.DeepEqual(body.Questions, expectedQuestions) {
			t.Fatal("unexpected live request contract")
		}
		for _, question := range body.Questions {
			if question.Type != "noul" {
				t.Fatal("live probe changed question type")
			}
		}
		return liveFakeResponse(200, valid, ""), nil
	})
	result, counts, err := liveFakeProbe(t, next, func(context.Context, time.Duration) error {
		t.Fatal("successful request attempted a retry")
		return nil
	})
	if err != nil || counts.get != 1 || counts.post != 1 || !counts.modelsOK || len(result.scores) != 8 || *result.metadata.Usage.Input != 100 || *result.metadata.Usage.Output != 16 {
		t.Fatal("successful fake live probe failed")
	}
}

func TestLiveHarnessCredentialFailuresNeverPost(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 529} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			private := "private-" + strings.Repeat("s", 20)
			next := liveFakeTransport(func(*http.Request) (*http.Response, error) {
				return liveFakeResponse(status, private, "0"), nil
			})
			_, counts, err := liveFakeProbe(t, next, func(context.Context, time.Duration) error {
				t.Fatal("credential failure retried")
				return nil
			})
			if err == nil || strings.Contains(err.Error(), private) || counts.get != 1 || counts.post != 0 || counts.modelsOK {
				t.Fatal("credential failure did not stop safely")
			}
		})
	}
	for _, body := range []string{`invalid`, `{"models":[]}`, `{"models":[{"name":"jev-preview"}]}`, `{"models":null}`} {
		_, counts, err := liveFakeProbe(t, liveFakeTransport(func(*http.Request) (*http.Response, error) {
			return liveFakeResponse(200, body, ""), nil
		}), liveWait)
		if err == nil || counts.get != 1 || counts.post != 0 {
			t.Fatal("invalid model listing did not stop before evaluation")
		}
	}
}

func TestLiveHarnessRetryIsLimitedAndSelective(t *testing.T) {
	valid := liveValidBody(t)
	for _, tt := range []struct {
		name, retryAfter string
		first, second    int
		posts, waits     int
		success          bool
	}{
		{"429 once", "0", 429, 200, 2, 1, true},
		{"529 once", time.Now().Add(time.Second).UTC().Format(http.TimeFormat), 529, 200, 2, 1, true},
		{"429 twice", "0", 429, 429, 2, 1, false},
		{"529 twice", "0", 529, 529, 2, 1, false},
		{"401", "0", 401, 200, 1, 0, false},
		{"403", "0", 403, 200, 1, 0, false},
		{"500", "0", 500, 200, 1, 0, false},
		{"missing delay", "", 429, 200, 1, 0, false},
		{"invalid delay", "later", 529, 200, 1, 0, false},
		{"excess delay", "31", 429, 200, 1, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			posts, waits := 0, 0
			next := liveFakeTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet {
					return liveFakeResponse(200, `{"models":[{"name":"jev-latest"}]}`, ""), nil
				}
				posts++
				status := tt.first
				if posts > 1 {
					status = tt.second
				}
				return liveFakeResponse(status, valid, tt.retryAfter), nil
			})
			_, counts, err := liveFakeProbe(t, next, func(context.Context, time.Duration) error {
				waits++
				return nil
			})
			if (err == nil) != tt.success || counts.get != 1 || counts.post != tt.posts || posts != tt.posts || waits != tt.waits || !counts.modelsOK {
				t.Fatal("incorrect retry outcome or request count")
			}
		})
	}
}

func TestLiveHarnessRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	deadline := now.Add(5 * time.Second)
	for _, tt := range []struct {
		value string
		want  time.Duration
		valid bool
	}{
		{"0", 0, true}, {"2", 2 * time.Second, true},
		{now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second, true},
		{now.Add(-time.Second).Format(http.TimeFormat), 0, true},
		{"5", 0, false}, {"6", 0, false}, {"-1", 0, false},
		{"1.5", 0, false}, {"", 0, false}, {"tomorrow", 0, false},
		{"999999999999999999999999", 0, false},
	} {
		delay, valid := liveRetryDelay(tt.value, now, deadline)
		if valid != tt.valid || (valid && delay != tt.want) {
			t.Fatal("incorrect Retry-After interpretation")
		}
	}
}

func TestLiveHarnessCancellationPreventsAdditionalAttempts(t *testing.T) {
	for _, stage := range []string{"before GET", "after GET", "retry wait"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), liveBudget)
			defer cancel()
			if stage == "before GET" {
				cancel()
			}
			counts := new(liveCounts)
			next := liveFakeTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet {
					if stage == "after GET" {
						cancel()
					}
					return liveFakeResponse(200, `{"models":[{"name":"jev-latest"}]}`, ""), nil
				}
				return liveFakeResponse(429, "", "0"), nil
			})
			_, err := runLiveProbe(ctx, "fixture-value", next, counts, time.Now, func(context.Context, time.Duration) error {
				cancel()
				return nil
			})
			wantGet, wantPost := 1, 0
			if stage == "before GET" {
				wantGet = 0
			}
			if stage == "retry wait" {
				wantPost = 1
			}
			if err == nil || counts.get != wantGet || counts.post != wantPost {
				t.Fatal("cancellation allowed another request")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if liveWait(ctx, time.Hour) == nil {
		t.Fatal("retry wait ignored cancellation")
	}
}

func TestLiveHarnessMetadataValidationAndSafeModel(t *testing.T) {
	valid := liveValidBody(t)
	for _, body := range []string{
		strings.Replace(valid, `"model":"jev-1.13.0"`, `"model":null`, 1),
		strings.Replace(valid, `"model":"jev-1.13.0"`, `"model":" "`, 1),
		strings.Replace(valid, `"usage":{"input_tokens":100,"output_tokens":16}`, `"usage":null`, 1),
		strings.Replace(valid, `"input_tokens":100`, `"input_tokens":-1`, 1),
		strings.Replace(valid, `"input_tokens":100`, `"input_tokens":1.5`, 1),
		strings.Replace(valid, `"input_tokens":100`, `"input_tokens":"100"`, 1),
		strings.Replace(valid, `"output_tokens":16`, `"output_tokens":null`, 1),
		`{"model":"jev-latest","usage":{"input_tokens":0,"output_tokens":0},"answers":{}}`,
		valid + `{}`,
	} {
		if _, err := parseLiveMetadata([]byte(body)); err == nil || err.Error() != "metadata_invalid" {
			t.Fatal("invalid metadata accepted or exposed")
		}
	}
	for _, body := range []string{valid, strings.Replace(valid, `"input_tokens":100`, `"input_tokens":0`, 1)} {
		if _, err := parseLiveMetadata([]byte(body)); err != nil {
			t.Fatal("valid metadata rejected")
		}
	}
	key := "fixture-" + strings.Repeat("p", 20)
	for _, model := range []string{key, "jev-" + key, "jev-latest\nprivate", strings.Repeat("a", 100), "unknown"} {
		if safeLiveModel(model, key) != "[unrecognized-model]" {
			t.Fatal("unsafe model could be logged")
		}
	}
	if safeLiveModel("jev-1.13.0", "1.13.0") != "[unrecognized-model]" || safeLiveModel("jev-1.13.0", key) != "jev-1.13.0" {
		t.Fatal("model sanitization failed")
	}
}

func TestLiveHarnessEndpointGuardAndNoRedirect(t *testing.T) {
	for _, url := range []string{"http://api.typesafe.ai/v1/models", "https://other.invalid/v1/models", liveModelsEndpoint + "?extra=1", liveModelsEndpoint + "#fragment", "https://user@api.typesafe.ai/v1/models"} {
		counts := new(liveCounts)
		capture := &liveCapture{counts: counts, next: liveFakeTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("forbidden endpoint dispatched")
			return nil, nil
		})}
		request, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatal("invalid endpoint fixture")
		}
		if _, err := capture.RoundTrip(request); err == nil || counts.get != 0 || counts.post != 0 {
			t.Fatal("endpoint guard failed")
		}
	}
	forwarded := 0
	_, counts, err := liveFakeProbe(t, liveFakeTransport(func(*http.Request) (*http.Response, error) {
		forwarded++
		response := liveFakeResponse(http.StatusTemporaryRedirect, "", "")
		response.Header.Set("Location", "https://other.invalid/private")
		return response, nil
	}), liveWait)
	if err == nil || forwarded != 1 || counts.get != 1 || counts.post != 0 {
		t.Fatal("redirect was followed")
	}
}

func TestLiveHarnessBoundsBodiesAndSanitizesErrors(t *testing.T) {
	private := "private-" + strings.Repeat("q", 20)
	invalidMetadata := strings.Replace(liveValidBody(t), `"usage":{"input_tokens":100,"output_tokens":16}`, `"usage":null`, 1)
	for _, next := range []liveFakeTransport{
		func(*http.Request) (*http.Response, error) { return nil, errors.New(private) },
		func(*http.Request) (*http.Response, error) {
			return liveFakeResponse(200, strings.Repeat("s", maxResponseBytes+1), ""), nil
		},
		func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return liveFakeResponse(200, `{"models":[{"name":"jev-latest"}]}`, ""), nil
			}
			return liveFakeResponse(200, invalidMetadata, ""), nil
		},
		func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return liveFakeResponse(200, `{"models":[{"name":"jev-latest"}]}`, ""), nil
			}
			return liveFakeResponse(200, private, ""), nil
		},
		func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return liveFakeResponse(200, `{"models":[{"name":"jev-latest"}]}`, ""), nil
			}
			return liveFakeResponse(200, strings.Repeat("s", maxResponseBytes+1), ""), nil
		},
	} {
		_, counts, err := liveFakeProbe(t, next, liveWait)
		if err == nil || strings.Contains(err.Error(), private) || counts.get != 1 || counts.post > 1 {
			t.Fatal("body limit or error sanitization failed")
		}
	}
}
