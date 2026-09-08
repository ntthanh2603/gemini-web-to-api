package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/zap"
)

type generationTestTransport func(*http.Request) (*http.Response, error)

func (f generationTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func generationTestModels() []geminiModel {
	// Flash deliberately comes first: an explicit Pro request must not pick it.
	return []geminiModel{
		{ModelInfo: ModelInfo{ID: "gemini-flash"}, ModelID: "1111111111111111", Capacity: 2, CapacityField: 12, ModelNumber: 1},
		{ModelInfo: ModelInfo{ID: "gemini-pro"}, ModelID: "2222222222222222", Capacity: 2, CapacityField: 12, ModelNumber: 3},
	}
}

func stubGenerationTransport(t *testing.T, inspect func(*http.Request, []interface{})) {
	t.Helper()
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = generationTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Scheme+"://"+r.URL.Host+r.URL.Path != EndpointGenerate {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		var outer []interface{}
		if err := json.Unmarshal([]byte(r.PostForm.Get("f.req")), &outer); err != nil || len(outer) != 2 {
			t.Fatalf("invalid outer payload: %v", err)
		}
		encoded, ok := outer[1].(string)
		if !ok {
			t.Fatal("inner payload must be JSON encoded as a string")
		}
		var inner []interface{}
		if err := json.Unmarshal([]byte(encoded), &inner); err != nil || len(inner) != 81 {
			t.Fatalf("invalid generation payload: length=%d, error=%v", len(inner), err)
		}
		inspect(r, inner)
		payload, _ := json.Marshal([]interface{}{nil, "conversation-id", nil, nil,
			[]interface{}{[]interface{}{"choice-id", []interface{}{"ok"}}}})
		body, _ := json.Marshal([]interface{}{[]interface{}{"wrb.fr", nil, string(payload)}})
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})
}

func TestGenerateContentSendsSelectedModelToGemini(t *testing.T) {
	for _, temporary := range []bool{false, true} {
		for _, requested := range []string{"gemini-advanced", "gemini-pro", "gemini-flash"} {
			t.Run(fmt.Sprintf("%s/temporary=%t", requested, temporary), func(t *testing.T) {
				models := generationTestModels()
				want := models[1]
				if requested == "gemini-flash" {
					want = models[0]
				}
				client := &Client{at: "test-token", cookieHeader: "session=test-cookie", cachedModels: models,
					language: "en", buildLabel: "test-build", sessionID: "google-session", log: zap.NewNop(), defaultTemporary: temporary}
				var lastRequestID, generationID string
				requests := 0
				stubGenerationTransport(t, func(r *http.Request, inner []interface{}) {
					requests++
					var header []interface{}
					if err := json.Unmarshal([]byte(r.Header.Get(geminiModelHeaderKey)), &header); err != nil || len(header) != 17 {
						t.Fatalf("invalid model header: %v", err)
					}
					if header[4] != want.ModelID || header[11] != float64(want.Capacity) || header[14] != float64(want.ModelNumber) {
						t.Fatalf("wrong model routing: %v", header)
					}
					if inner[3] != nil || inner[79] != float64(want.ModelNumber) || inner[80] != float64(1) || header[7] != float64(0) {
						t.Fatalf("payload/header disagree on model selection: %v", header)
					}
					if temporary && inner[45] != float64(1) || !temporary && inner[45] != nil {
						t.Fatalf("unexpected temporary mode: %v", inner[45])
					}
					var requestHeader []interface{}
					if err := json.Unmarshal([]byte(r.Header.Get("x-goog-ext-525005358-jspb")), &requestHeader); err != nil || len(requestHeader) != 2 {
						t.Fatalf("missing request header for text-only generation: %v", err)
					}
					requestID, _ := inner[59].(string)
					if requestID == "" || requestID == lastRequestID || requestHeader[0] != requestID {
						t.Fatal("request UUID must be fresh and shared by the payload and request header")
					}
					lastRequestID = requestID
					session, _ := header[16].(string)
					if session == "" || session == "google-session" || session == requestID || generationID != "" && generationID != session {
						t.Fatal("model header must use a stable client session UUID")
					}
					generationID = session
					if r.Header.Get("Cookie") != "session=test-cookie" || r.PostForm.Get("at") != "test-token" ||
						r.URL.Query().Get("f.sid") != "google-session" || r.URL.Query().Get("bl") != "test-build" || r.URL.Query().Get("rt") != "c" {
						t.Fatal("missing session data on generation request")
					}
					if r.Header.Get("x-goog-ext-73010989-jspb") != "[0]" || r.Header.Get("x-goog-ext-73010990-jspb") != "[0,0,0]" {
						t.Fatal("missing auxiliary model headers")
					}
				})
				for range 2 {
					response, err := client.GenerateContent(context.Background(), "Hello", WithModel(requested))
					if err != nil || response.Text != "ok" {
						t.Fatalf("GenerateContent failed: response=%v error=%v", response, err)
					}
					if response.Model != want.ID || response.ModelID != want.ModelID {
						t.Fatalf("response omitted resolved model: %#v", response)
					}
				}
				if requests != 2 {
					t.Fatalf("expected two generation requests, got %d", requests)
				}
			})
		}
	}
}

