package gemini

import (
	"context"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/fx"
)

var Module = fx.Options(
	fx.Provide(NewGeminiService),
	fx.Provide(NewGeminiController),
	fx.Invoke(RegisterRoutes),
)

func RegisterRoutes(lc fx.Lifecycle, app *fiber.App, c *GeminiController) {
	lc.Append(fx.Hook{
		OnStop: func(context.Context) error {
			c.Close()
			return nil
		},
	})

	// Gemini routes (prefixed with /gemini)
	geminiGroup := app.Group("/gemini")
	geminiV1 := geminiGroup.Group("/v1beta")
	c.Register(geminiV1)
}

