package gemini

import (
	"strings"
	"testing"

	"gemini-web-to-api/internal/modules/gemini/dto"

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
