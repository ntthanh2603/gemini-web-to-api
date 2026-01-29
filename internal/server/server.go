package server

import (
	"context"
	"fmt"
	"sync"
	"time"

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
	appMu          sync.Mutex
}

func New(lc fx.Lifecycle, geminiHandler *handlers.GeminiHandler, openaiHandler *handlers.OpenAIHandler, claudeHandler *handlers.ClaudeHandler, cfg *config.Config, log *zap.Logger) (*Server, error) {
	// Inject logger into handlers
	geminiHandler.SetLogger(log)
	openaiHandler.SetLogger(log)
	claudeHandler.SetLogger(log)

	server := &Server{
		geminiHandler: geminiHandler,
		openaiHandler: openaiHandler,
		claudeHandler: claudeHandler,
		cfg:           cfg,
		log:           log,
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			app := buildApp(log, geminiHandler, openaiHandler, claudeHandler)
			
			server.appMu.Lock()
			server.app = app
			server.appMu.Unlock()

			// Start server in goroutine to avoid blocking
			go func() {
				if err := server.startServerWithFallback(); err != nil {
					log.Fatal("Could not start server on any port", zap.Error(err))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			server.appMu.Lock()
			defer server.appMu.Unlock()
			
			if server.app != nil {
				return server.app.ShutdownWithContext(ctx)
			}
			return nil
		},
	})

	return server, nil
}

// startServerWithFallback attempts to start the server on the configured port with fallback options
func (s *Server) startServerWithFallback() error {
	port := s.cfg.Server.Port
	if err := s.app.Listen(":" + port); err == nil {
		s.log.Info("Server started on port", zap.String("port", port))
		return nil
	}
	
	s.log.Warn("Failed to bind to configured port, trying alternatives", zap.String("port", port))
	
	// Try alternative ports
	alternativePorts := []string{"3001", "3002", "3003", "3004", "3005", "8080", "8081", "8082", "9000", "9001"}
	
	for _, altPort := range alternativePorts {
		s.log.Info("Attempting to start server on alternative port", zap.String("port", altPort))
		
		// Create new app instance for each attempt
		altApp := buildApp(s.log, s.geminiHandler, s.openaiHandler, s.claudeHandler)
		
		if err := altApp.Listen(":" + altPort); err == nil {
			s.log.Info("Server started successfully on alternative port", zap.String("port", altPort))
			
			// Update server app reference
			s.appMu.Lock()
			s.app = altApp
			s.appMu.Unlock()
			
			return nil
		}
		s.log.Debug("Failed to bind to alternative port", zap.String("port", altPort))
	}
	
	return fmt.Errorf("failed to start server on any available port")
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
		health := fiber.Map{
			"status":    "ok",
			"service":   "ai-bridges",
			"timestamp": time.Now().Unix(),
		}
		return c.JSON(health)
	})

	return app
}
