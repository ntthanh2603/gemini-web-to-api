package main

import (
	"log"

	"ai-bridges/internal/server"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
)

func main() {
	app := fiber.New()
	app.Use(logger.New())

	api := app.Group("/api/v1")
	server.RegisterRoutes(api)

	log.Fatal(app.Listen(":3000"))
}
