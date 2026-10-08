package openai

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"time"

	models "gemini-web-to-api/internal/commons/models"
	utils "gemini-web-to-api/internal/commons/utils"
	"gemini-web-to-api/internal/modules/openai/dto"
	"gemini-web-to-api/internal/modules/providers"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"
)

type OpenAIController struct {
	service *OpenAIService
	log     *zap.Logger
}

func NewOpenAIController(service *OpenAIService, log *zap.Logger) *OpenAIController {
	return &OpenAIController{
		service: service,
		log:     log,
	}
}

// SetLogger sets the logger for this handler
func (h *OpenAIController) SetLogger(log *zap.Logger) {
	h.log = log
}

// GetModelData returns raw model data for internal use (e.g. unified list)
func (h *OpenAIController) GetModelData() []models.ModelData {
	availableModels := h.service.ListModels()

	var data []models.ModelData
	for _, m := range availableModels {
		data = append(data, models.ModelData{
			ID:          m.ID,
			Object:      "model",
			Created:     m.Created,
			OwnedBy:     m.OwnedBy,
			DisplayName: m.DisplayName,
		})
	}
	return data
}

// HandleModels returns the list of supported models
// @Summary List OpenAI Models
// @Description Returns a list of models supported by the OpenAI-compatible API
// @Tags OpenAI
// @Accept json
// @Produce json
// @Success 200 {object} models.ModelListResponse
// @Router /openai/v1/models [get]
func (h *OpenAIController) HandleModels(c fiber.Ctx) error {
	data := h.GetModelData()

	return c.JSON(models.ModelListResponse{
		Object: "list",
		Data:   data,
	})
}

// HandleModel returns one model using the same resolution rules as generation.
func (h *OpenAIController) HandleModel(c fiber.Ctx) error {
	model, err := h.service.ResolveModel(c.Params("model"))
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(utils.ErrorToResponse(err, "invalid_request_error"))
	}
	return c.JSON(models.ModelData{ID: model.ID, Object: "model", Created: model.Created, OwnedBy: model.OwnedBy, DisplayName: model.DisplayName})
}

func openAIRequestError(c fiber.Ctx, err error) error {
	var modelErr *providers.ModelSelectionError
	if errors.As(err, &modelErr) {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ErrorToResponse(err, "invalid_request_error"))
	}
	return c.Status(fiber.StatusInternalServerError).JSON(utils.ErrorToResponse(err, "api_error"))
}

// HandleChatCompletions accepts requests in OpenAI format
// @Summary Chat Completions (OpenAI)
// @Description Generates a completion for the chat message. Supports both standard JSON and streaming (SSE) response.
// @Tags OpenAI
// @Accept json
// @Produce json
// @Produce text/event-stream
// @Param request body dto.ChatCompletionRequest true "Chat Completion Request"
// @Success 200 {object} dto.ChatCompletionResponse
// @Success 200 {string} string "SSE stream of dto.ChatCompletionChunk JSON objects"
// @Failure 400 {object} map[string]interface{}
// @Failure 500 {object} map[string]interface{}
// @Router /openai/v1/chat/completions [post]
func (h *OpenAIController) HandleChatCompletions(c fiber.Ctx) error {
	var req dto.ChatCompletionRequest
	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ErrorToResponse(fmt.Errorf("invalid request body: %w", err), "invalid_request_error"))
	}
	if err := h.service.ValidateChatCompletion(req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ErrorToResponse(err, "invalid_request_error"))
	}

	if req.Stream {
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("X-Accel-Buffering", "no")

		c.RequestCtx().SetBodyStreamWriter(func(w *bufio.Writer) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			err := h.service.CreateChatCompletionStream(ctx, req, func(chunk dto.ChatCompletionChunk) bool {
				return utils.SendSSEEvent(w, h.log, chunk)
			})
			if err != nil {
				h.log.Error("CreateChatCompletionStream failed", zap.Error(err), zap.String("model", req.Model))
				errChunk := dto.ChatCompletionChunk{
					ID:      fmt.Sprintf("chatcmpl-err-%d", time.Now().UnixNano()),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   req.Model,
					Choices: []dto.ChunkChoice{{
						Index:        0,
						Delta:        dto.ChatCompletionChunkDelta{Content: fmt.Sprintf("[ERROR] %s", err.Error())},
						FinishReason: "stop",
					}},
				}
				utils.SendSSEEvent(w, h.log, errChunk)
				_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
				_ = w.Flush()
				return
			}
			_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
			_ = w.Flush()
		})

		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	response, err := h.service.CreateChatCompletion(ctx, req)
	if err != nil {
		h.log.Error("CreateChatCompletion failed", zap.Error(err), zap.String("model", req.Model))
		return openAIRequestError(c, err)
	}

	return c.JSON(response)
}