func TestGenerateContentRejectsUnavailableProBeforeSending(t *testing.T) {
	stubGenerationTransport(t, func(*http.Request, []interface{}) { t.Fatal("unavailable Pro must not generate with Flash") })
	client := &Client{at: "test-token", cachedModels: generationTestModels()[:1], log: zap.NewNop()}
	for _, model := range []string{"gemini-advanced", "gemini-pro", "unknown-model"} {
		if _, err := client.GenerateContent(context.Background(), "Hello", WithModel(model)); err == nil {
			t.Fatalf("expected unavailable model error for %s", model)
		}
	}
}

func TestChatSessionUsesSelectedModelAndPreservesMetadata(t *testing.T) {
	models := generationTestModels()
	client := &Client{at: "test-token", cachedModels: models, log: zap.NewNop()}
	chat := client.StartChat(WithChatModel("gemini-advanced"), WithChatMetadata(&SessionMetadata{
		ConversationID: "old-conversation", ResponseID: "old-response", ChoiceID: "old-choice",
	}))
	requests := 0
	stubGenerationTransport(t, func(r *http.Request, inner []interface{}) {
		var header []interface{}
		if err := json.Unmarshal([]byte(r.Header.Get(geminiModelHeaderKey)), &header); err != nil {
			t.Fatal(err)
		}
		want := models[1]
		if requests == 1 {
			want = models[0] // Explicit per-message override.
		}
		if header[4] != want.ModelID || inner[79] != float64(want.ModelNumber) {
			t.Fatalf("chat ignored selected model: %v", header)
		}
		if requests == 0 && !reflect.DeepEqual(inner[2], []interface{}{"old-conversation", "old-response", "old-choice"}) {
			t.Fatalf("lost chat metadata: %v", inner[2])
		}
		requests++
	})
	if _, err := chat.SendMessage(context.Background(), "Hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.SendMessage(context.Background(), "Again", WithModel("gemini-flash")); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(chat.GetHistory()) != 4 || chat.GetMetadata().ConversationID != "conversation-id" {
		t.Fatal("chat requests/history/metadata were not updated")
	}
}

func TestBuildGenerateInnerPreservesUploadedFiles(t *testing.T) {
	inner := buildGenerateInner("Describe", []uploadedFile{{ID: "uploaded-id", Name: "image.png"}}, 3, "en", "request-id", false)
	want := []interface{}{"Describe", 0, nil, []interface{}{[]interface{}{[]interface{}{"uploaded-id"}, "image.png"}}, nil, nil, 0}
	if !reflect.DeepEqual(inner[0], want) || inner[79] != 3 {
		t.Fatal("model selection must preserve the uploaded-file payload")
	}
}

func TestChatSessionKeepsDefaultModelAcrossRegistryRefresh(t *testing.T) {
	models := generationTestModels()
	client := &Client{at: "test-token", cachedModels: models, log: zap.NewNop()}
	chat := client.StartChat()
	client.cachedModels = []geminiModel{models[1], models[0]}
	stubGenerationTransport(t, func(r *http.Request, inner []interface{}) {
		var header []interface{}
		if err := json.Unmarshal([]byte(r.Header.Get(geminiModelHeaderKey)), &header); err != nil {
			t.Fatal(err)
		}
		if header[4] != models[0].ModelID {
			t.Fatal("registry order changed an existing chat's model")
		}
	})
	if _, err := chat.SendMessage(context.Background(), "Hello"); err != nil {
		t.Fatal(err)
	}
}

func TestStartChatRestoresModelFromMetadata(t *testing.T) {
	client := &Client{cachedModels: generationTestModels()}
	chat := client.StartChat(WithChatMetadata(&SessionMetadata{Model: "gemini-advanced"}))
	if chat.GetMetadata().Model != "gemini-advanced" {
		t.Fatal("restored Pro conversation must not select default Flash")
	}
}

func TestBuildGeminiModelHeadersSupportsAlternateCapacityField(t *testing.T) {
	model := generationTestModels()[1]
	model.CapacityField = 13
	headers, err := buildGeminiModelHeaders(model, "request-id", "session-id")
	if err != nil {
		t.Fatal(err)
	}
	var header []interface{}
	if err := json.Unmarshal([]byte(headers[geminiModelHeaderKey]), &header); err != nil {
		t.Fatal(err)
	}
	// Fixture shape from the reference protocol's capacity-field-13 header.
	const wantJSON = `[1,null,null,null,"2222222222222222",null,null,0,[4,5,6,8],null,null,null,2,null,null,3,1,"session-id"]`
	var want []interface{}
	if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(header, want) {
		t.Fatalf("invalid alternate capacity header: %v", header)
	}
}

func TestResponseModelRecognizesOnlyUnambiguousProtocolEvidence(t *testing.T) {
	models := generationTestModels()
	client := &Client{cachedModels: models}
	got, ok := client.responseModel(`[["model","` + models[0].ModelID + `"]]`)
	if !ok || got.ModelID != models[0].ModelID {
		t.Fatalf("response model = %#v, %t", got, ok)
	}
	if _, ok := client.responseModel(models[0].ModelID + models[1].ModelID); ok {
		t.Fatal("multiple model IDs must be treated as ambiguous")
	}
	if _, ok := client.responseModel("no model metadata"); ok {
		t.Fatal("missing model ID must not be inferred")
	}
}
