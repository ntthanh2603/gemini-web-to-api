package openai

import (
	"fmt"
	"strings"
	"time"

	"ai-bridges/internal/providers"
	"ai-bridges/internal/providers/gemini"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

type Handler struct {
	client *gemini.Client
	log    *zap.Logger
}

func NewHandler(client *gemini.Client, log *zap.Logger) *Handler {
	return &Handler{
		client: client,
		log:    log,
	}
}

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

	// Build context from history
	var promptBuilder strings.Builder
	for _, msg := range req.Messages {
		role := "User"
		if msg.Role == "assistant" || msg.Role == "model" {
			role = "Model"
		} else if msg.Role == "system" {
			role = "System"
		}
		
		// For the last message (current user query), we don't need the prefix if we want it native,
		// but providing a structured dialogue format is often safer for context.
		// However, simple concatenation works best for Gemini logic:
		promptBuilder.WriteString(fmt.Sprintf("%s: %s\n", role, msg.Content))
	}
	
	// The prompt is the entire conversation
	prompt := promptBuilder.String()

	if prompt == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: Error{
				Message: "No messages found",
				Type:    "invalid_request_error",
				Code:    "empty_messages",
			},
		})
	}

	// Generate response using shared client
	// We use the accumulated prompt as the single message input.
	// Gemini handles long context well.
	opts := []providers.GenerateOption{}
	if req.Model != "" {
		opts = append(opts, providers.WithModel(req.Model))
	}

	response, err := h.client.GenerateContent(c.Context(), prompt, opts...)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
			Error: Error{
				Message: "Generation failed: " + err.Error(),
				Type:    "api_error",
				Code:    "generation_failed",
			},
		})
	}

	// Convert to OpenAI format
	return c.JSON(h.convertToOpenAIFormat(response, req.Model, req.Stream))
}

func (h *Handler) convertToOpenAIFormat(response *providers.Response, model string, stream bool) ChatCompletionResponse {
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