// HandleImageGenerations accepts image generation requests in OpenAI format
// @Summary Image Generations (OpenAI)
// @Description Generates images from a text prompt.
// @Tags OpenAI
// @Accept json
// @Produce json
// @Param request body dto.ImageGenerationRequest true "Image Generation Request"
// @Success 200 {object} dto.ImageGenerationResponse
// @Failure 400 {object} map[string]interface{}
// @Failure 500 {object} map[string]interface{}
// @Router /openai/v1/images/generations [post]
func (h *OpenAIController) HandleImageGenerations(c fiber.Ctx) error {
	var req dto.ImageGenerationRequest
	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ErrorToResponse(fmt.Errorf("invalid request body: %w", err), "invalid_request_error"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	response, err := h.service.CreateImageGeneration(ctx, req)
	if err != nil {
		if err.Error() == "prompt is required" || err.Error() == "n must be between 1 and 10" {
			return c.Status(fiber.StatusBadRequest).JSON(utils.ErrorToResponse(err, "invalid_request_error"))
		}
		h.log.Error("CreateImageGeneration failed", zap.Error(err), zap.String("model", req.Model))
		return openAIRequestError(c, err)
	}

	return c.JSON(response)
}

// videoRequestError maps video job errors to OpenAI-style HTTP errors.
func videoRequestError(c fiber.Ctx, err error) error {
	var validationErr *providers.VideoValidationError
	switch {
	case errors.As(err, &validationErr):
		return c.Status(fiber.StatusBadRequest).JSON(utils.ErrorToResponse(err, "invalid_request_error"))
	case errors.Is(err, providers.ErrVideoBusy):
		return c.Status(fiber.StatusTooManyRequests).JSON(utils.ErrorToResponse(err, "rate_limit_error"))
	case errors.Is(err, providers.ErrVideoJobNotFound):
		return c.Status(fiber.StatusNotFound).JSON(utils.ErrorToResponse(err, "invalid_request_error"))
	case errors.Is(err, providers.ErrVideoNotReady):
		return c.Status(fiber.StatusConflict).JSON(utils.ErrorToResponse(err, "invalid_request_error"))
	}
	return openAIRequestError(c, err)
}

// HandleCreateVideo starts a text-to-video job
// @Summary Create video (OpenAI Videos API)
// @Description Starts an asynchronous text-to-video job using Gemini Web's Videos tool. Accepts JSON or multipart/form-data. Poll the job, then download its content.
// @Tags OpenAI
// @Accept json
// @Accept mpfd
// @Produce json
// @Param request body dto.VideoGenerationRequest true "Video Generation Request"
// @Success 200 {object} dto.Video
// @Failure 400 {object} map[string]interface{}
// @Failure 429 {object} map[string]interface{}
// @Router /openai/v1/videos [post]
func (h *OpenAIController) HandleCreateVideo(c fiber.Ctx) error {
	var req dto.VideoGenerationRequest
	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ErrorToResponse(fmt.Errorf("invalid request body: %w", err), "invalid_request_error"))
	}
	if form, err := c.MultipartForm(); err == nil && len(form.File["input_reference"]) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ErrorToResponse(errors.New("input_reference is not supported; only text-to-video is available"), "invalid_request_error"))
	}
	video, err := h.service.CreateVideo(req)
	if err != nil {
		h.log.Warn("CreateVideo failed", zap.Error(err), zap.String("model", req.Model))
		return videoRequestError(c, err)
	}
	return c.JSON(video)
}

// HandleListVideos lists retained video jobs
// @Summary List videos
// @Tags OpenAI
// @Produce json
// @Success 200 {object} dto.VideoList
// @Router /openai/v1/videos [get]
func (h *OpenAIController) HandleListVideos(c fiber.Ctx) error {
	return c.JSON(h.service.ListVideos())
}

// HandleGetVideo returns a video job
// @Summary Retrieve video
// @Tags OpenAI
// @Produce json
// @Param video_id path string true "Video ID"
// @Success 200 {object} dto.Video
// @Failure 404 {object} map[string]interface{}
// @Router /openai/v1/videos/{video_id} [get]
func (h *OpenAIController) HandleGetVideo(c fiber.Ctx) error {
	video, err := h.service.GetVideo(c.Params("id"))
	if err != nil {
		return videoRequestError(c, err)
	}
	return c.JSON(video)
}

