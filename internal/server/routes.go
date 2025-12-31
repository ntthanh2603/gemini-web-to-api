package server

import (
	"ai-bridges/internal/providers/gemini"

	"github.com/gofiber/fiber/v2"
)

type ChatRequest struct {
	Message string `json:"message"`
	Cookies struct {
		Secure1PSID   string `json:"__Secure-1PSID"`
		Secure1PSIDTS string `json:"__Secure-1PSIDTS"`
	} `json:"cookies"`
}

type ChatResponse struct {
	Response string `json:"response"`
	Error    string `json:"error,omitempty"`
}

func RegisterRoutes(router fiber.Router) {
	geminiGroup := router.Group("/gemini")
	geminiGroup.Post("/chat", handleGeminiChat)
}

func handleGeminiChat(c *fiber.Ctx) error {
	var req ChatRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ChatResponse{Error: "Invalid request body"})
	}

	if req.Message == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ChatResponse{Error: "Message cannot be empty"})
	}

	if req.Cookies.Secure1PSID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ChatResponse{Error: "Missing __Secure-1PSID cookie"})
	}

	// Initialize Gemini Client
	client := gemini.NewClient(req.Cookies.Secure1PSID, req.Cookies.Secure1PSIDTS)

	// Perform Handshake/Auth
	if err := client.Init(); err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ChatResponse{Error: "Failed to authenticate with Gemini: " + err.Error()})
	}

	// Generate Content
	response, err := client.GenerateContent(req.Message)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ChatResponse{Error: "Generate content failed: " + err.Error()})
	}

	return c.JSON(ChatResponse{Response: response})
}
