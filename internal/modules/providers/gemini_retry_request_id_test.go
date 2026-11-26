package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// A retry must look like the web UI's Redo: a new request UUID in the payload
// and the request header, not a byte-identical copy of the failed request.
func TestGenerateContentRetryUsesFreshRequestID(t *testing.T) {
	type attempt struct {
		payloadID, headerID, sessionID, body string
	}
	var attempts []attempt

	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = generationTestTransport(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		var outer []interface{}
		if err := json.Unmarshal([]byte(r.PostForm.Get("f.req")), &outer); err != nil || len(outer) != 2 {
			t.Fatalf("invalid outer payload: %v", err)
		}
		var inner []interface{}
		if err := json.Unmarshal([]byte(outer[1].(string)), &inner); err != nil {
			t.Fatalf("invalid inner payload: %v", err)
		}
		var requestHeader, modelHeader []interface{}
		if err := json.Unmarshal([]byte(r.Header.Get("x-goog-ext-525005358-jspb")), &requestHeader); err != nil || len(requestHeader) == 0 {
			t.Fatalf("missing request header: %v", err)
		}
		if err := json.Unmarshal([]byte(r.Header.Get(geminiModelHeaderKey)), &modelHeader); err != nil || len(modelHeader) != 17 {
			t.Fatalf("invalid model header: %v", err)
		}
		payloadID, _ := inner[59].(string)
		headerID, _ := requestHeader[0].(string)
		sessionID, _ := modelHeader[16].(string)
		attempts = append(attempts, attempt{payloadID: payloadID, headerID: headerID, sessionID: sessionID, body: string(raw)})

		if len(attempts) == 1 {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader("busy")), Request: r}, nil
		}
		payload, _ := json.Marshal([]interface{}{nil, "conversation-id", nil, nil,
			[]interface{}{[]interface{}{"choice-id", []interface{}{"ok"}}}})
		body, _ := json.Marshal([]interface{}{[]interface{}{"wrb.fr", nil, string(payload)}})
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})

	client := &Client{at: "test-token", cookieHeader: "session=test-cookie", cachedModels: generationTestModels(),
		language: "en", sessionID: "google-session", log: zap.NewNop(), maxRetries: 3}
	response, err := client.GenerateContent(context.Background(), "Hello", WithModel("gemini-flash"))
	if err != nil || response.Text != "ok" {
		t.Fatalf("GenerateContent: response=%v err=%v", response, err)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(attempts))
	}
	first, retry := attempts[0], attempts[1]
	for i, a := range attempts {
		if a.payloadID == "" || a.payloadID != a.headerID {
			t.Errorf("attempt %d: payload and header request IDs disagree (%q vs %q)", i+1, a.payloadID, a.headerID)
		}
	}
	if retry.payloadID == first.payloadID {
		t.Errorf("retry reused request ID %q", first.payloadID)
	}
	if retry.body == first.body {
		t.Error("retry sent a byte-identical request body")
	}
	if retry.sessionID == "" || retry.sessionID != first.sessionID {
		t.Errorf("client session UUID must stay stable across retries: %q vs %q", first.sessionID, retry.sessionID)
	}
}
