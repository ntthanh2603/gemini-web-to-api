package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gemini-web-to-api/internal/modules/providers"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"
)

func TestGeminiThinkingLevelAcceptsEveryOpenAIReasoningEffort(t *testing.T) {
	for effort, want := range map[string]int{
		"": 1, "none": 1, "minimal": 1, "low": 1, "medium": 4, "high": 2, "xhigh": 2, " XHigh ": 2,
	} {
		got, err := providers.ParseThinkingLevel(geminiThinkingLevel(effort))
		if err != nil || got != want {
			t.Fatalf("reasoning_effort %q: level=%d err=%v, want %d", effort, got, err, want)
		}
	}
}

func TestChatCompletionsRejectInvalidReasoningEffortBeforeModelResolution(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			// An empty registry must not hide the more actionable request error.
			// This client has no credentials or HTTP transport and cannot call Gemini.
			service := NewOpenAIService(&providers.Client{}, zap.NewNop())
			app := fiber.New()
			NewOpenAIController(service, zap.NewNop()).Register(app.Group("/openai/v1"))
			input := fmt.Sprintf(`{"model":"gemini-3.6-flash","reasoning_effort":"ultra","stream":%t,"messages":[{"role":"user","content":"Hello"}]}`, streaming)
			req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(input))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", resp.StatusCode, body)
			}
			if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
				t.Fatalf("content type = %q, want JSON before opening SSE", resp.Header.Get("Content-Type"))
			}
			var result struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatal(err)
			}
			if result.Error.Type != "invalid_request_error" || !strings.Contains(result.Error.Message, "reasoning_effort") {
				t.Fatalf("expected reasoning_effort validation error, got %s", body)
			}
		})
	}
}
