package controllers

import (
	"github.com/gofiber/fiber/v2"

	"ai-bridges/internal/handlers"
)

// ClaudeController registers Claude-compatible endpoints and contains Swagger annotations.
type ClaudeController struct {
	handler *handlers.ClaudeHandler
}

func NewClaudeController(h *handlers.ClaudeHandler) *ClaudeController {
	return &ClaudeController{handler: h}
}

// HandleModels returns a list of models
// @Summary List Claude models (Internal)
// @Description Returns a list of Claude models
// @Tags Claude Compatible
// @Accept json
// @Produce json
// @Success 200 {object} models.ModelListResponse
// @Router /claude/v1/models [get]
func (c *ClaudeController) HandleModels(ctx *fiber.Ctx) error {
	return c.handler.HandleModels(ctx)
}

// HandleModelByID returns details of a specific model
// @Summary Get Claude model by ID
// @Description Returns a specific Claude model by ID
// @Tags Claude Compatible
// @Accept json
// @Produce json
// @Param model_id path string true "Model ID"
// @Success 200 {object} models.ModelData
// @Router /claude/v1/models/{model_id} [get]
func (c *ClaudeController) HandleModelByID(ctx *fiber.Ctx) error {
	return c.handler.HandleModelByID(ctx)
}

// HandleMessages handles the main chat endpoint
// @Summary Claude-compatible chat
// @Description Accepts requests in Anthropic Claude format
// @Tags Claude Compatible
// @Accept json
// @Produce json
// @Param request body models.MessageRequest true "Message request"
// @Success 200 {object} models.MessageResponse
// @Failure 400 {object} map[string]interface{}
// @Failure 500 {object} map[string]interface{}
// @Router /claude/v1/messages [post]
func (c *ClaudeController) HandleMessages(ctx *fiber.Ctx) error {
	return c.handler.HandleMessages(ctx)
}

// HandleCountTokens handles token counting
// @Summary Count tokens
// @Description Estimates token count for a Claude request
// @Tags Claude Compatible
// @Accept json
// @Produce json
// @Param request body models.MessageRequest true "Message request"
// @Success 200 {object} map[string]interface{}
// @Router /claude/v1/messages/count_tokens [post]
func (c *ClaudeController) HandleCountTokens(ctx *fiber.Ctx) error {
	return c.handler.HandleCountTokens(ctx)
}

// Register registers the Claude routes onto the provided group
func (c *ClaudeController) Register(group fiber.Router) {
	group.Get("/models", c.HandleModels)
	group.Get("/models/:model_id", c.HandleModelByID)
	group.Post("/messages", c.HandleMessages)
	group.Post("/messages/count_tokens", c.HandleCountTokens)
}