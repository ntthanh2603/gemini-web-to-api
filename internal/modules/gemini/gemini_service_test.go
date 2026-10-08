package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gemini-web-to-api/internal/modules/gemini/dto"
	"gemini-web-to-api/internal/modules/providers"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"
)

func TestGeminiBuildPrompt(t *testing.T) {
	service := &GeminiService{log: zap.NewNop()}

	t.Run("single turn prompt", func(t *testing.T) {
		req := dto.GeminiGenerateRequest{
			Contents: []dto.Content{
				{
					Role:  "user",
					Parts: []dto.Part{{Text: "Hello, world!"}},
				},
			},
		}
		prompt, files, err := service.buildPrompt(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(files) != 0 {
			t.Errorf("expected 0 files, got %d", len(files))
		}
		if prompt != "Hello, world!" {
			t.Errorf("got %q, want %q", prompt, "Hello, world!")
		}
	})

	t.Run("multi-turn conversation with roles", func(t *testing.T) {
		req := dto.GeminiGenerateRequest{
			Contents: []dto.Content{
				{
					Role:  "user",
					Parts: []dto.Part{{Text: "Hi, who are you?"}},
				},
				{
					Role:  "model",
					Parts: []dto.Part{{Text: "I am Gemini."}},
				},
				{
					Role:  "user",
					Parts: []dto.Part{{Text: "What can you do?"}},
				},
			},
		}
		prompt, _, err := service.buildPrompt(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(prompt, "User: Hi, who are you?") {
			t.Errorf("missing User role prefix in prompt: %s", prompt)
		}
		if !strings.Contains(prompt, "Model: I am Gemini.") {
			t.Errorf("missing Model role prefix in prompt: %s", prompt)
		}
		if !strings.Contains(prompt, "User: What can you do?") {
			t.Errorf("missing second User role prefix in prompt: %s", prompt)
		}
	})

	t.Run("system instruction inclusion", func(t *testing.T) {
		req := dto.GeminiGenerateRequest{
			SystemInstruction: &dto.Content{
				Parts: []dto.Part{{Text: "You are a helpful coding assistant."}},
			},
			Contents: []dto.Content{
				{
					Role:  "user",
					Parts: []dto.Part{{Text: "Write a test."}},
				},
			},
		}
		prompt, _, err := service.buildPrompt(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(prompt, "System: You are a helpful coding assistant.") {
			t.Errorf("prompt does not start with System instruction: %s", prompt)
		}
		if !strings.Contains(prompt, "Write a test.") {
			t.Errorf("prompt does not contain user text: %s", prompt)
		}
	})
}

func TestVideoOperationMatchesGoogleGenAI(t *testing.T) {
	var gotAspect providers.VideoAspectRatio
	client := providers.NewVideoTestClient([]providers.ModelInfo{{ID: "gemini-pro"}},
		func(ctx context.Context, prompt, model string, aspect providers.VideoAspectRatio) (*providers.VideoResult, error) {
			gotAspect = aspect
			return &providers.VideoResult{Video: providers.Video{MimeType: "video/mp4"}, Data: []byte("mp4-bytes")}, nil
		})
	t.Cleanup(func() { _ = client.Close() })
	controller := NewGeminiController(NewGeminiService(client, zap.NewNop()), zap.NewNop())
	t.Cleanup(controller.Close)
	app := fiber.New()
	controller.Register(app.Group("/gemini/v1beta"))
	do := func(req *http.Request) (int, []byte) {
		resp, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}

	// Body shape sent by client.models.generate_videos.
	req := httptest.NewRequest(http.MethodPost, "/gemini/v1beta/models/veo-3.0-generate-001:predictLongRunning",
		strings.NewReader(`{"instances":[{"prompt":"A cat"}],"parameters":{"aspectRatio":"9:16","durationSeconds":8}}`))
	req.Header.Set("Content-Type", "application/json")
	status, body := do(req)
	var operation dto.VideoOperation
	if status != http.StatusOK || json.Unmarshal(body, &operation) != nil || !strings.HasPrefix(operation.Name, "models/veo-3.0-generate-001/operations/") {
		t.Fatalf("start: %d %s", status, body)
	}

	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && !operation.Done; time.Sleep(5 * time.Millisecond) {
		_, body = do(httptest.NewRequest(http.MethodGet, "/gemini/v1beta/"+operation.Name, nil))
		operation = dto.VideoOperation{}
		_ = json.Unmarshal(body, &operation)
	}
	if !operation.Done || operation.Response == nil || len(operation.Response.GenerateVideoResponse.GeneratedSamples) != 1 || gotAspect != providers.VideoAspectPortrait {
		t.Fatalf("operation: %s", body)
	}
	uri := operation.Response.GenerateVideoResponse.GeneratedSamples[0].Video.URI
	if !strings.HasSuffix(uri, ":download?alt=media") || !strings.Contains(uri, "/gemini/v1beta/files/") {
		t.Fatalf("unexpected uri %q", uri)
	}
	id := operation.Name[strings.LastIndex(operation.Name, "/")+1:]

	// files.download with an https URI requests files/{id}:download; with an
	// http URI it appends the whole URI to the path.
	for _, path := range []string{
		"/gemini/v1beta/files/" + id + ":download?alt=media",
		"/gemini/v1beta/files/http://localhost:4981/gemini/v1beta/files/" + id + ":download?alt=media:download?alt=media",
	} {
		if status, body := do(httptest.NewRequest(http.MethodGet, path, nil)); status != http.StatusOK || string(body) != "mp4-bytes" {
			t.Fatalf("download %s: %d %s", path, status, body)
		}
	}

	bad := httptest.NewRequest(http.MethodPost, "/gemini/v1beta/models/veo:predictLongRunning",
		strings.NewReader(`{"instances":[{"prompt":"A cat","image":{"bytesBase64Encoded":"AA=="}}]}`))
	bad.Header.Set("Content-Type", "application/json")
	if status, body := do(bad); status != http.StatusBadRequest {
		t.Fatalf("image-to-video must be rejected: %d %s", status, body)
	}
}
