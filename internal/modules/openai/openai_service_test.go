package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gemini-web-to-api/internal/modules/openai/dto"
	"gemini-web-to-api/internal/modules/providers"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"
)

func TestOpenAIToolBridgeParsing(t *testing.T) {
	service := &OpenAIService{log: zap.NewNop()}

	req := dto.ChatCompletionRequest{
		Tools: []dto.ToolDefinition{
			{
				Type: "function",
				Function: dto.ToolFunctionDefinition{
					Name: "get_current_weather",
				},
			},
		},
	}

	t.Run("parse valid tool call", func(t *testing.T) {
		input := "```json\n{\"status\":\"tool_use\",\"tool_calls\":[{\"name\":\"get_current_weather\",\"arguments\":{\"location\":\"Hanoi\"}}]}\n```"
		calls, content := service.parseToolBridgeOutput(req, input)
		if len(calls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(calls))
		}
		if calls[0].Function.Name != "get_current_weather" {
			t.Errorf("got name %q, want get_current_weather", calls[0].Function.Name)
		}
		if content != "" {
			t.Errorf("got content %q, want empty", content)
		}
	})

	t.Run("parse tool call with surrounding text", func(t *testing.T) {
		input := "I'll check the weather for Hanoi right now.\n{\"status\":\"tool_use\",\"tool_calls\":[{\"name\":\"get_current_weather\",\"arguments\":{\"location\":\"Hanoi\"}}]}\nDone."
		calls, _ := service.parseToolBridgeOutput(req, input)
		if len(calls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(calls))
		}
		if calls[0].Function.Name != "get_current_weather" {
			t.Errorf("got name %q, want get_current_weather", calls[0].Function.Name)
		}
	})

	t.Run("parse non-tool plain text", func(t *testing.T) {
		input := "The weather in Hanoi is nice and sunny today."
		calls, content := service.parseToolBridgeOutput(req, input)
		if len(calls) != 0 {
			t.Errorf("expected 0 tool calls, got %d", len(calls))
		}
		if content != input {
			t.Errorf("got content %q, want %q", content, input)
		}
	})
}

func newVideoTestApp(t *testing.T, release chan struct{}) *fiber.App {
	t.Helper()
	client := providers.NewVideoTestClient([]providers.ModelInfo{{ID: "gemini-pro"}},
		func(ctx context.Context, prompt, model string, aspect providers.VideoAspectRatio) (*providers.VideoResult, error) {
			<-release
			return &providers.VideoResult{Video: providers.Video{MimeType: "video/mp4", Width: 1280, Height: 720, Duration: 9.99},
				Data: []byte("mp4-bytes"), ConversationID: "c_test", Model: model}, nil
		})
	t.Cleanup(func() { _ = client.Close() })
	app := fiber.New()
	NewOpenAIController(NewOpenAIService(client, zap.NewNop()), zap.NewNop()).Register(app.Group("/openai/v1"))
	return app
}

func doVideoRequest(t *testing.T, app *fiber.App, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func TestVideosAPIMatchesOpenAISDK(t *testing.T) {
	release := make(chan struct{})
	app := newVideoTestApp(t, release)

	// The OpenAI SDK always sends multipart/form-data.
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	_ = writer.WriteField("prompt", "A cat")
	_ = writer.WriteField("model", "sora-2")
	_ = writer.WriteField("seconds", "8")
	_ = writer.WriteField("size", "1280x720")
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/videos", &form)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	status, body := doVideoRequest(t, app, req)
	var created dto.Video
	if status != http.StatusOK || json.Unmarshal(body, &created) != nil || !strings.HasPrefix(created.ID, "video_") ||
		created.Status != "in_progress" || created.Object != "video" || created.Model != "gemini-pro" || created.Size != "1280x720" {
		t.Fatalf("create: %d %s", status, body)
	}

	status, body = doVideoRequest(t, app, httptest.NewRequest(http.MethodGet, "/openai/v1/videos/"+created.ID+"/content", nil))
	if status != http.StatusConflict {
		t.Fatalf("content before completion: %d %s", status, body)
	}
	jsonReq := httptest.NewRequest(http.MethodPost, "/openai/v1/videos", strings.NewReader(`{"prompt":"Another"}`))
	jsonReq.Header.Set("Content-Type", "application/json")
	if status, body = doVideoRequest(t, app, jsonReq); status != http.StatusTooManyRequests {
		t.Fatalf("concurrent create: %d %s", status, body)
	}

	close(release)
	var video dto.Video
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		_, body = doVideoRequest(t, app, httptest.NewRequest(http.MethodGet, "/openai/v1/videos/"+created.ID, nil))
		if json.Unmarshal(body, &video) == nil && video.Status != "in_progress" {
			break
		}
	}
	if video.Status != "completed" || video.Progress != 100 || video.Seconds != "10" || video.CompletedAt == nil || video.ExpiresAt == nil {
		t.Fatalf("retrieve: %s", body)
	}

	status, body = doVideoRequest(t, app, httptest.NewRequest(http.MethodGet, "/openai/v1/videos/"+created.ID+"/content?variant=video", nil))
	if status != http.StatusOK || string(body) != "mp4-bytes" {
		t.Fatalf("content: %d %s", status, body)
	}
	status, body = doVideoRequest(t, app, httptest.NewRequest(http.MethodGet, "/openai/v1/videos", nil))
	var list dto.VideoList
	if status != http.StatusOK || json.Unmarshal(body, &list) != nil || len(list.Data) != 1 || *list.FirstID != created.ID {
		t.Fatalf("list: %d %s", status, body)
	}
	status, body = doVideoRequest(t, app, httptest.NewRequest(http.MethodDelete, "/openai/v1/videos/"+created.ID, nil))
	if status != http.StatusOK || !strings.Contains(string(body), `"deleted":true`) {
		t.Fatalf("delete: %d %s", status, body)
	}
	if status, _ = doVideoRequest(t, app, httptest.NewRequest(http.MethodGet, "/openai/v1/videos/"+created.ID, nil)); status != http.StatusNotFound {
		t.Fatalf("deleted video still retrievable: %d", status)
	}
}

func TestVideosAPIRejectsInvalidRequests(t *testing.T) {
	app := newVideoTestApp(t, make(chan struct{}))
	for _, body := range []string{`{"prompt":""}`, `{"prompt":"A cat","size":"640x480"}`, `{"prompt":"A cat","model":"unknown"}`} {
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/videos", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if status, resp := doVideoRequest(t, app, req); status != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, status, resp)
		}
	}
}
