package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ai-bridges/internal/models"
	"ai-bridges/internal/providers"
	"ai-bridges/internal/providers/gemini"

	"github.com/gofiber/fiber/v2"
)

type OpenAIHandler struct {
	client *gemini.Client
}

func NewOpenAIHandler(client *gemini.Client) *OpenAIHandler {
	return &OpenAIHandler{
		client: client,
	}
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
	// 1. Handle Authorization (accept but not strictly required for internal use)
	authHeader := c.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") && authHeader != "" {
		// Log warning or handle as needed
	}

	var req models.ChatCompletionRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: models.Error{
				Message: "Invalid request body",
				Type:    "invalid_request_error",
				Code:    "invalid_request",
			},
		})
	}

	// 2. Build prompt from messages
	var promptBuilder strings.Builder
	for _, msg := range req.Messages {
		role := "User"
		if strings.EqualFold(msg.Role, "assistant") || strings.EqualFold(msg.Role, "model") {
			role = "Model"
		} else if strings.EqualFold(msg.Role, "system") {
			role = "System"
		}
		promptBuilder.WriteString(fmt.Sprintf("%s: %s\n", role, msg.Content))
	}
	
	prompt := promptBuilder.String()
	if prompt == "" {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: models.Error{
				Message: "No messages found",
				Type:    "invalid_request_error",
			},
		})
	}

	opts := []providers.GenerateOption{}
	if req.Model != "" {
		opts = append(opts, providers.WithModel(req.Model))
	}
	if req.MaxTokens > 0 {
		// Note: The interface might need updating if we want to pass these to the provider
	}

	// 3. Handle Streaming
	if req.Stream {
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("Transfer-Encoding", "chunked")

		c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
			response, err := h.client.GenerateContent(c.Context(), prompt, opts...)
			if err != nil {
				errData, _ := json.Marshal(models.ErrorResponse{Error: models.Error{Message: err.Error(), Type: "api_error"}})
				fmt.Fprintf(w, "data: %s\n\n", string(errData))
				w.Flush()
				return
			}

			// We don't have real-time streaming from the web client yet,
			// so we simulate it by sending the full response in one chunk for now,
			// or we could split by words. Let's split by words for a better "AI feel".
			words := strings.Split(response.Text, " ")
			id := fmt.Sprintf("chatcmpl-%d", time.Now().Unix())
			created := time.Now().Unix()

			for i, word := range words {
				content := word
				if i < len(words)-1 {
					content += " "
				}

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
				
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", string(data))
				w.Flush()
				
				// Small delay to simulate streaming
				time.Sleep(20 * time.Millisecond)
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
			finalData, _ := json.Marshal(finalChunk)
			fmt.Fprintf(w, "data: %s\n\n", string(finalData))
			fmt.Fprintf(w, "data: [DONE]\n\n")
			w.Flush()
		})
		return nil
	}

	// 4. Non-streaming response
	response, err := h.client.GenerateContent(c.Context(), prompt, opts...)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: models.Error{
				Message: "Generation failed: " + err.Error(),
				Type:    "api_error",
			},
		})
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