// HandleDeleteVideo discards a finished video job
// @Summary Delete video
// @Tags OpenAI
// @Produce json
// @Param video_id path string true "Video ID"
// @Success 200 {object} dto.VideoDeleted
// @Failure 404 {object} map[string]interface{}
// @Failure 409 {object} map[string]interface{}
// @Router /openai/v1/videos/{video_id} [delete]
func (h *OpenAIController) HandleDeleteVideo(c fiber.Ctx) error {
	deleted, err := h.service.DeleteVideo(c.Params("id"))
	if err != nil {
		return videoRequestError(c, err)
	}
	return c.JSON(deleted)
}

// HandleVideoContent downloads the MP4 of a completed job
// @Summary Download video content
// @Tags OpenAI
// @Produce video/mp4
// @Param video_id path string true "Video ID"
// @Success 200 {file} binary
// @Failure 404 {object} map[string]interface{}
// @Failure 409 {object} map[string]interface{}
// @Router /openai/v1/videos/{video_id}/content [get]
func (h *OpenAIController) HandleVideoContent(c fiber.Ctx) error {
	if variant := c.Query("variant"); variant != "" && variant != "video" {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ErrorToResponse(fmt.Errorf("variant %q is not available; only video is supported", variant), "invalid_request_error"))
	}
	id := c.Params("id")
	data, mimeType, err := h.service.VideoContent(id)
	if err != nil {
		return videoRequestError(c, err)
	}
	c.Set(fiber.HeaderContentType, mimeType)
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+id+`.mp4"`)
	return c.Send(data)
}

// musicRequestError maps music generation errors to OpenAI-style HTTP errors.
func musicRequestError(c fiber.Ctx, err error) error {
	var validationErr *providers.MusicValidationError
	var generationErr *providers.VideoError
	switch {
	case errors.As(err, &validationErr):
		return c.Status(fiber.StatusBadRequest).JSON(utils.ErrorToResponse(err, "invalid_request_error"))
	case errors.As(err, &generationErr):
		status := fiber.StatusBadGateway
		switch generationErr.Code {
		case providers.VideoErrorQuota:
			status = fiber.StatusTooManyRequests
		case providers.VideoErrorRefused:
			status = fiber.StatusUnprocessableEntity
		case providers.VideoErrorTimeout:
			status = fiber.StatusGatewayTimeout
		}
		message := generationErr.Message
		if generationErr.ProviderMessage != "" {
			message += " (Gemini: " + generationErr.ProviderMessage + ")"
		}
		return c.Status(status).JSON(fiber.Map{"error": fiber.Map{"message": message, "type": "api_error", "code": generationErr.Code}})
	}
	return openAIRequestError(c, err)
}

// HandleSpeech generates music for the OpenAI audio/speech endpoint
// @Summary Create music (OpenAI audio/speech)
// @Description Generates a music track with Gemini Web's music tool. input is the music prompt; voice "instrumental" or "vocals" selects the vocal mode; length and genre are optional extensions. Returns the audio file.
// @Tags OpenAI
// @Accept json
// @Produce audio/mpeg
// @Param request body dto.SpeechRequest true "Speech (music) request"
// @Success 200 {file} binary
// @Failure 400 {object} map[string]interface{}
// @Failure 502 {object} map[string]interface{}
// @Router /openai/v1/audio/speech [post]
func (h *OpenAIController) HandleSpeech(c fiber.Ctx) error {
	var req dto.SpeechRequest
	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ErrorToResponse(fmt.Errorf("invalid request body: %w", err), "invalid_request_error"))
	}
	if req.StreamFormat == "sse" {
		return c.Status(fiber.StatusBadRequest).JSON(utils.ErrorToResponse(errors.New("stream_format sse is not supported; music is returned as one audio file"), "invalid_request_error"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	data, mimeType, err := h.service.CreateSpeech(ctx, req)
	if err != nil {
		h.log.Warn("CreateSpeech (music) failed", zap.Error(err), zap.String("model", req.Model))
		return musicRequestError(c, err)
	}
	c.Set(fiber.HeaderContentType, mimeType)
	return c.Send(data)
}

// Register registers the OpenAI routes onto the provided group
func (c *OpenAIController) Register(group fiber.Router) {
	group.Get("/models", c.HandleModels)
	group.Get("/models/:model", c.HandleModel)
	group.Post("/chat/completions", c.HandleChatCompletions)
	group.Post("/images/generations", c.HandleImageGenerations)
	group.Post("/audio/speech", c.HandleSpeech)
	group.Post("/videos", c.HandleCreateVideo)
	group.Get("/videos", c.HandleListVideos)
	group.Get("/videos/:id", c.HandleGetVideo)
	group.Delete("/videos/:id", c.HandleDeleteVideo)
	group.Get("/videos/:id/content", c.HandleVideoContent)
}
