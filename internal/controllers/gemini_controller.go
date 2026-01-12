package controllers

import (
	"github.com/gofiber/fiber/v2"

	"ai-bridges/internal/handlers"
)

// GeminiController registers Gemini endpoints and contains Swagger annotations.
// Note: these are the v1beta (official) endpoints
type GeminiController struct{
	handler *handlers.GeminiHandler
}

func NewGeminiController(h *handlers.GeminiHandler) *GeminiController {
	return &GeminiController{handler: h}
}

// HandleV1BetaModels returns the list of models in Gemini format
// @Summary List Gemini Models (v1beta)
// @Description Returns models supported by the Gemini provider
// @Tags Gemini v1beta
// @Produce json
// @Success 200 {object} models.GeminiModelsResponse
// @Router /gemini/v1beta/models [get]
func (g *GeminiController) HandleV1BetaModels(ctx *fiber.Ctx) error {
	return g.handler.HandleV1BetaModels(ctx)
}

// HandleV1BetaGenerateContent handles the official Gemini generateContent endpoint
// @Summary Generate Content (v1beta)
// @Description Compatible with official Google Gemini API
// @Tags Gemini v1beta
// @Accept json
// @Produce json
// @Param model path string true "Model name"
// @Param request body models.GeminiGenerateRequest true "Gemini request"
// @Success 200 {object} models.GeminiGenerateResponse
// @Router /gemini/v1beta/models/{model}:generateContent [post]
func (g *GeminiController) HandleV1BetaGenerateContent(ctx *fiber.Ctx) error {
	return g.handler.HandleV1BetaGenerateContent(ctx)
}

// HandleV1BetaStreamGenerateContent handles the official Gemini streaming endpoint
// @Summary Stream Generate Content (v1beta)
// @Description Returns a stream of JSON chunks (standard Gemini format)
// @Tags Gemini v1beta
// @Accept json
// @Produce json
// @Param model path string true "Model name"
// @Param request body models.GeminiGenerateRequest true "Gemini request"
// @Router /gemini/v1beta/models/{model}:streamGenerateContent [post]
func (g *GeminiController) HandleV1BetaStreamGenerateContent(ctx *fiber.Ctx) error {
	return g.handler.HandleV1BetaStreamGenerateContent(ctx)
}

// Register registers the Gemini routes on the provided router (typically a group)
func (g *GeminiController) Register(group fiber.Router) {
	group.Get("/models", g.HandleV1BetaModels)
	group.Post("/models/:model\\:generateContent", g.HandleV1BetaGenerateContent)
	group.Post("/models/:model\\:streamGenerateContent", g.HandleV1BetaStreamGenerateContent)
}