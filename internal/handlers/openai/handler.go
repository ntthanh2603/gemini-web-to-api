package openai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ai-bridges/internal/providers"
	"ai-bridges/internal/providers/gemini"

	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	client *gemini.Client
}

func NewHandler(client *gemini.Client) *Handler {
	return &Handler{
		client: client,
	}
}

// GetModelData returns raw model data for internal use (e.g. unified list)
func (h *Handler) GetModelData() []ModelData {
	availableModels := h.client.ListModels()

	var data []ModelData
	for _, m := range availableModels {
		data = append(data, ModelData{
			ID:      m.ID,
			Object:  "model",
			Created: m.Created,
			OwnedBy: m.OwnedBy,
		})
	}
	return data
}

// HandleModels returns the list of supported models
// @Summary List OpenAI models
// @Description Returns a list of models supported by the OpenAI-compatible API
// @Tags OpenAI Compatible
// @Accept json
// @Produce json
// @Success 200 {object} ModelListResponse
// @Router /v1/models [get]
func (h *Handler) HandleModels(c *fiber.Ctx) error {
	data := h.GetModelData()

	return c.JSON(ModelListResponse{
		Object: "list",
		Data:   data,
	})
}


// HandleChatCompletions accepts requests in OpenAI format
// @Summary OpenAI-compatible chat completions
// @Description Accepts requests in OpenAI format
// @Tags OpenAI Compatible
// @Accept json
// @Produce json
// @Param request body ChatCompletionRequest true "Chat request"
// @Success 200 {object} ChatCompletionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /v1/chat/completions [post]
func (h *Handler) HandleChatCompletions(c *fiber.Ctx) error {
	// 1. Handle Authorization (accept but not strictly required for internal use)
	authHeader := c.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") && authHeader != "" {
		// Log warning or handle as needed
	}

	var req ChatCompletionRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: Error{
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
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: Error{Message: "No messages found", Type: "invalid_request_error"},
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
				errData, _ := json.Marshal(ErrorResponse{Error: Error{Message: err.Error()}})
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

				chunk := ChatCompletionChunk{
					ID:      id,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   req.Model,
					Choices: []ChunkChoice{
						{
							Index: 0,
							Delta: Delta{Content: content},
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
			finalChunk := ChatCompletionChunk{
				ID:      id,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   req.Model,
				Choices: []ChunkChoice{
					{
						Index:        0,
						Delta:        Delta{},
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
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
			Error: Error{
				Message: "Generation failed: " + err.Error(),
				Type:    "api_error",
			},
		})
	}

	return c.JSON(h.convertToOpenAIFormat(response, req.Model))
}

func (h *Handler) convertToOpenAIFormat(response *providers.Response, model string) ChatCompletionResponse {
	return ChatCompletionResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: response.Text,
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}
}
