package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"
	"time"

	"gemini-web-to-api/internal/commons/configs"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/gofiber/fiber/v3/middleware/limiter"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// New creates a new Fiber app instance
func NewGeminiWebToAPI(log *zap.Logger, cfg *configs.Config) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName: "Gemini Web To API",
	})

	app.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Requested-With", "x-api-key", "x-goog-api-key", "anthropic-version"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowCredentials: false,
	}))

	app.Use(recover.New())

	if cfg.Server.APIKey != "" {
		app.Use(apiKeyAuth(cfg.Server.APIKey, cfg.Server.APIKeyAlt))
	}

	if cfg.RateLimit.Enabled {
		app.Use(limiter.New(limiter.Config{
			Max:        cfg.RateLimit.MaxRequests,
			Expiration: time.Duration(cfg.RateLimit.WindowMs) * time.Millisecond,
		}))
	}

	app.Get("/docs", ScalarUI)
	app.Get("/openapi.json", OpenAPISpec)

	app.Get("/health", HealthCheck)

	return app
}

func HealthCheck(c fiber.Ctx) error {
	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"status":  "ok",
		"service": "gemini-web-to-api",
	})
}

// publicPaths stay reachable without an API key so health checks and the
// interactive docs keep working when auth is enabled.
var publicPaths = map[string]bool{
	"/health":       true,
	"/docs":         true,
	"/openapi.json": true,
}

// apiKeyAuth rejects requests that do not present the configured key. It reads
// the key from the headers each client SDK sends by default, so OpenAI
// (Authorization: Bearer), Claude (x-api-key) and Gemini (x-goog-api-key or
// ?key=) clients work without custom configuration.
func apiKeyAuth(apiKey, altKey string) fiber.Handler {
	keyMatches := func(presented string) bool {
		if subtle.ConstantTimeCompare([]byte(presented), []byte(apiKey)) == 1 {
			return true
		}
		// The admin cookie endpoint authenticates with its own key on the same
		// header; accept it here so that endpoint stays reachable. The admin
		// route still verifies its key itself.
		return altKey != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(altKey)) == 1
	}

	return func(c fiber.Ctx) error {
		if publicPaths[c.Path()] {
			return c.Next()
		}
		presented := strings.TrimSpace(c.Get(fiber.HeaderAuthorization))
		if strings.HasPrefix(presented, "Bearer ") {
			presented = strings.TrimSpace(strings.TrimPrefix(presented, "Bearer "))
		}
		if presented == "" {
			presented = strings.TrimSpace(c.Get("x-api-key"))
		}
		if presented == "" {
			presented = strings.TrimSpace(c.Get("x-goog-api-key"))
		}
		if presented == "" {
			presented = strings.TrimSpace(c.Query("key"))
		}
		if !keyMatches(presented) {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": fiber.Map{
					"type":    "invalid_request_error",
					"message": "Missing or invalid API key. Set API_KEY on the server and send it as a Bearer token, x-api-key, x-goog-api-key header, or ?key= query parameter.",
					"code":    "invalid_api_key",
				},
			})
		}
		return c.Next()
	}
}

// Register404Handler registers the 404 handler for unmatched routes
// This must be called AFTER all other routes are registered
func Register404Handler(app *fiber.App) {
	app.All("*", func(c fiber.Ctx) error {
		method := c.Method()
		path := c.Path()
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"status":  fiber.StatusNotFound,
			"error":   "Not Found",
			"message": fmt.Sprintf("Cannot %s %s", method, path),
		})
	})
}

// RegisterFiberLifecycle registers the Fiber app lifecycle hooks
func RegisterFiberLifecycle(lc fx.Lifecycle, app *fiber.App, cfg *configs.Config, log *zap.Logger) {
	port := cfg.Server.Port
	address := fmt.Sprintf(":%s", port)

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			Register404Handler(app)
			log.Info("Starting server", zap.String("address", address))
			// Start server in a goroutine to not block
			go func() {
				if err := app.Listen(address); err != nil {
					log.Error("Server error", zap.Error(err))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			log.Info("Shutting down server")
			return app.ShutdownWithContext(ctx)
		},
	})
}
