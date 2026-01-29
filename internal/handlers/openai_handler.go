package handlers

import (
	"bufio"
	"context"
	"fmt"
	"time"

	"ai-bridges/internal/models"
	"ai-bridges/internal/providers"
	"ai-bridges/internal/providers/gemini"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

type OpenAIHandler struct {
	client *gemini.Client
	log    *zap.Logger
}

func NewOpenAIHandler(client *gemini.Client) *OpenAIHandler {
	return &OpenAIHandler{
		client: client,
		log:    zap.NewNop(),
	}
}

// SetLogger sets the logger for this handler
func (h *OpenAIHandler) SetLogger(log *zap.Logger) {
	h.log = log
}

// GetModelData returns raw model data for internal use (e.g. unified list)
func (h *OpenAIHandler) GetModelData() []models.ModelData {
	availableModels := h.client.ListModels()

	var data []models.ModelData
	for _, m := range availableModels {
		data = append(data, models.ModelData{
			ID:      m.ID,
			Object:  "model",
			Created: m.Created,
			OwnedBy: m.OwnedBy,
		})
	}
	return data
}

// HandleModels returns the list of supported models
func (h *OpenAIHandler) HandleModels(c *fiber.Ctx) error {
	data := h.GetModelData()

	return c.JSON(models.ModelListResponse{
		Object: "list",
		Data:   data,
	})
}


// HandleChatCompletions accepts requests in OpenAI format
func (h *OpenAIHandler) HandleChatCompletions(c *fiber.Ctx) error {
	var req models.ChatCompletionRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(errorToResponse(fmt.Errorf("invalid request body: %w", err), "invalid_request_error"))
	}

	// Validate messages
	if err := validateMessages(req.Messages); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(errorToResponse(err, "invalid_request_error"))
	}

	// Validate generation parameters
	if err := validateGenerationRequest(req.Model, req.MaxTokens, req.Temperature); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(errorToResponse(err, "invalid_request_error"))
	}

	// Build prompt from messages
	prompt := buildPromptFromMessages(req.Messages, "")
	if prompt == "" {
		return c.Status(fiber.StatusBadRequest).JSON(errorToResponse(fmt.Errorf("no valid content in messages"), "invalid_request_error"))
	}

	opts := []providers.GenerateOption{}
	if req.Model != "" {
		opts = append(opts, providers.WithModel(req.Model))
	}

	// Handle Streaming
	if req.Stream {
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("Transfer-Encoding", "chunked")

		c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
			// Add timeout
			ctx, cancel := context.WithTimeout(c.Context(), 5*time.Minute)
			defer cancel()

			response, err := h.client.GenerateContent(ctx, prompt, opts...)
			if err != nil {
				h.log.Error("GenerateContent streaming failed", zap.Error(err), zap.String("model", req.Model))
				errResponse := errorToResponse(err, "api_error")
				_ = marshalJSONSafely(h.log, errResponse) // Use safe marshal
				return
			}

			id := fmt.Sprintf("chatcmpl-%d", time.Now().Unix())
			created := time.Now().Unix()
			chunks := splitResponseIntoChunks(response.Text, 20)

			for i, content := range chunks {
				chunk := models.ChatCompletionChunk{
					ID:      id,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   req.Model,
					Choices: []models.ChunkChoice{
						{
							Index: 0,
							Delta: models.Delta{Content: content},
						},
					},
				}

				if err := sendSSEChunk(w, h.log, "data", chunk); err != nil {
					h.log.Error("Failed to send SSE chunk", zap.Error(err), zap.Int("chunk_index", i))
					return
				}

				// Check context cancellation
				if !sleepWithCancel(c.Context(), 20*time.Millisecond) {
					h.log.Info("Stream cancelled by client")
					return
				}
			}

			// Send final chunk with finish_reason
			finalChunk := models.ChatCompletionChunk{
				ID:      id,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   req.Model,
				Choices: []models.ChunkChoice{
					{
						Index:        0,
						Delta:        models.Delta{},
						FinishReason: "stop",
					},
				},
			}
			_ = sendSSEChunk(w, h.log, "data", finalChunk)

			// Send done marker
			if _, err := fmt.Fprintf(w, "data: [DONE]\n\n"); err != nil {
				h.log.Error("Failed to write DONE marker", zap.Error(err))
			}
			_ = w.Flush()
		})
		return nil
	}

	// Non-streaming response
	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Minute)
	defer cancel()

	response, err := h.client.GenerateContent(ctx, prompt, opts...)
	if err != nil {
		h.log.Error("GenerateContent failed", zap.Error(err), zap.String("model", req.Model))
		return c.Status(fiber.StatusInternalServerError).JSON(errorToResponse(err, "api_error"))
	}

	return c.JSON(h.convertToOpenAIFormat(response, req.Model))
}

func (h *OpenAIHandler) convertToOpenAIFormat(response *providers.Response, model string) models.ChatCompletionResponse {
	return models.ChatCompletionResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []models.Choice{
			{
				Index: 0,
				Message: models.Message{
					Role:    "assistant",
					Content: response.Text,
				},
				FinishReason: "stop",
			},
		},
		Usage: models.Usage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}
}
