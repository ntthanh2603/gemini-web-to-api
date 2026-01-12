package main

import (
	"context"

	"ai-bridges/internal/config"
	"ai-bridges/internal/handlers"
	"ai-bridges/internal/providers"
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
			providers.NewProviderManager,
			gemini.NewClient,
			handlers.NewGeminiHandler,
			handlers.NewOpenAIHandler,
			handlers.NewClaudeHandler,
		),
		fx.Invoke(
			server.New,
		),
		fx.Invoke(func(pm *providers.ProviderManager, c *gemini.Client, cfg *config.Config, log *zap.Logger) {
			pm.Register("gemini", c)
			// Initialize all providers (non-blocking, logs warnings on failure)
			pm.InitAllProviders(context.Background())
			// Select the provider based on config
			if err := pm.SelectProvider(cfg.Providers.ProviderType); err != nil {
				log.Error("Failed to select provider", zap.Error(err))
			} else {
				log.Debug("Active provider selected", zap.String("provider_type", cfg.Providers.ProviderType))
			}
		}),
		fx.NopLogger, 
	).Run()
}
