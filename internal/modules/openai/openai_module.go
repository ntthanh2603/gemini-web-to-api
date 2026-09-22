package openai

import (
	"github.com/gofiber/fiber/v3"
	"go.uber.org/fx"
)

var Module = fx.Options(
	fx.Provide(NewOpenAIService),
	fx.Provide(NewOpenAIController),
	fx.Invoke(RegisterRoutes),
	fx.Invoke(registerVideoRoutes),
)

func RegisterRoutes(app *fiber.App, c *OpenAIController) {
	// OpenAI routes (prefixed with /openai)
	openaiGroup := app.Group("/openai")
	openaiV1 := openaiGroup.Group("/v1")
	c.Register(openaiV1)
}
