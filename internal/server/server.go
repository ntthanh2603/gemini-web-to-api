package server

import (
	"context"

	"ai-bridges/internal/config"
	"ai-bridges/internal/controllers"
	"ai-bridges/internal/handlers"
	"ai-bridges/pkg/logger"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
	fiberSwagger "github.com/swaggo/fiber-swagger"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

type Server struct {
	app            *fiber.App
	geminiHandler  *handlers.GeminiHandler
	openaiHandler  *handlers.OpenAIHandler
	claudeHandler  *handlers.ClaudeHandler
	cfg            *config.Config
	log            *zap.Logger
}

func New(lc fx.Lifecycle, geminiHandler *handlers.GeminiHandler, openaiHandler *handlers.OpenAIHandler, claudeHandler *handlers.ClaudeHandler, cfg *config.Config, log *zap.Logger) (*Server, error) {
	app := buildApp(log, geminiHandler, openaiHandler, claudeHandler)

	server := &Server{
		app:           app,
		geminiHandler: geminiHandler,
		openaiHandler: openaiHandler,
		claudeHandler: claudeHandler,
		cfg:           cfg,
		log:           log,
	}

		lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			// Since Fiber's Listen is blocking, we'll try to start the server in a goroutine
			// and handle port conflicts by trying alternatives
			go func() {
				// Attempt to start the main server on the configured port
				if err := app.Listen(":" + cfg.Server.Port); err != nil {
					log.Warn("Failed to bind to configured port", zap.String("port", cfg.Server.Port), zap.Error(err))
					
					// Define alternative ports to try
					alternativePorts := []string{"3001", "3002", "3003", "3004", "3005", "8080", "8081", "8082", "9000", "9001"}
					
					for _, port := range alternativePorts {
						log.Info("Attempting to start server on alternative port", zap.String("port", port))
						
						// Create a new Fiber app with the same configuration and handlers
						altApp := buildApp(log, geminiHandler, openaiHandler, claudeHandler)
						
						if listenErr := altApp.Listen(":" + port); listenErr == nil {
							log.Info("Server started successfully on alternative port", zap.String("port", port))
							return // Successfully started on alternative port
						} else {
							log.Warn("Failed to bind to alternative port", zap.String("port", port), zap.Error(listenErr))
						}
					}
					
					// If all predefined ports fail, try a random port
					log.Info("Attempting to start server on random available port")
					randomPortApp := buildApp(log, geminiHandler, openaiHandler, claudeHandler)
					// Start server on random port - this will block if successful, so no need for else clause
					if listenErr := randomPortApp.Listen(":0"); listenErr != nil {
						log.Fatal("Could not start server on any port", zap.Error(listenErr))
					}
					// If Listen succeeds with random port, the server is running and this goroutine continues blocked
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

// buildApp creates and configures a Fiber app with all middleware and routes
func buildApp(log *zap.Logger, geminiHandler *handlers.GeminiHandler, openaiHandler *handlers.OpenAIHandler, claudeHandler *handlers.ClaudeHandler) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName: "AI Bridges API",
	})

	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowHeaders: "Origin, Content-Type, Accept, Authorization, X-Requested-With, x-api-key, anthropic-version",
		AllowMethods: "GET, POST, PUT, DELETE, OPTIONS, PATCH",
	}))
	
	app.Use(logger.NewMiddleware(log))
	app.Use(recover.New())

	// --- Gemini routes (prefixed with /gemini) ---
	geminiGroup := app.Group("/gemini")
	geminiV1 := geminiGroup.Group("/v1beta")
	controllers.NewGeminiController(geminiHandler).Register(geminiV1)

	// --- OpenAI routes (prefixed with /openai) ---
	openaiGroup := app.Group("/openai")
	openaiV1 := openaiGroup.Group("/v1")
	controllers.NewOpenAIController(openaiHandler).Register(openaiV1)

	// --- Claude routes (prefixed with /claude) ---
	claudeGroup := app.Group("/claude")
	claudeV1 := claudeGroup.Group("/v1")
	controllers.NewClaudeController(claudeHandler).Register(claudeV1)

	app.Get("/swagger/*", fiberSwagger.WrapHandler)

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":  "ok",
			"service": "ai-bridges",
		})
	})

	return app
}
