package gemini

import (
	"bufio"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"ai-bridges/internal/providers"
	"ai-bridges/internal/providers/gemini"

	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	client *gemini.Client
	mu     sync.Mutex
}

func NewHandler(client *gemini.Client) *Handler {
	return &Handler{
		client: client,
	}
}

// --- Official Gemini API (v1beta) ---

// HandleV1BetaModels returns the list of models in Gemini format
// @Summary List Gemini Models (v1beta)
// @Description Returns models supported by the Gemini provider
// @Tags Gemini v1beta
// @Produce json
// @Success 200 {object} GeminiModelsResponse
// @Router /v1beta/models [get]
func (h *Handler) HandleV1BetaModels(c *fiber.Ctx) error {
	models := h.client.ListModels()
	var geminiModels []GeminiModel
	for _, m := range models {
		geminiModels = append(geminiModels, GeminiModel{
			Name:                       "models/" + m.ID,
			DisplayName:               m.ID,
			SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent"},
		})
	}
	return c.JSON(GeminiModelsResponse{Models: geminiModels})
}

// HandleV1BetaGenerateContent handles the official Gemini generateContent endpoint
// @Summary Generate Content (v1beta)
// @Description Compatible with official Google Gemini API
// @Tags Gemini v1beta
// @Accept json
// @Produce json
// @Param model path string true "Model name"
// @Param request body GeminiGenerateRequest true "Gemini request"
// @Success 200 {object} GeminiGenerateResponse
// @Router /v1beta/models/{model}:generateContent [post]
func (h *Handler) HandleV1BetaGenerateContent(c *fiber.Ctx) error {
	model := c.Params("model")
	var req GeminiGenerateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "Invalid request body"})
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
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "Empty content"})
	}

	opts := []providers.GenerateOption{providers.WithModel(model)}

	response, err := h.client.GenerateContent(c.Context(), prompt, opts...)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: err.Error()})
	}

	return c.JSON(GeminiGenerateResponse{
		Candidates: []Candidate{
			{
				Index: 0,
				Content: Content{
					Role:  "model",
					Parts: []Part{{Text: response.Text}},
				},
				FinishReason: "STOP",
			},
		},
		UsageMetadata: &UsageMetadata{
			TotalTokenCount: 0,
		},
	})
}

// HandleV1BetaStreamGenerateContent handles the official Gemini streaming endpoint
// @Summary Stream Generate Content (v1beta)
// @Description Returns a stream of JSON chunks (standard Gemini format)
// @Tags Gemini v1beta
// @Accept json
// @Produce json
// @Param model path string true "Model name"
// @Param request body GeminiGenerateRequest true "Gemini request"
// @Router /v1beta/models/{model}:streamGenerateContent [post]
func (h *Handler) HandleV1BetaStreamGenerateContent(c *fiber.Ctx) error {
	model := c.Params("model")
	var req GeminiGenerateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "Invalid request body"})
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
			errData, _ := json.Marshal(ErrorResponse{Error: err.Error()})
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

			chunk := GeminiGenerateResponse{
				Candidates: []Candidate{
					{
						Index: 0,
						Content: Content{
							Role:  "model",
							Parts: []Part{{Text: content}},
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

		finalChunk := GeminiGenerateResponse{
			Candidates: []Candidate{
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

