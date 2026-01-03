package main

import (
	"context"

	"ai-bridges/internal/config"
	claudeHandlers "ai-bridges/internal/handlers/claude"
	geminiHandlers "ai-bridges/internal/handlers/gemini"
	openaiHandlers "ai-bridges/internal/handlers/openai"
	"ai-bridges/internal/providers/gemini"
	"ai-bridges/internal/server"
	"ai-bridges/pkg/logger"

	_ "ai-bridges/cmd/swag/docs"

	"go.uber.org/fx"
	"go.uber.org/zap"
)

// @title AI Bridges API
// @version 1.0
// @description 🚀 High-performance WebAI-to-API gateway. Seamlessly bridge Google Gemini into standardized OpenAI, Anthropic (Claude), and Google Native REST APIs.
// @host localhost:3000
// @BasePath /
func main() {
	fx.New(
		fx.Provide(
			config.New,
			logger.New,
			gemini.NewClient,
			geminiHandlers.NewHandler,
			openaiHandlers.NewHandler,
			claudeHandlers.NewHandler,
		),
		fx.Invoke(
			server.New,
		),
		fx.Invoke(func(c *gemini.Client, log *zap.Logger) {
			if err := c.Init(context.Background()); err != nil {
				log.Warn("Gemini client initialization warning", zap.Error(err))
			}
		}),
		fx.NopLogger, 
	).Run()
}
