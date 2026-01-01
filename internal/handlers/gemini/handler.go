package gemini

import (
	"fmt"
	"sync"

	"ai-bridges/internal/providers"
	"ai-bridges/internal/providers/gemini"

	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	client *gemini.Client
	mu     sync.Mutex
}

func NewHandler(client *gemini.Client) *Handler {
	return &Handler{
		client: client,
	}
}

// @Summary Generate content with Gemini
// @Description Generate a single response from Gemini
// @Tags Gemini
// @Accept json
// @Produce json
// @Param request body GenerateRequest true "Generate request"
// @Success 200 {object} GenerateResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /gemini/generate [post]
func (h *Handler) HandleGenerate(c *fiber.Ctx) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	var req GenerateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: "Invalid request body",
		})
	}

	if req.Message == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: "Message cannot be empty",
		})
	}

	opts := []providers.GenerateOption{}
	if req.Model != "" {
		opts = append(opts, providers.WithModel(req.Model))
	}

	response, err := h.client.GenerateContent(c.Context(), req.Message, opts...)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
			Error: "Generation failed: " + err.Error(),
		})
	}

	return c.JSON(GenerateResponse{
		Response: response.Text,
		Metadata: response.Metadata,
	})
}

// @Summary Chat with Gemini
// @Description Send a message in a chat session
// @Tags Gemini
// @Accept json
// @Produce json
// @Param request body ChatRequest true "Chat request"
// @Success 200 {object} ChatResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /gemini/chat [post]
func (h *Handler) HandleChat(c *fiber.Ctx) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	var req ChatRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: "Invalid request body",
		})
	}

	if req.Message == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: "Message cannot be empty",
		})
	}

	// Create or restore chat session
	chatOpts := []providers.ChatOption{}
	if req.Model != "" {
		chatOpts = append(chatOpts, providers.WithChatModel(req.Model))
	}
	if req.Metadata != nil {
		chatOpts = append(chatOpts, providers.WithChatMetadata(req.Metadata))
	}

	chat := h.client.StartChat(chatOpts...)
	
	response, err := chat.SendMessage(c.Context(), req.Message)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
			Error: "Chat failed: " + err.Error(),
		})
	}

	return c.JSON(ChatResponse{
		Response: response.Text,
		Metadata: chat.GetMetadata(),
		History:  chat.GetHistory(),
	})
}

// @Summary Translate text with Gemini
// @Description Translate text to a target language using Gemini
// @Tags Gemini
// @Accept json
// @Produce json
// @Param request body TranslateRequest true "Translate request"
// @Success 200 {object} GenerateResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /gemini/translate [post]
func (h *Handler) HandleTranslate(c *fiber.Ctx) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	
	var req TranslateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: "Invalid request body",
		})
	}

	if req.Message == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: "Message cannot be empty",
		})
	}

	// Translation specific prompt
	prompt := fmt.Sprintf("Translate the following text. Preserve the tone and format:\n\n%s", req.Message)
	if req.TargetLang != "" {
		prompt = fmt.Sprintf("Translate the following text to %s. Preserve the tone and format:\n\n%s", req.TargetLang, req.Message)
	}

	response, err := h.client.GenerateContent(c.Context(), prompt)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
			Error: "Translation failed: " + err.Error(),
		})
	}

	return c.JSON(GenerateResponse{
		Response: response.Text,
		Metadata: response.Metadata,
	})
}
