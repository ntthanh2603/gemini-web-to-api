package server

import (
	"context"

	"ai-bridges/internal/config"
	geminiHandlers "ai-bridges/internal/handlers/gemini"
	openaiHandlers "ai-bridges/internal/handlers/openai"
	"ai-bridges/pkg/logger"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
	fiberSwagger "github.com/swaggo/fiber-swagger"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

type Server struct {
	app           *fiber.App
	geminiHandler *geminiHandlers.Handler
	openaiHandler *openaiHandlers.Handler
	cfg           *config.Config
	log           *zap.Logger
}

func New(lc fx.Lifecycle, geminiHandler *geminiHandlers.Handler, openaiHandler *openaiHandlers.Handler, cfg *config.Config, log *zap.Logger) (*Server, error) {
	app := fiber.New(fiber.Config{
		AppName: "AI Bridges API",
	})

	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowHeaders: "Origin, Content-Type, Accept, Authorization, X-Requested-With",
		AllowMethods: "GET, POST, PUT, DELETE, OPTIONS, PATCH",
	}))
	
	app.Use(logger.NewMiddleware(log))
	app.Use(recover.New())

	server := &Server{
		app:           app,
		geminiHandler: geminiHandler,
		openaiHandler: openaiHandler,
		cfg:           cfg,
		log:           log,
	}

	server.registerRoutes()

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			go func() {
				if err := app.Listen(":" + cfg.Server.Port); err != nil {
					log.Error("Failed to start server", zap.Error(err))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return app.Shutdown()
		},
	})

	return server, nil
}

func (s *Server) registerRoutes() {
	s.app.Get("/swagger/*", fiberSwagger.WrapHandler)

	// Gemini v1beta routes (Standard)
	v1betaGroup := s.app.Group("/v1beta")
	v1betaGroup.Get("/models", s.geminiHandler.HandleV1BetaModels)
	v1betaGroup.Post("/models/:model\\:generateContent", s.geminiHandler.HandleV1BetaGenerateContent)
	v1betaGroup.Post("/models/:model\\:streamGenerateContent", s.geminiHandler.HandleV1BetaStreamGenerateContent)
	v1betaGroup.Post("/models/:model\\:embedContent", s.geminiHandler.HandleV1BetaEmbedContent)

	// OpenAI routes
	v1Group := s.app.Group("/v1")
	v1Group.Get("/models", s.openaiHandler.HandleModels)
	v1Group.Post("/chat/completions", s.openaiHandler.HandleChatCompletions)
	v1Group.Post("/embeddings", s.openaiHandler.HandleEmbeddings)

	s.app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":  "ok",
			"service": "ai-bridges",
		})
	})
}
