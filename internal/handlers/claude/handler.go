package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ai-bridges/internal/providers"
	"ai-bridges/internal/providers/gemini"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
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
func (h *Handler) GetModelData() []fiber.Map {
	return []fiber.Map{
		{
			"id":           "claude-3-5-sonnet-20240620",
			"type":         "model",
			"created_at":   1718841600,
			"display_name": "Claude 3.5 Sonnet",
		},
		{
			"id":           "claude-3-opus-20240229",
			"type":         "model",
			"created_at":   1709164800,
			"display_name": "Claude 3 Opus",
		},
		{
			"id":           "claude-3-7-sonnet-20250219",
			"type":         "model",
			"created_at":   1739923200,
			"display_name": "Claude 3.7 Sonnet",
		},
	}
}

// HandleModels returns a list of models
// @Summary List Claude models (Internal)
// @Description Returns a list of Claude models
// @Tags Claude Compatible
// @Accept json
// @Produce json
// @Success 200 {object} ModelListResponse
func (h *Handler) HandleModels(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"data": h.GetModelData(),
	})
}

// HandleModelByID returns details of a specific model
// @Summary Get Claude model by ID
// @Description Returns a specific Claude model by ID
// @Tags Claude Compatible
// @Accept json
// @Produce json
// @Param model_id path string true "Model ID"
// @Success 200 {object} ModelData
// @Router /v1/models/{model_id} [get]
func (h *Handler) HandleModelByID(c *fiber.Ctx) error {
	modelID := c.Params("model_id")
	return c.JSON(fiber.Map{
		"id":           modelID,
		"type":         "model",
		"created_at":   time.Now().Unix(),
		"display_name": modelID,
	})
}

// HandleMessages handles the main chat endpoint
// @Summary Claude-compatible chat
// @Description Accepts requests in Anthropic Claude format
// @Tags Claude Compatible
// @Accept json
// @Produce json
// @Param request body MessageRequest true "Message request"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} map[string]interface{}
// @Failure 500 {object} map[string]interface{}
// @Router /v1/messages [post]
func (h *Handler) HandleMessages(c *fiber.Ctx) error {
	// x-api-key check (loose check)
	if c.Get("x-api-key") == "" {
		// Proceed even if missing
	}


	var req MessageRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"type":  "error",
			"error": fiber.Map{"type": "invalid_request_error", "message": "Invalid JSON body"},
		})
	}

	// Prepare Prompt for backend

	var promptBuilder strings.Builder
	if req.System != "" {
		promptBuilder.WriteString(fmt.Sprintf("System: %s\n\n", req.System))
	}
	for _, msg := range req.Messages {
		role := "User"
		if msg.Role == "assistant" {
			role = "Model"
		}
		promptBuilder.WriteString(fmt.Sprintf("%s: %s\n", role, msg.Content))
	}
	prompt := promptBuilder.String()

	var opts []providers.GenerateOption // Declared once here
	// Map Claude model to Gemini model if needed, or just pass valid gemini model
	// For now we use default or stick to what openai handler does.
	// We'll just pass the client default.

	// Call Backend
	msgID := fmt.Sprintf("msg_%s", uuid.New().String())

	// Handle Streaming
	if req.Stream {
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")

		c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
			response, err := h.client.GenerateContent(c.Context(), prompt, opts...)
			if err != nil {
				// Send error event
				errData, _ := json.Marshal(fiber.Map{
					"type": "error",
					"error": fiber.Map{
						"type": "api_error",
						"message": err.Error(),
					},
				})
				// For SSE error is tricky, usually we just close or send specific event
				// But let's try to send a text error.
				fmt.Fprintf(w, "event: error\ndata: %s\n\n", string(errData))
				return
			}

			// Simulate Streaming
			sendEvent(w, "message_start", fiber.Map{
				"type": "message_start",
				"message": MessageResponse{
					ID:    msgID,
					Type:  "message",
					Role:  "assistant",
					Model: req.Model,
					Usage: Usage{InputTokens: 10, OutputTokens: 1}, 
					Content: []ConfigContent{}, 
					StopReason: "",
				},
			})

			sendEvent(w, "content_block_start", fiber.Map{
				"type": "content_block_start",
				"index": 0,
				"content_block": ConfigContent{Type: "text", Text: ""},
			})

			// Simulated chunks
			words := strings.Split(response.Text, " ")
			for _, word := range words {
				sendEvent(w, "content_block_delta", fiber.Map{
					"type": "content_block_delta",
					"index": 0,
					"delta": Delta{Type: "text_delta", Text: word + " "},
				})
				time.Sleep(20 * time.Millisecond)
			}

			sendEvent(w, "content_block_stop", fiber.Map{"type": "content_block_stop", "index": 0})
			sendEvent(w, "message_stop", fiber.Map{"type": "message_stop", "stop_reason": "end_turn"})
		})
		return nil
	}


	response, err := h.client.GenerateContent(c.Context(), prompt, opts...)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"type": "error",
			"error": fiber.Map{"type": "api_error", "message": err.Error()},
		})
	}

	// Construct Response
	content := []ConfigContent{{Type: "text", Text: response.Text}}
	
	return c.JSON(MessageResponse{
		ID:         msgID,
		Type:       "message",
		Role:       "assistant",
		Model:      req.Model,
		Content:    content,
		StopReason: "end_turn",
		Usage: Usage{
			InputTokens:  15, 
			OutputTokens: len(response.Text) / 4,
		},
	})
}

// HandleCountTokens handles token counting
// @Summary Count tokens
// @Description Estimates token count for a Claude request
// @Tags Claude Compatible
// @Accept json
// @Produce json
// @Param request body MessageRequest true "Message request"
// @Success 200 {object} map[string]interface{}
// @Router /v1/messages/count_tokens [post]
func (h *Handler) HandleCountTokens(c *fiber.Ctx) error {
	var req MessageRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"type":  "error",
			"error": fiber.Map{"type": "invalid_request_error", "message": "Invalid JSON body"},
		})
	}

	// Simple estimation
	totalChars := len(req.System)
	for _, m := range req.Messages {
		totalChars += len(m.Content)
	}

	return c.JSON(fiber.Map{
		"input_tokens": totalChars / 4,
	})
}

func sendEvent(w *bufio.Writer, event string, data interface{}) {
	jsonData, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\n", event)
	fmt.Fprintf(w, "data: %s\n\n", jsonData)
}
