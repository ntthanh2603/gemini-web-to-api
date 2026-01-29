package handlers

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"ai-bridges/internal/models"
	"ai-bridges/internal/providers"
	"ai-bridges/internal/providers/gemini"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

type GeminiHandler struct {
	client *gemini.Client
	log    *zap.Logger
	mu     sync.RWMutex
}

func NewGeminiHandler(client *gemini.Client) *GeminiHandler {
	return &GeminiHandler{
		client: client,
		log:    zap.NewNop(), // Will be injected via wire if needed
	}
}

// SetLogger sets the logger for this handler (for dependency injection)
func (h *GeminiHandler) SetLogger(log *zap.Logger) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.log = log
}

// --- Official Gemini API (v1beta) ---

// HandleV1BetaModels returns the list of models in Gemini format
func (h *GeminiHandler) HandleV1BetaModels(c *fiber.Ctx) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	availableModels := h.client.ListModels()
	var geminiModels []models.GeminiModel
	for _, m := range availableModels {
		geminiModels = append(geminiModels, models.GeminiModel{
			Name:                       "models/" + m.ID,
			DisplayName:                m.ID,
			SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent"},
		})
	}
	return c.JSON(models.GeminiModelsResponse{Models: geminiModels})
}

// HandleV1BetaGenerateContent handles the official Gemini generateContent endpoint
func (h *GeminiHandler) HandleV1BetaGenerateContent(c *fiber.Ctx) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	model := c.Params("model")
	var req models.GeminiGenerateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(errorToResponse(fmt.Errorf("invalid request body: %w", err), "invalid_request_error"))
	}

	// Extract prompt from contents
	var promptBuilder strings.Builder
	for _, content := range req.Contents {
		for _, part := range content.Parts {
			if part.Text != "" {
				promptBuilder.WriteString(part.Text)
				promptBuilder.WriteString("\n")
			}
		}
	}

	prompt := strings.TrimSpace(promptBuilder.String())
	if prompt == "" {
		return c.Status(fiber.StatusBadRequest).JSON(errorToResponse(fmt.Errorf("empty content"), "invalid_request_error"))
	}

	opts := []providers.GenerateOption{providers.WithModel(model)}

	// Add timeout to context
	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Minute)
	defer cancel()

	response, err := h.client.GenerateContent(ctx, prompt, opts...)
	if err != nil {
		h.log.Error("GenerateContent failed", zap.Error(err), zap.String("model", model))
		return c.Status(fiber.StatusInternalServerError).JSON(errorToResponse(err, "api_error"))
	}

	return c.JSON(models.GeminiGenerateResponse{
		Candidates: []models.Candidate{
			{
				Index: 0,
				Content: models.Content{
					Role:  "model",
					Parts: []models.Part{{Text: response.Text}},
				},
				FinishReason: "STOP",
			},
		},
		UsageMetadata: &models.UsageMetadata{
			TotalTokenCount: 0,
		},
	})
}

// HandleV1BetaStreamGenerateContent handles the official Gemini streaming endpoint
func (h *GeminiHandler) HandleV1BetaStreamGenerateContent(c *fiber.Ctx) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	model := c.Params("model")
	var req models.GeminiGenerateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(errorToResponse(fmt.Errorf("invalid request body: %w", err), "invalid_request_error"))
	}

	var promptBuilder strings.Builder
	for _, content := range req.Contents {
		for _, part := range content.Parts {
			if part.Text != "" {
				promptBuilder.WriteString(part.Text)
				promptBuilder.WriteString("\n")
			}
		}
	}

	prompt := strings.TrimSpace(promptBuilder.String())
	if prompt == "" {
		return c.Status(fiber.StatusBadRequest).JSON(errorToResponse(fmt.Errorf("empty content"), "invalid_request_error"))
	}

	opts := []providers.GenerateOption{providers.WithModel(model)}

	c.Set("Content-Type", "application/json")
	c.Set("Transfer-Encoding", "chunked")

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		// Add timeout to context
		ctx, cancel := context.WithTimeout(c.Context(), 5*time.Minute)
		defer cancel()

		resp, err := h.client.GenerateContent(ctx, prompt, opts...)
		if err != nil {
			h.log.Error("GenerateContent streaming failed", zap.Error(err), zap.String("model", model))
			errResponse := errorToResponse(err, "api_error")
			_ = sendStreamChunk(w, h.log, errResponse)
			return
		}

		chunks := splitResponseIntoChunks(resp.Text, 30)
		for i, content := range chunks {
			chunk := models.GeminiGenerateResponse{
				Candidates: []models.Candidate{
					{
						Index: 0,
						Content: models.Content{
							Role:  "model",
							Parts: []models.Part{{Text: content}},
						},
					},
				},
			}

			if err := sendStreamChunk(w, h.log, chunk); err != nil {
				h.log.Error("Failed to send stream chunk", zap.Error(err), zap.Int("chunk_index", i))
				return
			}

			// Check for context cancellation and sleep
			if !sleepWithCancel(c.Context(), 30*time.Millisecond) {
				h.log.Info("Stream cancelled by client")
				return
			}
		}

		// Send final chunk
		finalChunk := models.GeminiGenerateResponse{
			Candidates: []models.Candidate{
				{
					Index:        0,
					FinishReason: "STOP",
				},
			},
		}
		_ = sendStreamChunk(w, h.log, finalChunk)
	})

	return nil
}


