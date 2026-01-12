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

	server := &Server{
		app:           app,
		geminiHandler: geminiHandler,
		openaiHandler: openaiHandler,
		claudeHandler: claudeHandler,
		cfg:           cfg,
		log:           log,
	}

	server.registerRoutes()

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
						altApp := createAltApp(geminiHandler, openaiHandler, claudeHandler, log)
						
						if listenErr := altApp.Listen(":" + port); listenErr == nil {
							log.Info("Server started successfully on alternative port", zap.String("port", port))
							return // Successfully started on alternative port
						} else {
							log.Warn("Failed to bind to alternative port", zap.String("port", port), zap.Error(listenErr))
						}
					}
					
					// If all predefined ports fail, try a random port
					log.Info("Attempting to start server on random available port")
					randomPortApp := createAltApp(geminiHandler, openaiHandler, claudeHandler, log)
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

// createAltApp creates an alternative Fiber app with the same configuration and routes
func createAltApp(geminiHandler *handlers.GeminiHandler, openaiHandler *handlers.OpenAIHandler, claudeHandler *handlers.ClaudeHandler, log *zap.Logger) *fiber.App {
	altApp := fiber.New(fiber.Config{
		AppName: "AI Bridges API",
	})

	altApp.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowHeaders: "Origin, Content-Type, Accept, Authorization, X-Requested-With, x-api-key, anthropic-version",
		AllowMethods: "GET, POST, PUT, DELETE, OPTIONS, PATCH",
	}))
	
	altApp.Use(logger.NewMiddleware(log))
	altApp.Use(recover.New())

	// --- Gemini routes (prefixed with /gemini) ---
	geminiGroup := altApp.Group("/gemini")
	geminiV1 := geminiGroup.Group("/v1beta")
	controllers.NewGeminiController(geminiHandler).Register(geminiV1)

	// --- OpenAI routes (prefixed with /openai) ---
	openaiGroup := altApp.Group("/openai")
	openaiV1 := openaiGroup.Group("/v1")
	controllers.NewOpenAIController(openaiHandler).Register(openaiV1)

	// --- Claude routes (prefixed with /claude) ---
	claudeGroup := altApp.Group("/claude")
	claudeV1 := claudeGroup.Group("/v1")
	controllers.NewClaudeController(claudeHandler).Register(claudeV1)

	altApp.Get("/swagger/*", fiberSwagger.WrapHandler)

	altApp.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":  "ok",
			"service": "ai-bridges",
		})
	})

	return altApp
}

func (s *Server) registerRoutes() {
	s.app.Get("/swagger/*", fiberSwagger.WrapHandler)

	// --- Gemini routes (prefixed with /gemini) ---
	geminiGroup := s.app.Group("/gemini")
	geminiV1 := geminiGroup.Group("/v1beta")
	controllers.NewGeminiController(s.geminiHandler).Register(geminiV1)

	// --- OpenAI routes (prefixed with /openai) ---
	openaiGroup := s.app.Group("/openai")
	openaiV1 := openaiGroup.Group("/v1")
	controllers.NewOpenAIController(s.openaiHandler).Register(openaiV1)

	// --- Claude routes (prefixed with /claude) ---
	claudeGroup := s.app.Group("/claude")
	claudeV1 := claudeGroup.Group("/v1")
	controllers.NewClaudeController(s.claudeHandler).Register(claudeV1)

	s.app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status": "ok",
			"service": "ai-bridges",
		})
	})
}
