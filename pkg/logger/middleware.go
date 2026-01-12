package logger

import (
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

func NewMiddleware(log *zap.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		
		err := c.Next()
		if err != nil {
			if handlerErr := c.App().Config().ErrorHandler(c, err); handlerErr != nil {
				_ = c.SendStatus(fiber.StatusInternalServerError)
			}
		}

		stop := time.Now()

		status := c.Response().StatusCode()
		method := c.Method()
		path := c.Path()
		latency := stop.Sub(start)
		ip := c.IP()

		// Skip logging for Swagger static files to reduce noise
		if method == "GET" && (
			path == "/swagger/swagger-ui.css" ||
			path == "/swagger/swagger-ui-bundle.js" ||
			path == "/swagger/swagger-ui-standalone-preset.js" ||
			path == "/swagger/favicon-32x32.png" ||
			path == "/swagger/doc.json") {
			return nil
		}

		reset := "\033[0m"
		
		statusColor := "\033[32m" 
		if status >= 500 {
			statusColor = "\033[31m" 
		} else if status >= 400 {
			statusColor = "\033[33m" 
		} else if status >= 300 {
			statusColor = "\033[34m" 
		}

		methodColor := "\033[36m" 
		if method == "POST" {
			methodColor = "\033[32m" 
		} else if method == "PUT" || method == "PATCH" {
			methodColor = "\033[33m" 
		} else if method == "DELETE" {
			methodColor = "\033[31m" 
		}

		msg := fmt.Sprintf("%s%d%s|%s|%s|%s%s%s|%s",
			statusColor, status, reset,
			latency,
			ip,
			methodColor, method, reset,
			path,
		)

		log.Info(msg)
		return nil 
	}
}
