package handlers

import (
	"bufio"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"ai-bridges/internal/models"
	"ai-bridges/internal/providers"
	"ai-bridges/internal/providers/gemini"

	"github.com/gofiber/fiber/v2"
)

type GeminiHandler struct {
	client *gemini.Client
	mu     sync.Mutex
}

func NewGeminiHandler(client *gemini.Client) *GeminiHandler {
	return &GeminiHandler{
		client: client,
	}
}

// --- Official Gemini API (v1beta) ---

// HandleV1BetaModels returns the list of models in Gemini format
func (h *GeminiHandler) HandleV1BetaModels(c *fiber.Ctx) error {
	availableModels := h.client.ListModels()
	var geminiModels []models.GeminiModel
	for _, m := range availableModels {
		geminiModels = append(geminiModels, models.GeminiModel{
			Name:                       "models/" + m.ID,
			DisplayName:               m.ID,
			SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent"},
		})
	}
	return c.JSON(models.GeminiModelsResponse{Models: geminiModels})
}

// HandleV1BetaGenerateContent handles the official Gemini generateContent endpoint
func (h *GeminiHandler) HandleV1BetaGenerateContent(c *fiber.Ctx) error {
	model := c.Params("model")
	var req models.GeminiGenerateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "Invalid request body"})
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
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: models.Error{Message: "Empty content", Type: "invalid_request_error"}})
	}

	opts := []providers.GenerateOption{providers.WithModel(model)}

	response, err := h.client.GenerateContent(c.Context(), prompt, opts...)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: models.Error{Message: err.Error(), Type: "api_error"}})
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
	model := c.Params("model")
	var req models.GeminiGenerateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "Invalid request body"})
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
	opts := []providers.GenerateOption{providers.WithModel(model)}

	c.Set("Content-Type", "application/json")
	c.Set("Transfer-Encoding", "chunked")

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		resp, err := h.client.GenerateContent(c.Context(), prompt, opts...)
		if err != nil {
			errData, _ := json.Marshal(models.ErrorResponse{Error: err.Error()})
			w.Write(errData)
			w.Flush()
			return
		}

		words := strings.Split(resp.Text, " ")
		for i, word := range words {
			content := word
			if i < len(words)-1 {
				content += " "
			}

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

			data, _ := json.Marshal(chunk)
			w.Write(data)
			w.Write([]byte("\n"))
			w.Flush()
			time.Sleep(30 * time.Millisecond)
		}

		finalChunk := models.GeminiGenerateResponse{
			Candidates: []models.Candidate{
				{
					Index:        0,
					FinishReason: "STOP",
				},
			},
		}
		data, _ := json.Marshal(finalChunk)
		w.Write(data)
		w.Write([]byte("\n"))
		w.Flush()
	})

	return nil
}

