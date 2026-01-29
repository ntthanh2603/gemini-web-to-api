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
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type ClaudeHandler struct {
	client *gemini.Client
	log    *zap.Logger
}

func NewClaudeHandler(client *gemini.Client) *ClaudeHandler {
	return &ClaudeHandler{
		client: client,
		log:    zap.NewNop(),
	}
}

// SetLogger sets the logger for this handler
func (h *ClaudeHandler) SetLogger(log *zap.Logger) {
	h.log = log
}

// GetModelData moved to models_handlers.go

// HandleModels returns a list of Claude models
func (h *ClaudeHandler) HandleModels(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"data": []fiber.Map{
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
		},
	})
}

// HandleModelByID returns a specific Claude model by ID
func (h *ClaudeHandler) HandleModelByID(c *fiber.Ctx) error {
	modelID := c.Params("model_id")
	return c.JSON(fiber.Map{
		"id":           modelID,
		"type":         "model",
		"created_at":   time.Now().Unix(),
		"display_name": modelID,
	})
}

// Model handlers moved to models_handlers.go


// HandleMessages handles the main chat endpoint
func (h *ClaudeHandler) HandleMessages(c *fiber.Ctx) error {
	var req models.MessageRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"type":  "error",
			"error": fiber.Map{"type": "invalid_request_error", "message": "Invalid JSON body"},
		})
	}

	// Validate messages
	if err := validateMessages(req.Messages); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"type":  "error",
			"error": fiber.Map{"type": "invalid_request_error", "message": err.Error()},
		})
	}

	// Build prompt
	prompt := buildPromptFromMessages(req.Messages, req.System)
	if prompt == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"type":  "error",
			"error": fiber.Map{"type": "invalid_request_error", "message": "no valid content in messages"},
		})
	}

	opts := []providers.GenerateOption{}
	msgID := fmt.Sprintf("msg_%s", uuid.New().String())

	// Handle Streaming
	if req.Stream {
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")

		c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
			// Add timeout
			ctx, cancel := context.WithTimeout(c.Context(), 5*time.Minute)
			defer cancel()

			response, err := h.client.GenerateContent(ctx, prompt, opts...)
			if err != nil {
				h.log.Error("GenerateContent streaming failed", zap.Error(err), zap.String("model", req.Model))
				_ = sendSSEChunk(w, h.log, "error", fiber.Map{
					"type": "error",
					"error": fiber.Map{
						"type":    "api_error",
						"message": err.Error(),
					},
				})
				return
			}

			// Simulate Streaming - Claude format
			_ = sendSSEChunk(w, h.log, "message_start", fiber.Map{
				"type": "message_start",
				"message": models.MessageResponse{
					ID:    msgID,
					Type:  "message",
					Role:  "assistant",
					Model: req.Model,
					Usage: models.Usage{InputTokens: 10, OutputTokens: 1},
				},
			})

			_ = sendSSEChunk(w, h.log, "content_block_start", fiber.Map{
				"type":           "content_block_start",
				"index":          0,
				"content_block":  models.ConfigContent{Type: "text", Text: ""},
			})

			// Send chunks
			chunks := splitResponseIntoChunks(response.Text, 20)
			for _, chunk := range chunks {
				_ = sendSSEChunk(w, h.log, "content_block_delta", fiber.Map{
					"type":  "content_block_delta",
					"index": 0,
					"delta": models.Delta{Type: "text_delta", Text: chunk},
				})

				// Check context cancellation
				if !sleepWithCancel(c.Context(), 20*time.Millisecond) {
					h.log.Info("Stream cancelled by client")
					return
				}
			}

			_ = sendSSEChunk(w, h.log, "content_block_stop", fiber.Map{"type": "content_block_stop", "index": 0})
			_ = sendSSEChunk(w, h.log, "message_stop", fiber.Map{"type": "message_stop", "stop_reason": "end_turn"})
		})
		return nil
	}

	// Non-streaming response
	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Minute)
	defer cancel()

	response, err := h.client.GenerateContent(ctx, prompt, opts...)
	if err != nil {
		h.log.Error("GenerateContent failed", zap.Error(err), zap.String("model", req.Model))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"type":  "error",
			"error": fiber.Map{"type": "api_error", "message": err.Error()},
		})
	}

	// Construct Response
	content := []models.ConfigContent{{Type: "text", Text: response.Text}}

	return c.JSON(models.MessageResponse{
		ID:         msgID,
		Type:       "message",
		Role:       "assistant",
		Model:      req.Model,
		Content:    content,
		StopReason: "end_turn",
		Usage: models.Usage{
			InputTokens:  len(prompt) / 4,
			OutputTokens: len(response.Text) / 4,
		},
	})
}

// HandleCountTokens handles token counting
func (h *ClaudeHandler) HandleCountTokens(c *fiber.Ctx) error {
	var req models.MessageRequest
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
