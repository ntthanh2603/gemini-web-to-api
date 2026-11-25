package providers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// rejectedStream is the whole reply Gemini Web sends for a prompt it refuses.
const rejectedStream = ")]}'\n\n38\n[[\"wrb.fr\",null,null,null,null,[3]]]\n57\n[[\"di\",188],[\"af.httprm\",188,\"2427941723060579906\",28]]\n25\n[[\"e\",4,null,null,132]]\n"

func TestParseResponseReportsRejection(t *testing.T) {
	c := &Client{log: zap.NewNop()}
	_, err := c.parseResponse(rejectedStream)
	var rejected *RequestRejectedError
	if !errors.As(err, &rejected) || rejected.Code != 3 {
		t.Fatalf("parseResponse error = %v, want RequestRejectedError code 3", err)
	}
	if rejected.Retryable() || !rejected.IsInvalidRequest() {
		t.Fatal("status 3 must be final and the caller's to fix")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Fatalf("error %q does not point at the prompt length", err)
	}
}

func TestExtractRequestRejectionIgnoresOtherFrames(t *testing.T) {
	for name, item := range map[string][]interface{}{
		"answer frame":     {"wrb.fr", nil, "[null]", nil, nil, []interface{}{float64(3)}},
		"other tag":        {"di", nil, nil, nil, nil, []interface{}{float64(3)}},
		"short frame":      {"wrb.fr", nil, nil},
		"no status":        {"wrb.fr", nil, nil, nil, nil, nil},
		"non-number code":  {"wrb.fr", nil, nil, nil, nil, []interface{}{"3"}},
		"fractional code":  {"wrb.fr", nil, nil, nil, nil, []interface{}{3.5}},
		"zero status code": {"wrb.fr", nil, nil, nil, nil, []interface{}{float64(0)}},
	} {
		if got := extractRequestRejection(item); got != nil {
			t.Errorf("%s: got rejection %+v, want none", name, got)
		}
	}
	if got := extractRequestRejection([]interface{}{"wrb.fr", nil, nil, nil, nil, []interface{}{float64(8)}}); got == nil || !got.Retryable() {
		t.Errorf("status 8: got %+v, want a retryable rejection", got)
	}
}

// A request Gemini Web refuses with status 3 is sent once, not
// GEMINI_MAX_RETRIES times.
func TestGenerateContentDoesNotRetryInvalidArgument(t *testing.T) {
	attempts := 0
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = generationTestTransport(func(r *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(rejectedStream)), Request: r}, nil
	})

	client := &Client{at: "test-token", cookieHeader: "session=test-cookie", cachedModels: generationTestModels(),
		language: "en", sessionID: "google-session", log: zap.NewNop(), maxRetries: 3}
	_, err := client.GenerateContent(context.Background(), "Hello", WithModel("gemini-flash"))
	var rejected *RequestRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("GenerateContent error = %v, want RequestRejectedError", err)
	}
	if attempts != 1 {
		t.Fatalf("sent %d requests, want 1", attempts)
	}
}
