package controllers

import (
	"github.com/gofiber/fiber/v2"

	"ai-bridges/internal/handlers"
)

// OpenAIController registers OpenAI-compatible endpoints and contains Swagger annotations.
type OpenAIController struct{
	handler *handlers.OpenAIHandler
}

func NewOpenAIController(h *handlers.OpenAIHandler) *OpenAIController {
	return &OpenAIController{handler: h}
}

// HandleModels returns the list of supported models
// @Summary List OpenAI models
// @Description Returns a list of models supported by the OpenAI-compatible API
// @Tags OpenAI Compatible
// @Accept json
// @Produce json
// @Success 200 {object} models.ModelListResponse
// @Router /openai/v1/models [get]
func (c *OpenAIController) HandleModels(ctx *fiber.Ctx) error {
	return c.handler.HandleModels(ctx)
}

// HandleChatCompletions accepts requests in OpenAI format
// @Summary OpenAI-compatible chat completions
// @Description Accepts requests in OpenAI format
// @Tags OpenAI Compatible
// @Accept json
// @Produce json
// @Param request body models.ChatCompletionRequest true "Chat request"
// @Success 200 {object} models.ChatCompletionResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Router /openai/v1/chat/completions [post]
func (c *OpenAIController) HandleChatCompletions(ctx *fiber.Ctx) error {
	return c.handler.HandleChatCompletions(ctx)
}

// Register registers the OpenAI routes onto the provided group
func (c *OpenAIController) Register(group fiber.Router) {
	group.Get("/models", c.HandleModels)
	group.Post("/chat/completions", c.HandleChatCompletions)
}